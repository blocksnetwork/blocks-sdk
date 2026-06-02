"""
Environment variable configuration for Blocks Network Python SDK.

After the sdk_remove_env_vars initiative, this module exports only the
env vars that remain supported for provider/runtime use:

- BLOCKS_CDM_URL     -- CDM endpoint override
- LOG_LEVEL          -- explicit log verbosity (error|warn|info|debug)
- ARTIFACT_INLINE_LIMIT_BYTES -- artifact inline threshold

Plus one platform-contract constant (NOT env-driven, intentionally
non-tunable — mirrors backend MAX_FILE_SIZE_BYTES):

- BLOCKS_MAX_UPLOAD_BYTES -- platform upload ceiling (25 MiB)
"""

from __future__ import annotations

import os


def _int_env(key: str, default: int) -> int:
    """Parse an integer environment variable with a fallback default."""
    val = os.environ.get(key, "")
    if not val:
        return default
    try:
        return int(val)
    except (ValueError, TypeError):
        return default


# ---------------------------------------------------------------------------
# CDM endpoint
# ---------------------------------------------------------------------------
BLOCKS_CDM_URL: str = os.environ.get("BLOCKS_CDM_URL", "")

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------
LOG_LEVEL: str = os.environ.get("LOG_LEVEL", "info").lower()


def debug_subsystem_enabled(subsystem: str) -> bool:
    """Return True if *subsystem* appears in BLOCKS_DEBUG_INTERNAL.

    BLOCKS_DEBUG_INTERNAL is a comma-separated list of opt-in debug
    subsystems, mirroring the Node SDK's env var of the same name and
    format. No subsystem is implied by LOG_LEVEL=debug. The only token
    the Python SDK currently honors is ``forward_transport`` (surface the
    underlying httpx/httpcore transport's own request logging). Parsed
    live (not cached at import) so tests and late env writes are honored.
    """
    raw = os.environ.get("BLOCKS_DEBUG_INTERNAL", "")
    return any(token.strip() == subsystem for token in raw.split(","))

# ---------------------------------------------------------------------------
# Artifact limits
# ---------------------------------------------------------------------------
ARTIFACT_INLINE_LIMIT_BYTES: int = _int_env("ARTIFACT_INLINE_LIMIT_BYTES", 16384)

# ---------------------------------------------------------------------------
# Platform upload ceiling (NOT env-driven — fixed contract with backend)
# ---------------------------------------------------------------------------
BLOCKS_MAX_UPLOAD_BYTES: int = 26_214_400  # 25 MB — must match the service's MAX_FILE_SIZE_BYTES
