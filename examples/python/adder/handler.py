"""
Adder handler -- adds two numbers and returns the sum.

Mirrors the Node adder example handler.
"""

from __future__ import annotations

import json
import math
from typing import Any, Dict, Optional

from blocks_network.types import RequestPart, StartTaskMessage, TaskContext


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    """Adder handler - adds two numbers and returns the sum.

    Expects a request part with numeric ``a`` and ``b`` fields:
        { "kind": "math_add", "a": 3, "b": 4 }

    Returns JSON: { "ok": true, "a": 3, "b": 4, "sum": 7 }
    """
    parsed = _parse_math_input(task)

    if parsed is None:
        raise ValueError(
            'Missing request part with numeric a and b fields. '
            'Send { "kind": "math_add", "a": <number>, "b": <number> }'
        )

    a, b = parsed

    if ctx:
        ctx.report_status(f"Adding {a} + {b}")

    total = a + b
    artifact = {"ok": True, "a": a, "b": b, "sum": total}

    return {
        "artifacts": [{"data": json.dumps(artifact, indent=2), "mimeType": "application/json"}],
    }


# ---------------------------------------------------------------------------
# Input parsing helpers
# ---------------------------------------------------------------------------


def _parse_math_input(task: StartTaskMessage) -> tuple[float, float] | None:
    """Parse ``a`` and ``b`` from request parts."""
    parts = task.request_parts or []
    for part in parts:
        content = _parse_part_content(part)
        kind = content.get("kind")
        if kind is not None and kind != "math_add":
            continue
        a = content.get("a")
        b = content.get("b")
        if _is_finite_number(a) and _is_finite_number(b):
            return (a, b)
    return None


def _parse_part_content(part: Any) -> Dict[str, Any]:
    """Extract structured content from a RequestPart or raw dict.

    The frontend serializes structured content into the ``text`` field as
    JSON.  This helper parses it back into a dict.  Falls back to the
    ``extra`` dict on a RequestPart, or the raw dict itself.
    """
    text = getattr(part, "text", None) if not isinstance(part, dict) else part.get("text")
    if isinstance(text, str):
        try:
            parsed = json.loads(text)
            if isinstance(parsed, dict):
                return parsed
        except (json.JSONDecodeError, ValueError):
            pass
        return {"text": text}
    if hasattr(part, "extra") and isinstance(part.extra, dict):
        return part.extra
    if isinstance(part, dict):
        return part
    return {}


def _is_finite_number(value: Any) -> bool:
    """Check whether *value* is a finite number (int or float, not NaN/Inf)."""
    if isinstance(value, bool):
        return False
    if isinstance(value, (int, float)):
        return math.isfinite(value)
    return False
