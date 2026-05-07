"""Tests for blocks_network.task_client -- TaskClient and subscribe helpers.

Port of ``sdks/node/tests/taskClient.test.ts``.
"""

from __future__ import annotations

import json
from io import BytesIO
from unittest.mock import MagicMock, call, patch

import pytest

from blocks_network.auth_provider import StaticAuthProvider
from blocks_network.task_client import (
    ListTasksParams,
    SendMessageParams,
    SendMessageRequestPart,
    TaskClient,
    TaskEventCallbacks,
    TaskInfo,
    create_task_client,
    subscribe_to_task,
)
from blocks_network.task_session import TaskSession


# ============================================================================
# Helpers
# ============================================================================


def create_fake_pubnub(history_messages=None, server_timetoken="17000000000000000"):
    """Create a fake PubNub client that tracks listeners and channels.

    Uses the builder pattern matching the real PubNub Python SDK:
    ``pubnub.subscribe().channels([ch]).execute()``

    Parameters
    ----------
    history_messages:
        List of fake history message objects for fetch_messages().
        Each should have ``message`` and ``timetoken`` attributes.
    server_timetoken:
        Timetoken returned by ``time().sync()``.
    """
    listeners: list = []
    subscribed_channels: list = []
    unsubscribed_channels: list = []
    _history = history_messages or []

    pn = MagicMock()

    pn.add_listener = MagicMock(side_effect=lambda l: listeners.append(l))
    pn.remove_listener = MagicMock()
    pn.set_token = MagicMock()

    def _make_subscribe_builder():
        builder = MagicMock()
        def _channels(chs):
            subscribed_channels.extend(chs)
            return builder
        builder.channels = _channels
        return builder

    pn.subscribe = _make_subscribe_builder

    def _make_unsubscribe_builder():
        builder = MagicMock()
        def _channels(chs):
            unsubscribed_channels.extend(chs)
            return builder
        builder.channels = _channels
        return builder

    pn.unsubscribe = _make_unsubscribe_builder

    # time().sync() -> result with .timetoken attribute
    time_result = MagicMock()
    time_result.result.timetoken = server_timetoken
    pn.time.return_value.sync.return_value = time_result

    # fetch_messages().channels([ch]).maximum_per_channel(n).sync() -> result
    fetch_messages_calls: list = []

    def _make_fetch_builder():
        builder = MagicMock()
        _captured_channel = [None]
        fetch_messages_calls.append(True)

        def _channels(chs):
            _captured_channel[0] = chs[0] if chs else None
            return builder

        builder.channels = _channels

        def _sync():
            ch = _captured_channel[0]
            result = MagicMock()
            result.result.channels = {ch: _history} if ch else {}
            return result

        builder.sync = _sync
        builder.maximum_per_channel.return_value = builder
        builder.start.return_value = builder
        return builder

    pn.fetch_messages = _make_fetch_builder

    return {
        "pubnub": pn,
        "listeners": listeners,
        "subscribed_channels": subscribed_channels,
        "unsubscribed_channels": unsubscribed_channels,
        "fetch_messages_calls": fetch_messages_calls,
    }


def _mock_urlopen_response(result):
    """Return a mock urlopen context manager that returns a JSON-RPC result."""
    body = json.dumps({"jsonrpc": "2.0", "id": "x", "result": result}).encode("utf-8")
    resp = MagicMock()
    resp.read.return_value = body
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return resp


# ============================================================================
# sendMessage
# ============================================================================


