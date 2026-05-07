"""Phase 3 agent instance runtime tests.

Covers:
- Three-tier connection model
- Unified create_stream API
- Instance-level APIs (publish_terminal, fail_stream)
- Lifecycle (request and pipe task)
- Removal verification (old APIs removed)
- Server-owned pipe terminals
"""

from __future__ import annotations

import threading
import time
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.agent_instance import start_agent_instance, _extract_owner_id
from blocks_network.types import (
    AgentInstanceOptions,
    StartTaskMessage,
    TaskContext,
)

from tests.conftest import minimal_card


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_env(monkeypatch: pytest.MonkeyPatch) -> None:
    """Set required env vars for agent instance tests.

    Note: AGENT_NAME, EXPECTED_INSTANCES, and CONCURRENCY env vars are
    no longer supported. Agent config is passed via options only.
    """
    pass



def _make_mock_pubnub() -> MagicMock:
    """Create a fully-stubbed PubNub client for agent instance tests."""
    pn = MagicMock()

    def _make_chain():
        chain = MagicMock()
        for method in (
            "channel", "channels", "message", "meta", "should_store",
            "use_post", "state", "file_name", "file_object",
            "file_id", "execute",
        ):
            getattr(chain, method).side_effect = lambda *a, _c=chain, **kw: _c
        chain.sync.return_value = MagicMock()
        return chain

    pn.publish.return_value = _make_chain()
    pn.subscribe.return_value = _make_chain()
    pn.set_state.return_value = _make_chain()
    pn.unsubscribe.return_value = _make_chain()
    pn.download_file.return_value = _make_chain()

    pn._listeners = []
    pn.add_listener.side_effect = lambda l: pn._listeners.append(l)
    pn.remove_listener.side_effect = lambda l: (
        pn._listeners.remove(l) if l in pn._listeners else None
    )

    pn.set_filter_expression = MagicMock()
    pn.config = MagicMock()
    pn.config.filter_expression = None
    pn.set_token = MagicMock()

    return pn


def _simulate_start_task(
    pn: MagicMock,
    task_id: str = "task-1",
    task_kind: str = "request",
    has_stream: bool = False,
    write_token: str = "t2-test",
    control_token: str = "",
    owner_id: str = "alice",
) -> None:
    """Simulate a StartTask control message."""
    msg = {
        "type": "StartTask",
        "taskId": task_id,
        "agentName": "test_agent",
        "ownerId": owner_id,
        "taskKind": task_kind,
        "hasStream": has_stream,
        "writeToken": write_token,
    }
    if control_token:
        msg["controlToken"] = control_token
    meta = {"instance": "AG-test-agent-xxx", "broadcast": "true"}
    event = MagicMock()
    event.message = msg
    event.user_metadata = meta
    for listener in list(pn._listeners):
        if hasattr(listener, "message"):
            listener.message(pn, event)


# ---------------------------------------------------------------------------
# Connection model tests
# ---------------------------------------------------------------------------


class TestConnectionModel:
    """Three-tier connection model tests."""

    def test_control_client_separate_from_task_client(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        # Track per-task PubNub creation
        created_clients = []
        original_create = None

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_create.return_value = _make_mock_pubnub()
            created_clients.append(mock_create.return_value)

            handler_called = threading.Event()
            handler_pn = [None]

            def handler(task, ctx):
                handler_pn[0] = "called"
                handler_called.set()

            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=handler,

            ))

            # Give registration thread time to subscribe
            time.sleep(0.2)

            _simulate_start_task(pn)
            handler_called.wait(timeout=2.0)

            # Per-task PubNub should have been created
            assert mock_create.called

            result["stop"]()

    def test_instance_returns_publish_terminal(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_create.return_value = _make_mock_pubnub()
            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=lambda task, ctx: None,

            ))

            assert "publish_terminal" in result
            assert callable(result["publish_terminal"])
            assert "fail_stream" in result
            assert callable(result["fail_stream"])

            result["stop"]()

    def test_instance_returns_expected_keys(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_create.return_value = _make_mock_pubnub()
            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=lambda task, ctx: None,

            ))

            assert "stop" in result
            assert "agent_name" in result
            assert "instance_id" in result
            assert "task_client" in result
            assert "publish_terminal" in result
            assert "fail_stream" in result
            assert result["agent_name"] == "test_agent"

            result["stop"]()


# ---------------------------------------------------------------------------
# Unified create_stream tests
# ---------------------------------------------------------------------------


