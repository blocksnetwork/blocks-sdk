"""
Tests for pipe task lifecycle in blocks_network.agent_instance.

Mirrors the Node SDK pipe lifecycle tests in
``sdks/node/tests/agent_instance_pipe.test.ts``.

Covers:
- ExpireTask signals handler, agent publishes terminal after artifact
- CancelTask on pipe: agent publishes canceled terminal
- TerminateTask on pipe: agent publishes terminal with reason
- Pipe handler voluntary return does NOT publish terminal
- Request task still auto-completes on handler return (regression)
- is_expired is true after ExpireTask
- is_expired is false after CancelTask
- Gated flag set for pipe tasks only
- Pipe writeToken NOT applied to instance PubNub (token isolation)
- Request tasks still use instance PubNub for writeToken
- ExpireTask handler only sets cancel event for in-flight tasks
"""

from __future__ import annotations

import time
import threading
from unittest.mock import MagicMock

import pytest

from blocks_network.agent_instance import start_agent_instance
from blocks_network import agent_instance as _ai_mod
from blocks_network.types import AgentInstanceOptions, TaskContext, StartTaskMessage

from tests.conftest import minimal_card


# ---------------------------------------------------------------------------
# Module-level state for per-task PubNub client tracking.
# Populated by the autouse _patch_create_pubnub_client fixture below.
# ---------------------------------------------------------------------------

_per_task_publish_records: list = []
_per_task_created_clients: list = []


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _simulate_message(mock_pn: MagicMock, msg: dict, meta: dict | None = None) -> None:
    """Simulate receiving a control message on the PubNub listener."""
    assert len(mock_pn._listeners) > 0, "No listener registered on mock PubNub"
    listener = mock_pn._listeners[0]

    event = MagicMock()
    event.message = msg
    event.user_metadata = meta

    if hasattr(listener, "message") and callable(listener.message):
        if isinstance(listener, dict):
            listener["message"](event)
        else:
            listener.message(mock_pn, event)
    elif isinstance(listener, dict) and "message" in listener:
        listener["message"](event)
    else:
        raise RuntimeError("Cannot dispatch message to registered listener")


def _wait_for(predicate, timeout_sec=3.0, poll_sec=0.05):
    """Spin-wait until *predicate* returns True or *timeout_sec* expires."""
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(poll_sec)
    assert predicate(), "Timed out waiting for predicate to become True"


def _find_publish(records, **criteria):
    """Find first publish record matching all criteria in message dict.

    Searches both the provided records AND _per_task_publish_records so that
    publishes routed through per-task PubNub clients are also found.
    """
    for r in list(records) + list(_per_task_publish_records):
        msg = r.get("message", {})
        if all(msg.get(k) == v for k, v in criteria.items()):
            return r
    return None


def _make_tracking_publish(records):
    """Create a tracking publish factory that records all publishes."""
    def _tracking():
        chain = MagicMock()
        record: dict = {}

        def _channel(ch):
            record["channel"] = ch
            return chain

        def _message(msg):
            record["message"] = msg
            return chain

        def _meta(m):
            record["meta"] = m
            return chain

        def _should_store(v):
            return chain

        def _use_post(v):
            return chain

        def _sync():
            records.append(dict(record))
            return MagicMock()

        chain.channel = _channel
        chain.message = _message
        chain.meta = _meta
        chain.should_store = _should_store
        chain.use_post = _use_post
        chain.sync = _sync
        return chain

    return _tracking


# ---------------------------------------------------------------------------
# Auto-mock create_pubnub_client for all tests in this module.
# The pubnub package is not installed in the test environment, so pipe
# tasks (which call create_pubnub_client for per-task clients) would fail
# without this patch.
# ---------------------------------------------------------------------------

@pytest.fixture(autouse=True)
def _patch_create_pubnub_client(monkeypatch):
    """Patch create_pubnub_client to return a MagicMock for per-task clients."""
    _per_task_publish_records.clear()
    _per_task_created_clients.clear()

    def _mock_create(**kwargs):
        mock_client = MagicMock()
        mock_client.set_token = MagicMock()
        mock_client.publish = _make_tracking_publish(_per_task_publish_records)
        mock_client.stop = MagicMock()
        _per_task_created_clients.append(mock_client)
        return mock_client
    monkeypatch.setattr(_ai_mod, "create_pubnub_client", _mock_create)


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