class TestSendMessage:
    """Tests for TaskClient.send_message() -> TaskSession wiring."""

    @staticmethod
    def _full_response(**overrides):
        """Build a full SendMessage response with extensions.blocks."""
        resp = {
            "taskId": "task-123",
            "idempotent": False,
            "queued": False,
            "extensions": {
                "blocks": {
                    "streamChannels": {"status": "u.user-1.task-123"},
                    "readToken": "T4-read-token",
                }
            },
        }
        resp.update(overrides)
        return resp

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_extracts_t4_and_status_channel_from_response(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[{"type": "text", "text": "Hello"}],
                owner_id="user-1",
            
        )

        assert isinstance(session, TaskSession)
        assert session.task_id == "task-123"
        assert session.idempotent is False
        assert session.queued is False

        # T4 readToken extracted from response
        assert session.read_token == "T4-read-token"

        # statusChannel from response extensions
        assert session.status_channel == "u.user-1.task-123"

        # _create_session_pubnub called with the T4 token
        mock_create_pn.assert_called_once_with("T4-read-token")

        # Eagerly subscribed to the response-provided channel
        assert "u.user-1.task-123" in fake["subscribed_channels"]

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["method"] == "SendMessage"
        assert body["params"]["agentName"] == "agent-b"
        assert body["params"]["ownerId"] == "user-1"

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_falls_back_to_derived_channel_without_extensions(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(
            {"taskId": "task-no-ext", "idempotent": False}
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        # Falls back to derived channel
        assert session.status_channel == "u.user-1.task-no-ext"
        assert session.read_token is None
        # Called with None (no T4)
        mock_create_pn.assert_called_once_with(None)
        assert "u.user-1.task-no-ext" in fake["subscribed_channels"]

        session.close()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_concurrent_sessions_get_independent_pubnub_clients(self, mock_urlopen):
        fake_a = create_fake_pubnub()
        fake_b = create_fake_pubnub()
        session_factory = MagicMock(side_effect=[fake_a["pubnub"], fake_b["pubnub"]])

        mock_urlopen.side_effect = [
            _mock_urlopen_response({
                "taskId": "task-A",
                "extensions": {"blocks": {
                    "streamChannels": {"status": "u.user-1.task-A"},
                    "readToken": "T4-token-A",
                }},
            }),
            _mock_urlopen_response({
                "taskId": "task-B",
                "extensions": {"blocks": {
                    "streamChannels": {"status": "u.user-1.task-B"},
                    "readToken": "T4-token-B",
                }},
            }),
        ]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", create_session_pubnub=session_factory, base_url="http://localhost:3001")
        session1 = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )
        session2 = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        # Two distinct PubNub instances
        assert fake_a["pubnub"] is not fake_b["pubnub"]

        # Each received its own T4 via set_token
        fake_a["pubnub"].set_token.assert_called_once_with("T4-token-A")
        fake_b["pubnub"].set_token.assert_called_once_with("T4-token-B")

        # Each subscribes to its own channel
        assert "u.user-1.task-A" in fake_a["subscribed_channels"]
        assert "u.user-1.task-B" in fake_b["subscribed_channels"]

        # Sessions independent
        assert session1.read_token == "T4-token-A"
        assert session2.read_token == "T4-token-B"

        session1.close()
        session2.close()

    @patch("blocks_network.pubnub_client.create_pubnub_client")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_fallback_uses_internal_create_pubnub_client_without_factory(
        self, mock_urlopen, mock_create_pn
    ):
        """When no create_pubnub factory is provided, _create_session_pubnub
        falls back to the internal create_pubnub_client."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        # No create_pubnub factory
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        # Internal create_pubnub_client was called
        mock_create_pn.assert_called_once()
        call_kwargs = mock_create_pn.call_args[1]
        assert call_kwargs["subscribe_key"] == "sub-c-test"

        # T4 token applied
        fake["pubnub"].set_token.assert_called_once_with("T4-read-token")

        session.close()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_session_close_stops_owned_pubnub(self, mock_urlopen):
        """Session-owned PubNub client is stopped on close()."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        factory = MagicMock(return_value=fake["pubnub"])

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", create_session_pubnub=factory, base_url="http://localhost:3001")
        session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        fake["pubnub"].stop.assert_not_called()
        session.close()
        fake["pubnub"].stop.assert_called_once()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_uses_params_owner_id_over_default(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(taskId="task-456")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            default_owner_id="default-user",
            base_url="http://localhost:3001",
        )
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="override-user",

        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["params"]["ownerId"] == "override-user"

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_includes_optional_fields(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(taskId="server-gen-id")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="test-user",
                idempotency_key="my-dedup-key",
                push_notification_config={"url": "https://example.com/webhook"},
                retry_policy={"maxRetries": 3, "expiresAfterSec": 300},
            
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["params"]["idempotencyKey"] == "my-dedup-key"
        assert "taskId" not in body["params"]
        assert body["params"]["pushNotificationConfig"] == {
            "url": "https://example.com/webhook"
        }
        assert body["params"]["retryPolicy"] == {
            "maxRetries": 3,
            "expiresAfterSec": 300,
        }

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_forwards_idempotency_key_on_wire(self, mock_urlopen, mock_create_pn):
        """idempotencyKey is forwarded as camelCase when set."""
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(taskId="task-idem")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="user-1",
                idempotency_key="dedup-abc",
            
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["params"]["idempotencyKey"] == "dedup-abc"

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_omits_idempotency_key_when_not_set(self, mock_urlopen, mock_create_pn):
        """idempotencyKey is absent from wire when not supplied."""
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(taskId="task-no-key")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="user-1",
            
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert "idempotencyKey" not in body["params"]
        assert "taskId" not in body["params"]

        session.close()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_terminal_idempotent_hit_creates_pre_closed_session(self, mock_urlopen):
        """Terminal idempotent hit (completed) creates a pre-closed session
        with no PubNub subscription."""
        mock_urlopen.return_value = _mock_urlopen_response({
            "taskId": "task-done",
            "idempotent": True,
            "state": "completed",
            "queued": False,
            "extensions": {
                "blocks": {
                    "streamChannels": {"status": "u.user-1.task-done"},
                    "readToken": "T4-done",
                }
            },
        })

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="user-1",
                idempotency_key="already-done",
            
        )

        assert isinstance(session, TaskSession)
        assert session.task_id == "task-done"
        assert session.idempotent is True
        assert session.state == "completed"
        assert session.is_closed
        assert session.read_token == "T4-done"

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_pending_idempotent_hit_creates_live_session(self, mock_urlopen, mock_create_pn):
        """Pending idempotent hit creates a normal live session."""
        mock_urlopen.return_value = _mock_urlopen_response({
            "taskId": "task-pending",
            "idempotent": True,
            "queued": True,
            "extensions": {
                "blocks": {
                    "streamChannels": {"status": "u.user-1.task-pending"},
                    "readToken": "T4-pending",
                }
            },
        })
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="user-1",
                idempotency_key="still-pending",
            
        )

        assert isinstance(session, TaskSession)
        assert session.task_id == "task-pending"
        assert session.idempotent is True
        assert not session.is_closed
        assert "u.user-1.task-pending" in fake["subscribed_channels"]

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_running_idempotent_hit_creates_live_session(self, mock_urlopen, mock_create_pn):
        """Running idempotent hit creates a normal live session."""
        mock_urlopen.return_value = _mock_urlopen_response({
            "taskId": "task-running",
            "idempotent": True,
            "state": "running",
            "queued": False,
            "extensions": {
                "blocks": {
                    "streamChannels": {"status": "u.user-1.task-running"},
                    "readToken": "T4-running",
                }
            },
        })
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="user-1",
                idempotency_key="in-progress",
            
        )

        assert isinstance(session, TaskSession)
        assert session.task_id == "task-running"
        assert session.idempotent is True
        assert not session.is_closed
        assert "u.user-1.task-running" in fake["subscribed_channels"]

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_includes_pipe_task_extensions(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(taskId="pipe-task")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="test-user",
                task_kind="pipe",
                duration=15,
            
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["params"]["extensions"] == {
            "blocks": {
                "taskKind": "pipe",
                "duration": 15,
            }
        }

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_includes_explicit_request_task_kind(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(taskId="request-task")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="test-user",
                task_kind="request",
            
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["params"]["extensions"] == {
            "blocks": {
                "taskKind": "request",
            }
        }

        session.close()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_rejects_pipe_without_duration(self, mock_urlopen):
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")

        with pytest.raises(ValueError, match="Pipe tasks require a duration"):
            client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="test-user",
                task_kind="pipe",
            )

        mock_urlopen.assert_not_called()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_rejects_pipe_with_duration_zero(self, mock_urlopen):
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")

        with pytest.raises(ValueError, match="Pipe tasks require a duration"):
            client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="test-user",
                task_kind="pipe",
                duration=0,
            )

        mock_urlopen.assert_not_called()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_rejects_pipe_with_duration_bool(self, mock_urlopen):
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")

        with pytest.raises(ValueError, match="Pipe tasks require a duration"):
            client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="test-user",
                task_kind="pipe",
                duration=True,
            )

        mock_urlopen.assert_not_called()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_rejects_pipe_with_duration_exceeding_max(self, mock_urlopen):
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")

        with pytest.raises(ValueError, match="Pipe tasks require a duration"):
            client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="test-user",
                task_kind="pipe",
                duration=43201,
            )

        mock_urlopen.assert_not_called()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_accepts_pipe_with_duration_min(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(taskId="pipe-min")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
            agent_name="agent-b",
            request_parts=[],
            owner_id="test-user",
            task_kind="pipe",
            duration=1,
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["params"]["extensions"]["blocks"]["duration"] == 1
        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_accepts_pipe_with_duration_max(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(taskId="pipe-max")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
            agent_name="agent-b",
            request_parts=[],
            owner_id="test-user",
            task_kind="pipe",
            duration=43200,
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["params"]["extensions"]["blocks"]["duration"] == 43200
        session.close()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_rejects_duration_without_pipe(self, mock_urlopen):
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")

        with pytest.raises(ValueError, match="Request tasks must not include a duration"):
            client.send_message(
                agent_name="agent-b",
                request_parts=[],
                owner_id="test-user",
                duration=5,
            )

        mock_urlopen.assert_not_called()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_includes_auth_header(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(taskId="task-789")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            auth_provider=StaticAuthProvider("jwt-token-123"),
            base_url="http://localhost:3001",
        )
        session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="test-user"
        )

        req = mock_urlopen.call_args[0][0]
        assert req.headers["Authorization"] == "Bearer jwt-token-123"

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_carries_push_config_id(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(
            self._full_response(pushConfigId="push-123")
        )
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        assert session.push_config_id == "push-123"

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_cancel_calls_cancel_task_rpc(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        mock_urlopen.return_value = _mock_urlopen_response(None)
        session.cancel()

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["method"] == "CancelTask"
        assert body["params"]["taskId"] == "task-123"

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_terminate_calls_terminate_task_rpc(self, mock_urlopen, mock_create_pn):
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        mock_urlopen.return_value = _mock_urlopen_response(None)
        session.terminate()

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["method"] == "TerminateTask"
        assert body["params"]["taskId"] == "task-123"

        session.close()

    # ========================================================================
    # BN-455: subscribe race condition fix — history-based catch-up
    # ========================================================================

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_fetches_history_after_rpc_for_fast_handlers(self, mock_urlopen, mock_create_pn):
        """sendMessage fetches history after RPC to catch events from fast handlers (BN-455)."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
            agent_name="agent-b",
            request_parts=[{"type": "text", "text": "Hello"}],
            owner_id="user-1",
        )

        fake["pubnub"].time.assert_called_once()
        assert len(fake["fetch_messages_calls"]) > 0

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_returns_pre_closed_session_when_history_shows_terminal(self, mock_urlopen, mock_create_pn):
        """Fast handler that completes before subscribe -> session populated from history."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())

        terminal_msg = MagicMock()
        terminal_msg.message = {"type": "terminal", "taskId": "task-123", "state": "completed"}
        terminal_msg.timetoken = "17000000000000001"

        fake = create_fake_pubnub(history_messages=[terminal_msg])
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
            agent_name="agent-b",
            request_parts=[],
            owner_id="user-1",
        )

        assert session.state == "completed"
        assert "u.user-1.task-123" not in fake["subscribed_channels"]

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_subscribes_from_history_high_water_mark(self, mock_urlopen, mock_create_pn):
        """Non-terminal history -> subscribes from the high-water mark timetoken."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())

        progress_msg = MagicMock()
        progress_msg.message = {"type": "progress", "taskId": "task-123", "progress": 50}
        progress_msg.timetoken = "17000000000000005"

        fake = create_fake_pubnub(history_messages=[progress_msg])
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
            agent_name="agent-b",
            request_parts=[],
            owner_id="user-1",
        )

        assert "u.user-1.task-123" in fake["subscribed_channels"]

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_uses_server_timetoken_when_history_empty(self, mock_urlopen, mock_create_pn):
        """Empty history -> uses server timetoken as subscribe cursor."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())

        fake = create_fake_pubnub(history_messages=[], server_timetoken="17000000099999999")
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
            agent_name="agent-b",
            request_parts=[],
            owner_id="user-1",
        )

        assert "u.user-1.task-123" in fake["subscribed_channels"]

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_falls_back_to_basic_session_if_history_fetch_throws(self, mock_urlopen, mock_create_pn):
        """Falls back to a basic session if history catch-up fails."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())

        fake = create_fake_pubnub()
        fake["pubnub"].time.return_value.sync.side_effect = RuntimeError("time failed")
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")

        session = client.send_message(
            agent_name="agent-b",
            request_parts=[],
            owner_id="user-1",
        )

        assert session.task_id == "task-123"
        fake["pubnub"].stop.assert_not_called()
        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_pre_populates_artifacts_from_history(self, mock_urlopen, mock_create_pn):
        """Artifacts from history are pre-populated in the session."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())

        artifact_msg = MagicMock()
        artifact_msg.message = {
            "type": "artifact",
            "taskId": "task-123",
            "artifactRef": {"kind": "inline", "mimeType": "text/plain", "data": "SGVsbG8="},
        }
        artifact_msg.timetoken = "17000000000000002"

        terminal_msg = MagicMock()
        terminal_msg.message = {"type": "terminal", "taskId": "task-123", "state": "completed"}
        terminal_msg.timetoken = "17000000000000003"

        fake = create_fake_pubnub(history_messages=[artifact_msg, terminal_msg])
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
            agent_name="agent-b",
            request_parts=[],
            owner_id="user-1",
        )

        assert session.state == "completed"
        artifacts = session.list_artifacts()
        assert len(artifacts) == 1
        assert artifacts[0].kind == "inline"

        session.close()


