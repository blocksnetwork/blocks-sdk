"""
Protocol versioning constants and helpers for the Blocks Python SDK.

This is the single source of truth for protocol version values in the
Python SDK. All modules that need version strings import from here.
"""

from __future__ import annotations

import importlib.metadata
from typing import List

# ---------------------------------------------------------------------------
# Protocol version constants
# ---------------------------------------------------------------------------

CURRENT_PROTOCOL_VERSION: str = "2026-05-01"
"""The current (preferred) Blocks wire protocol version."""

SUPPORTED_PROTOCOL_VERSIONS: List[str] = ["2026-05-01"]
"""All protocol versions this SDK can speak."""

DEPRECATED_PROTOCOL_VERSIONS: List[str] = []
"""Protocol versions that are still supported but deprecated."""

PROTOCOL_VERSION_HEADER: str = "Blocks-Protocol-Version"
"""HTTP header name for protocol version on all guarded endpoints."""

# ---------------------------------------------------------------------------
# SDK package version
# ---------------------------------------------------------------------------


def _resolve_sdk_version() -> str:
    """Resolve the SDK package version from installed metadata.

    Falls back to ``"unknown"`` if the package is not installed or
    metadata is unavailable.
    """
    try:
        return importlib.metadata.version("blocks-network")
    except Exception:
        return "unknown"


SDK_VERSION: str = _resolve_sdk_version()
"""The installed ``blocks-network`` Python package version."""

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def is_supported(version: str) -> bool:
    """Return ``True`` if *version* is in the supported set."""
    return version in SUPPORTED_PROTOCOL_VERSIONS


def is_deprecated(version: str) -> bool:
    """Return ``True`` if *version* is deprecated but still supported."""
    return version in DEPRECATED_PROTOCOL_VERSIONS
