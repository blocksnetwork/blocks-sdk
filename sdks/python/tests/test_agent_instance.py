"""
Tests for blocks_network.agent_instance -- core agent instance runtime behavior.

Updated for Phase 3: three-tier connection model where ALL tasks use
per-task PubNub clients.

All tests use a mocked PubNub client (no real network calls).
"""

from __future__ import annotations

import sys
import time
import threading
from unittest.mock import MagicMock, patch, call

import pytest

from blocks_network.agent_instance import start_agent_instance, _extract_owner_id
from blocks_network.agent_registry import ConnectAgentResult
from blocks_network.types import AgentInstanceOptions

from tests.conftest import minimal_card


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_chain_mock():
    """A builder-chain mock for PubNub."""
    mock = MagicMock()
    for method in (
        "channel", "channels", "message", "meta", "should_store",
        "use_post", "with_presence", "state", "file_name",
        "file_object", "file_id", "execute",
    ):
        getattr(mock, method).side_effect = lambda *a, _c=mock, **kw: _c
    mock.sync.return_value = MagicMock()
    return mock


def _make_mock_pubnub():
    """Create a fully-stubbed PubNub client."""
    pn = MagicMock()
    pn.publish.return_value = _make_chain_mock()
    pn.subscribe.return_value = _make_chain_mock()
    pn.set_state.return_value = _make_chain_mock()
    pn.unsubscribe.return_value = _make_chain_mock()
    pn.download_file.return_value = _make_chain_mock()
    pn.here_now.return_value = _make_chain_mock()

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
    mock_pn: MagicMock, msg: dict, meta: dict | None = None,
) -> None:
    """Simulate receiving a control message on the PubNub listener."""
    assert len(mock_pn._listeners) > 0, "No listener registered on mock PubNub"
    listener = mock_pn._listeners[0]

    if hasattr(listener, "message") and callable(listener.message):
        event = MagicMock()
        event.message = msg
        event.user_metadata = meta
        if isinstance(listener, dict):
            listener["message"](event)
        else:
            listener.message(mock_pn, event)
    elif isinstance(listener, dict) and "message" in listener:
        event = MagicMock()
        event.message = msg
        event.user_metadata = meta
        listener["message"](event)
    else:
        raise RuntimeError("Cannot dispatch message to registered listener")


def _wait_for(predicate, timeout_sec=2.0, poll_sec=0.05):
    """Spin-wait until predicate returns True or timeout expires."""
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(poll_sec)
    assert predicate(), "Timed out waiting for predicate to become True"


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestStartAgentInstanceReturnsStop:
    def test_start_agent_instance_returns_stop(self) -> None:
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",

            )
        )
        assert callable(result["stop"])
        assert result["agent_name"] == "acme_echo"
        assert isinstance(result["instance_id"], str)
        assert result["instance_id"].startswith("AG-acme_echo-")
        result["stop"]()


class TestAgentNameRequired:
    def test_agent_name_required(self) -> None:
        pn = _make_mock_pubnub()

        with pytest.raises(ValueError, match="agent_name is required"):
            start_agent_instance(
                AgentInstanceOptions(card=minimal_card(), pubnub=pn, agent_name="")
            )

    def test_leaked_agent_name_env_does_not_override_options(self, monkeypatch) -> None:
        """Leaked AGENT_NAME env var must not affect options.agent_name."""
        monkeypatch.setenv("AGENT_NAME", "leaked_name")
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(card=minimal_card(), pubnub=pn, agent_name="correct_name")
        )
        assert result["agent_name"] == "correct_name"
        result["stop"]()

    def test_leaked_agent_name_env_does_not_provide_fallback(self, monkeypatch) -> None:
        """Leaked AGENT_NAME env var must not serve as a fallback."""
        monkeypatch.setenv("AGENT_NAME", "leaked_name")
        pn = _make_mock_pubnub()
        with pytest.raises(ValueError, match="agent_name is required"):
            start_agent_instance(
                AgentInstanceOptions(card=minimal_card(), pubnub=pn, agent_name="")
            )