# ============================================================================
# File-bearing part contract enforcement
# ============================================================================


class TestFileBearingParts:
    """Tests for SDK contract rules on file-bearing request parts."""

    @staticmethod
    def _full_response(**overrides):
        resp = {
            "taskId": "task-file",
            "idempotent": False,
            "queued": False,
            "extensions": {
                "blocks": {
                    "streamChannels": {"status": "u.user-1.task-file"},
                    "readToken": "T4-read-token",
                }
            },
        }
        resp.update(overrides)
        return resp

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_dataclass_part_with_text_and_file_omits_text(self, mock_urlopen, mock_create_pn):
        """A SendMessageRequestPart with both text and file produces a wire part
        with artifactRef but NOT text (text or file, not both)."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        small_file = b"hello"  # well under 16 KB inline threshold
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[
                    SendMessageRequestPart(
                        part_id="input-1",
                        text="some caption",
                        file=small_file,
                        file_name="hello.txt",
                        content_type="text/plain",
                    )
                ],
                owner_id="user-1",
            
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        wire_part = body["params"]["requestParts"][0]

        assert "artifactRef" in wire_part
        assert "text" not in wire_part
        assert wire_part["partId"] == "input-1"

        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_dict_part_with_text_and_file_omits_text(self, mock_urlopen, mock_create_pn):
        """A dict part with both text and file produces a wire part
        with artifactRef but NOT text."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        small_file = b"hello"
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[
                    {
                        "partId": "input-1",
                        "text": "some caption",
                        "file": small_file,
                        "fileName": "hello.txt",
                        "contentType": "text/plain",
                    }
                ],
                owner_id="user-1",
            
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        wire_part = body["params"]["requestParts"][0]

        assert "artifactRef" in wire_part
        assert "text" not in wire_part
        assert wire_part["partId"] == "input-1"

        session.close()

    def test_dataclass_part_with_file_but_no_part_id_raises(self):
        """A SendMessageRequestPart with file but no partId raises ValueError."""
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")

        with pytest.raises(ValueError, match="partId is required for file-bearing request parts"):
            client.send_message(
                    agent_name="agent-b",
                    request_parts=[
                        SendMessageRequestPart(
                            text="some text",
                            file=b"data",
                            file_name="test.bin",
                        )
                    ],
                    owner_id="user-1",
                
            )

    def test_dict_part_with_file_but_no_part_id_raises(self):
        """A dict part with file but no partId raises ValueError."""
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")

        with pytest.raises(ValueError, match="partId is required for file-bearing request parts"):
            client.send_message(
                    agent_name="agent-b",
                    request_parts=[
                        {
                            "text": "some text",
                            "file": b"data",
                            "fileName": "test.bin",
                        }
                    ],
                    owner_id="user-1",
                
            )

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_text_only_part_preserves_text(self, mock_urlopen, mock_create_pn):
        """A part with text but no file preserves text on the wire."""
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        session = client.send_message(
                agent_name="agent-b",
                request_parts=[
                    SendMessageRequestPart(
                        part_id="input-1",
                        text="just text, no file",
                    )
                ],
                owner_id="user-1",
            
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        wire_part = body["params"]["requestParts"][0]

        assert wire_part["text"] == "just text, no file"
        assert "artifactRef" not in wire_part

        session.close()


# ============================================================================
# Task lifecycle
# ============================================================================


class TestTaskLifecycle:
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_get_task(self, mock_urlopen):
        mock_urlopen.return_value = _mock_urlopen_response(
            {"task": {"taskId": "task-1", "state": "running"}}
        )

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        result = client.get_task("task-1")

        assert result.task_id == "task-1"
        assert result.state == "running"

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["method"] == "GetTask"
        assert body["params"]["taskId"] == "task-1"

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_list_tasks(self, mock_urlopen):
        mock_urlopen.return_value = _mock_urlopen_response(
            {"tasks": [{"taskId": "a"}, {"taskId": "b"}], "totalCount": 2}
        )

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        result = client.list_tasks(ListTasksParams(owner_id="user-1", limit=10))

        assert len(result.tasks) == 2
        assert result.total_count == 2

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["method"] == "ListTasks"
        assert body["params"]["ownerId"] == "user-1"
        assert body["params"]["limit"] == 10

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_cancel_task(self, mock_urlopen):
        mock_urlopen.return_value = _mock_urlopen_response(None)

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        client.cancel_task("task-1")

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["method"] == "CancelTask"
        assert body["params"]["taskId"] == "task-1"

    @pytest.mark.parametrize(
        "method_name,rpc_method",
        [
            ("pause_task", "PauseTask"),
            ("resume_task", "ResumeTask"),
            ("retry_task", "RetryTask"),
            ("terminate_task", "TerminateTask"),
        ],
    )
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_lifecycle_methods(self, mock_urlopen, method_name, rpc_method):
        mock_urlopen.return_value = _mock_urlopen_response(None)

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        getattr(client, method_name)("task-1")

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["method"] == rpc_method
        assert body["params"]["taskId"] == "task-1"


# ============================================================================
# subscribeToTask
# ============================================================================


class TestSubscribeToTask:
    def test_subscribes_to_correct_channel(self):
        fake = create_fake_pubnub()

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", pubnub=fake["pubnub"], base_url="http://localhost:3001")
        sub = client.subscribe_to_task("task-1", "owner-1", TaskEventCallbacks())

        assert "u.owner-1.task-1" in fake["subscribed_channels"]
        sub.unsubscribe()

    def test_throws_without_pubnub(self):
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")

        with pytest.raises(RuntimeError, match="TaskClient requires a pubnub instance"):
            client.subscribe_to_task("task-1", "owner-1", TaskEventCallbacks())

    def test_dispatches_events_to_typed_callbacks(self):
        fake = create_fake_pubnub()
        on_progress = MagicMock()
        on_artifact = MagicMock()
        on_terminal = MagicMock()
        on_system = MagicMock()
        on_event = MagicMock()

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", pubnub=fake["pubnub"], base_url="http://localhost:3001")
        client.subscribe_to_task(
            "task-1",
            "owner-1",
            TaskEventCallbacks(
                on_progress=on_progress,
                on_artifact=on_artifact,
                on_terminal=on_terminal,
                on_system=on_system,
                on_event=on_event,
            ),
        )

        listener = fake["listeners"][-1]
        channel = "u.owner-1.task-1"

        def _emit(msg_dict):
            """Send a message through the listener (works with both dict and SubscribeCallback)."""
            evt = MagicMock()
            evt.channel = msg_dict["channel"]
            evt.message = msg_dict["message"]
            if callable(getattr(listener, "message", None)):
                listener.message(fake["pubnub"], evt)
            else:
                listener["message"](msg_dict)

        # Progress event
        _emit({"channel": channel, "message": {"type": "progress", "taskId": "task-1", "progress": 50}})
        assert on_progress.call_count == 1
        assert on_event.call_count == 1

        # Artifact event
        _emit({"channel": channel, "message": {"type": "artifact", "taskId": "task-1"}})
        assert on_artifact.call_count == 1
        assert on_event.call_count == 2

        # Terminal event
        _emit({"channel": channel, "message": {"type": "terminal", "taskId": "task-1", "state": "completed"}})
        assert on_terminal.call_count == 1
        assert on_event.call_count == 3

        # System event
        _emit({"channel": channel, "message": {"type": "system", "taskId": "task-1", "status": "paused"}})
        assert on_system.call_count == 1
        assert on_event.call_count == 4

    def test_ignores_messages_from_other_channels(self):
        fake = create_fake_pubnub()
        on_progress = MagicMock()

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", pubnub=fake["pubnub"], base_url="http://localhost:3001")
        client.subscribe_to_task(
            "task-1", "owner-1", TaskEventCallbacks(on_progress=on_progress)
        )

        listener = fake["listeners"][-1]
        evt = MagicMock()
        evt.channel = "u.owner-1.other-task"
        evt.message = {"type": "progress", "taskId": "other-task"}
        if callable(getattr(listener, "message", None)):
            listener.message(fake["pubnub"], evt)
        else:
            listener["message"]({"channel": evt.channel, "message": evt.message})

        on_progress.assert_not_called()

    def test_ignores_messages_without_type(self):
        fake = create_fake_pubnub()
        on_event = MagicMock()

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", pubnub=fake["pubnub"], base_url="http://localhost:3001")
        client.subscribe_to_task(
            "task-1", "owner-1", TaskEventCallbacks(on_event=on_event)
        )

        listener = fake["listeners"][-1]

        for msg in ({"foo": "bar"}, None):
            evt = MagicMock()
            evt.channel = "u.owner-1.task-1"
            evt.message = msg
            if callable(getattr(listener, "message", None)):
                listener.message(fake["pubnub"], evt)
            else:
                listener["message"]({"channel": evt.channel, "message": msg})

        on_event.assert_not_called()

    def test_unsubscribe_cleans_up(self):
        fake = create_fake_pubnub()

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", pubnub=fake["pubnub"], base_url="http://localhost:3001")
        sub = client.subscribe_to_task("task-1", "owner-1", TaskEventCallbacks())
        sub.unsubscribe()

        fake["pubnub"].remove_listener.assert_called_once_with(fake["listeners"][-1])
        assert "u.owner-1.task-1" in fake["unsubscribed_channels"]


# ============================================================================
# createPubNub factory (lazy init)
# ============================================================================


class TestCreatePubNubFactory:
    def test_lazily_creates_pubnub_on_first_subscribe(self):
        fake = create_fake_pubnub()
        factory = MagicMock(return_value=fake["pubnub"])

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", create_pubnub=factory, base_url="http://localhost:3001")

        # Factory not called yet
        factory.assert_not_called()

        # First subscribe triggers factory
        sub = client.subscribe_to_task("task-1", "owner-1", TaskEventCallbacks())
        factory.assert_called_once()
        assert "u.owner-1.task-1" in fake["subscribed_channels"]

        sub.unsubscribe()

    def test_factory_called_once_across_multiple_subscribes(self):
        fake = create_fake_pubnub()
        factory = MagicMock(return_value=fake["pubnub"])

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", create_pubnub=factory, base_url="http://localhost:3001")

        sub1 = client.subscribe_to_task("task-1", "owner-1", TaskEventCallbacks())
        sub2 = client.subscribe_to_task("task-2", "owner-1", TaskEventCallbacks())

        factory.assert_called_once()

        sub1.unsubscribe()
        sub2.unsubscribe()

    def test_direct_pubnub_takes_precedence_over_factory(self):
        direct_fake = create_fake_pubnub()
        factory_fake = create_fake_pubnub()
        factory = MagicMock(return_value=factory_fake["pubnub"])

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            pubnub=direct_fake["pubnub"],
            create_pubnub=factory,
            base_url="http://localhost:3001",
        )

        sub = client.subscribe_to_task("task-1", "owner-1", TaskEventCallbacks())

        factory.assert_not_called()
        assert "u.owner-1.task-1" in direct_fake["subscribed_channels"]

        sub.unsubscribe()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_send_message_uses_session_factory(self, mock_urlopen):
        """When create_session_pubnub is provided, send_message() uses it."""
        mock_urlopen.return_value = _mock_urlopen_response({
            "taskId": "task-factory",
            "extensions": {"blocks": {
                "streamChannels": {"status": "u.user-1.task-factory"},
                "readToken": "T4-factory",
            }},
        })
        fake = create_fake_pubnub()
        session_factory = MagicMock(return_value=fake["pubnub"])

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            create_session_pubnub=session_factory,
            base_url="http://localhost:3001",
        )

        session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        session_factory.assert_called_once()
        fake["pubnub"].set_token.assert_called_once_with("T4-factory")
        assert "u.user-1.task-factory" in fake["subscribed_channels"]

        session.close()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_send_message_does_not_use_shared_factory(self, mock_urlopen):
        """send_message() does NOT use the shared create_pubnub for sessions."""
        mock_urlopen.return_value = _mock_urlopen_response({
            "taskId": "task-sep",
            "extensions": {"blocks": {
                "streamChannels": {"status": "u.user-1.task-sep"},
                "readToken": "T4-sep",
            }},
        })
        shared_factory = MagicMock()

        # Only shared factory, no session factory — falls back to internal
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", create_pubnub=shared_factory, base_url="http://localhost:3001")

        with patch("blocks_network.pubnub_client.create_pubnub_client") as mock_internal:
            fake = create_fake_pubnub()
            mock_internal.return_value = fake["pubnub"]

            session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
            )

            # Shared factory NOT called by send_message
            shared_factory.assert_not_called()

            # Internal fallback was used instead
            mock_internal.assert_called_once()

            session.close()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_session_factory_called_once_per_session(self, mock_urlopen):
        """Session factory is called once per send_message, not cached."""
        fake_a = create_fake_pubnub()
        fake_b = create_fake_pubnub()
        session_factory = MagicMock(side_effect=[fake_a["pubnub"], fake_b["pubnub"]])

        mock_urlopen.side_effect = [
            _mock_urlopen_response({
                "taskId": "task-A",
                "extensions": {"blocks": {
                    "streamChannels": {"status": "u.user-1.task-A"},
                    "readToken": "T4-A",
                }},
            }),
            _mock_urlopen_response({
                "taskId": "task-B",
                "extensions": {"blocks": {
                    "streamChannels": {"status": "u.user-1.task-B"},
                    "readToken": "T4-B",
                }},
            }),
        ]

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", create_session_pubnub=session_factory, base_url="http://localhost:3001")
        session1 = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )
        session2 = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        assert session_factory.call_count == 2
        assert fake_a["pubnub"] is not fake_b["pubnub"]
        fake_a["pubnub"].set_token.assert_called_once_with("T4-A")
        fake_b["pubnub"].set_token.assert_called_once_with("T4-B")

        session1.close()
        session2.close()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_subscribe_to_task_still_uses_shared_client(self, mock_urlopen):
        """subscribe_to_task() still uses the shared create_pubnub factory."""
        shared_fake = create_fake_pubnub()
        shared_factory = MagicMock(return_value=shared_fake["pubnub"])

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", create_pubnub=shared_factory, base_url="http://localhost:3001")

        sub = client.subscribe_to_task("task-1", "owner-1", TaskEventCallbacks())

        shared_factory.assert_called_once()
        assert "u.owner-1.task-1" in shared_fake["subscribed_channels"]

        sub.unsubscribe()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_shared_pubnub_not_mutated_by_send_message(self, mock_urlopen):
        """The shared TaskClient pubnub is never mutated by send_message()."""
        shared_fake = create_fake_pubnub()
        session_fake = create_fake_pubnub()
        shared_factory = MagicMock(return_value=shared_fake["pubnub"])
        session_factory = MagicMock(return_value=session_fake["pubnub"])

        mock_urlopen.return_value = _mock_urlopen_response({
            "taskId": "task-sm",
            "extensions": {"blocks": {
                "streamChannels": {"status": "u.user-1.task-sm"},
                "readToken": "T4-session",
            }},
        })

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            create_pubnub=shared_factory,
            create_session_pubnub=session_factory,
            base_url="http://localhost:3001",
        )

        # Trigger shared client creation via subscribe_to_task
        sub = client.subscribe_to_task("task-0", "owner-0", TaskEventCallbacks())

        # Now send_message
        shared_fake["pubnub"].set_token.reset_mock()
        session = client.send_message(agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        # Shared client untouched
        shared_fake["pubnub"].set_token.assert_not_called()

        # Session client got T4
        session_fake["pubnub"].set_token.assert_called_once_with("T4-session")

        sub.unsubscribe()
        session.close()


# ============================================================================
# destroy
# ============================================================================


class TestDestroy:
    def test_destroy_stops_owned_pubnub(self):
        fake = create_fake_pubnub()
        factory = MagicMock(return_value=fake["pubnub"])

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", create_pubnub=factory, base_url="http://localhost:3001")
        # Trigger lazy creation
        sub = client.subscribe_to_task("task-1", "owner-1", TaskEventCallbacks())
        sub.unsubscribe()

        client.destroy()
        fake["pubnub"].stop.assert_called_once()

    def test_destroy_does_not_stop_external_pubnub(self):
        fake = create_fake_pubnub()

        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", pubnub=fake["pubnub"], base_url="http://localhost:3001")
        client.destroy()
        fake["pubnub"].stop.assert_not_called()

    def test_destroy_is_safe_when_no_pubnub(self):
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free", base_url="http://localhost:3001")
        client.destroy()  # Should not raise


# ============================================================================
# TaskInfo.from_dict
# ============================================================================


class TestTaskInfo:
    def test_from_dict_known_fields(self):
        info = TaskInfo.from_dict(
            {
                "taskId": "t-1",
                "agentName": "echo",
                "owner": "u-1",
                "state": "running",
                "createdTime": "2024-01-01",
                "updatedTime": "2024-01-02",
            }
        )
        assert info.task_id == "t-1"
        assert info.agent_name == "echo"
        assert info.owner == "u-1"
        assert info.state == "running"
        assert info.created_time == "2024-01-01"
        assert info.updated_time == "2024-01-02"
        assert info.extra == {}

    def test_from_dict_extra_fields(self):
        info = TaskInfo.from_dict(
            {"taskId": "t-1", "customField": "value", "another": 42}
        )
        assert info.task_id == "t-1"
        assert info.extra == {"customField": "value", "another": 42}


# ============================================================================
# update_keys
# ============================================================================


class TestUpdateKeys:
    def test_updates_subscribe_and_publish_keys(self):
        client = TaskClient(
            subscribe_key="sub-c-old",
            billing_mode="free",
            publish_key="pub-c-old",
        )
        assert client._subscribe_key == "sub-c-old"
        assert client._publish_key == "pub-c-old"

        client.update_keys(
            subscribe_key="sub-c-new",
            publish_key="pub-c-new",
        )
        assert client._subscribe_key == "sub-c-new"
        assert client._publish_key == "pub-c-new"

    def test_publish_key_defaults_to_empty_string(self):
        client = TaskClient(
            subscribe_key="sub-c-old",
            billing_mode="free",
            publish_key="pub-c-old",
        )

        client.update_keys(subscribe_key="sub-c-new")
        assert client._publish_key == ""

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_rpc_calls_use_updated_keys(self, mock_urlopen):
        """After update_keys(), RPC calls use the updated subscribe key."""
        client = TaskClient(
            subscribe_key="sub-c-old",
            billing_mode="free",
            auth_provider=StaticAuthProvider("old-token"),
            base_url="http://localhost:3001",
        )

        client.update_keys(
            subscribe_key="sub-c-new",
        )

        mock_urlopen.return_value = _mock_urlopen_response(
            {"taskId": "task-1", "state": "running"}
        )
        client.get_task("task-1")

        req = mock_urlopen.call_args[0][0]
        assert req.full_url == "http://localhost:3001/api/v1/rpc"
        assert req.headers["Authorization"] == "Bearer old-token"


# ============================================================================
# create_task_client convenience function
# ============================================================================


class TestCreateTaskClient:
    """Tests for the module-level create_task_client() helper."""

    @patch("blocks_network.task_client.TaskClient.create")
    @patch("dotenv.load_dotenv")
    def test_defaults_to_free_billing_mode(self, _mock_dotenv, mock_create):
        mock_create.return_value = MagicMock()
        create_task_client()
        mock_create.assert_called_once_with(
            billing_mode="free",
            api_key="bk_test_fixture",
            token_endpoint=None,
            token_provider=None,
        )

    @patch("blocks_network.task_client.TaskClient.create")
    @patch("dotenv.load_dotenv")
    def test_paid_billing_mode_forwarded(self, _mock_dotenv, mock_create):
        mock_create.return_value = MagicMock()
        create_task_client(billing_mode="paid")
        mock_create.assert_called_once_with(
            billing_mode="paid",
            api_key="bk_test_fixture",
            token_endpoint=None,
            token_provider=None,
        )

    @patch("blocks_network.task_client.TaskClient.create")
    @patch("dotenv.load_dotenv")
    def test_explicit_api_key(self, _mock_dotenv, mock_create):
        mock_create.return_value = MagicMock()
        create_task_client(api_key="explicit-key")
        mock_create.assert_called_once_with(
            billing_mode="free",
            api_key="explicit-key",
            token_endpoint=None,
            token_provider=None,
        )

    @patch("blocks_network.task_client.TaskClient.create")
    @patch("dotenv.load_dotenv")
    def test_missing_api_key_raises(self, _mock_dotenv, mock_create, monkeypatch):
        monkeypatch.delenv("BLOCKS_API_KEY", raising=False)
        with pytest.raises(KeyError):
            create_task_client()

    @patch("blocks_network.task_client.TaskClient.create")
    @patch("dotenv.load_dotenv")
    def test_token_endpoint_skips_api_key(self, _mock_dotenv, mock_create, monkeypatch):
        monkeypatch.delenv("BLOCKS_API_KEY", raising=False)
        mock_create.return_value = MagicMock()
        create_task_client(token_endpoint="https://proxy.example.com/token")
        mock_create.assert_called_once_with(
            billing_mode="free",
            api_key=None,
            token_endpoint="https://proxy.example.com/token",
            token_provider=None,
        )

    @patch("blocks_network.task_client.TaskClient.create")
    @patch("dotenv.load_dotenv")
    def test_token_provider_skips_api_key(self, _mock_dotenv, mock_create, monkeypatch):
        monkeypatch.delenv("BLOCKS_API_KEY", raising=False)
        mock_create.return_value = MagicMock()
        provider = MagicMock()
        create_task_client(token_provider=provider)
        mock_create.assert_called_once_with(
            billing_mode="free",
            api_key=None,
            token_endpoint=None,
            token_provider=provider,
        )

    @patch("blocks_network.task_client.TaskClient.create")
    @patch("dotenv.load_dotenv")
    def test_forwards_kwargs(self, _mock_dotenv, mock_create):
        mock_create.return_value = MagicMock()
        create_task_client(cdm_url="https://custom.cdm", subscribe_key="sk")
        mock_create.assert_called_once_with(
            billing_mode="free",
            api_key="bk_test_fixture",
            token_endpoint=None,
            token_provider=None,
            cdm_url="https://custom.cdm",
            subscribe_key="sk",
        )

    @patch("blocks_network.task_client.TaskClient.create")
    def test_calls_load_dotenv(self, mock_create):
        mock_create.return_value = MagicMock()
        with patch("dotenv.load_dotenv") as mock_ld:
            create_task_client()
            mock_ld.assert_called_once()

    def test_importable_from_package(self):
        from blocks_network import create_task_client as fn
        assert callable(fn)


# ============================================================================
# Billing Mode Contract — TaskClient direct ctor + sendMessage RPC params
# ============================================================================


class TestTaskClientBillingModeRequired:
    """Direct ``TaskClient.__init__`` requires ``billing_mode``.

    Mirrors Node SDK direct-ctor required-billingMode behavior. The
    factory ``TaskClient.create(billing_mode=...)`` already enforces this
    (see ``test_billing_mode_parity.py``); the ctor enforcement is
    additional plumbing to ensure advanced direct-construction callers
    cannot send tasks without declaring caller-owned billing mode.
    """

    def test_direct_ctor_requires_billing_mode_kwarg(self):
        with pytest.raises(TypeError):
            TaskClient(subscribe_key="sub-c-test")  # type: ignore[call-arg]

    def test_direct_ctor_rejects_invalid_billing_mode(self):
        with pytest.raises(ValueError, match="billing_mode must be"):
            TaskClient(subscribe_key="sub-c-test", billing_mode="network")  # type: ignore[arg-type]

    def test_direct_ctor_stores_billing_mode_free(self):
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="free")
        assert client._billing_mode == "free"

    def test_direct_ctor_stores_billing_mode_paid(self):
        client = TaskClient(subscribe_key="sub-c-test", billing_mode="paid")
        assert client._billing_mode == "paid"


