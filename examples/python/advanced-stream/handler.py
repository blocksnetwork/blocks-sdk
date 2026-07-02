"""advanced-stream handler.

Demonstrates three streaming patterns on a single pipe task, each selected
by its ``declared_stream`` name from the agent card:

  - ``events``    outbound, format="events"  — structured JSON events that
                  conform to the schema declared on the card.
  - ``raw``       outbound, format="bytes"   — raw UTF-8 chunks the consumer
                  reads as bytes.
  - ``broadcast`` outbound, format="events", affinity="shared" — a shared
                  channel with no per-task stream_end marker.

Affinity ("dedicated" vs "shared") is declared on the card, not passed to
create_stream(). The handler only names the stream via ``declared_stream``.
"""

from __future__ import annotations

import json
import time
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

from blocks_network.types import StartTaskMessage, TaskContext

DEFAULT_TICKS = 5
MAX_TICKS = 50


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    log = lambda msg: print(f"[advanced-stream] {msg}")

    if ctx is None:
        return {
            "artifacts": [{"data": json.dumps({"error": "TaskContext is required for streaming"}), "mimeType": "application/json"}],
        }

    if task.task_kind and task.task_kind != "pipe":
        raise RuntimeError("advanced-stream only supports pipe tasks")

    ticks = _parse_ticks(task.request_parts)
    log(f"Task {task.task_id}: emitting {ticks} tick(s) on each stream")

    # Open all three streams up front. Each is selected by its card-declared
    # name; there are no invented stream IDs and no post-create sleeps.
    events = ctx.create_stream(declared_stream="events", format="events")
    raw = ctx.create_stream(declared_stream="raw", format="bytes")
    broadcast = ctx.create_stream(declared_stream="broadcast", format="events")
    log(f"Streams open: events={events.channel} raw={raw.channel} broadcast={broadcast.channel}")

    ctx.report_status(f"Streaming {ticks} ticks across events/raw/broadcast...")

    emitted = 0
    try:
        tick = 0
        while tick < ticks and not ctx.is_cancelled:
            tick += 1

            # Schema-validated event: {tick, label, at} matches the card schema.
            events.write({"tick": tick, "label": f"event #{tick}", "at": datetime.now(timezone.utc).isoformat()})

            # Raw bytes: consumer decodes each chunk from bytes.
            raw.write(f"chunk {tick}\n".encode("utf-8"))

            # Shared broadcast: fans in to a channel shared across tasks.
            broadcast.write({"tick": tick, "kind": "broadcast"})

            emitted += 1
            _sleep_or_cancel(0.5, ctx)
    except (_CancelledError, RuntimeError):
        # RuntimeError from write() on an ended stream means the SDK closed
        # the stream (e.g. SIGINT) before the loop detected cancellation.
        pass

    # end() publishes a stream_end marker on dedicated streams so consumers
    # know they are complete. Shared streams suppress the per-task marker.
    for stream in (events, raw, broadcast):
        try:
            stream.end()
        except RuntimeError:
            pass  # Already ended by SDK shutdown
    log(f"Streams ended ({emitted} ticks emitted)")

    completion_reason = "canceled" if ctx.is_cancelled else "completed"
    ctx.report_status(f"Streaming {completion_reason} ({emitted} ticks)")

    return {
        "artifacts": [{
            "data": json.dumps(
                {"ticksRequested": ticks, "ticksEmitted": emitted, "completionReason": completion_reason},
                indent=2,
            ),
            "mimeType": "application/json",
        }],
    }


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


class _CancelledError(Exception):
    pass


def _sleep_or_cancel(seconds: float, ctx: TaskContext) -> None:
    """Sleep in small increments, raising _CancelledError if cancelled."""
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if ctx.is_cancelled:
            raise _CancelledError()
        time.sleep(min(0.05, end - time.monotonic()))


def _parse_ticks(parts: Optional[List[Any]]) -> int:
    for part in parts or []:
        content = _parse_part_content(part)
        value = content.get("ticks")
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            return min(MAX_TICKS, max(1, int(value)))
    return DEFAULT_TICKS


def _parse_part_content(part: Any) -> Dict[str, Any]:
    text = getattr(part, "text", None) if not isinstance(part, dict) else part.get("text")
    if isinstance(text, str):
        try:
            parsed = json.loads(text)
            if isinstance(parsed, dict):
                return parsed
        except (json.JSONDecodeError, ValueError):
            pass
    if isinstance(part, dict):
        return part
    return {}