class TestPipeTaskLifecycle:
    """Pipe task lifecycle tests -- mirrors Node SDK agent_instance_pipe.test.ts."""

    def test_expire_task_signals_handler_cooperative_cancellation(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """ExpireTask signals the handler to stop. In Phase 3, the server
        publishes the terminal event for pipe tasks, not the agent. The agent
        only decrements refCount and cleans up."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_exited = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            for _ in range(100):
                if ctx.is_cancelled:
                    handler_exited.set()
                    return {}
                time.sleep(0.02)
            handler_exited.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "expire-task-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })
        time.sleep(0.1)

        _simulate_message(mock_pubnub, {
            "type": "ExpireTask",
            "taskId": "expire-task-1",
            "reason": "duration_expired",
        })

        assert handler_exited.wait(timeout=3.0), "Handler should exit after ExpireTask"

        # Agent publishes terminal after artifact for correct event ordering
        time.sleep(0.2)
        terminal = _find_publish(
            records, taskId="expire-task-1", type="terminal",
        )
        assert terminal is not None, \
            "Agent should publish terminal for pipe task expire"
        msg = terminal["message"]
        assert msg["state"] == "completed"
        assert msg.get("completionReason") == "duration_expired"

        result["stop"]()

    def test_cancel_task_on_pipe_signals_handler(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """CancelTask on pipe task signals cooperative cancellation.
        In Phase 3, the server publishes the terminal event for pipe tasks."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_exited = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            for _ in range(100):
                if ctx.is_cancelled:
                    handler_exited.set()
                    return {}
                time.sleep(0.02)
            handler_exited.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "cancel-pipe-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })
        time.sleep(0.1)

        _simulate_message(mock_pubnub, {
            "type": "CancelTask",
            "taskId": "cancel-pipe-1",
        })

        assert handler_exited.wait(timeout=3.0), "Handler should exit after CancelTask"

        # Agent publishes terminal after artifact for correct event ordering
        time.sleep(0.2)
        terminal = _find_publish(
            records, taskId="cancel-pipe-1", type="terminal",
        )
        assert terminal is not None, \
            "Agent should publish terminal for pipe task cancel"
        assert terminal["message"]["state"] == "canceled"

        result["stop"]()

    def test_pipe_handler_voluntary_return_does_not_publish_terminal(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Pipe handler voluntary return does NOT publish a terminal event."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        # Start a pipe task
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "voluntary-return-pipe",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        # Wait for handler to complete
        assert handler_done.wait(timeout=3.0), "Handler did not complete"
        # Give time for any async publishes
        time.sleep(0.1)

        # Verify NO terminal event was published for the pipe task
        terminal = _find_publish(
            records,
            taskId="voluntary-return-pipe",
            type="terminal",
        )
        assert terminal is None, "Pipe voluntary return should not produce terminal event"

        result["stop"]()

    def test_expire_after_handler_return_publishes_terminal_via_cached_creds(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """ExpireTask after handler return uses cached credentials to publish terminal."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "ext-expire-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
            "writeToken": "test-write-token",
        })

        assert handler_done.wait(timeout=3.0)
        time.sleep(0.1)

        # Handler has returned — send ExpireTask (external stream scenario)
        _simulate_message(mock_pubnub, {
            "type": "ExpireTask",
            "taskId": "ext-expire-1",
            "reason": "duration_expired",
        })

        time.sleep(0.2)

        terminal = _find_publish(
            records, taskId="ext-expire-1", type="terminal",
        )
        assert terminal is not None, \
            "ExpireTask after handler return should publish terminal via cached creds"
        assert terminal["message"]["state"] == "completed"
        assert terminal["message"].get("completionReason") == "duration_expired"

        result["stop"]()

    def test_cancel_after_handler_return_publishes_terminal_via_cached_creds(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """CancelTask after handler return uses cached credentials to publish terminal."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "ext-cancel-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
            "writeToken": "test-write-token",
        })

        assert handler_done.wait(timeout=3.0)
        time.sleep(0.1)

        _simulate_message(mock_pubnub, {
            "type": "CancelTask",
            "taskId": "ext-cancel-1",
        })

        time.sleep(0.2)

        terminal = _find_publish(
            records, taskId="ext-cancel-1", type="terminal",
        )
        assert terminal is not None, \
            "CancelTask after handler return should publish terminal via cached creds"
        assert terminal["message"]["state"] == "canceled"

        result["stop"]()

    def test_terminate_after_handler_return_publishes_terminal_via_cached_creds(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """TerminateTask after handler return uses cached credentials to publish terminal."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "ext-terminate-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
            "writeToken": "test-write-token",
        })

        assert handler_done.wait(timeout=3.0)
        time.sleep(0.1)

        _simulate_message(mock_pubnub, {
            "type": "TerminateTask",
            "taskId": "ext-terminate-1",
        })

        time.sleep(0.2)

        terminal = _find_publish(
            records, taskId="ext-terminate-1", type="terminal",
        )
        assert terminal is not None, \
            "TerminateTask after handler return should publish terminal via cached creds"
        assert terminal["message"]["state"] == "canceled"
        assert terminal["message"].get("reason") == "terminated"

        result["stop"]()

    def test_local_timer_fires_before_expire_task(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Local duration timer expires the task without server ExpireTask."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_exited = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            for _ in range(100):
                if ctx.is_cancelled:
                    handler_exited.set()
                    return {}
                time.sleep(0.02)
            handler_exited.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        # durationExpiresAtMs 0.2s from now — local timer should fire
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "timer-expire-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 1,
            "durationExpiresAtMs": int(time.time() * 1000) + 200,
            "writeToken": "test-token",
        })

        assert handler_exited.wait(timeout=3.0), "Handler should exit via local timer"
        time.sleep(0.2)

        terminal = _find_publish(
            records, taskId="timer-expire-1", type="terminal",
        )
        assert terminal is not None, \
            "Local timer should trigger terminal publish"
        assert terminal["message"]["state"] == "completed"
        assert terminal["message"].get("completionReason") == "duration_expired"

        result["stop"]()

    def test_local_timer_cancelled_on_voluntary_return(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Voluntary handler return cancels the local timer (no late expiry)."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        # durationExpiresAtMs 0.3s from now, but handler returns immediately
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "timer-voluntary-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 1,
            "durationExpiresAtMs": int(time.time() * 1000) + 300,
            "writeToken": "test-token",
        })

        assert handler_done.wait(timeout=3.0)
        # Wait past the expiresAt deadline
        time.sleep(0.5)

        terminal = _find_publish(
            records, taskId="timer-voluntary-1", type="terminal",
        )
        assert terminal is None, \
            "Timer should be cancelled on voluntary return — no terminal"

        result["stop"]()

    def test_cancel_before_local_timer_fires(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """CancelTask before local timer: terminal is canceled, not expired."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_exited = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            for _ in range(100):
                if ctx.is_cancelled:
                    handler_exited.set()
                    return {}
                time.sleep(0.02)
            handler_exited.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "timer-cancel-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 10,
            "durationExpiresAtMs": int(time.time() * 1000) + 600000,
            "writeToken": "test-token",
        })
        time.sleep(0.1)

        _simulate_message(mock_pubnub, {
            "type": "CancelTask",
            "taskId": "timer-cancel-1",
        })

        assert handler_exited.wait(timeout=3.0)
        time.sleep(0.2)

        terminal = _find_publish(
            records, taskId="timer-cancel-1", type="terminal",
        )
        assert terminal is not None, "CancelTask should produce terminal"
        assert terminal["message"]["state"] == "canceled"

        result["stop"]()

    def test_expire_task_arrives_before_local_timer_no_duplicate_terminal(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """ExpireTask before local timer fires: exactly one terminal, no duplicate."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_exited = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            for _ in range(200):
                if ctx.is_cancelled:
                    handler_exited.set()
                    return {}
                time.sleep(0.02)
            handler_exited.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        # durationExpiresAtMs 10 minutes from now — local timer will NOT fire soon
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "dedup-expire-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 10,
            "durationExpiresAtMs": int(time.time() * 1000) + 600000,
            "writeToken": "test-token",
        })
        time.sleep(0.1)

        # Send ExpireTask before local timer fires
        _simulate_message(mock_pubnub, {
            "type": "ExpireTask",
            "taskId": "dedup-expire-1",
            "reason": "duration_expired",
        })

        assert handler_exited.wait(timeout=3.0), "Handler should exit after ExpireTask"
        time.sleep(0.2)

        # Assert terminal published with correct state
        terminal = _find_publish(
            records, taskId="dedup-expire-1", type="terminal",
        )
        assert terminal is not None, \
            "ExpireTask should trigger terminal publish"
        assert terminal["message"]["state"] == "completed"
        assert terminal["message"].get("completionReason") == "duration_expired"

        # Count all terminal publishes for this task (instance + per-task clients)
        terminal_count = sum(
            1 for r in list(records) + list(_per_task_publish_records)
            if r.get("message", {}).get("taskId") == "dedup-expire-1"
            and r.get("message", {}).get("type") == "terminal"
        )
        assert terminal_count == 1, \
            f"Expected exactly 1 terminal publish, got {terminal_count}"

        result["stop"]()

    def test_request_task_still_auto_completes_on_handler_return(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Request task still auto-completes on handler return (regression)."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        # Start a request task (default taskKind)
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "request-task-1",
            "ownerId": "user1",
        })

        # Wait for terminal/completed event
        def _completed_found():
            return _find_publish(
                records,
                taskId="request-task-1",
                type="terminal",
                state="completed",
            ) is not None

        _wait_for(_completed_found)

        result["stop"]()

    def test_is_expired_true_after_expire_task(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """is_expired is true when handler detects cancellation after ExpireTask."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        captured_is_expired: list = []

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            for _ in range(200):
                if ctx.is_cancelled:
                    captured_is_expired.append(ctx.is_expired)
                    return {}
                time.sleep(0.01)
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "expire-flag-check",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        time.sleep(0.05)

        _simulate_message(mock_pubnub, {
            "type": "ExpireTask",
            "taskId": "expire-flag-check",
        })

        _wait_for(lambda: len(captured_is_expired) > 0)

        assert captured_is_expired[0] is True, "is_expired should be True after ExpireTask"

        result["stop"]()

    def test_is_expired_false_after_cancel_task(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """is_expired is false when handler detects cancellation after CancelTask."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        captured_is_expired: list = []

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            for _ in range(200):
                if ctx.is_cancelled:
                    captured_is_expired.append(ctx.is_expired)
                    return {}
                time.sleep(0.01)
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "cancel-flag-check",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        time.sleep(0.05)

        _simulate_message(mock_pubnub, {
            "type": "CancelTask",
            "taskId": "cancel-flag-check",
        })

        _wait_for(lambda: len(captured_is_expired) > 0)

        assert captured_is_expired[0] is False, "is_expired should be False after CancelTask"

        result["stop"]()

    def test_create_stream_api_available_on_task_context(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """The unified create_stream() API is available on TaskContext.
        In Phase 3, streams use the setup handshake, so actual stream
        creation requires a live Functions backend. This test verifies
        the API is callable and raises appropriately without a live backend."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        ctx_ref = [None]
        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            ctx_ref[0] = ctx
            handler_done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                concurrency=3,

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "ctx-check",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
            "hasStream": True,
        })

        assert handler_done.wait(timeout=3.0)
        ctx = ctx_ref[0]
        assert ctx is not None
        assert hasattr(ctx, "create_stream")
        assert callable(ctx.create_stream)

        result["stop"]()