class TestTaskClientCreateNoRegistryLookup:
    """``TaskClient.create(billing_mode=...)`` does NOT call into the registry.

    Per IMPL §6 Python 1, the consumer-side factory takes the caller's
    explicit ``billing_mode`` and maps directly to the CDM keyset. It
    must not perform a registry GET — that would silently bind the
    consumer's billing-mode declaration to the agent's current value
    and defeat the mismatch contract.
    """

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    @patch("blocks_network.task_client.get_agent")
    def test_create_does_not_call_get_agent(self, mock_get_agent, mock_cdm):
        from blocks_network.cdm_config import CdmApiConfig, CdmConfig, CdmKeyset

        mock_cdm.return_value = CdmConfig(
            playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
            network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
            api=CdmApiConfig(base_url="https://api.example.com"),
        )

        TaskClient.create(billing_mode="paid")

        mock_get_agent.assert_not_called()


class TestSendMessageBillingModeOnWire:
    """SendMessage RPC params dict carries camelCase ``billingMode``.

    The wire field is camelCase ``billingMode`` even though the Python
    SDK parameter is snake_case ``billing_mode`` (per IMPL §6 Python 5
    + the agent prompt's naming reminder).
    """

    @staticmethod
    def _full_response(**overrides):
        resp = {
            "taskId": "task-bm",
            "idempotent": False,
            "queued": False,
            "extensions": {
                "blocks": {
                    "streamChannels": {"status": "u.user-1.task-bm"},
                    "readToken": "T4",
                }
            },
        }
        resp.update(overrides)
        return resp

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_send_message_includes_billing_mode_free(
        self, mock_urlopen, mock_create_pn
    ):
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url="http://localhost:3001",
        )
        session = client.send_message(
            agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["params"]["billingMode"] == "free"
        # Verify wire field is camelCase, not snake_case
        assert "billing_mode" not in body["params"]
        session.close()

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_send_message_includes_billing_mode_paid(
        self, mock_urlopen, mock_create_pn
    ):
        mock_urlopen.return_value = _mock_urlopen_response(self._full_response())
        fake = create_fake_pubnub()
        mock_create_pn.return_value = fake["pubnub"]

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="paid",
            base_url="http://localhost:3001",
        )
        session = client.send_message(
            agent_name="agent-b", request_parts=[], owner_id="user-1"
        )

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body["params"]["billingMode"] == "paid"
        session.close()


