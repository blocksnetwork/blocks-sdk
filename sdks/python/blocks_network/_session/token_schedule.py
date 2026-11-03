"""When route and consumer session tokens are refreshed, how a failed refresh retries, and
which HTTP statuses are final refusals for every session request."""

from __future__ import annotations

from .._rate_limit import RateLimitedError

TOKEN_REFRESH_AT = 0.8
TOKEN_RETRY_MIN_S = 5.0
TOKEN_RETRY_MAX_S = 300.0
_TRANSIENT_STATUSES = frozenset({408, 429})


def is_refusal_status(status: int) -> bool:
    """A 4xx other than 408 or 429: the backend turned the call down, and retrying will not help."""
    return 400 <= status < 500 and status not in _TRANSIENT_STATUSES


def rate_limited_wait_s(err: BaseException) -> float:
    """The wait a rate-limited failure asked for, or 0: a retry backs off at least this long."""
    if isinstance(err, RateLimitedError) and err.retry_after is not None:
        return err.retry_after
    return 0.0
