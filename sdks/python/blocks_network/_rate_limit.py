"""The backend's per-API-key rate limit: ``429 RATE_LIMITED`` with a ``Retry-After`` hint."""

from __future__ import annotations

import email.utils
import math
import threading
import time
from typing import Any, Optional

HTTP_TOO_MANY_REQUESTS = 429
# A Retry-After is capped at the backend's rate-limit window.
RETRY_AFTER_MAX_S = 3600.0
# How long a 429 without a usable hint holds further calls off.
RATE_LIMITED_FALLBACK_S = 30.0


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
    """Seconds to wait from a ``Retry-After`` value (delta-seconds or HTTP-date).

    ``None`` when the value is absent or unparseable; capped at :data:`RETRY_AFTER_MAX_S`.
    ``now`` is wall-clock epoch seconds, used to resolve an HTTP-date.
    """
    if not isinstance(value, str) or not value.strip():
        return None
    text = value.strip()
    try:
        seconds = float(text)
    except ValueError:
        try:
            when = email.utils.parsedate_to_datetime(text)
        except (TypeError, ValueError):
            return None
        if when.tzinfo is None:
            return None
        seconds = when.timestamp() - (time.time() if now is None else now)
        seconds = max(seconds, 0.0)
    if not math.isfinite(seconds) or seconds < 0:
        return None
    return min(seconds, RETRY_AFTER_MAX_S)


def retry_after_of(headers: Any) -> Optional[float]:
    """The parsed ``Retry-After`` of a response's headers, if any."""
    getter = getattr(headers, "get", None)
    return parse_retry_after(getter("Retry-After")) if callable(getter) else None


class RateLimitGate:
    """Holds calls off after a 429 until its ``Retry-After`` has passed.

    While held, :meth:`check` raises :class:`RateLimitedError` carrying the remaining wait
    instead of letting the caller reach the network.
    """

    def __init__(self) -> None:
        self._until = 0.0
        # Concurrent refreshes can answer out of order; a shorter hint must not shorten the hold.
        self._lock = threading.Lock()

    def hold(self, err: RateLimitedError) -> None:
        wait = RATE_LIMITED_FALLBACK_S if err.retry_after is None else err.retry_after
        with self._lock:
            self._until = max(self._until, time.monotonic() + wait)

    def check(self, what: str) -> None:
        remaining = self._until - time.monotonic()
        if remaining > 0:
            raise RateLimitedError(
                f"{what} held off: rate limited for another {remaining:.1f}s", remaining
            )
