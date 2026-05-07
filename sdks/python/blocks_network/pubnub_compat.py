"""
PubNub SDK compatibility patches.

The PubNub Python SDK (10.x) has a bug where
``PNSubscriptionRegistryCallback.file()`` calls ``listener.file_message()``
on every object returned by ``get_all_listeners()``, including
``PubNubSubscription`` and ``PubNubSubscriptionSet`` instances that inherit
from ``PNEventEmitter`` — which does not define ``file_message``.

This module patches ``PNEventEmitter`` with a no-op ``file_message`` so
that subscription objects do not raise ``AttributeError`` when a file
message arrives on any channel.
"""

from __future__ import annotations

_patched = False


def patch_pubnub_file_message() -> None:
    """Add a no-op ``file_message`` to ``PNEventEmitter`` if missing."""
    global _patched
    if _patched:
        return
    _patched = True

    try:
        from pubnub.models.subscription import PNEventEmitter

        if not hasattr(PNEventEmitter, "file_message"):
            PNEventEmitter.file_message = lambda self, *a, **kw: None  # type: ignore[attr-defined]
    except ImportError:
        pass
