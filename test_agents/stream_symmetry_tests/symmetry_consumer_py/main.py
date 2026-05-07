"""BLOCKS-262 symmetry test consumer — Python.

Mirror of symmetry_consumer/index.ts. Sends a 1-minute pipe task to
symmetry_provider_py, opens all four streams, runs the same shared
helpers as the provider, and prints PASS/FAIL based on whether the four
hash pairs (P->C bytes/events, C->P bytes/events) match between sides.
"""

from __future__ import annotations

import json
import os
import sys
import threading
import time
from pathlib import Path
from typing import Any, Dict, List

from dotenv import load_dotenv

load_dotenv()

# Reach the sibling symmetry_shared/ directory so this driver loads the
# SAME helpers and payloads the handler uses. Symmetry-by-construction.
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

from blocks_network import TaskClient, decode_inline_artifact  # noqa: E402


api_key = os.environ.get("BLOCKS_API_KEY")
if not api_key:
    print("BLOCKS_API_KEY not set", file=sys.stderr)
    sys.exit(1)

AGENT_NAME = os.environ.get("AGENT_NAME", "symmetry_provider_py")
# Pipe-task duration is integer minutes per the Python SDK validation
# (TaskClient.send_message: not isinstance(duration, int) is rejected).
DURATION_MIN = int(os.environ.get("DURATION_MIN", "1"))
PUBLISH_GRACE_S = 2.0
DEADLINE_BUFFER_S = 2.0


