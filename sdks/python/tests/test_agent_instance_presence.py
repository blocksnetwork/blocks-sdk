"""
Tests for agent instance presence state tracking.

Mirrors Node's agent_instance_presence.test.ts (unit tests only).
Tracks set_state calls to verify presence state updates during task lifecycle.
"""

from __future__ import annotations

import time
import threading
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.agent_instance import start_agent_instance
from blocks_network.agent_registry import ConnectAgentResult
from blocks_network.types import AgentInstanceOptions

from tests.conftest import minimal_card


def _make_mock_per_task():
    """Create a mock PubNub for per-task clients."""
    pn = MagicMock()
    pn.set_token = MagicMock()
    pn.stop = MagicMock()
    pn.publish.return_value = MagicMock()
    return pn


@pytest.fixture(autouse=True)
def _patch_create_pubnub(monkeypatch):
    """Ensure per-task PubNub creation works in presence tests."""
    import blocks_network.agent_instance as _ai_mod
    monkeypatch.setattr(
        _ai_mod,
        "create_pubnub_client",
        lambda **kw: _make_mock_per_task(),
    )


@pytest.fixture(autouse=True)
def _patch_connect_agent(monkeypatch):
    """Provide a mock connect_agent that returns a controlChannel."""
    import blocks_network.agent_registry as _reg_mod
    monkeypatch.setattr(
        _reg_mod,
        "connect_agent",
        lambda *a, **kw: ConnectAgentResult(control_channel="agent.test-presence-id.control"),
    )


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_tracking_set_state():
    """Create a set_state mock that records all state updates."""
    records = []

    def _tracking():
        chain = MagicMock()
        record = {}

        def _channels(chs):
            record["channels"] = chs
            return chain

        def _state(s):
            record["state"] = s
            return chain

        def _sync():
            records.append(dict(record))
            return MagicMock()

        chain.channels = _channels
        chain.state = _state
        chain.sync = _sync
        return chain

    return _tracking, records


def _simulate_start_task(mock_pn, msg):
    """Simulate receiving a control message via the listener."""
    assert len(mock_pn._listeners) > 0
    listener = mock_pn._listeners[0]
    event = MagicMock()
    event.message = msg
    if isinstance(listener, dict):
        listener["message"](event)
    elif hasattr(listener, "message") and callable(listener.message):
        listener.message(mock_pn, event)


def _wait_for(predicate, timeout_sec=2.0, poll_sec=0.05):
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(poll_sec)
    assert predicate(), "Timed out waiting for predicate"


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestInitialPresenceState:
    def test_initial_state(self, mock_pubnub) -> None:
        set_state_fn, records = _make_tracking_set_state()
        mock_pubnub.set_state = set_state_fn

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                concurrency=4,

            )
        )

        # set_state now happens asynchronously in a daemon thread
        _wait_for(lambda: len(records) >= 1)
        assert len(records) >= 1
        state = records[0]["state"]
        assert state["activeTasks"] == 0
        assert state["concurrency"] == 4
        assert isinstance(state["startedAt"], int)
        assert state["startedAt"] > 0
        assert state["instanceId"] == result["instance_id"]

        result["stop"]()


class TestPresenceIncrementOnTaskStart:
    def test_increments_active_tasks(self, mock_pubnub) -> None:
        set_state_fn, records = _make_tracking_set_state()
        mock_pubnub.set_state = set_state_fn

        # Use a handler that blocks until we release it
        barrier = threading.Event()

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                concurrency=2,

                on_start_task=lambda task, pn: barrier.wait(timeout=5),
            )
        )

        _simulate_start_task(mock_pubnub, {
            "type": "StartTask",
            "taskId": "task-1",
            "callerClaims": {"sub": "user1"},
        })

        # Wait for the set_state call with activeTasks=1
        _wait_for(
            lambda: any(r["state"]["activeTasks"] == 1 for r in records),
            timeout_sec=2.0,
        )

        barrier.set()
        result["stop"]()


class TestPresenceDecrementOnTaskCompletion:
    def test_decrements_active_tasks(self, mock_pubnub) -> None:
        set_state_fn, records = _make_tracking_set_state()
        mock_pubnub.set_state = set_state_fn

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                concurrency=2,

                # Handler completes immediately
                on_start_task=lambda task, pn: None,
            )
        )

        _simulate_start_task(mock_pubnub, {
            "type": "StartTask",
            "taskId": "task-1",
            "callerClaims": {"sub": "user1"},
        })

        # After task completes, activeTasks should go back to 0
        _wait_for(
            lambda: any(
                r["state"]["activeTasks"] == 0
                for r in records[1:]  # Skip initial state
            ),
            timeout_sec=2.0,
        )

        result["stop"]()


class TestConcurrentTaskTracking:
    def test_multiple_concurrent_tasks(self, mock_pubnub) -> None:
        set_state_fn, records = _make_tracking_set_state()
        mock_pubnub.set_state = set_state_fn

        barrier = threading.Event()

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                concurrency=3,

                on_start_task=lambda task, pn: barrier.wait(timeout=5),
            )
        )

        # Start two tasks
        _simulate_start_task(mock_pubnub, {
            "type": "StartTask",
            "taskId": "task-1",
            "callerClaims": {"sub": "user1"},
        })
        time.sleep(0.1)

        _simulate_start_task(mock_pubnub, {
            "type": "StartTask",
            "taskId": "task-2",
            "callerClaims": {"sub": "user2"},
        })

        # Wait for activeTasks >= 2
        _wait_for(
            lambda: any(r["state"]["activeTasks"] >= 2 for r in records),
            timeout_sec=2.0,
        )

        barrier.set()
        result["stop"]()


class TestUnlimitedCapacityPresence:
    def test_concurrency_zero_reported_in_presence(self, mock_pubnub) -> None:
        """When concurrency=0 (unlimited), concurrency=0 in presence."""
        set_state_fn, records = _make_tracking_set_state()
        mock_pubnub.set_state = set_state_fn

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="acme_echo",
                concurrency=0,

            )
        )

        # set_state now happens asynchronously in a daemon thread
        _wait_for(lambda: len(records) >= 1)
        assert len(records) >= 1
        state = records[0]["state"]
        assert state["concurrency"] == 0

        result["stop"]()
