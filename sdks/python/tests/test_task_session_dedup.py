"""Terminal-event dedup tests for Python TaskSession.

Mirror of blocks-sdk/sdks/node/tests/task-session.dedup.test.ts.
"""
from __future__ import annotations

from unittest.mock import MagicMock

from blocks_network.task_session import TaskSession


def _make_mock_pubnub() -> MagicMock:
    pn = MagicMock()
    pn._listeners = []
    pn.add_listener.side_effect = lambda l: pn._listeners.append(l)
    pn.remove_listener.side_effect = lambda l: (
        pn._listeners.remove(l) if l in pn._listeners else None
    )
    sub_chain = MagicMock()
    sub_chain.channels.return_value = sub_chain
    sub_chain.with_timetoken.return_value = sub_chain
    sub_chain.execute.return_value = None
    pn.subscribe.return_value = sub_chain
    unsub_chain = MagicMock()
    unsub_chain.channels.return_value = unsub_chain
    unsub_chain.execute.return_value = None
    pn.unsubscribe.return_value = unsub_chain
    return pn


_counter = [0]


def _emit(pn: MagicMock, channel: str, message: dict) -> None:
    _counter[0] += 1
    evt = MagicMock()
    evt.channel = channel
    evt.message = message
    evt.timetoken = f"tt-{_counter[0]}"
    for l in list(pn._listeners):
        if hasattr(l, "message"):
            l.message(pn, evt)


def _make_session(pn: MagicMock) -> TaskSession:
    return TaskSession(
        task_id="t1",
        owner_id="alice",
        read_token="t4",
        agent_name="echo",
        pubnub=pn,
        sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
    )


def test_on_terminal_fires_exactly_once_when_two_wire_terminals_arrive() -> None:
    pn = _make_mock_pubnub()
    session = _make_session(pn)
    seen: list = []
    session.on_terminal(lambda e: seen.append(e.raw))

    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "canceled"})
    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "canceled"})

    assert len(seen) == 1


def test_first_terminal_wins_across_different_states() -> None:
    pn = _make_mock_pubnub()
    session = _make_session(pn)
    seen: list = []
    session.on_terminal(lambda e: seen.append(e.raw))

    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "canceled", "reason": "force_canceled"})
    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "completed"})

    assert len(seen) == 1
    assert seen[0]["state"] == "canceled"
    assert seen[0]["reason"] == "force_canceled"


def test_on_terminal_registered_after_wire_arrival_fires_synchronously_once() -> None:
    pn = _make_mock_pubnub()
    session = _make_session(pn)

    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "canceled"})

    seen: list = []
    session.on_terminal(lambda e: seen.append(e.raw))

    assert len(seen) == 1
    assert seen[0]["state"] == "canceled"

    # A second wire terminal must not re-deliver (already-delivered guard).
    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "completed"})
    assert len(seen) == 1


def test_wait_for_terminal_returns_first_event_when_two_arrive() -> None:
    pn = _make_mock_pubnub()
    session = _make_session(pn)

    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "canceled"})
    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "completed"})

    evt = session.wait_for_terminal(timeout=1.0)
    assert evt.raw["state"] == "canceled"