class TestCardRequired:
    """``card`` is a required AgentInstanceOptions field.

    Parity with Node: ``blocks-sdk/sdks/node/src/runtime/agent-instance.ts:108``
    declares ``card: AgentCard`` (non-optional), enforced at the type level.
    Python has no compile-time type check, so the parity lives at two
    levels:

    1. The dataclass has no default for ``card`` (see
       ``blocks_network/types.py``) — constructing ``AgentInstanceOptions()``
       without a card raises ``TypeError``.
    2. ``start_agent_instance`` (``blocks_network/agent_instance.py``)
       re-validates at entry and raises a ``ValueError`` whose message
       clearly names the missing field.
    """

    def test_agent_instance_options_without_card_raises_type_error(self) -> None:
        """Constructing AgentInstanceOptions with no card is a TypeError."""
        with pytest.raises(TypeError):
            AgentInstanceOptions()  # type: ignore[call-arg]

    def test_start_agent_instance_rejects_empty_card_dict(self) -> None:
        """Even if a caller forces ``card={}`` past the dataclass, the
        runtime check rejects the start."""
        pn = _make_mock_pubnub()
        with pytest.raises(ValueError, match="card is required"):
            start_agent_instance(
                AgentInstanceOptions(card={}, pubnub=pn, agent_name="acme_echo")
            )

    def test_start_agent_instance_rejects_non_dict_card(self) -> None:
        """Wrong-typed card is rejected with the same fail-fast message."""
        pn = _make_mock_pubnub()
        with pytest.raises(ValueError, match="card is required"):
            start_agent_instance(
                AgentInstanceOptions(
                    card="not-a-dict",  # type: ignore[arg-type]
                    pubnub=pn,
                    agent_name="acme_echo",
                )
            )

    def test_start_agent_instance_rejects_none_options(self) -> None:
        """Calling with ``options=None`` must not silently default the card."""
        with pytest.raises(ValueError, match="options is required"):
            start_agent_instance(None)


class TestCardRequiredParityWithNode:
    """Cross-SDK parity: Python's runtime error shape matches Node's
    type-level contract.

    Node's equivalent assertion lives at
    ``blocks-sdk/sdks/node/tests/consumer-simplify-phase-b.test.ts:302``
    ("card is required on AgentInstanceOptions (type level)"), which
    compiles only because ``AgentInstanceOptions.card: AgentCard`` has
    no ``?`` in ``blocks-sdk/sdks/node/src/runtime/agent-instance.ts:108``.

    The parity invariant this test pins: in both SDKs, a caller cannot
    start an agent instance without supplying a card. In Node the check
    is at compile time; in Python it is at dataclass construction and
    at ``start_agent_instance`` entry. The user-visible failure mode
    (a clear error naming ``card``) is the same.
    """

    def test_python_runtime_check_names_card_field(self) -> None:
        """The runtime error must name the ``card`` field so users can
        fix it without reading the stack trace."""
        pn = _make_mock_pubnub()
        with pytest.raises(ValueError) as exc_info:
            start_agent_instance(
                AgentInstanceOptions(card={}, pubnub=pn, agent_name="acme_echo")
            )
        msg = str(exc_info.value)
        assert "card" in msg
        assert "required" in msg

    def test_python_matches_node_card_shape(self) -> None:
        """A card built in the shape used by Node's parity test
        (``consumer-simplify-phase-b.test.ts:302``) starts successfully
        in Python with no additional translation."""
        pn = _make_mock_pubnub()
        # Same minimum-viable card shape used in the Node parity test.
        node_shaped_card = {
            "identity": {
                "agentName": "test",
                "displayName": "Test",
                "description": "Test",
                "version": "1.0.0",
                "provider": {"organization": "test-org"},
            },
            "capabilities": {"taskKinds": ["request"]},
            "skills": [],
            "streams": {"_default": {"direction": "outbound", "format": "bytes"}},
        }
        result = start_agent_instance(
            AgentInstanceOptions(
                card=node_shaped_card,
                pubnub=pn,
                agent_name="test",
            )
        )
        try:
            assert result["agent_name"] == "test"
        finally:
            result["stop"]()


class TestBlocksApiKeyRequired:
    def test_raises_without_blocks_api_key(self, monkeypatch) -> None:
        """start_agent_instance() fails fast when BLOCKS_API_KEY is not set."""
        pn = _make_mock_pubnub()
        monkeypatch.delenv("BLOCKS_API_KEY", raising=False)

        with pytest.raises(RuntimeError, match="BLOCKS_API_KEY is required"):
            start_agent_instance(
                AgentInstanceOptions(card=minimal_card(), pubnub=pn, agent_name="acme_echo")
            )


