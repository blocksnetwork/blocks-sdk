"""
Stream Consumer -- submit a pipe task, discover and consume a stream.

Demonstrates the pipe-task streaming consumer flow:
  1. Create a TaskClient with API key authentication
  2. Send a pipe task via send_message() with task_kind and duration
  3. Discover the stream via wait_for_stream()
  4. Open the stream and iterate decoded events via stream.events()
  5. Close the session after the stream ends

Usage:
    python main.py

Environment variables:
    BLOCKS_API_KEY  -- Blocks API key (required)
    BLOCKS_CDM_URL  -- CDM config URL (optional, defaults to production CDN)
"""

from __future__ import annotations

import os
import sys

from dotenv import load_dotenv

load_dotenv()

from blocks_network import TaskClient

api_key = os.environ.get("BLOCKS_API_KEY")
if not api_key:
    print("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.", file=sys.stderr)
    sys.exit(1)

def main() -> None:
    client = TaskClient.create(
        billing_mode="free",
        api_key=api_key,
    )

    print("Sending pipe task to echo_stream agent...")

    # owner_id is omitted on purpose: the server validates it against the
    # authenticated user behind the API key, so the SDK's default (derived
    # from ConsumerAuth identity in TaskClient) is the only value that
    # passes the "ownerId must match authenticated user" check.
    session = client.send_message(
        agent_name="echo_stream",
        task_kind="pipe",
        duration=1,
        request_parts=[{"partId": "text", "text": "Stream this text back to me."}],
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

    # echo-stream emits `format: "events"` (see its agent-card.json
    # `streams._default.format`). `stream.events()` yields one decoded
    # event per yield, flattening any producer-side batching. For
    # `format: "bytes"` streams, prefer `stream.bytes()` instead. The
    # lower-level `stream.inbound` iterator yields raw wire envelopes
    # (with `seq`, `ts`, `encoding`) — reach for it only when you need
    # that metadata.
    chunk_count = 0
    for event in stream.events():
        chunk_count += 1
        # echo-stream writes string events; other agents may write dicts.
        text = event if isinstance(event, str) else str(event)
        print(f"[chunk {chunk_count}] {text}")

    print("--- Stream ended ---")

    session.close()
    client.destroy()


if __name__ == "__main__":
    main()
