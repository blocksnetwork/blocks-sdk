"""BLOCKS-262 symmetry test provider — Python.

Mirror of symmetry_provider/handler.ts. Opens four streams (P->C bytes,
P->C events, C->P bytes, C->P events), produces and consumes via the
shared helpers, and returns a JSON artifact with the four hash digests.
"""

from __future__ import annotations

import json
import sys
import threading
from pathlib import Path
from typing import Any, Dict, Optional

# Reach the sibling symmetry_shared/ directory so both this handler and the
# Python consumer load the SAME helpers and payloads. Symmetry-by-construction.
_SHARED = Path(__file__).resolve().parent.parent / "symmetry_shared"
if str(_SHARED) not in sys.path:
    sys.path.insert(0, str(_SHARED))

from helpers import (  # noqa: E402
    consume_bytes,
    consume_events,
    produce_bytes,
    produce_events,
    sleep,
)
from payloads import BYTES_VARIANTS, EVENTS_VARIANTS  # noqa: E402

from blocks_network.types import StartTaskMessage, TaskContext  # noqa: E402


PUBLISH_GRACE_S = 2.0


def handler(
    task: StartTaskMessage,
    ctx: Optional[TaskContext] = None,
) -> Dict[str, Any]:
    tag = f"[handler {task.task_id[:8]}]"
    print(f"{tag} start kind={task.task_kind} duration={task.duration}min")
    if ctx is None:
        return {"artifacts": [{"data": "{}", "mimeType": "application/json"}]}

    print(f"{tag} opening 4 streams")
    p_to_c_bytes = ctx.create_stream(
        declared_stream="p_to_c_bytes", direction="outbound", format="bytes"
    )
    p_to_c_events = ctx.create_stream(
        declared_stream="p_to_c_events", direction="outbound", format="events"
    )
    c_to_p_bytes = ctx.create_stream(
        declared_stream="c_to_p_bytes", direction="inbound", format="bytes"
    )
    c_to_p_events = ctx.create_stream(
        declared_stream="c_to_p_events", direction="inbound", format="events"
    )
    print(f"{tag} all streams open")

    results: Dict[str, Any] = {}
    errors: list[BaseException] = []

    def produce_p_to_c() -> None:
        try:
            sleep(PUBLISH_GRACE_S)
            print(f"{tag} producing P->C bytes ({len(BYTES_VARIANTS)} payloads)")
            r1 = produce_bytes(p_to_c_bytes, list(BYTES_VARIANTS))
            print(
                f"{tag} P->C bytes done: {r1['totalBytes']}B / "
                f"{r1['chunkCount']} writes / hash={r1['hash'][:16]}…"
            )
            results["provider_sent_bytes"] = r1

            print(f"{tag} producing P->C events ({len(EVENTS_VARIANTS)} events)")
            r2 = produce_events(p_to_c_events, list(EVENTS_VARIANTS))
            print(
                f"{tag} P->C events done: {r2['eventCount']} events / "
                f"hash={r2['hash'][:16]}…"
            )
            results["provider_sent_events"] = r2
        except BaseException as exc:
            errors.append(exc)
            raise

    def consume_c_to_p() -> None:
        try:
            print(f"{tag} consuming C->P bytes…")
            r1 = consume_bytes(c_to_p_bytes)
            print(
                f"{tag} C->P bytes done: {r1['totalBytes']}B / "
                f"{r1['chunkCount']} chunks / hash={r1['hash'][:16]}…"
            )
            results["provider_received_bytes"] = r1

            print(f"{tag} consuming C->P events…")
            r2 = consume_events(c_to_p_events)
            print(
                f"{tag} C->P events done: {r2['eventCount']} events / "
                f"hash={r2['hash'][:16]}…"
            )
            results["provider_received_events"] = r2
        except BaseException as exc:
            errors.append(exc)
            raise

    t_produce = threading.Thread(target=produce_p_to_c, name="produce_p_to_c")
    t_consume = threading.Thread(target=consume_c_to_p, name="consume_c_to_p")
    t_produce.start()
    t_consume.start()
    t_produce.join()
    t_consume.join()

    if errors:
        raise errors[0]

    print(f"{tag} report: {json.dumps(results)}")
    return {
        "artifacts": [
            {
                "data": json.dumps(results),
                "mimeType": "application/json",
                "outputId": "report",
            }
        ]
    }
