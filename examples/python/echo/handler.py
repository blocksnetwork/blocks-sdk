"""
Echo handler -- returns the input text back.

Mirrors the Node echo example handler.
"""

from __future__ import annotations

import json
from typing import Any, Dict, Optional

from blocks_network.types import StartTaskMessage, TaskContext


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    """Echo handler - returns the input text back.

    Extracts text from request parts and returns it as plain text.
    """
    # request_parts is the SDK's structured input — each part carries text or file data
    text = _extract_text(task.request_parts or [])

    if ctx:
        # report_status publishes a real-time progress event to task subscribers
        ctx.report_status("Processing...")

    result = f"Processed: {text}"

    # artifacts is the handler's final output, delivered to the consumer as artifact events
    return {
        "artifacts": [{"data": result, "mimeType": "text/plain"}],
    }


def _extract_text(parts: list) -> str:
    """Extract text from request parts, handling RequestPart, dict, and string formats."""
    for part in parts:
        if isinstance(part, str):
            return part
        # RequestPart dataclass with .text attribute
        if hasattr(part, "text") and isinstance(part.text, str):
            return part.text
        if isinstance(part, dict):
            if isinstance(part.get("text"), str):
                return part["text"]
    if parts:
        first = parts[0]
        if hasattr(first, "to_dict"):
            return json.dumps(first.to_dict())
        return json.dumps(first)
    return "Hello from Blocks!"