class TestPerTaskPubNubClient:
    """Per-task PubNub client tests for pipe tasks (D7: two-tier connections)."""

    def test_pipe_write_token_not_applied_to_instance_pubnub(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Pipe task writeToken is NOT applied to the instance PubNub client."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_done.set()
            time.sleep(0.05)
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        # Record setToken calls before sending the pipe task
        mock_pubnub.set_token.reset_mock()

        # Start a pipe task WITH writeToken
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "pipe-with-token",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
            "writeToken": "per-task-write-token-abc",
        })

        assert handler_done.wait(timeout=3.0)

        # The instance pubnub.set_token should NOT be called with the pipe writeToken
        pipe_token_calls = [
            c for c in mock_pubnub.set_token.call_args_list
            if c.args and c.args[0] == "per-task-write-token-abc"
        ]
        assert len(pipe_token_calls) == 0, \
            "Pipe writeToken should not be applied to instance PubNub"

        result["stop"]()

    def test_request_tasks_use_per_task_pubnub_for_write_token(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """In Phase 3, ALL tasks (including request) use per-task PubNub.
        The instance PubNub (controlClient) should NOT have writeToken applied."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        mock_pubnub.set_token.reset_mock()

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "request-with-token",
            "ownerId": "user1",
            "writeToken": "request-write-token-xyz",
        })

        assert handler_done.wait(timeout=3.0)
        time.sleep(0.2)

        # The instance pubnub.set_token should NOT be called with the task writeToken
        # (it is applied to the per-task PubNub client instead)
        request_token_calls = [
            c for c in mock_pubnub.set_token.call_args_list
            if c.args and c.args[0] == "request-write-token-xyz"
        ]
        assert len(request_token_calls) == 0, \
            "Request writeToken should NOT be applied to instance PubNub (Phase 3 three-tier model)"

        # Verify per-task client received the token
        assert len(_per_task_created_clients) >= 1
        per_task_token_calls = [
            c for client in _per_task_created_clients
            for c in client.set_token.call_args_list
            if c.args and c.args[0] == "request-write-token-xyz"
        ]
        assert len(per_task_token_calls) >= 1, \
            "Request writeToken should be applied to per-task PubNub"

        result["stop"]()

    def test_expire_task_handler_only_signals_in_flight_tasks(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """ExpireTask for a non-existent task should not crash."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=lambda task, ctx: {},
            )
        )

        # Send ExpireTask for a non-existent task -- should log but not crash
        _simulate_message(mock_pubnub, {
            "type": "ExpireTask",
            "taskId": "non-existent-task",
        })

        # Give time for any processing
        time.sleep(0.1)

        # No terminal event should be published for non-existent task
        terminal = _find_publish(
            records,
            taskId="non-existent-task",
            type="terminal",
        )
        assert terminal is None, \
            "No terminal event should be published for non-existent task on ExpireTask"

        result["stop"]()

    def test_terminate_task_signals_handler_for_pipe_task(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """TerminateTask signals the handler to exit. In Phase 3, the server
        publishes the terminal for pipe tasks. Agent only decrements refCount."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_running = threading.Event()
        handler_exited = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_running.set()
            while not ctx.is_cancelled:
                time.sleep(0.02)
            handler_exited.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "terminate-test-1",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        assert handler_running.wait(timeout=3.0), "Handler did not start"

        _simulate_message(mock_pubnub, {
            "type": "TerminateTask",
            "taskId": "terminate-test-1",
        })

        assert handler_exited.wait(timeout=3.0), "Handler should exit after TerminateTask"

        # Agent publishes terminal after artifact for correct event ordering
        time.sleep(0.2)
        terminal = _find_publish(
            records, taskId="terminate-test-1", type="terminal",
        )
        assert terminal is not None, \
            "Agent should publish terminal for pipe task terminate"
        assert terminal["message"].get("reason") == "terminated"

        result["stop"]()

    def test_terminate_task_non_in_flight_is_ignored(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """TerminateTask for non-in-flight task should be silently ignored."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=lambda t, c: {},
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "TerminateTask",
            "taskId": "never-started",
        })

        time.sleep(0.05)

        terminal = _find_publish(
            records,
            taskId="never-started",
            type="terminal",
        )
        assert terminal is None, \
            "No terminal event should be published for non-in-flight TerminateTask"

        result["stop"]()

    def test_concurrent_pipe_tasks_use_separate_per_task_clients(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Two concurrent pipe tasks get separate per-task PubNub clients
        with independent token isolation (neither token touches instance PubNub)."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        # Events to keep both handlers in-flight simultaneously
        both_started = threading.Barrier(2, timeout=5.0)
        release = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            both_started.wait()  # block until both tasks are running
            release.wait(timeout=5.0)  # hold open until test releases
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                concurrency=2,

                handler=_handler,
            )
        )

        # Reset instance set_token tracking before sending pipe tasks
        mock_pubnub.set_token.reset_mock()

        # Start two pipe tasks with different writeTokens
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "concurrent-pipe-A",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
            "writeToken": "token-A",
        })
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "concurrent-pipe-B",
            "ownerId": "user2",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
            "writeToken": "token-B",
        })

        # Wait until both handlers are running concurrently (barrier passed)
        _wait_for(lambda: both_started.n_waiting == 0 or both_started.broken, timeout_sec=5.0)
        # Small grace period for per-task client setup to complete
        time.sleep(0.05)

        # --- Assertions ---

        # At least 2 per-task clients were created
        assert len(_per_task_created_clients) >= 2, (
            f"Expected at least 2 per-task clients, got {len(_per_task_created_clients)}"
        )

        # Collect which tokens each per-task client received via set_token
        clients_by_token: dict[str, MagicMock] = {}
        for client in _per_task_created_clients:
            for call in client.set_token.call_args_list:
                tok = call.args[0] if call.args else None
                if tok in ("token-A", "token-B"):
                    clients_by_token[tok] = client

        # One per-task client got token-A
        assert "token-A" in clients_by_token, (
            "No per-task client had set_token called with 'token-A'"
        )
        # A different per-task client got token-B
        assert "token-B" in clients_by_token, (
            "No per-task client had set_token called with 'token-B'"
        )
        # The two tokens were applied to different clients
        assert clients_by_token["token-A"] is not clients_by_token["token-B"], (
            "token-A and token-B should be on separate per-task clients"
        )

        # Instance PubNub should NOT have set_token called with either pipe token
        instance_token_calls = [
            c for c in mock_pubnub.set_token.call_args_list
            if c.args and c.args[0] in ("token-A", "token-B")
        ]
        assert len(instance_token_calls) == 0, (
            "Pipe writeTokens should not be applied to the instance PubNub client"
        )

        # Release handlers and clean up
        release.set()
        _wait_for(lambda: True)  # let threads finish
        time.sleep(0.1)

        result["stop"]()

    def test_per_task_client_always_created_for_pipe_tasks(
        self, mock_pubnub: MagicMock, monkeypatch,
    ) -> None:
        """Per-task PubNub client is created for pipe tasks even without write_token.

        Mirrors Node test: 'per-task client always created for pipe tasks'.
        Verifies that create_pubnub_client is called when a pipe task arrives
        without write_token, and that set_token is NOT called on the per-task client.
        """
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        # Track create_pubnub_client calls via monkeypatch
        created_clients: list = []

        def _mock_create(**kwargs):
            mock_client = MagicMock()
            mock_client._create_kwargs = kwargs
            mock_client.set_token = MagicMock()
            # Wire up publish chain so the agent can publish events
            mock_client.publish = _make_tracking_publish(records)
            created_clients.append(mock_client)
            return mock_client

        monkeypatch.setattr(_ai_mod, "create_pubnub_client", _mock_create)

        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_done.set()
            time.sleep(0.05)
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        # Start a pipe task WITHOUT writeToken
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "pipe-no-token",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        assert handler_done.wait(timeout=3.0), "Handler did not complete"

        # Per-task client should be created even without writeToken
        assert len(created_clients) >= 1, \
            "create_pubnub_client should be called for pipe task without writeToken"

        # set_token should NOT be called on the per-task client since no writeToken
        per_task_client = created_clients[-1]
        per_task_client.set_token.assert_not_called()

        result["stop"]()


