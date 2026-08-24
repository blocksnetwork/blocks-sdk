"""Regression: AgentAuth + TaskClient.send_message cross-SDK parity.

Mirrors sdks/node/tests/agent-auth-task-client.test.ts for cross-SDK parity
Verifies that:
1. The T4 readToken from the RPC response is applied to the per-session PubNub
   via set_token() -- the smoking gun.
2. The SDK subscribes to the backend-named status channel.
3. Artifact and terminal events dispatched on the channel reach on_artifact /
   wait_for_terminal callbacks.

Mock approach mirrors test_task_client.py: urllib.request.urlopen is patched
in the relevant modules; the session PubNub is intercepted via the
create_session_pubnub factory kwarg.
"""

from __future__ import annotations

import json
import threading
import time
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.agent_auth import AgentAuth
from blocks_network.task_client import TaskClient

# ---------------------------------------------------------------------------
# Named constants (mirrors the Node test)
# ---------------------------------------------------------------------------

TEST_API_KEY = "bk_test-api-key-blocks-111"
TEST_BASE_URL = "http://localhost:3001"
TEST_SUBSCRIBE_KEY = "sub-c-test"

TASK_ID = "task-blocks-111"
CALLER_ORG_ID = "org-caller"
READ_TOKEN = "T4-read-token-blocks-111"
STATUS_CHANNEL = f"u.{CALLER_ORG_ID}.{TASK_ID}"

# ---------------------------------------------------------------------------
# Fake PubNub factory
# ---------------------------------------------------------------------------


def create_fake_pubnub():
    """Create a fake PubNub that tracks set_token calls, listeners, and channels.

    Mirrors the helper in test_task_client.py, extended with set_token tracking.
    The subscribe builder uses .with_timetoken().execute() (send_message path).
    """
    listeners: list = []
    subscribed_channels: list = []
    set_token_calls: list = []

    pn = MagicMock()

    pn.set_token = MagicMock(side_effect=lambda tok: set_token_calls.append(tok))
    pn.add_listener = MagicMock(side_effect=lambda l: listeners.append(l))
    pn.remove_listener = MagicMock()

    def _make_subscribe_builder():
        builder = MagicMock()

        def _channels(chs):
            subscribed_channels.extend(chs)
            return builder

        builder.channels = _channels
        builder.with_timetoken.return_value = builder
        builder.execute = MagicMock()
        return builder

    pn.subscribe = _make_subscribe_builder

    def _make_unsubscribe_builder():
        builder = MagicMock()

        def _channels(chs):
            return builder

        builder.channels = _channels
        builder.execute = MagicMock()
        return builder

    pn.unsubscribe = _make_unsubscribe_builder

    # time().sync() -> object with .result.timetoken
    time_result = MagicMock()
    time_result.result.timetoken = "17000000000000000"
    pn.time.return_value.sync.return_value = time_result

    # fetch_messages() builder used by _fetch_and_parse_history
    def _make_fetch_builder():
        builder = MagicMock()
        _ch = [None]

        def _channels(chs):
            _ch[0] = chs[0] if chs else None
            return builder

        builder.channels = _channels
        builder.maximum_per_channel.return_value = builder
        builder.start.return_value = builder

        def _sync():
            ch = _ch[0]
            result = MagicMock()
            result.result.channels = {ch: []} if ch else {}
            return result

        builder.sync = _sync
        return builder

    pn.fetch_messages = _make_fetch_builder

    return {
        "pubnub": pn,
        "listeners": listeners,
        "subscribed_channels": subscribed_channels,
        "set_token_calls": set_token_calls,
    }


# ---------------------------------------------------------------------------
# Mock HTTP responses
# ---------------------------------------------------------------------------


def _make_urlopen_router():
    """Return a side_effect function routing by URL.

    Routes:
      POST /api/v1/auth/agent/connect -> AgentAuth.init() response
      POST /api/v1/rpc               -> SendMessage JSON-RPC response
    """
    connect_body = json.dumps({
        "agentName": "transplant_web_caller",
        "name": "transplant_web_caller",
        "accessToken": "jwt-agent-token",
        "refreshToken": "rt-agent-token",
        "expiresIn": 3600,
    }).encode()

    rpc_body = json.dumps({
        "jsonrpc": "2.0",
        "id": "x",
        "result": {
            "taskId": TASK_ID,
            "orgId": CALLER_ORG_ID,
            "idempotent": False,
            "queued": False,
            "state": "pending",
            "extensions": {
                "blocks": {
                    "streamChannels": {"status": STATUS_CHANNEL},
                    "readToken": READ_TOKEN,
                    "subscribeKey": TEST_SUBSCRIBE_KEY,
                    "publishKey": "pub-c-test",
                }
            },
        },
    }).encode()

    def _make_resp(body: bytes, status: int = 200):
        resp = MagicMock()
        resp.read.return_value = body
        resp.status = status
        resp.getheader = MagicMock(return_value=None)
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        return resp

    def _router(req, **kwargs):
        url = req.full_url
        if "/auth/agent/connect" in url:
            return _make_resp(connect_body, 201)
        if "/rpc" in url:
            return _make_resp(rpc_body)
        raise AssertionError(f"Unexpected URL in test: {url}")

    return _router


# ---------------------------------------------------------------------------
# Helper: push a fake event through the buf_listener added to the PubNub
# ---------------------------------------------------------------------------


