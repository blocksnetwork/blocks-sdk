"""
request_stream agent handler.

Accepts a string message and an integer (seconds). Streams one event per
second for N seconds, then returns an artifact summarising the run.
"""

from __future__ import annotations

import json
import time
from typing import Any, Dict, Optional

from blocks_network.types import StartTaskMessage, TaskContext


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    request_parts = getattr(task, "request_parts", None) or []

    message = ""
    seconds = 0
    for part in request_parts:
        raw_text = getattr(part, "text", None) if hasattr(part, "text") else part.get("text") if isinstance(part, dict) else None
        if raw_text:
            try:
                payload = json.loads(raw_text)
                message = str(payload.get("message", ""))
                seconds = int(payload.get("seconds", 0))
            except (json.JSONDecodeError, ValueError, TypeError):
                message = str(raw_text)

    if not message:
        raise ValueError('Missing "message" in input')
    if seconds <= 0:
        raise ValueError('"seconds" must be a positive integer')

    if ctx:
        ctx.report_status("Starting stream...")
        stream = ctx.create_stream()

        for i in range(1, seconds + 1):
            if ctx.is_cancelled:
                break
            time.sleep(1)
            stream.write(f"{i} seconds")
            ctx.report_status(f"{i}/{seconds} seconds")

        stream.end()
        ctx.report_status("Done")

    return {
        "artifacts": [{"data": f"I ran for {seconds} seconds and your message to me was {message}", "mimeType": "text/plain"}],
    }
