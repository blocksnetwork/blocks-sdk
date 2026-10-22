"""Trust policy for every HTTPS request the SDK makes itself.

The SDK verifies servers against the certifi bundle rather than the
interpreter's default store, so the trusted roots move with the
``certifi`` floor in ``pyproject.toml`` instead of with whatever CA file
the host OS or Python build happens to ship. PubNub's HTTPX transport
also defaults to certifi, so both paths share one trust store.
"""

from __future__ import annotations

import ssl

import certifi


def https_context() -> ssl.SSLContext:
    """Return a verifying TLS context that trusts only the certifi bundle."""
    return ssl.create_default_context(cafile=certifi.where())