class TestCapacityNack:
    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_capacity_nack(self, mock_create) -> None:
        """When at capacity, a new StartTask publishes terminal/failed."""
        pn = _make_mock_pubnub()
        mock_per_task = _make_mock_pubnub()
        mock_create.return_value = mock_per_task

        publish_records = []

        def _tracking_publish():
            chain = MagicMock()
            record = {}
            chain.channel = lambda ch: (record.__setitem__("channel", ch), chain)[1]
            chain.message = lambda msg: (record.__setitem__("message", msg), chain)[1]
            chain.meta = lambda m: (record.__setitem__("meta", m), chain)[1]
            chain.should_store = lambda v: chain
            chain.use_post = lambda v: chain
            chain.sync = lambda: (publish_records.append(dict(record)), MagicMock())[1]
            return chain

        pn.publish = _tracking_publish

        blocker = threading.Event()

        def blocking_handler(task, ctx):
            blocker.wait(timeout=5)

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                handler=blocking_handler,

                concurrency=1,
            )
        )
        time.sleep(0.2)

        # Start first task (fills capacity)
        _simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t1",
            "agentName": "acme_echo", "ownerId": "alice",
            "taskKind": "request", "hasStream": False,
        }, {"instance": result["instance_id"]})
        time.sleep(0.3)

        # Start second task (should NACK -- targeted, not broadcast)
        _simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t2",
            "agentName": "acme_echo", "ownerId": "alice",
            "taskKind": "request", "hasStream": False,
        }, {"instance": result["instance_id"]})
        time.sleep(0.3)

        nack = [r for r in publish_records
                if isinstance(r.get("message"), dict) and
                r["message"].get("error") == "agent_at_capacity"]
        assert len(nack) >= 1

        blocker.set()
        time.sleep(0.3)
        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_broadcast_at_capacity_ignored(self, mock_create) -> None:
        """Broadcast messages at capacity are silently ignored."""
        pn = _make_mock_pubnub()
        mock_create.return_value = _make_mock_pubnub()

        blocker = threading.Event()

        def blocking_handler(task, ctx):
            blocker.wait(timeout=5)

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                handler=blocking_handler,

                concurrency=1,
            )
        )
        time.sleep(0.2)

        _simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t1",
            "agentName": "acme_echo", "ownerId": "alice",
            "taskKind": "request", "hasStream": False,
        }, {"instance": result["instance_id"]})
        time.sleep(0.3)

        # Broadcast at capacity -- should be silently ignored
        _simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t2",
            "agentName": "acme_echo", "ownerId": "alice",
            "taskKind": "request", "hasStream": False,
        }, {"broadcast": "true"})

        blocker.set()
        time.sleep(0.3)
        result["stop"]()


class TestCooperativeCancellation:
    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_cancel_sets_event_and_handler_returns_canceled(self, mock_create) -> None:
        pn = _make_mock_pubnub()
        mock_per_task = _make_mock_pubnub()
        mock_create.return_value = mock_per_task

        publish_records = []

        def _tracking_publish():
            chain = MagicMock()
            record = {}
            chain.channel = lambda ch: (record.__setitem__("channel", ch), chain)[1]
            chain.message = lambda msg: (record.__setitem__("message", msg), chain)[1]
            chain.meta = lambda m: (record.__setitem__("meta", m), chain)[1]
            chain.should_store = lambda v: chain
            chain.use_post = lambda v: chain
            chain.sync = lambda: (publish_records.append(dict(record)), MagicMock())[1]
            return chain

        mock_per_task.publish = _tracking_publish

        cancel_seen = threading.Event()

        def handler(task, ctx):
            ctx.cancel_event.wait(timeout=5)
            cancel_seen.set()

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                handler=handler,

                concurrency=4,
            )
        )
        time.sleep(0.2)

        _simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t1",
            "agentName": "acme_echo", "ownerId": "alice",
            "taskKind": "request", "hasStream": False,
        })
        time.sleep(0.2)

        _simulate_start_task(pn, {
            "type": "CancelTask", "taskId": "t1",
        })

        assert cancel_seen.wait(timeout=3)
        time.sleep(0.3)

        terminals = [r for r in publish_records
                     if isinstance(r.get("message"), dict) and
                     r["message"].get("type") == "terminal"]
        assert len(terminals) >= 1
        assert terminals[-1]["message"]["state"] == "canceled"

        result["stop"]()


class TestNoObsChannelPublish:
    def test_no_obs_channel_publish(self) -> None:
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",

            )
        )
        time.sleep(0.2)
        result["stop"]()
        # No calls to obs.* channels