class TestCapacityRollbackOnStartupFailure:
    """P0 bug fix: per-task client creation failure must roll back capacity state."""

    def test_per_task_client_creation_failure_rolls_back_capacity(
        self, mock_pubnub: MagicMock, monkeypatch,
    ) -> None:
        """If per-task client creation throws, capacity state is rolled back
        and a terminal:failed event is published."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        call_count = 0

        def _failing_create(**kwargs):
            nonlocal call_count
            call_count += 1
            raise RuntimeError("PubNub client creation failed")

        monkeypatch.setattr(_ai_mod, "create_pubnub_client", _failing_create)

        handler_called = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_called.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                concurrency=1,

                handler=_handler,
            )
        )

        # Start a pipe task (triggers per-task client creation which will fail)
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "fail-create-task",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        # Give time for processing
        time.sleep(0.2)

        # Handler should NOT have been called
        assert not handler_called.is_set(), "Handler should not be called when client creation fails"

        # A terminal:failed event should be published
        terminal = _find_publish(
            records,
            taskId="fail-create-task",
            type="terminal",
            state="failed",
        )
        assert terminal is not None, "terminal:failed should be published on client creation failure"

        # Capacity should be restored: a second task should succeed
        # Re-patch to allow creation this time
        monkeypatch.setattr(_ai_mod, "create_pubnub_client", lambda **kw: MagicMock(
            set_token=MagicMock(),
            publish=_make_tracking_publish(records),
            stop=MagicMock(),
        ))

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "recovery-task",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        _wait_for(lambda: handler_called.is_set(), timeout_sec=3.0)
        assert handler_called.is_set(), "After rollback, next task should be accepted"

        result["stop"]()


class TestCancelExpireRaceCondition:
    """P1 bug fix: cancel event must be registered before executor.submit
    so CancelTask/ExpireTask arriving early still works."""

    def test_cancel_before_worker_starts_still_signals_handler(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """CancelTask arriving between state increment and worker thread start
        still signals the cancel event in the handler."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        captured_is_cancelled: list = []

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            # Poll for cancellation
            for _ in range(200):
                if ctx.is_cancelled:
                    captured_is_cancelled.append(True)
                    return {}
                time.sleep(0.01)
            captured_is_cancelled.append(False)
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        # Start a pipe task
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "early-cancel-race",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        # Immediately send CancelTask (before worker thread likely started)
        _simulate_message(mock_pubnub, {
            "type": "CancelTask",
            "taskId": "early-cancel-race",
        })

        # Wait for handler to notice cancellation
        _wait_for(lambda: len(captured_is_cancelled) > 0, timeout_sec=3.0)
        assert captured_is_cancelled[0] is True, \
            "Cancel event should be signaled even when CancelTask arrives before worker starts"

        result["stop"]()

    def test_expire_before_worker_starts_still_signals_handler(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """ExpireTask arriving between state increment and worker thread start
        still signals the cancel event and sets is_expired."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        captured: list = []

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            for _ in range(200):
                if ctx.is_cancelled:
                    captured.append({"cancelled": True, "expired": ctx.is_expired})
                    return {}
                time.sleep(0.01)
            captured.append({"cancelled": False, "expired": False})
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",

                handler=_handler,
            )
        )

        # Start a pipe task
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "early-expire-race",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        # Immediately send ExpireTask (before worker thread likely started)
        _simulate_message(mock_pubnub, {
            "type": "ExpireTask",
            "taskId": "early-expire-race",
        })

        _wait_for(lambda: len(captured) > 0, timeout_sec=3.0)
        assert captured[0]["cancelled"] is True, \
            "Cancel event should be signaled by ExpireTask arriving before worker starts"
        assert captured[0]["expired"] is True, \
            "is_expired should be True when ExpireTask arrives before worker starts"

        result["stop"]()


class TestPipeDurationGuard:
    """Provider-side guard: reject pipe StartTask with missing/invalid duration.

    Mirrors Node SDK parity tests for Fix 4 provider-side validation.
    """

    def test_pipe_start_task_no_duration_rejected(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Pipe StartTask without duration/durationExpiresAtMs is rejected
        with terminal failed and error 'invalid_start_task'."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_called = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_called.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                handler=_handler,
            )
        )

        # Pipe StartTask with NO duration fields
        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "no-duration-pipe",
            "ownerId": "user1",
            "taskKind": "pipe",
        })

        time.sleep(0.3)

        assert not handler_called.is_set(), \
            "Handler must NOT be called for pipe StartTask with missing duration"

        terminal = _find_publish(
            records, taskId="no-duration-pipe", type="terminal",
        )
        assert terminal is not None, \
            "Terminal failed should be published for invalid pipe StartTask"
        assert terminal["message"]["state"] == "failed"
        assert terminal["message"]["error"] == "invalid_start_task"

        result["stop"]()

    def test_pipe_start_task_zero_duration_rejected(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Pipe StartTask with duration=0 is rejected."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_called = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_called.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "zero-dur-pipe",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 0,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        time.sleep(0.3)

        assert not handler_called.is_set(), \
            "Handler must NOT be called for pipe StartTask with duration=0"

        terminal = _find_publish(
            records, taskId="zero-dur-pipe", type="terminal",
        )
        assert terminal is not None
        assert terminal["message"]["state"] == "failed"
        assert terminal["message"]["error"] == "invalid_start_task"

        result["stop"]()

    def test_pipe_start_task_bool_duration_rejected(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Pipe StartTask with duration=True is rejected (bool is not int)."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_called = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_called.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "bool-dur-pipe",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": True,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        time.sleep(0.3)

        assert not handler_called.is_set(), \
            "Handler must NOT be called for pipe StartTask with duration=True"

        terminal = _find_publish(
            records, taskId="bool-dur-pipe", type="terminal",
        )
        assert terminal is not None
        assert terminal["message"]["state"] == "failed"
        assert terminal["message"]["error"] == "invalid_start_task"

        result["stop"]()

    def test_pipe_start_task_exceeding_max_duration_rejected(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Pipe StartTask with duration > 43200 is rejected."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_called = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_called.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "over-max-pipe",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 43201,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        time.sleep(0.3)

        assert not handler_called.is_set(), \
            "Handler must NOT be called for pipe StartTask with duration > 43200"

        terminal = _find_publish(
            records, taskId="over-max-pipe", type="terminal",
        )
        assert terminal is not None
        assert terminal["message"]["state"] == "failed"
        assert terminal["message"]["error"] == "invalid_start_task"

        result["stop"]()

    def test_pipe_start_task_missing_expires_at_rejected(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Pipe StartTask with valid duration but missing durationExpiresAtMs
        is rejected."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_called = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_called.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "no-expires-pipe",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
        })

        time.sleep(0.3)

        assert not handler_called.is_set(), \
            "Handler must NOT be called for pipe StartTask without durationExpiresAtMs"

        terminal = _find_publish(
            records, taskId="no-expires-pipe", type="terminal",
        )
        assert terminal is not None
        assert terminal["message"]["state"] == "failed"
        assert terminal["message"]["error"] == "invalid_start_task"

        result["stop"]()

    def test_pipe_start_task_valid_duration_accepted(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Pipe StartTask with valid duration and durationExpiresAtMs starts
        the handler normally."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_called = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_called.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "valid-pipe-task",
            "ownerId": "user1",
            "taskKind": "pipe",
            "duration": 60,
            "durationExpiresAtMs": int(time.time() * 1000) + 3600000,
        })

        _wait_for(lambda: handler_called.is_set(), timeout_sec=3.0)
        assert handler_called.is_set(), \
            "Handler should be called for valid pipe StartTask"

        result["stop"]()

    def test_request_start_task_without_duration_accepted(
        self, mock_pubnub: MagicMock,
    ) -> None:
        """Request StartTask (no taskKind or taskKind='request') without
        duration is accepted normally -- guard only applies to pipe."""
        records: list = []
        mock_pubnub.publish = _make_tracking_publish(records)

        handler_called = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext):
            handler_called.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                handler=_handler,
            )
        )

        _simulate_message(mock_pubnub, {
            "type": "StartTask",
            "taskId": "request-no-dur",
            "ownerId": "user1",
        })

        _wait_for(lambda: handler_called.is_set(), timeout_sec=3.0)
        assert handler_called.is_set(), \
            "Handler should be called for request StartTask without duration"

        result["stop"]()