class TestCreateStream:
    """Unified create_stream API tests."""

    def test_task_context_has_create_stream(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        task_ctx_ref = [None]
        handler_done = threading.Event()

        def handler(task, ctx):
            task_ctx_ref[0] = ctx
            handler_done.set()

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_create.return_value = _make_mock_pubnub()

            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=handler,

            ))

            time.sleep(0.2)
            _simulate_start_task(pn, has_stream=True)
            handler_done.wait(timeout=2.0)

            ctx = task_ctx_ref[0]
            assert ctx is not None
            assert hasattr(ctx, "create_stream")
            assert callable(ctx.create_stream)

            result["stop"]()

    def test_create_stream_requires_has_stream(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        error_ref = [None]
        handler_done = threading.Event()

        def handler(task, ctx):
            try:
                ctx.create_stream()
            except RuntimeError as e:
                error_ref[0] = str(e)
            handler_done.set()

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_create.return_value = _make_mock_pubnub()

            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=handler,

            ))

            time.sleep(0.2)
            _simulate_start_task(pn, has_stream=False)
            handler_done.wait(timeout=2.0)

            assert error_ref[0] is not None
            assert "not negotiated" in error_ref[0]

            result["stop"]()

    def test_request_task_rejects_inbound_direction(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        error_ref = [None]
        handler_done = threading.Event()

        def handler(task, ctx):
            try:
                ctx.create_stream(direction="inbound")
            except RuntimeError as e:
                error_ref[0] = str(e)
            handler_done.set()

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_create.return_value = _make_mock_pubnub()

            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=handler,

            ))

            time.sleep(0.2)
            _simulate_start_task(pn, has_stream=True, task_kind="request")
            handler_done.wait(timeout=2.0)

            assert error_ref[0] is not None
            assert "outbound" in error_ref[0]

            result["stop"]()

    def test_request_task_rejects_external(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        error_ref = [None]
        handler_done = threading.Event()

        def handler(task, ctx):
            try:
                ctx.create_stream(external=True)
            except RuntimeError as e:
                error_ref[0] = str(e)
            handler_done.set()

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_create.return_value = _make_mock_pubnub()

            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=handler,

            ))

            time.sleep(0.2)
            _simulate_start_task(pn, has_stream=True, task_kind="request")
            handler_done.wait(timeout=2.0)

            assert error_ref[0] is not None
            assert "external" in error_ref[0].lower()

            result["stop"]()


# ---------------------------------------------------------------------------
# Lifecycle tests
# ---------------------------------------------------------------------------


class TestLifecycle:
    """Request-task and pipe-task lifecycle tests."""

    def test_request_task_auto_complete(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        handler_done = threading.Event()
        publishes = []

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_pn = _make_mock_pubnub()

            # Track publishes on the per-task PubNub
            def _track_publish():
                chain = MagicMock()
                record = {}

                def _ch(ch):
                    record["channel"] = ch
                    return chain

                def _msg(m):
                    record["message"] = m
                    return chain

                def _meta(m):
                    record["meta"] = m
                    return chain

                chain.channel = _ch
                chain.message = _msg
                chain.meta = _meta
                chain.should_store = lambda v: chain
                chain.use_post = lambda v: chain
                chain.sync = lambda: (publishes.append(dict(record)), MagicMock())[1]
                return chain

            mock_pn.publish = _track_publish
            mock_create.return_value = mock_pn

            def handler(task, ctx):
                handler_done.set()

            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=handler,

            ))

            time.sleep(0.2)
            _simulate_start_task(pn, task_kind="request")
            handler_done.wait(timeout=2.0)
            time.sleep(0.3)

            # Should have published progress + terminal
            terminal_msgs = [
                p for p in publishes
                if isinstance(p.get("message"), dict) and p["message"].get("type") == "terminal"
            ]
            assert len(terminal_msgs) >= 1
            assert terminal_msgs[0]["message"]["state"] == "completed"

            result["stop"]()

    def test_pipe_task_no_auto_terminal(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        handler_done = threading.Event()
        publishes = []

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_pn = _make_mock_pubnub()

            def _track_publish():
                chain = MagicMock()
                record = {}

                def _ch(ch):
                    record["channel"] = ch
                    return chain

                def _msg(m):
                    record["message"] = m
                    return chain

                chain.channel = _ch
                chain.message = _msg
                chain.meta = lambda m: chain
                chain.should_store = lambda v: chain
                chain.use_post = lambda v: chain
                chain.sync = lambda: (publishes.append(dict(record)), MagicMock())[1]
                return chain

            mock_pn.publish = _track_publish
            mock_create.return_value = mock_pn

            def handler(task, ctx):
                handler_done.set()

            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=handler,

            ))

            time.sleep(0.2)
            _simulate_start_task(pn, task_kind="pipe")
            handler_done.wait(timeout=2.0)
            time.sleep(0.3)

            # Pipe tasks should NOT auto-publish terminal on voluntary return
            terminal_msgs = [
                p for p in publishes
                if isinstance(p.get("message"), dict) and p["message"].get("type") == "terminal"
            ]
            assert len(terminal_msgs) == 0

            result["stop"]()