def main() -> int:
    client = TaskClient.create(billing_mode="free", api_key=api_key)

    print(f"Sending pipe task to {AGENT_NAME} (duration={DURATION_MIN} min)")
    # owner_id is omitted: the server validates it against the authenticated
    # user behind the API key, so the SDK's default (derived from
    # ConsumerAuth identity, see TaskClient line 851) is the only safe value.
    session = client.send_message(
        agent_name=AGENT_NAME,
        task_kind="pipe",
        duration=DURATION_MIN,
        request_parts=[{"partId": "request", "data": {}}],
    )
    print(f"Task created: {session.task_id}")

    deadline = time.time() + DURATION_MIN * 60.0 - DEADLINE_BUFFER_S
    consumer_sent: Dict[str, dict] = {}
    consumer_received: Dict[str, dict] = {}
    stream_threads: List[threading.Thread] = []
    provider_report_holder: List[str] = []

    def on_stream(stream_ref: Any) -> None:
        name = stream_ref.descriptor.declared_stream
        local_dir = stream_ref.descriptor.local_direction
        fmt = stream_ref.descriptor.format
        print(f"[stream] {name} ({local_dir}/{fmt})")
        stream = stream_ref.open()

        def _consume_p_to_c_bytes() -> None:
            r = consume_bytes(stream)
            consumer_received["bytes"] = r
            print(
                f"[p_to_c_bytes] got {r['totalBytes']}B / "
                f"{r['chunkCount']} chunks / hash={r['hash'][:16]}…"
            )

        def _consume_p_to_c_events() -> None:
            r = consume_events(stream)
            consumer_received["events"] = r
            print(
                f"[p_to_c_events] got {r['eventCount']} events / "
                f"hash={r['hash'][:16]}…"
            )

        def _produce_c_to_p_bytes() -> None:
            sleep(PUBLISH_GRACE_S)
            if time.time() > deadline:
                print("[c_to_p_bytes] deadline reached before publishing; ending")
                stream.end()
                return
            r = produce_bytes(stream, list(BYTES_VARIANTS))
            consumer_sent["bytes"] = r
            print(
                f"[c_to_p_bytes] sent {r['totalBytes']}B / "
                f"{r['chunkCount']} writes / hash={r['hash'][:16]}…"
            )

        def _produce_c_to_p_events() -> None:
            sleep(PUBLISH_GRACE_S)
            if time.time() > deadline:
                print("[c_to_p_events] deadline reached before publishing; ending")
                stream.end()
                return
            r = produce_events(stream, list(EVENTS_VARIANTS))
            consumer_sent["events"] = r
            print(
                f"[c_to_p_events] sent {r['eventCount']} events / "
                f"hash={r['hash'][:16]}…"
            )

        targets = {
            "p_to_c_bytes": _consume_p_to_c_bytes,
            "p_to_c_events": _consume_p_to_c_events,
            "c_to_p_bytes": _produce_c_to_p_bytes,
            "c_to_p_events": _produce_c_to_p_events,
        }
        target = targets.get(name)
        if target is None:
            print(f"[stream] unexpected stream '{name}'")
            return

        t = threading.Thread(target=target, name=f"stream-{name}", daemon=True)
        t.start()
        stream_threads.append(t)

    session.on_stream(on_stream)

    def on_artifact(event: Any) -> None:
        ref = event.artifact_ref
        try:
            if ref.kind == "inline" and ref.data:
                data = decode_inline_artifact(ref).decode("utf-8")
            else:
                dl = session.download_artifact(ref)
                data = dl.data.decode("utf-8")
            provider_report_holder.append(data)
            print(f"[artifact] received provider report ({len(data)}B)")
        except Exception as exc:
            print(f"[artifact] decode failed: {exc}", file=sys.stderr)

    session.on_artifact(on_artifact)

    terminal = threading.Event()
    terminal_state_holder: List[str] = []

    def on_terminal(event: Any) -> None:
        terminal_state_holder.append(event.state)
        terminal.set()

    session.on_terminal(on_terminal)

    waited = terminal.wait(timeout=DURATION_MIN * 60.0 + 30.0)
    if not waited:
        print("FAIL: terminal event never fired", file=sys.stderr)
        session.close()
        client.destroy()
        return 1

    state = terminal_state_holder[0] if terminal_state_holder else "?"
    print(f"\n[terminal] {state}")

    for t in stream_threads:
        t.join(timeout=10.0)

    if not provider_report_holder:
        print("FAIL: no provider report received", file=sys.stderr)
        session.close()
        client.destroy()
        return 1

    try:
        provider_report = json.loads(provider_report_holder[0])
    except json.JSONDecodeError as exc:
        print(f"FAIL: provider report not valid JSON: {exc}", file=sys.stderr)
        session.close()
        client.destroy()
        return 1

    checks = [
        (
            "P->C bytes ",
            provider_report["provider_sent_bytes"]["hash"],
            (consumer_received.get("bytes") or {}).get("hash"),
        ),
        (
            "P->C events",
            provider_report["provider_sent_events"]["hash"],
            (consumer_received.get("events") or {}).get("hash"),
        ),
        (
            "C->P bytes ",
            (consumer_sent.get("bytes") or {}).get("hash"),
            provider_report["provider_received_bytes"]["hash"],
        ),
        (
            "C->P events",
            (consumer_sent.get("events") or {}).get("hash"),
            provider_report["provider_received_events"]["hash"],
        ),
    ]

    print("\nHash comparison (expected = sender, actual = receiver):")
    pass_ = True
    for name, expected, actual in checks:
        ok = bool(expected) and bool(actual) and expected == actual
        mark = "✓" if ok else "✗"
        exp_s = (expected[:16] + "…") if expected else "MISSING"
        act_s = (actual[:16] + "…") if actual else "MISSING"
        print(f"  {mark} {name}  expected={exp_s}  actual={act_s}")
        if not ok:
            pass_ = False

    print("\n" + ("SYMMETRY TEST PASSED" if pass_ else "SYMMETRY TEST FAILED"))

    session.close()
    client.destroy()
    return 0 if pass_ else 1


if __name__ == "__main__":
    code = main()
    # PubNub's Python SDK leaves non-daemon background threads alive even
    # after pubnub.stop() / session.close() / client.destroy(), so a normal
    # `sys.exit` hangs until those threads time out. Force-terminate the
    # process now that all our work is done and the result is reported.
    os._exit(code)
