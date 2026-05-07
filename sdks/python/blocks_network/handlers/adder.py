"""
Adder handler -- adds two numbers and returns the sum.

Port of ``src/agent-runtime/node/handlers/adder.ts``.
The Node handler looks for a request part with numeric ``a`` and ``b`` fields
(optionally gated by ``kind == "math_add"``).  If found it returns
``{"ok": true, "a": ..., "b": ..., "sum": ...}``; otherwise it returns an
error payload.
"""

from __future__ import annotations

import json
import math
from typing import Any, Dict, List, Optional, Tuple


def _is_finite_number(value: Any) -> bool:
    """Check whether *value* is a finite number (int or float, not NaN/Inf)."""
    if isinstance(value, bool):
        return False
    if isinstance(value, (int, float)):
        return math.isfinite(value)
    return False


def _extract_text_input(request_parts: List[Any]) -> str:
    """Fallback text extraction matching Node ``extractTextInput``."""
    if isinstance(request_parts, list):
        for part in request_parts:
            if hasattr(part, "text") and part.text is not None:
                return str(part.text)
            if isinstance(part, dict) and "text" in part:
                return str(part["text"])
    serializable = [
        p.to_dict() if hasattr(p, "to_dict") else p for p in request_parts
    ] if isinstance(request_parts, list) else request_parts
    return json.dumps(serializable)


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


def _parse_math_add_input(
    request_parts: List[Any],
) -> Optional[Tuple[float, float]]:
    """Parse ``a`` and ``b`` from request parts matching Node ``parseMathAddInput``.

    Iterates parts looking for a RequestPart or dict with finite-number
    ``a`` and ``b``.  If ``kind`` is present it must equal ``"math_add"``.
    """
    if not isinstance(request_parts, list):
        return None

    for part in request_parts:
        content = _parse_part_content(part)
        kind = content.get("kind")
        if kind is not None and kind != "math_add":
            continue
        a = content.get("a")
        b = content.get("b")
        if _is_finite_number(a) and _is_finite_number(b):
            return (a, b)

    return None


def adder_handler(task: Any, ctx: Optional[Any] = None) -> Dict[str, Any]:
    """Adder handler - adds two numbers and returns the sum.

    Mirrors the Node ``adderHandler``:
    - Parses ``a`` and ``b`` from ``task.request_parts``.
    - Returns ``{"ok": true, "a": ..., "b": ..., "sum": ...}`` on success.
    - Returns ``{"ok": false, "error": ..., "input": ...}`` on bad input.

    Parameters
    ----------
    task : StartTaskMessage
        The incoming task message with ``request_parts``.
    ctx : TaskContext, optional
        Task context for status reporting (unused by adder).

    Returns
    -------
    dict
        ``{"artifacts": [{"data": str, "mimeType": "application/json"}]}``
    """
    request_parts = getattr(task, "request_parts", None) or []
    parsed = _parse_math_add_input(request_parts)

    if parsed is None:
        fallback = _extract_text_input(request_parts)
        artifact = {
            "ok": False,
            "error": "Missing math_add request with numeric a and b",
            "input": fallback,
        }
        return {
            "artifacts": [{"data": json.dumps(artifact, indent=2), "mimeType": "application/json"}],
        }

    a, b = parsed
    total = a + b
    artifact = {"ok": True, "a": a, "b": b, "sum": total}

    return {
        "artifacts": [{"data": json.dumps(artifact, indent=2), "mimeType": "application/json"}],
    }
