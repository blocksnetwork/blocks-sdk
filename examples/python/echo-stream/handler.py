"""
Echo-stream handler.

Streams input back chunk-by-chunk (line-by-line when multiline,
otherwise word-by-word), then returns the full text artifact.

Uses the unified create_stream() API which performs a setup handshake
with the streamSetup Function to obtain a T7a stream token.
"""

from __future__ import annotations

import json
from typing import Any, Dict, List, Optional

from blocks_network.types import StartTaskMessage, TaskContext


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    """Handle an incoming task by streaming the echo before returning artifact."""
    full_text = _extract_input_text(task.request_parts or [])

    if ctx:
        ctx.report_status("Streaming echo output...")
        stream = ctx.create_stream(
            bundle_size_bytes=2048,
            max_latency_ms=50,
        )
        for chunk in _chunk_text(full_text):
            stream.write(chunk)
        stream.end()
        ctx.report_status("Streaming complete")

    return {
        "artifacts": [{"data": full_text, "mimeType": "text/plain"}],
    }


def _extract_input_text(request_parts: List[Any]) -> str:
    if not request_parts:
        return "Hello from echo-stream"

    pieces: List[str] = []
    for part in request_parts:
        if isinstance(part, str):
            pieces.append(part)
            continue
        # RequestPart dataclass with .text attribute
        if hasattr(part, "text") and isinstance(part.text, str):
            pieces.append(part.text)
            continue
        if isinstance(part, dict) and isinstance(part.get("text"), str):
            pieces.append(part["text"])
            continue
        if hasattr(part, "to_dict"):
            pieces.append(json.dumps(part.to_dict()))
        else:
            pieces.append(json.dumps(part))

    return "\n".join(pieces)


def _chunk_text(text: str) -> List[str]:
    if text == "":
        return [""]

    if "\n" in text:
        # Keep line separators on each chunk except possibly the last line.
        return [chunk for chunk in text.splitlines(keepends=True) if chunk] or [text]

    # Word-by-word with trailing whitespace preserved so concatenation matches input.
    return [chunk for chunk in _word_chunks(text)] or [text]


def _word_chunks(text: str) -> List[str]:
    chunks: List[str] = []
    current = []
    saw_non_space = False

    for char in text:
        current.append(char)
        if not char.isspace():
            saw_non_space = True
        elif saw_non_space:
            chunks.append("".join(current))
            current = []
            saw_non_space = False

    if current:
        chunks.append("".join(current))

    return chunks
