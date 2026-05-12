"""
Connect Consumer -- connect to an existing task and inspect its state.

Demonstrates reconnecting to a task that is already in progress or
has completed:
  1. Create a TaskClient with API key authentication
  2. Connect to an existing task by task_id via connect()
  3. For terminal tasks: list streams, list artifacts, download results
  4. For active tasks: receive live events through callbacks

Usage:
    python main.py <taskId>

Environment variables:
    BLOCKS_API_KEY  -- Blocks API key (required)
    BLOCKS_CDM_URL  -- CDM config URL (optional, defaults to production CDN)
"""

from __future__ import annotations

import os
import sys
import threading

from dotenv import load_dotenv

load_dotenv()

from blocks_network import TaskClient

api_key = os.environ.get("BLOCKS_API_KEY")
if not api_key:
    print("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.", file=sys.stderr)
    sys.exit(1)

if len(sys.argv) < 2:
    print("Usage: python main.py <taskId>", file=sys.stderr)
    sys.exit(1)

task_id = sys.argv[1]


def main() -> None:
    client = TaskClient.create(
        billing_mode="free",
        api_key=api_key,
    )

    print(f"Connecting to task: {task_id}")

    session = client.connect(task_id)

    print(f"Connected. State: {session.state}")

    # List streams discovered from history
    streams = session.list_streams()
    print(f"Streams found: {len(streams)}")
    for ref in streams:
        print(
            f"  Stream: {ref.descriptor.stream_id} "
            f"({ref.descriptor.local_direction})"
        )

    # List artifacts discovered from history
    artifacts = session.list_artifacts()
    print(f"Artifacts found: {len(artifacts)}")

    for ref in artifacts:
        try:
            downloaded = session.download_artifact(ref)
            text = downloaded.data.decode("utf-8", errors="replace")
            preview = text[:200] + ("..." if len(text) > 200 else "")
            print(f"  Artifact ({downloaded.mime_type}): {preview}")
        except Exception as err:
            print(f"  Download failed: {err}", file=sys.stderr)

    # For active tasks, register live event callbacks and wait
    if not session.is_closed:
        print("\nTask is active. Listening for live events...")

        session.on_progress(lambda event: print(f"Progress: {event}"))
        session.on_artifact(lambda event: print(f"Artifact: {event}"))

        session.on_stream(lambda ref: _consume_stream_async(ref))

        try:
            terminal = session.wait_for_terminal(timeout=300)
            print(f"Terminal: {terminal}")
        except TimeoutError:
            print("Timed out waiting for terminal.", file=sys.stderr)

    session.close()
    client.destroy()
    print("Done.")


def _consume_stream(stream_ref) -> None:
    """Open and consume a discovered stream.

    Generic consumer: branches on ``stream_ref.descriptor.format`` so we
    exercise the right decoded iterator for each stream shape:

    * ``format: "bytes"`` -> ``stream.bytes()`` yields decoded ``bytes``
      per chunk.
    * ``format: "events"`` -> ``stream.events()`` yields one event per
      yield.

    Iterating ``stream.inbound`` directly would yield raw wire envelopes
    (``InboundMessage`` objects with ``data``, ``encoding``, ``seq``,
    ``ts``) -- reach for it only when you need that metadata.
    """
    descriptor = stream_ref.descriptor
    print(f"Live stream: {descriptor.stream_id} (format={descriptor.format})")
    stream = stream_ref.open()

    if descriptor.format == "bytes":
        total = 0
        for chunk in stream.bytes():
            total += len(chunk)
            print(f"[stream:bytes] +{len(chunk)}B (total {total}B)")
    else:
        for ev in stream.events():
            text = ev if isinstance(ev, str) else str(ev)
            print(f"[stream:events] {text}")


def _consume_stream_async(stream_ref) -> None:
    """Hand stream consumption off the listener thread."""
    thread = threading.Thread(
        target=_consume_stream,
        args=(stream_ref,),
        daemon=True,
    )
    thread.start()


if __name__ == "__main__":
    main()
