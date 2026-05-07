"""
Auth Consumer -- API key mode.

Demonstrates server-side consumer authentication using an API key.
The SDK exchanges the API key for a consumer JWT via the Blocks
backend and manages token refresh transparently.

This is the recommended mode for backend services, scripts, and
cron jobs where the API key can be stored securely.

Usage:
    python api_key.py

Environment variables:
    BLOCKS_API_KEY  -- Blocks API key (required)
    BLOCKS_CDM_URL  -- CDM config URL (optional)
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

def _on_auth_error(err: Exception) -> None:
    print(f"Auth refresh failed permanently: {err}", file=sys.stderr)


def main() -> None:
    # Mode 1: API key -- the SDK handles JWT acquisition and refresh.
    client = TaskClient.create(
        billing_mode="free",
        api_key=api_key,
        on_auth_error=_on_auth_error,
    )

    print("Authenticated with API key. Sending task...")

    # owner_id is omitted: the server validates it against the
    # authenticated user behind the API key, so the SDK's default
    # (derived from ConsumerAuth identity) is the only safe value.
    session = client.send_message(
        agent_name="echo",
        request_parts=[{"partId": "text", "text": "Hello from API key auth!"}],
    )

    print(f"Task created: {session.task_id}")

    try:
        terminal = session.wait_for_terminal(timeout=60)
        print(f"Terminal: {terminal}")
    except TimeoutError:
        print("Timed out waiting for terminal.", file=sys.stderr)
    finally:
        session.close()
        client.destroy()


if __name__ == "__main__":
    main()
