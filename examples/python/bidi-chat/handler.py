"""Bidirectional chat handler (Python).

The agent opens a `direction="bidirectional"` stream, optionally writes a
greeting echo, then loops on inbound `{ text }` events and replies with
`AGENT> <UPPERCASED>`. The loop exits when the consumer sends `bye`.
"""

from __future__ import annotations

from typing import Any, Dict, List, Optional

from blocks_network.types import StartTaskMessage, TaskContext


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    # A bidirectional chat needs a live stream. When streaming wasn't
    # negotiated (no ctx, or a request consumer opted out via stream=False →
    # has_stream false), there's nothing to stream — return an
    # artifact instead of raising in create_stream().
    if ctx is None or not ctx.has_stream:
        return {"artifacts": [{"data": "no stream negotiated — nothing streamed", "mimeType": "text/plain"}]}

    greeting = _extract_greeting(task.request_parts or [])

    ctx.report_status("Opening bidirectional stream...")
    stream = ctx.create_stream(
        direction="bidirectional",
        format="events",
        bundle_size_bytes=512,
        max_latency_ms=25,
    )

    transcript: List[str] = []

    if greeting:
        reply = f"AGENT> {greeting.upper()}"
        stream.write({"text": reply})
        transcript.append(f"> {greeting}")
        transcript.append(reply)

    ctx.report_status("Awaiting consumer messages...")

    for event in stream.events():
        if isinstance(event, dict) and isinstance(event.get("text"), str):
            inbound_text = event["text"]
        elif isinstance(event, str):
            inbound_text = event
        else:
            inbound_text = str(event)

        transcript.append(f"> {inbound_text}")
        reply = f"AGENT> {inbound_text.upper()}"
        stream.write({"text": reply})
        transcript.append(reply)

        if inbound_text.strip().lower() == "bye":
            break

    stream.end()
    ctx.report_status("Stream ended")

    return {
        "artifacts": [{"data": "\n".join(transcript), "mimeType": "text/plain"}],
    }


def _extract_greeting(request_parts: List[Any]) -> Optional[str]:
    for part in request_parts:
        if isinstance(part, str):
            return part
        if hasattr(part, "text") and isinstance(part.text, str):
            return part.text
        if isinstance(part, dict) and isinstance(part.get("text"), str):
            return part["text"]
    return None
