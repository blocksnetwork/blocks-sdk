"""
Echo handler -- returns the input text back.

Port of ``src/agent-runtime/node/handlers/echo.ts``.
The Node handler extracts text from the first requestPart that has a ``text``
field, then returns ``"Echoed request: <text>"`` as ``text/plain``.
"""

from __future__ import annotations

import json
from typing import Any, Dict, List, Optional


def _extract_text_input(request_parts: List[Any]) -> str:
    """Extract text from request parts, matching Node ``extractTextInput``.

    Iterates through parts looking for a RequestPart with a ``text``
    attribute, or a dict with a ``text`` key.  Falls back to
    JSON-serialising the entire parts list.
    """
    if isinstance(request_parts, list):
        for part in request_parts:
            # RequestPart dataclass
            if hasattr(part, "text") and part.text is not None:
                return str(part.text)
            # Legacy dict fallback
            if isinstance(part, dict) and "text" in part:
                return str(part["text"])
    serializable = [
        p.to_dict() if hasattr(p, "to_dict") else p for p in request_parts
    ] if isinstance(request_parts, list) else request_parts
    return json.dumps(serializable)


def echo_handler(task: Any, ctx: Optional[Any] = None) -> Dict[str, Any]:
    """Echo handler - returns the input text back.

    Mirrors the Node ``echoHandler``:
    - Extracts text input from ``task.request_parts``.
    - Returns ``"Echoed request: <input>"`` as plain text.

    Parameters
    ----------
    task : StartTaskMessage
        The incoming task message with ``request_parts``.
    ctx : TaskContext, optional
        Task context for status reporting (unused by echo).

    Returns
    -------
    dict
        ``{"artifacts": [{"data": str, "mimeType": "text/plain"}]}``
    """
    request_parts = getattr(task, "request_parts", None) or []
    text_input = _extract_text_input(request_parts)
    content = f"Echoed request: {text_input}"

    return {
        "artifacts": [{"data": content, "mimeType": "text/plain"}],
    }