class TestPublishMetaIncludesAgentName:
    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_publish_meta_includes_agent_name(self, mock_create) -> None:
        pn = _make_mock_pubnub()
        mock_per_task = _make_mock_pubnub()
        mock_create.return_value = mock_per_task

        publish_records = []

        def _tracking_publish():
            chain = MagicMock()
            record = {}
            chain.channel = lambda ch: (record.__setitem__("channel", ch), chain)[1]
            chain.message = lambda msg: (record.__setitem__("message", msg), chain)[1]
            chain.meta = lambda m: (record.__setitem__("meta", m), chain)[1]
            chain.should_store = lambda v: chain
            chain.use_post = lambda v: chain
            chain.sync = lambda: (publish_records.append(dict(record)), MagicMock())[1]
            return chain

        mock_per_task.publish = _tracking_publish

        handler_done = threading.Event()

        def handler(task, ctx):
            handler_done.set()

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                handler=handler,

            )
        )
        time.sleep(0.2)

        _simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t1",
            "agentName": "acme_echo", "ownerId": "alice",
            "taskKind": "request", "hasStream": False,
        })
        handler_done.wait(timeout=2)
        time.sleep(0.3)

        meta_calls = [r.get("meta") for r in publish_records if r.get("meta")]
        assert len(meta_calls) > 0
        for m in meta_calls:
            assert m.get("agentName") == "acme_echo"
            assert "taskId" in m

        result["stop"]()


class TestPresenceStateSetOnStartup:
    @patch(
        "blocks_network.agent_registry.connect_agent",
        return_value=ConnectAgentResult(control_channel="agent.test-id.control"),
    )
    def test_presence_state_set_on_startup(self, _mock_connect) -> None:
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",

            )
        )
        time.sleep(0.3)
        pn.set_state.assert_called()
        result["stop"]()



class TestSubscribeToControlChannel:
    @patch(
        "blocks_network.agent_registry.connect_agent",
        return_value=ConnectAgentResult(control_channel="agent.test-id.control"),
    )
    def test_subscribe_to_control_channel(self, _mock_connect) -> None:
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",

            )
        )
        time.sleep(0.3)
        pn.subscribe.assert_called()
        result["stop"]()


class TestFilterExpressionSet:
    def test_filter_expression_set(self) -> None:
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",

            )
        )
        pn.set_filter_expression.assert_called_once()
        result["stop"]()


class TestStopFunction:
    @patch(
        "blocks_network.agent_registry.connect_agent",
        return_value=ConnectAgentResult(control_channel="agent.test-id.control"),
    )
    def test_stop_removes_listener_and_unsubscribes(self, _mock_connect) -> None:
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",

            )
        )
        time.sleep(0.3)
        result["stop"]()
        pn.remove_listener.assert_called()
        pn.unsubscribe.assert_called()


class TestOwnerIdFromMessage:
    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_uses_owner_id_from_message(self, mock_create) -> None:
        pn = _make_mock_pubnub()
        mock_per_task = _make_mock_pubnub()
        mock_create.return_value = mock_per_task

        publish_records = []

        def _tracking_publish():
            chain = MagicMock()
            record = {}
            chain.channel = lambda ch: (record.__setitem__("channel", ch), chain)[1]
            chain.message = lambda msg: (record.__setitem__("message", msg), chain)[1]
            chain.meta = lambda m: (record.__setitem__("meta", m), chain)[1]
            chain.should_store = lambda v: chain
            chain.use_post = lambda v: chain
            chain.sync = lambda: (publish_records.append(dict(record)), MagicMock())[1]
            return chain

        mock_per_task.publish = _tracking_publish

        done = threading.Event()

        def handler(task, ctx):
            done.set()

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                handler=handler,

            )
        )
        time.sleep(0.2)

        _simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t1",
            "agentName": "acme_echo", "ownerId": "bob",
            "taskKind": "request", "hasStream": False,
        })
        done.wait(timeout=2)
        time.sleep(0.3)

        channels = [r.get("channel", "") for r in publish_records]
        assert any("u.bob" in ch for ch in channels)

        result["stop"]()


class TestTokenManagement:
    def test_set_token_called(self) -> None:
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                token="test-token",

            )
        )
        pn.set_token.assert_called_with("test-token")
        result["stop"]()

    def test_set_token_not_called_without_token(self) -> None:
        pn = _make_mock_pubnub()
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",

            )
        )
        pn.set_token.assert_not_called()
        result["stop"]()


