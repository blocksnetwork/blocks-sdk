"""BLOCKS-370: on_cancel_requested tests for Python TaskSession.

Mirror of blocks-sdk/sdks/node/tests/task-session.cancel-requested.test.ts.
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


def test_on_cancel_requested_fires_when_event_arrives() -> None:
    pn = _make_mock_pubnub()
    session = _make_session(pn)
    seen: list = []
    session.on_cancel_requested(lambda e: seen.append(e.raw))

    _emit(
        pn,
        "u.alice.t1",
        {"type": "cancel_requested", "protocolVersion": "2026-05-27", "taskId": "t1", "ts": 1716800000000},
    )

    assert len(seen) == 1
    assert seen[0]["type"] == "cancel_requested"
    assert seen[0]["ts"] == 1716800000000


def test_multiple_subscribers_all_receive_event() -> None:
    pn = _make_mock_pubnub()
    session = _make_session(pn)
    seen1: list = []
    seen2: list = []
    session.on_cancel_requested(lambda e: seen1.append(e.raw))
    session.on_cancel_requested(lambda e: seen2.append(e.raw))

    _emit(
        pn,
        "u.alice.t1",
        {"type": "cancel_requested", "protocolVersion": "2026-05-27", "taskId": "t1", "ts": 1},
    )

    assert len(seen1) == 1
    assert len(seen2) == 1


def test_unsubscribe_stops_callback() -> None:
    pn = _make_mock_pubnub()
    session = _make_session(pn)
    seen: list = []
    unsub = session.on_cancel_requested(lambda e: seen.append(e.raw))
    unsub()

    _emit(
        pn,
        "u.alice.t1",
        {"type": "cancel_requested", "protocolVersion": "2026-05-27", "taskId": "t1", "ts": 1},
    )

    assert len(seen) == 0


def test_does_not_fire_after_terminal_delivered() -> None:
    pn = _make_mock_pubnub()
    session = _make_session(pn)
    seen: list = []
    session.on_cancel_requested(lambda e: seen.append(e.raw))

    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "canceled"})
    _emit(pn, "u.alice.t1", {"type": "cancel_requested", "protocolVersion": "2026-05-27", "taskId": "t1", "ts": 1})

    assert len(seen) == 0


def test_fires_zero_or_once_on_duplicate_wire_emissions() -> None:
    """BLOCKS-370: docs claim cancel_requested fires zero-or-once.
    Two back-to-back wire events (e.g. PubNub cache replay) must not
    double-fire.
    """
    pn = _make_mock_pubnub()
    session = _make_session(pn)
    seen: list = []
    session.on_cancel_requested(lambda e: seen.append(e.raw))

    _emit(
        pn,
        "u.alice.t1",
        {"type": "cancel_requested", "protocolVersion": "2026-05-27", "taskId": "t1", "ts": 1716800000000},
    )
    _emit(
        pn,
        "u.alice.t1",
        {"type": "cancel_requested", "protocolVersion": "2026-05-27", "taskId": "t1", "ts": 1716800001000},
    )

    assert len(seen) == 1
    assert seen[0]["ts"] == 1716800000000


def test_replays_first_cancel_requested_to_late_callback() -> None:
    """BLOCKS-370: a callback registered AFTER the wire event arrived
    still receives a synthetic replay of the first event. Mirrors
    on_terminal's sticky-replay behavior.
    """
    pn = _make_mock_pubnub()
    session = _make_session(pn)

    _emit(
        pn,
        "u.alice.t1",
        {"type": "cancel_requested", "protocolVersion": "2026-05-27", "taskId": "t1", "ts": 1700000000000},
    )

    received: list = []
    session.on_cancel_requested(lambda e: received.append(e))

    assert len(received) == 1
    assert received[0].raw["type"] == "cancel_requested"
    assert received[0].raw["taskId"] == "t1"
    assert received[0].raw["ts"] == 1700000000000


def test_does_not_double_fire_cancel_requested_across_registration_order() -> None:
    """BLOCKS-370: an early callback fires once when the wire event
    arrives; a late callback registered afterward gets a synthetic
    replay of the same event. Neither callback fires twice.
    """
    pn = _make_mock_pubnub()
    session = _make_session(pn)

    early_received: list = []
    session.on_cancel_requested(lambda e: early_received.append(e))

    _emit(
        pn,
        "u.alice.t1",
        {"type": "cancel_requested", "protocolVersion": "2026-05-27", "taskId": "t1", "ts": 1700000000000},
    )

    late_received: list = []
    session.on_cancel_requested(lambda e: late_received.append(e))

    assert len(early_received) == 1
    assert len(late_received) == 1


def test_does_not_replay_after_terminal_delivered() -> None:
    """BLOCKS-370: a callback registered AFTER a terminal was delivered must
    NOT receive a replayed cancel_requested. Mirrors the Node parity test.
    """
    pn = _make_mock_pubnub()
    session = _make_session(pn)

    _emit(
        pn,
        "u.alice.t1",
        {
            "type": "cancel_requested",
            "protocolVersion": "2026-05-27",
            "taskId": "t1",
            "ts": 1700000000000,
        },
    )
    _emit(pn, "u.alice.t1", {"type": "terminal", "taskId": "t1", "state": "canceled"})

    received: list = []
    session.on_cancel_requested(lambda e: received.append(e))

    assert received == []