def _push_event(fake: dict, channel: str, message: dict, timetoken: str = "17000000000001000") -> None:
    """Deliver a fake PubNub message event to all registered listeners.

    send_message() registers a _BufferListener via add_listener. After session
    construction the listener's _dispatch_fn is set. Calling .message() on the
    listener object dispatches the event through the session's _handle_event.

    Asserts SDK preconditions before dispatching so a regression where the SDK
    skips add_listener or subscribe(...).channels(...) surfaces here, rather
    than silently as "expected event not delivered".
    """
    assert fake["listeners"], (
        "Python SDK did not register any PubNub listeners — regression"
    )
    assert channel in fake["subscribed_channels"], (
        f"Python SDK did not subscribe to {channel!r} — regression"
    )

    event = MagicMock()
    event.channel = channel
    event.message = message
    event.timetoken = timetoken

    for listener in fake["listeners"]:
        if hasattr(listener, "message"):
            listener.message(None, event)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestAgentAuthTaskClientBlocks111:
    """Regression: AgentAuth + TaskClient.send_message parity."""

    def _setup(self):
        """Returns an AgentAuth post-init with a fake PubNub."""
        fake = create_fake_pubnub()

        auth = AgentAuth(api_key=TEST_API_KEY, base_url=TEST_BASE_URL)
        with patch("urllib.request.urlopen", side_effect=_make_urlopen_router()):
            auth.init(registration_payload={
                "agentName": "transplant_web_caller",
                "instanceId": "AG-transplant_web_caller-nextjs",
                "billingMode": "free",
                "expectedInstances": 1,
                "concurrency": 1,
                "sdkLanguage": "Python",
            })

        return auth, fake

    def test_read_token_applied_subscribes_to_status_channel_and_fires_on_artifact(self):
        """Applies readToken via set_token, subscribes to status channel, fires on_artifact.

        Smoking-gun: without set_token(readToken) on the per-session PubNub,
        PubNub PAM rejects the subscribe and on_artifact silently never fires.
        """
        auth, fake = self._setup()

        client = TaskClient(
            subscribe_key=TEST_SUBSCRIBE_KEY,
            billing_mode="free",
            base_url=TEST_BASE_URL,
            agent_auth=auth,
            create_session_pubnub=lambda: fake["pubnub"],
        )

        with patch("blocks_network.rpc_client.urllib.request.urlopen", side_effect=_make_urlopen_router()):
            session = client.send_message(
                agent_name="turkish_hair_transplant",
                request_parts=[{"type": "text", "text": "Hello"}],
                owner_id="user-caller",
            )

        # SMOKING-GUN ASSERTION: readToken must be applied to the per-session PubNub.
        assert fake["set_token_calls"] == [READ_TOKEN]

        # SDK must subscribe to the server-provided status channel.
        assert STATUS_CHANNEL in fake["subscribed_channels"]

        # Register artifact callback BEFORE pushing event (tests live-dispatch path).
        artifact_events: list = []
        session.on_artifact(lambda e: artifact_events.append(e))

        artifact_ref = {
            "kind": "inline",
            "partId": "transplant-preview",
            "mimeType": "image/png",
        }
        _push_event(fake, STATUS_CHANNEL, {
            "type": "artifact",
            "taskId": TASK_ID,
            "artifactRef": artifact_ref,
        })

        assert len(artifact_events) == 1
        assert artifact_events[0].raw.get("artifactRef") == artifact_ref

        session.close()

    def test_wait_for_terminal_resolves_and_fires_on_terminal(self):
        """wait_for_terminal() resolves and on_terminal fires when a terminal event arrives."""
        auth, fake = self._setup()

        client = TaskClient(
            subscribe_key=TEST_SUBSCRIBE_KEY,
            billing_mode="free",
            base_url=TEST_BASE_URL,
            agent_auth=auth,
            create_session_pubnub=lambda: fake["pubnub"],
        )

        with patch("blocks_network.rpc_client.urllib.request.urlopen", side_effect=_make_urlopen_router()):
            session = client.send_message(
                agent_name="turkish_hair_transplant",
                request_parts=[{"type": "text", "text": "Hello"}],
                owner_id="user-caller",
            )

        terminal_events: list = []
        session.on_terminal(lambda e: terminal_events.append(e))

        # Start wait_for_terminal BEFORE the event lands (tests live-dispatch path).
        resolved: list = []
        exc_holder: list = []

        def _wait():
            try:
                resolved.append(session.wait_for_terminal(timeout=2.0))
            except Exception as e:
                exc_holder.append(e)

        waiter = threading.Thread(target=_wait, daemon=True)
        waiter.start()
        # Brief yield so the waiter thread reaches wait_for_terminal() before we
        # _push_event(). Without this the main thread can dispatch the terminal
        # message before the waiter has installed its observer; the SDK's
        # _BufferListener does buffer pre-call messages so the test still
        # passes, but the live-dispatch path is what this regression cares about.
        time.sleep(0.05)

        _push_event(fake, STATUS_CHANNEL, {
            "type": "terminal",
            "taskId": TASK_ID,
            "state": "completed",
        }, timetoken="17000000000002000")

        waiter.join(timeout=3.0)

        assert not exc_holder, f"wait_for_terminal raised: {exc_holder[0]}"
        assert len(resolved) == 1
        assert resolved[0].state == "completed"

        assert len(terminal_events) == 1
        assert terminal_events[0].state == "completed"
