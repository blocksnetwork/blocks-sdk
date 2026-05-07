"""
Local console logging helper for agent instance events.

Mirrors the Node SDK's ``logAgentInstanceEvent`` (index.ts:150-167).
Uses LOG_LEVEL to control which log levels are emitted:
  error  -- errors only
  warn   -- warnings + errors
  info   -- info + warnings + errors (default)
  debug  -- debug + info + warnings + errors
"""

from __future__ import annotations

import json
import sys
import time
from typing import Any

from . import config as _cfg

# Map log level names to numeric thresholds for comparison.
_LEVEL_ORDER = {"error": 0, "warn": 1, "info": 2, "debug": 3}


def _should_log(level: str) -> bool:
    """Return True if *level* should be emitted under the current LOG_LEVEL."""
    threshold = _LEVEL_ORDER.get(_cfg.LOG_LEVEL, 2)  # default to info
    message_level = _LEVEL_ORDER.get(level, 2)
    return message_level <= threshold


def log_agent_instance_event(
    level: str,
    message: str,
    **meta: Any,
) -> None:
    """Log a structured agent instance event to the console.

    Parameters
    ----------
    level:
        One of ``"debug"``, ``"info"``, ``"warn"``, ``"error"``.
    message:
        Human-readable log message.
    **meta:
        Arbitrary key-value pairs included in the log entry.
    """
    if not _should_log(level):
        return

    entry: dict[str, Any] = {
        "level": level,
        "message": message,
        "ts": int(time.time() * 1000),
        **meta,
    }

    if level in ("error", "warn"):
        print(f"[AgentInstance] {json.dumps(entry, default=str)}", file=sys.stderr)
    else:
        print(f"[AgentInstance] {json.dumps(entry, default=str)}")
