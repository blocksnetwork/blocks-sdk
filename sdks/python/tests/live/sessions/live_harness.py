"""Session symbols are imported inside tests, so collection works before the SDK has them."""

from __future__ import annotations

import base64
import json
import os
import time
import urllib.parse
import urllib.request
from typing import Any, Callable, Dict, List, TypeVar

import pytest

import blocks_network
from blocks_network.agent_registry import remove_agent

REQUIRED_ENV = ("BLOCKS_API_KEY", "BLOCKS_BACKEND_URL")
LIVE = os.environ.get("PUBNUB_LIVE_TEST") == "1" and all(os.environ.get(n) for n in REQUIRED_ENV)

# The suite-wide autouse fixture overwrites BLOCKS_API_KEY; read the real one at import.
LIVE_API_KEY = os.environ.get("BLOCKS_API_KEY", "")

live_only = pytest.mark.skipif(
    not LIVE, reason=f"needs PUBNUB_LIVE_TEST=1 and {', '.join(REQUIRED_ENV)}"
)
# Same gate as the Node harness's sessionsFlagOn: the backend has sessions on.
sessions_on = pytest.mark.skipif(
    os.environ.get("SESSIONS_FLAG_ON") != "1", reason="needs SESSIONS_FLAG_ON=1"
)

# The consumer client ships separately from the provider runtime; skip until the installed SDK has it.
has_client = hasattr(blocks_network, "BlocksClient")
needs_client = pytest.mark.skipif(
    not has_client, reason="the installed blocks_network has no BlocksClient yet"
)

PROTOCOL_VERSION = "2026-05-01"
ROUTE_WAIT_S = 30.0

T = TypeVar("T")


def base_url() -> str:
    url = os.environ["BLOCKS_BACKEND_URL"].rstrip("/")
    # Only http(s): urlopen would otherwise follow file:// or a custom scheme.
    if urllib.parse.urlsplit(url).scheme not in ("http", "https"):
        raise ValueError(f"BLOCKS_BACKEND_URL must be http(s), got {url!r}")
    return url


def make_card(agent_name: str) -> Dict[str, Any]:
    """Python twin of the Node tests/helpers/test-card.ts makeTestCard."""
    return {
        "identity": {
            "agentName": agent_name,
            "displayName": "Test Agent",
            "description": "Minimal test agent card",
            "version": "1.0.0",
            "provider": {"organization": "test-org"},
        },
        "capabilities": {"taskKinds": ["request"]},
        "tags": [{"id": "main", "name": "Main", "description": "Test tag"}],
        "runtime": {"handler": "index.js"},
        "streams": {"_default": {"direction": "outbound", "format": "bytes"}},
    }


def publish_agent(prefix: str, created: List[str]) -> str:
    agent_name = f"{prefix}_{int(time.time() * 1000)}"
    body = {
        "agentName": agent_name,
        "card": make_card(agent_name),
        "billingMode": "free",
        # Private, so a run interrupted before cleanup leaves no public test agent.
        "listing": "private",
        "protocolVersions": [PROTOCOL_VERSION],
        "preferredProtocolVersion": PROTOCOL_VERSION,
    }
    request = urllib.request.Request(
        f"{base_url()}/api/v1/registry/agents",
        data=json.dumps(body).encode(),
        method="POST",
        headers={
            "Content-Type": "application/json",
            "Blocks-Protocol-Version": PROTOCOL_VERSION,
            "Authorization": f"Bearer {LIVE_API_KEY}",
        },
    )
    with urllib.request.urlopen(request, timeout=30):  # nosec B310 - the suite's configured backend URL
        pass
    created.append(agent_name)
    return agent_name


def remove_agents(names: List[str]) -> None:
    for name in names:
        try:
            remove_agent(name, base_url=base_url())
        except Exception as err:
            print(f"[cleanup] remove_agent({name}) failed: {err}")


PROGRESS_MESSAGE = "working"


def echo_result(exchange: Any) -> Dict[str, Any]:
    exchange.report_status(PROGRESS_MESSAGE)
    text = "".join(_field(p, "text") or "" for p in exchange.request_parts)
    payload = {"exchangeId": exchange.exchange_id, "text": text, "sdk": "python"}
    return {"artifacts": [{"data": json.dumps(payload), "mimeType": "application/json"}]}


def decode_echo(ref: Any) -> Dict[str, Any]:
    return json.loads(base64.b64decode(_field(ref, "data")))


def when_routed(attempt: Callable[[], T]) -> T:
    """Retry ``attempt`` while the provider's route is not yet open."""
    deadline = time.monotonic() + ROUTE_WAIT_S
    while True:
        try:
            return attempt()
        except Exception as err:
            if getattr(err, "code", None) != "SESSION_NO_ROUTE" or time.monotonic() > deadline:
                raise
            time.sleep(0.5)


def state_name(value: Any) -> str:
    return getattr(value, "value", value)


def _field(obj: Any, name: str) -> Any:
    return obj.get(name) if isinstance(obj, dict) else getattr(obj, name, None)
