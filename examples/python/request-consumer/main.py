"""
Request Consumer -- submit a request task, wait for terminal, download artifacts.

Demonstrates the simplest consumer flow:
  1. Create a TaskClient with API key authentication
  2. Send a request task via send_message()
  3. Wait for the terminal event
  4. List and download artifacts from the completed task
  5. Clean up with session.close() and client.destroy()

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
    print("BLOCKS_API_KEY not set. Run 'blocks publish' or 'blocks login --write-env' first.", file=sys.stderr)
    sys.exit(1)

def main() -> None:
    client = TaskClient.create(
        billing_mode="free",
        api_key=api_key,
    )

    print("Sending request task to echo agent...")

    # owner_id is omitted: the server validates it against the
    # authenticated user behind the API key, so the SDK's default
    # (derived from ConsumerAuth identity) is the only safe value.
    session = client.send_message(
        agent_name="echo",
        request_parts=[{"partId": "text", "text": "Hello from the request consumer!"}],
    )

    print(f"Task created: {session.task_id}")

    session.on_progress(lambda event: print(f"Progress: {event}"))
    session.on_artifact(lambda event: print(f"Artifact event: {event}"))

    try:
        terminal = session.wait_for_terminal(timeout=60)
        print(f"Terminal: {terminal}")
    except TimeoutError:
        print("Timed out waiting for terminal event.", file=sys.stderr)
        session.close()
        client.destroy()
        sys.exit(1)

    artifacts = session.list_artifacts()
    print(f"Artifacts found: {len(artifacts)}")

    for ref in artifacts:
        try:
            downloaded = session.download_artifact(ref)
            text = downloaded.data.decode("utf-8", errors="replace")
            print(f"Downloaded artifact ({downloaded.mime_type}): {text}")
        except Exception as err:
            print(f"Failed to download artifact: {err}", file=sys.stderr)

    session.close()
    client.destroy()


if __name__ == "__main__":
    main()
