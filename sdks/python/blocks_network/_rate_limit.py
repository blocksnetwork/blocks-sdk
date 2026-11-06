"""The backend's per-API-key rate limit: ``429 RATE_LIMITED`` with a ``Retry-After`` hint."""

from __future__ import annotations

import email.utils
import random as _random
import re
import threading
import time
from typing import Any, Callable, Iterator, Optional

HTTP_TOO_MANY_REQUESTS = 429
# A Retry-After is capped at the backend's rate-limit window.
RETRY_AFTER_MAX_S = 3600.0
# How long a 429 without a usable hint holds further calls off.
RATE_LIMIT_NO_HINT_HOLD_S = 30.0
# A retry after a 429 without a usable hint: 30 s, doubling per consecutive 429, to 10 min.
RATE_LIMIT_BACKOFF_BASE_S = 30.0
RATE_LIMIT_BACKOFF_MAX_S = 600.0
# A zero or past Retry-After must not turn a retry into a busy loop.
RATE_LIMIT_MIN_WAIT_S = 1.0
# Spreads the reconnects of instances that share one API key.
RATE_LIMIT_JITTER_FRACTION = 0.1

_DELTA_SECONDS = re.compile(r"[0-9]+")
# 2**16 * 30 s is far past the cap; a larger exponent would only risk float overflow.
_MAX_BACKOFF_EXPONENT = 16


class RateLimitedError(RuntimeError):
    """The backend rate-limited the API key. Retryable, never fatal.

    ``retry_after`` is the wait in seconds the backend asked for, or ``None`` when it
    gave no usable ``Retry-After``.
    """

    status = HTTP_TOO_MANY_REQUESTS

    def __init__(self, message: str, retry_after: Optional[float] = None) -> None:
        super().__init__(message)
        self.retry_after = retry_after


def parse_retry_after(value: Any, now: Optional[float] = None) -> Optional[float]:
    """Seconds to wait from a ``Retry-After`` value: RFC 9110 delta-seconds or an HTTP-date.

    ``None`` when the value is absent or unparseable; capped at :data:`RETRY_AFTER_MAX_S`.
    ``now`` is wall-clock epoch seconds, used to resolve an HTTP-date.
    """
    if not isinstance(value, str) or not value.strip():
        return None
    text = value.strip()
    if _DELTA_SECONDS.fullmatch(text):
        seconds = float(text)
    else:
        try:
            when = email.utils.parsedate_to_datetime(text)
        except (TypeError, ValueError):
            return None
        if when.tzinfo is None:
            return None
        seconds = max(when.timestamp() - (time.time() if now is None else now), 0.0)
    return min(seconds, RETRY_AFTER_MAX_S)


def retry_after_from(headers: Any) -> Optional[float]:
    """The parsed ``Retry-After`` of a response's headers, if any."""
    getter = getattr(headers, "get", None)
    return parse_retry_after(getter("Retry-After")) if callable(getter) else None


def _error_chain(err: Optional[BaseException]) -> Iterator[BaseException]:
    depth = 0
    while err is not None and depth < 4:
        yield err
        err = err.__cause__
        depth += 1


class BlocksHttpError(Exception):
    """A Blocks endpoint's HTTP error. Only these, and :class:`RateLimitedError`, can be the
    Blocks rate limit: a customer token endpoint's or token provider's error with a ``status``
    of 429 is a failed refresh, not a wait."""

    def __init__(self, status: int, message: str, retry_after: Optional[float] = None) -> None:
        super().__init__(message)
        self.status = status
        # The response's Retry-After, when it had one.
        self.retry_after = retry_after


def is_rate_limited(err: Optional[BaseException]) -> bool:
    """True when ``err``, or an error it was raised from, is a :class:`RateLimitedError` or a
    Blocks 429."""
    return any(
        isinstance(e, RateLimitedError)
        or (isinstance(e, BlocksHttpError) and e.status == HTTP_TOO_MANY_REQUESTS)
        for e in _error_chain(err)
    )


def retry_after_of(err: Optional[BaseException]) -> Optional[float]:
    """The server-requested wait carried by ``err`` or an error it was raised from."""
    for e in _error_chain(err):
        seconds = getattr(e, "retry_after", None)
        if isinstance(seconds, (int, float)) and not isinstance(seconds, bool):
            return float(seconds)
    return None


def rate_limit_backoff_s(attempt: int, retry_after: Optional[float]) -> float:
    """The wait before retrying after the ``attempt``th consecutive 429 (0-based)."""
    if retry_after is None:
        exponent = min(attempt, _MAX_BACKOFF_EXPONENT)
        retry_after = min(RATE_LIMIT_BACKOFF_BASE_S * 2**exponent, RATE_LIMIT_BACKOFF_MAX_S)
    return max(RATE_LIMIT_MIN_WAIT_S, retry_after)


def connect_retry_delay_s(
    attempt: int, retry_after: Optional[float], random: Callable[[], float] = _random.random
) -> float:
    """:func:`rate_limit_backoff_s` plus up to :data:`RATE_LIMIT_JITTER_FRACTION` jitter."""
    return rate_limit_backoff_s(attempt, retry_after) * (
        1 + random() * RATE_LIMIT_JITTER_FRACTION
    )


class RateLimitHold:
    """Holds calls off after a 429 until its ``Retry-After`` has passed.

    While held, :meth:`check` raises :class:`RateLimitedError` carrying the remaining wait
    instead of letting the caller reach the network.
    """

    def __init__(self) -> None:
        self._until = 0.0
        # Concurrent refreshes can answer out of order; a shorter hint must not shorten the hold.
        self._lock = threading.Lock()

    def record(self, err: RateLimitedError) -> None:
        wait = RATE_LIMIT_NO_HINT_HOLD_S if err.retry_after is None else err.retry_after
        with self._lock:
            self._until = max(self._until, time.monotonic() + wait)

    def check(self, what: str) -> None:
        remaining = self._until - time.monotonic()
        if remaining > 0:
            raise RateLimitedError(
                f"{what} held off: rate limited for another {remaining:.1f}s", remaining
            )
