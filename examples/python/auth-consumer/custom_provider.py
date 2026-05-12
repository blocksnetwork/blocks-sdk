"""
Auth Consumer -- custom token provider mode.

Demonstrates maximum-flexibility authentication using a custom
function. The developer provides an arbitrary function that returns
a fresh token. The SDK calls it on init and before each token expiry.

This mode covers any auth architecture: OAuth2, custom SSO,
multi-tenant routing, or wrapping a non-standard proxy endpoint
that requires custom headers or credentials.

Usage:
    python custom_provider.py

Environment variables:
    BLOCKS_API_KEY  -- Blocks API key (used by the custom provider function)
    BLOCKS_CDM_URL  -- CDM config URL (optional)
"""

from __future__ import annotations

import json
import os
import ssl
import sys
import urllib.request

import certifi
from dotenv import load_dotenv

load_dotenv()

from blocks_network import TaskClient
from blocks_network.consumer_auth import TokenResult

api_key = os.environ.get("BLOCKS_API_KEY")
if not api_key:
    print("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.", file=sys.stderr)
    sys.exit(1)

def _fetch_token() -> TokenResult:
    """Custom token acquisition function.

    In a real application, this would call your own auth service,
    OAuth2 provider, or custom proxy endpoint. Here we demonstrate
    the pattern by calling the Blocks consumer-token endpoint directly
    (in production, this would be behind your own proxy).
    """
    backend_url = os.environ.get(
        "BLOCKS_BACKEND_URL", "https://api.blocksnetwork.io"
    )
    url = f"{backend_url}/api/v1/auth/agent/consumer-token"
    payload = json.dumps({"apiKey": api_key}).encode("utf-8")

    ssl_ctx = ssl.create_default_context(cafile=certifi.where())
    req = urllib.request.Request(
        url,
        data=payload,
        headers={
            "Content-Type": "application/json",
            "X-Custom-Header": "custom-value",
        },
        method="POST",
    )

    with urllib.request.urlopen(req, context=ssl_ctx, timeout=30) as resp:
        data = json.loads(resp.read().decode("utf-8"))

    return TokenResult(
        token=data["accessToken"],
        expires_in=data.get("expiresIn", 60),
        user_id=data.get("userId"),
    )


def _on_auth_error(err: Exception) -> None:
    print(f"Auth refresh failed permanently: {err}", file=sys.stderr)


def main() -> None:
    # Mode 3: Custom provider -- you control the entire token acquisition.
    client = TaskClient.create(
        billing_mode="free",
        token_provider=_fetch_token,
        on_auth_error=_on_auth_error,
    )

    print("Authenticated with custom provider. Sending task...")

    # owner_id is omitted: the server validates it against the
    # authenticated user behind the API key, so the SDK's default
    # (derived from ConsumerAuth identity) is the only safe value.
    session = client.send_message(
        agent_name="echo",
        request_parts=[{"partId": "text", "text": "Hello from custom auth provider!"}],
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
