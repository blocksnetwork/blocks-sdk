"""Dedup + on_cancel_requested for Python subscribe_to_task.

Mirror of blocks-sdk/sdks/node/tests/subscribe-to-task.dedup.test.ts.
"""
from __future__ import annotations

from unittest.mock import MagicMock

from blocks_network.task_client import (
    TaskEventCallbacks,
    subscribe_to_task,
)


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


def _emit(pn: MagicMock, channel: str, message: dict) -> None:
    evt = MagicMock()
    evt.channel = channel
    evt.message = message
    for l in list(pn._listeners):
        if hasattr(l, "message"):
            l.message(pn, evt)


def test_on_terminal_fires_exactly_once_when_two_wire_terminals_arrive() -> None:
    pn = _make_mock_pubnub()
    seen: list = []
    sub = subscribe_to_task(
        pn,
        "t1",
        "alice",
        TaskEventCallbacks(on_terminal=lambda m: seen.append(m)),
    )

    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "canceled"})
    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "canceled"})

    assert len(seen) == 1
    sub.unsubscribe()


def test_first_terminal_wins_across_different_states() -> None:
    pn = _make_mock_pubnub()
    seen: list = []
    sub = subscribe_to_task(
        pn,
        "t2",
        "alice",
        TaskEventCallbacks(on_terminal=lambda m: seen.append(m)),
    )

    _emit(
        pn,
        "u.alice.t2",
        {"type": "terminal", "taskId": "t2", "state": "canceled", "reason": "force_canceled"},
    )
    _emit(
        pn,
        "u.alice.t2",
        {"type": "terminal", "taskId": "t2", "state": "completed"},
    )

    assert len(seen) == 1
    assert seen[0]["state"] == "canceled"
    assert seen[0]["reason"] == "force_canceled"
    sub.unsubscribe()


def test_subscriptions_are_isolated() -> None:
    pn = _make_mock_pubnub()
    seen_a: list = []
    seen_b: list = []
    sub_a = subscribe_to_task(
        pn,
        "a",
        "alice",
        TaskEventCallbacks(on_terminal=lambda m: seen_a.append(m)),
    )
    sub_b = subscribe_to_task(
        pn,
        "b",
        "alice",
        TaskEventCallbacks(on_terminal=lambda m: seen_b.append(m)),
    )

    _emit(pn, "u.alice.a", {"type": "terminal", "taskId": "a", "state": "canceled"})
    _emit(pn, "u.alice.a", {"type": "terminal", "taskId": "a", "state": "canceled"})
    _emit(pn, "u.alice.b", {"type": "terminal", "taskId": "b", "state": "completed"})

    assert len(seen_a) == 1
    assert len(seen_b) == 1
    sub_a.unsubscribe()
    sub_b.unsubscribe()


def test_on_cancel_requested_dispatched() -> None:
    pn = _make_mock_pubnub()
    seen: list = []
    sub = subscribe_to_task(
        pn,
        "t3",
        "alice",
        TaskEventCallbacks(on_cancel_requested=lambda m: seen.append(m)),
    )

    _emit(
        pn,
        "u.alice.t3",
        {"type": "cancel_requested", "taskId": "t3", "ts": 1716800000000},
    )

    assert len(seen) == 1
    assert seen[0]["ts"] == 1716800000000
    sub.unsubscribe()


def test_on_cancel_requested_suppressed_after_terminal() -> None:
    pn = _make_mock_pubnub()
    seen: list = []
    sub = subscribe_to_task(
        pn,
        "t4",
        "alice",
        TaskEventCallbacks(
            on_cancel_requested=lambda m: seen.append(m),
            on_terminal=lambda _m: None,
        ),
    )

    _emit(pn, "u.alice.t4", {"type": "terminal", "taskId": "t4", "state": "canceled"})
    _emit(pn, "u.alice.t4", {"type": "cancel_requested", "taskId": "t4", "ts": 1})

    assert len(seen) == 0
    sub.unsubscribe()


def test_on_cancel_requested_fires_zero_or_once_on_duplicate_wire_emissions() -> None:
    """Docs claim cancel_requested fires zero-or-once.
    Duplicate emissions (e.g. PubNub cache replay) must not double-fire.
    """
    pn = _make_mock_pubnub()
    seen: list = []
    sub = subscribe_to_task(
        pn,
        "t5",
        "alice",
        TaskEventCallbacks(on_cancel_requested=lambda m: seen.append(m)),
    )

    _emit(
        pn,
        "u.alice.t5",
        {"type": "cancel_requested", "taskId": "t5", "ts": 1716800000000},
    )
    _emit(
        pn,
        "u.alice.t5",
        {"type": "cancel_requested", "taskId": "t5", "ts": 1716800001000},
    )

    assert len(seen) == 1
    assert seen[0]["ts"] == 1716800000000
    sub.unsubscribe()
