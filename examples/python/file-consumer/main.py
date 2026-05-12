"""
File Consumer -- submit a task with file input, receive and download artifacts.

Demonstrates file exchange between consumer and agent:
  1. Create a TaskClient with API key authentication
  2. Send a task with a file attachment in request_parts
  3. Wait for the terminal event
  4. List and download all artifacts (inline and file-based)

Usage:
    python main.py [/path/to/input-file.txt]

Environment variables:
    BLOCKS_API_KEY  -- Blocks API key (required)
    BLOCKS_CDM_URL  -- CDM config URL (optional, defaults to production CDN)
"""

from __future__ import annotations

import os
import sys

from dotenv import load_dotenv

load_dotenv()

from blocks_network import TaskClient, SendMessageRequestPart

api_key = os.environ.get("BLOCKS_API_KEY")
if not api_key:
    print("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.", file=sys.stderr)
    sys.exit(1)

def main() -> None:
    client = TaskClient.create(
        billing_mode="free",
        api_key=api_key,
    )

    # Build request parts: text part and a file part.
    parts = [
        {"partId": "text", "text": "Process this file and return results."},
    ]

    file_path = sys.argv[1] if len(sys.argv) > 1 else None
    if file_path and os.path.isfile(file_path):
        with open(file_path, "rb") as f:
            file_data = f.read()
        file_name = os.path.basename(file_path)
        print(f"Attaching file: {file_name} ({len(file_data)} bytes)")
        parts.append(
            SendMessageRequestPart(
                part_id="input_file",
                file=file_data,
                file_name=file_name,
                content_type="application/octet-stream",
            )
        )
    else:
        # Use a small inline sample when no file is provided
        sample_data = b"Sample file content for the agent to process."
        parts.append(
            SendMessageRequestPart(
                part_id="input_file",
                file=sample_data,
                file_name="sample.txt",
                content_type="text/plain",
            )
        )

    print("Sending task with file attachment to echo agent...")

    # owner_id is omitted: the server validates it against the
    # authenticated user behind the API key, so the SDK's default
    # (derived from ConsumerAuth identity) is the only safe value.
    session = client.send_message(
        agent_name="echo",
        request_parts=parts,
    )

    print(f"Task created: {session.task_id}")

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
    print(f"\nTotal artifacts: {len(artifacts)}")

    for ref in artifacts:
        mime = getattr(ref, "mimeType", getattr(ref, "mime_type", "unknown"))
        kind = getattr(ref, "kind", "unknown")
        print(f"\nArtifact: kind={kind}, mimeType={mime}")

        try:
            downloaded = session.download_artifact(ref)
            print(f"  Downloaded: {downloaded.mime_type}, {len(downloaded.data)} bytes")
            if downloaded.file_name:
                print(f"  File name: {downloaded.file_name}")
            # Print text content for text artifacts
            if downloaded.mime_type and downloaded.mime_type.startswith("text/"):
                text = downloaded.data.decode("utf-8", errors="replace")
                preview = text[:200] + ("..." if len(text) > 200 else "")
                print(f"  Content: {preview}")
        except Exception as err:
            print(f"  Download failed: {err}", file=sys.stderr)

    session.close()
    client.destroy()


if __name__ == "__main__":
    main()