class TestHandlerError:
    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_exception_publishes_terminal_failed(self, mock_create) -> None:
        pn = _make_mock_pubnub()
        mock_per_task = _make_mock_pubnub()
        mock_create.return_value = mock_per_task

        publish_records = []

        def _tracking_publish():
            chain = MagicMock()
            record = {}
            chain.channel = lambda ch: (record.__setitem__("channel", ch), chain)[1]
            chain.message = lambda msg: (record.__setitem__("message", msg), chain)[1]
            chain.meta = lambda m: (record.__setitem__("meta", m), chain)[1]
            chain.should_store = lambda v: chain
            chain.use_post = lambda v: chain
            chain.sync = lambda: (publish_records.append(dict(record)), MagicMock())[1]
            return chain

        mock_per_task.publish = _tracking_publish

        def handler(task, ctx):
            raise ValueError("test error")

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                handler=handler,

            )
        )
        time.sleep(0.2)

        _simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t1",
            "agentName": "acme_echo", "ownerId": "alice",
            "taskKind": "request", "hasStream": False,
        })
        time.sleep(0.5)

        failed = [r for r in publish_records
                  if isinstance(r.get("message"), dict) and
                  r["message"].get("state") == "failed"]
        assert len(failed) >= 1
        assert "test error" in failed[0]["message"].get("error", "")

        result["stop"]()


class TestOnErrorCallback:
    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_on_error_invoked(self, mock_create) -> None:
        pn = _make_mock_pubnub()
        mock_create.return_value = _make_mock_pubnub()

        errors = []

        def on_error(task_id, exc):
            errors.append((task_id, str(exc)))

        def handler(task, ctx):
            raise ValueError("boom")

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                handler=handler,
                on_error=on_error,

            )
        )
        time.sleep(0.2)

        _simulate_start_task(pn, {
            "type": "StartTask", "taskId": "t1",
            "agentName": "acme_echo", "ownerId": "alice",
            "taskKind": "request", "hasStream": False,
        })
        time.sleep(0.5)

        assert len(errors) >= 1
        assert errors[0][0] == "t1"
        assert "boom" in errors[0][1]

        result["stop"]()


class TestMultipleConcurrentTasks:
    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_three_tasks_run_concurrently(self, mock_create) -> None:
        pn = _make_mock_pubnub()
        mock_create.return_value = _make_mock_pubnub()

        running = threading.Event()
        barrier = threading.Barrier(3, timeout=5)
        task_count = [0]
        count_lock = threading.Lock()

        def handler(task, ctx):
            with count_lock:
                task_count[0] += 1
            try:
                barrier.wait()
            except threading.BrokenBarrierError:
                pass
            running.set()

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                handler=handler,

                concurrency=4,
            )
        )
        time.sleep(0.2)

        for i in range(3):
            _simulate_start_task(pn, {
                "type": "StartTask", "taskId": f"t{i}",
                "agentName": "acme_echo", "ownerId": "alice",
                "taskKind": "request", "hasStream": False,
            })

        assert running.wait(timeout=5)
        time.sleep(0.3)
        assert task_count[0] == 3

        result["stop"]()


class TestUnlimitedCapacity:
    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_concurrency_zero_accepts_tasks(self, mock_create) -> None:
        pn = _make_mock_pubnub()
        mock_create.return_value = _make_mock_pubnub()

        count = [0]
        count_lock = threading.Lock()
        all_done = threading.Event()

        def handler(task, ctx):
            with count_lock:
                count[0] += 1
                if count[0] >= 5:
                    all_done.set()

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=pn,
                agent_name="acme_echo",
                handler=handler,

                concurrency=0,
            )
        )
        time.sleep(0.2)

        for i in range(5):
            _simulate_start_task(pn, {
                "type": "StartTask", "taskId": f"t{i}",
                "agentName": "acme_echo", "ownerId": "alice",
                "taskKind": "request", "hasStream": False,
            })

        assert all_done.wait(timeout=5)
        result["stop"]()


class TestExtractOwnerId:
    def test_owner_id_takes_priority(self) -> None:
        assert _extract_owner_id("alice") == "alice"

    def test_caller_claims_sub(self) -> None:
        assert _extract_owner_id(None, {"sub": "bob"}) == "bob"

    def test_fallback_anonymous(self) -> None:
        assert _extract_owner_id() == "anonymous"