class TestSendMessageBillingModeMismatch:
    """Backend ``BillingModeMismatch`` errors surface as the typed exception.

    Per IMPL §6 Python 6-8: SDK maps RPC ``code: 'BillingModeMismatch'``
    to ``BillingModeMismatchError`` carrying ``expected``/``got`` from
    ``error.data.details``. SDK does NOT auto-retry.
    """

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_mismatch_raises_typed_exception(self, mock_urlopen, mock_create_pn):
        from blocks_network.rpc_client import BillingModeMismatchError

        # Backend emits JSON-RPC error envelope per bmc-data Phase 1 wire shape:
        #   error.data = { code: 'BillingModeMismatch',
        #                  details: { expected, got } }
        mismatch_error = {
            "jsonrpc": "2.0",
            "id": "x",
            "error": {
                "code": -32000,
                "message": "Billing mode mismatch: caller declared 'free', agent is 'paid'",
                "data": {
                    "code": "BillingModeMismatch",
                    "details": {"expected": "paid", "got": "free"},
                },
            },
        }
        body_bytes = json.dumps(mismatch_error).encode("utf-8")
        resp = MagicMock()
        resp.read.return_value = body_bytes
        resp.__enter__ = MagicMock(return_value=resp)
        resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = resp
        mock_create_pn.return_value = create_fake_pubnub()["pubnub"]

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url="http://localhost:3001",
        )

        with pytest.raises(BillingModeMismatchError) as exc_info:
            client.send_message(
                agent_name="agent-b", request_parts=[], owner_id="user-1"
            )

        assert exc_info.value.expected == "paid"
        assert exc_info.value.got == "free"

    @patch("blocks_network.task_client.TaskClient._create_session_pubnub")
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_mismatch_does_not_retry(self, mock_urlopen, mock_create_pn):
        """Mismatch must not trigger auto-retry. The single RPC HTTP call is the only attempt."""
        from blocks_network.rpc_client import BillingModeMismatchError

        mismatch_error = {
            "jsonrpc": "2.0",
            "id": "x",
            "error": {
                "code": -32000,
                "message": "mismatch",
                "data": {
                    "code": "BillingModeMismatch",
                    "details": {"expected": "free", "got": "paid"},
                },
            },
        }
        body_bytes = json.dumps(mismatch_error).encode("utf-8")
        resp = MagicMock()
        resp.read.return_value = body_bytes
        resp.__enter__ = MagicMock(return_value=resp)
        resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = resp
        mock_create_pn.return_value = create_fake_pubnub()["pubnub"]

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="paid",
            base_url="http://localhost:3001",
        )

        with pytest.raises(BillingModeMismatchError):
            client.send_message(
                agent_name="agent-b", request_parts=[], owner_id="user-1"
            )

        # Exactly ONE urlopen call — no auto-retry, no auto-correct
        assert mock_urlopen.call_count == 1
