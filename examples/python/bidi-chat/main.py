"""bidi-chat consumer (Python).

Submits a request to the bidi-chat agent, opens the discovered
bidirectional stream, sends a few lines, reads each agent reply, then
ends the conversation with `bye`.

Exercises the consumer-side stream UUID fix from
https://github.com/pubnub/blocksnetwork/pull/835: when consumer and
provider share the same agent name on their first stream, the
consumer-side StreamClient must derive its publisher UUID from the
consumer's user id (not the provider's agent name) so the self-echo
filter does not silently drop one side's messages.

Usage:
    BLOCKS_BACKEND_URL=http://localhost:3031 \\
    BLOCKS_API_KEY=bk_... \\
        python main.py [line1] [line2] ...
"""

from __future__ import annotations

import os
import sys
import threading
import time
from typing import List, Optional

from dotenv import load_dotenv

load_dotenv()

from blocks_network import TaskClient
from blocks_network.agent_registry import get_agent

AGENT_NAME = "bidi_chat_python"


def main() -> None:
    api_key = os.environ.get("BLOCKS_API_KEY")
    if not api_key:
        print("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.", file=sys.stderr)
        sys.exit(1)

    base_url = os.environ.get("BLOCKS_BACKEND_URL")
    cdm_url = os.environ.get("BLOCKS_CDM_URL")

    entry = get_agent(AGENT_NAME, base_url=base_url, api_key=api_key)
    if entry is None:
        print(f"Agent '{AGENT_NAME}' not found at {base_url}.", file=sys.stderr)
        sys.exit(1)
    billing_mode = entry.billing_mode or "free"
    print(
        f"Registry says {AGENT_NAME} is billing_mode={billing_mode}; "
        f"using {'network' if billing_mode == 'paid' else 'playground'} keyset."
    )

    client = TaskClient.create(
        billing_mode=billing_mode,
        api_key=api_key,
        base_url=base_url,
        cdm_url=cdm_url,
    )

    lines = sys.argv[1:]
    messages_to_send: List[str] = lines if lines else ["ping", "hello there", "how are you"]

    print(f"Sending pipe task to {AGENT_NAME}...")
    session = client.send_message(
        agent_name=AGENT_NAME,
        task_kind="pipe",
        duration=1,
        request_parts=[{"partId": "greeting", "text": "opening greeting"}],
    )
    print(f"Task created: {session.task_id}")

    try:
        stream_ref = session.wait_for_stream(timeout=30.0)
    except TimeoutError:
        print("Timed out waiting for stream.", file=sys.stderr)
        session.close()
        client.destroy()
        sys.exit(1)

    print(
        f"Stream discovered: {stream_ref.descriptor.stream_id} "
        f"({stream_ref.descriptor.local_direction})"
    )

    stream = stream_ref.open()
    print(f"Consumer stream uuid: {stream.uuid}")
    print(f"Stream channel:        {stream.channel}")

    received: List[str] = []
    received_lock = threading.Lock()

    # Producer thread: write messages on a delay so logs interleave readably.
    def producer() -> None:
        for line in messages_to_send:
            print(f"[to agent ] {line}")
            stream.write({"text": line})
            time.sleep(0.3)
        print("[to agent ] bye")
        stream.write({"text": "bye"})

    producer_thread = threading.Thread(target=producer, name="bidi-producer", daemon=True)
    producer_thread.start()

    # Consumer reads on the main thread. Per the SDK contract a
    # bidirectional stream does NOT publish stream_end, so we end our
    # own side once we see the agent's reply to "bye" and break out.
    for event in stream.events():
        text: str
        if isinstance(event, dict) and isinstance(event.get("text"), str):
            text = event["text"]
        elif isinstance(event, str):
            text = event
        else:
            text = str(event)
        with received_lock:
            received.append(text)
        print(f"[from agent] {text}")
        if "BYE" in text.upper().split():
            stream.end()
            break

    print("--- inbound iterator closed ---")
    producer_thread.join(timeout=5.0)

    print("\n--- Awaiting final artifact ---")

    # Pipe tasks do not auto-terminal when the handler returns, so we
    # only wait briefly for the artifact event the handler published.
    artifact_seen = threading.Event()

    def on_artifact(event: object) -> None:
        artifact_seen.set()
        data: Optional[object] = getattr(event, "data", None)
        if data is None and isinstance(event, dict):
            data = event.get("data")
        print(f"Artifact: {data if isinstance(data, str) else repr(event)}")

    session.on_artifact(on_artifact)
    artifact_seen.wait(timeout=5.0)

    print(f"\nReceived {len(received)} message(s) from agent.")
    if not received:
        print(
            "FAIL: bidirectional read returned 0 messages — likely consumer/provider UUID collision.",
            file=sys.stderr,
        )
        session.close()
        client.destroy()
        sys.exit(2)

    session.close()
    client.destroy()
    print("Done.")


if __name__ == "__main__":
    main()
