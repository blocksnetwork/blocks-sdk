"""
Write-affinity header tracking for non-browser clients.

The backend returns X-Write-Affinity (Unix timestamp) on successful
mutations. Echoing it on subsequent requests forces reads from
primary during the affinity window, avoiding stale replica reads
after a write.

Module-level state -- affinity is per-process, not per-user.

Thread safety: ``capture`` and ``inject`` acquire a module-level lock,
so concurrent callers from threadpool workers or ``asyncio.run_in_executor``
cannot interleave read-modify-write on the stored expiry. ``capture`` is
monotonic -- an older expiry never overwrites a newer one -- so out-of-order
response completions do not shorten the affinity window.
"""

from __future__ import annotations

import math
import threading
import time
from typing import Any

_HEADER = "X-Write-Affinity"

_lock = threading.Lock()
_stored_expiry: str | None = None
_stored_expiry_value: float = 0.0


def _parse_expiry(raw: str) -> float | None:
    """Parse an expiry header value as a float. Returns ``None`` if malformed.

    Rejects non-finite values (``inf``, ``-inf``, ``nan``) which ``float()``
    otherwise accepts -- parity with the Node SDK's ``Number.isFinite`` guard.
    Without this, ``X-Write-Affinity: inf`` would pin all reads to primary
    forever.
    """
    try:
        parsed = float(raw)
    except (TypeError, ValueError):
        return None
    if not math.isfinite(parsed):
        return None
    return parsed


def capture_affinity(response: Any) -> None:
    """Capture the affinity header from an HTTP response.

    Works with both ``http.client.HTTPResponse`` and any object
    that has a ``getheader(name)`` method. Monotonic: a captured
    value only replaces the stored one if it is strictly newer.
    """
    if response is None or not hasattr(response, "getheader"):
        return
    value = response.getheader(_HEADER)
    if not value:
        return
    parsed = _parse_expiry(value)
    if parsed is None:
        return
    global _stored_expiry, _stored_expiry_value
    with _lock:
        if parsed > _stored_expiry_value:
            _stored_expiry = value
            _stored_expiry_value = parsed


def inject_affinity(headers: dict[str, str]) -> None:
    """Inject, or strip, the affinity header on an outgoing request."""
    global _stored_expiry, _stored_expiry_value
    with _lock:
        if _stored_expiry and _stored_expiry_value > time.time():
            headers[_HEADER] = _stored_expiry
            return
        if _stored_expiry:
            _stored_expiry = None
            _stored_expiry_value = 0.0
        headers.pop(_HEADER, None)


def reset_affinity() -> None:
    """Reset stored affinity (for tests)."""
    global _stored_expiry, _stored_expiry_value
    with _lock:
        _stored_expiry = None
        _stored_expiry_value = 0.0