# ---------------------------------------------------------------------------
# Removal verification tests
# ---------------------------------------------------------------------------


class TestRemovalVerification:
    """Verify old APIs are removed."""

    def test_task_context_no_old_apis(self) -> None:
        ctx = TaskContext(task_id="test")
        # These old APIs must NOT exist
        assert not hasattr(ctx, "open_inbound_stream")
        assert not hasattr(ctx, "wait_for_stream_end")

    def test_no_stream_bundle_in_exports(self) -> None:
        """StreamBundle and AgentStream should not be in public exports."""
        import blocks_network
        assert not hasattr(blocks_network, "StreamBundle")
        assert not hasattr(blocks_network, "AgentStream")
        assert not hasattr(blocks_network, "StreamOptions")

    def test_new_types_in_exports(self) -> None:
        """New Phase 3 types should be in public exports."""
        import blocks_network
        assert hasattr(blocks_network, "StreamRegistry")
        assert hasattr(blocks_network, "CredentialCache")
        assert hasattr(blocks_network, "StreamObject")
        assert hasattr(blocks_network, "ExternalStreamObject")
        assert hasattr(blocks_network, "StreamRef")
        assert hasattr(blocks_network, "TaskSession")
        assert hasattr(blocks_network, "TaskEvent")
        assert hasattr(blocks_network, "OnActivateCallback")


# ---------------------------------------------------------------------------
# Instance-level API tests
# ---------------------------------------------------------------------------


class TestInstanceLevelAPIs:
    """publish_terminal and fail_stream tests."""

    def test_publish_terminal_no_credentials_raises(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_create.return_value = _make_mock_pubnub()

            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=lambda task, ctx: None,

            ))

            with pytest.raises(RuntimeError, match="No cached credentials"):
                result["publish_terminal"]("unknown-task", {
                    "type": "terminal",
                    "taskId": "unknown-task",
                    "state": "completed",
                })

            result["stop"]()

    def test_fail_stream_nonexistent_is_noop(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_create.return_value = _make_mock_pubnub()

            result = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="test_agent",
                handler=lambda task, ctx: None,

            ))

            # Should not raise
            result["fail_stream"]("nonexistent-stream", "test error")

            result["stop"]()


# ---------------------------------------------------------------------------
# Extract owner ID helper
# ---------------------------------------------------------------------------


class TestExtractOwnerId:
    """_extract_owner_id helper tests."""

    def test_explicit_owner_id(self) -> None:
        assert _extract_owner_id("alice") == "alice"

    def test_caller_claims_sub(self) -> None:
        assert _extract_owner_id(None, {"sub": "bob"}) == "bob"

    def test_fallback_anonymous(self) -> None:
        assert _extract_owner_id() == "anonymous"
        assert _extract_owner_id(None, {}) == "anonymous"
        assert _extract_owner_id("", {}) == "anonymous"


class TestPamTokenIsolation:
    """BLOCKS-232: PAM tokens must not be exposed to handler code."""

    def test_handler_does_not_receive_tokens(self, monkeypatch: pytest.MonkeyPatch) -> None:
        _make_env(monkeypatch)
        pn = _make_mock_pubnub()
        received_task = {}

        with patch("blocks_network.agent_instance.create_pubnub_client") as mock_create:
            mock_task_pn = _make_mock_pubnub()
            mock_task_pn.publish = lambda: MagicMock(
                channel=lambda c: MagicMock(
                    message=lambda m: MagicMock(
                        meta=lambda mt: MagicMock(
                            should_store=lambda v: MagicMock(
                                use_post=lambda v: MagicMock(sync=MagicMock())
                            )
                        )
                    )
                )
            )
            mock_create.return_value = mock_task_pn

            def handler(task: StartTaskMessage, ctx: TaskContext) -> dict:
                received_task["task"] = task
                return {}

            handle = start_agent_instance(AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent",
                pubnub=pn,
                handler=handler,
            ))
            time.sleep(0.1)

            _simulate_start_task(
                pn,
                task_id="task-token-test",
                write_token="secret-wt-abc123",
                control_token="secret-ct-xyz789",
            )
            time.sleep(0.5)

            task = received_task.get("task")
            assert task is not None
            assert task.task_id == "task-token-test"
            assert not hasattr(task, "write_token")
            assert not hasattr(task, "control_token")

            # JSON serialization must not contain tokens
            import json
            serialized = json.dumps(task.to_dict())
            assert "secret-wt-abc123" not in serialized
            assert "secret-ct-xyz789" not in serialized
            assert "writeToken" not in serialized
            assert "controlToken" not in serialized

            handle.stop()
