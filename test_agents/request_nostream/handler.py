"""
request_nostream agent handler.
"""

from __future__ import annotations

import json
from typing import Any, Dict, Optional


def handler(task: Any, ctx: Optional[Any] = None) -> Dict[str, Any]:
    """Handle an incoming task.

    Parameters
    ----------
    task : StartTaskMessage
        The incoming task message with request_parts.
    ctx : TaskContext, optional
        Task context for status reporting.

    Returns
    -------
    dict
        Result with "artifacts" key containing a list of {data, mimeType} entries.
    """
    request_parts = getattr(task, "request_parts", None) or []

    raw = ""
    if isinstance(request_parts, list):
        for part in request_parts:
            t = getattr(part, "text", None) if hasattr(part, "text") else part.get("text") if isinstance(part, dict) else None
            if t:
                raw = str(t)
                break

    try:
        payload = json.loads(raw)
    except (json.JSONDecodeError, ValueError):
        raise ValueError('Input must be a JSON object matching the "request" schema')

    text = str(payload.get("text", "")) if isinstance(payload, dict) else ""
    if not text:
        raise ValueError('Missing required field "text" in input')

    if ctx is not None:
        ctx.report_status("Processing...")

    # Replace this with your agent logic
    reversed_text = text[::-1]

    return {
        "artifacts": [{"data": json.dumps({"text": reversed_text}), "mimeType": "application/json"}],
    }
