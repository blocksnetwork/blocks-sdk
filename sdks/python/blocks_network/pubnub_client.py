"""
PubNub client factory.

Creates a configured ``PubNub`` instance using the ``pubnub`` Python SDK.
Falls back gracefully if the ``pubnub`` package is not installed.
"""

from __future__ import annotations

import threading
from typing import Any, Optional

# Attempt import -- allow graceful failure so the rest of the package
# can be imported even without the pubnub dependency installed.
try:
    from pubnub.pnconfiguration import PNConfiguration
    from pubnub.pubnub import PubNub

    _PUBNUB_AVAILABLE = True
except ImportError:  # pragma: no cover
    _PUBNUB_AVAILABLE = False
    PNConfiguration = None  # type: ignore[assignment,misc]
    PubNub = None  # type: ignore[assignment,misc]


def _ensure_thread_safe_publish_sequence(pn: Any) -> None:
    """Wrap ``pn._publish_sequence_manager.get_next_sequence`` with a
    lock so concurrent publishes can't race it. Idempotent; no-op if
    the manager is already a thread-safe subclass or already wrapped;
    degrades to a no-op if the pubnub library's internal attribute
    layout changes. See PUBNUB_PYTHON_BUG_REPORT.md for context.
    """
    try:
        mgr = pn._publish_sequence_manager
    except AttributeError:
        return
    if mgr is None:
        return
    # Already wrapped by us on a prior call — idempotent guard.
    if getattr(mgr, "_blocks_thread_safe_seq", False):
        return
    # If the library has been fixed and is using the Native subclass
    # (which already locks), don't double-wrap.
    try:
        from pubnub.pubnub import NativePublishSequenceManager
        if isinstance(mgr, NativePublishSequenceManager):
            return
    except ImportError:
        pass  # Class moved or removed in a future release; fall through

    lock = threading.Lock()
    original_get_next = mgr.get_next_sequence

    def _thread_safe_get_next_sequence() -> int:
        with lock:
            return original_get_next()

    mgr.get_next_sequence = _thread_safe_get_next_sequence  # type: ignore[method-assign]
    mgr._blocks_thread_safe_seq = True  # type: ignore[attr-defined]


def create_pubnub_client(
    *,
    user_id: Optional[str] = None,
    publish_key: Optional[str] = None,
    subscribe_key: Optional[str] = None,
    presence_timeout: Optional[int] = None,
) -> Any:
    """Create and return a configured :class:`pubnub.pubnub.PubNub` instance.

    Parameters are resolved from explicit arguments first, then from
    environment variables via :mod:`blocks_network.config`.

    Parameters
    ----------
    user_id:
        PubNub user ID (typically the instance ID).
    publish_key:
        PubNub publish key.
    subscribe_key:
        PubNub subscribe key.

    Returns
    -------
    PubNub
        A configured PubNub client instance.

    Raises
    ------
    ImportError
        If the ``pubnub`` package is not installed.
    ValueError
        If ``subscribe_key`` is not provided (neither argument nor env).
    """
    if not _PUBNUB_AVAILABLE:
        raise ImportError(
            "The 'pubnub' package is required but not installed. "
            "Install it with: pip install pubnub>=10.6.0"
        )

    effective_user_id = user_id or "blocks-agent"
    effective_publish_key = publish_key or ""
    effective_subscribe_key = subscribe_key or ""

    if not effective_subscribe_key:
        raise ValueError(
            "subscribe_key is required: provide it as an argument or via CDM config"
        )

    pnconfig = PNConfiguration()
    pnconfig.subscribe_key = effective_subscribe_key
    pnconfig.user_id = effective_user_id
    pnconfig.daemon = True

    if effective_publish_key:
        pnconfig.publish_key = effective_publish_key

    if presence_timeout is not None:
        pnconfig.presence_timeout = presence_timeout

    pn = PubNub(pnconfig)
    _ensure_thread_safe_publish_sequence(pn)
    return pn
