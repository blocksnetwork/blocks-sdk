"""
Tests for StreamClient reorder buffer.

Covers the 11 IMPL-doc test cases plus post-review additions:
  1. ordered passthrough
  2. out-of-order reorder
  3. adjacent swap
  4. duplicate drop
  5. gap with timeout
  6. stream_end before late data
  7. stream_end with gap + timeout
  8. events format (seq starts at 1)
  9. bytes first arrival out of order
 10. tail-gap: stream_end with lost final msgs
 11. on_inbound_done callback can call end() without deadlock
 12. malformed stream_end (no integer seq) warned and ignored
 13. reorder_timeout_ms=0 disables
"""

from __future__ import annotations

import threading
from unittest.mock import patch, MagicMock

import pytest
from blocks_network.stream.stream_client import StreamClient, _reset_uuid_counter
from blocks_network.stream.types import InboundMessage


@pytest.fixture(autouse=True)
def reset_counter():
    _reset_uuid_counter()
    yield
    _reset_uuid_counter()


class _FakeConfig:
    """Minimal PNConfiguration stand-in."""

    def __init__(self):
        self.subscribe_key = None
        self.publish_key = None
        self.user_id = None


@pytest.fixture(autouse=True)
def mock_pubnub():
    """Mock PubNub and PNConfiguration for all tests."""
    def _pubnub_factory(config):
        instance = MagicMock()
        subscribe_execute = MagicMock()
        subscribe_channels = MagicMock(return_value=MagicMock(execute=subscribe_execute))
        instance.subscribe.return_value = MagicMock(channels=subscribe_channels)

        publish_sync = MagicMock()
        publish_should_store = MagicMock(return_value=MagicMock(sync=publish_sync))
        publish_meta = MagicMock(return_value=MagicMock(should_store=publish_should_store))
        publish_message = MagicMock(return_value=MagicMock(meta=publish_meta))
        publish_channel = MagicMock(return_value=MagicMock(message=publish_message))
        instance.publish.return_value = MagicMock(channel=publish_channel)

        here_now_result = MagicMock()
        here_now_result.result.channels = []
        here_now_sync = MagicMock(return_value=here_now_result)
        here_now_channels = MagicMock(return_value=MagicMock(sync=here_now_sync))
        instance.here_now.return_value = MagicMock(channels=here_now_channels)

        instance.add_listener = MagicMock()
        instance.remove_listener = MagicMock()
        instance.unsubscribe_all = MagicMock()
        instance.stop = MagicMock()
        instance.set_token = MagicMock()
        return instance

    fake_config = _FakeConfig()
    with patch("blocks_network.stream.stream_client.PubNub") as mock_cls, \
         patch("blocks_network.stream.stream_client.PNConfiguration", return_value=fake_config):
        mock_cls.side_effect = _pubnub_factory
        yield mock_cls


def _make_client(**overrides) -> StreamClient:
    defaults = dict(
        subscribe_key="sub-key",
        publish_key="pub-key",
        token="test-token",
        agent_name="test_agent",
        stream_id="my-stream",
        direction="inbound",
    )
    defaults.update(overrides)
    return StreamClient(**defaults)


def _data_msg(seq: int, chunks=None, encoding="utf8") -> dict:
    """Build a stream_data wire message."""
    return {
        "type": "stream_data",
        "streamId": "my-stream",
        "seq": seq,
        "ts": 1700000000000 + seq,
        "encoding": encoding,
        "chunks": chunks if chunks is not None else [f"chunk-{seq}"],
    }


def _event_msg(seq: int, events=None) -> dict:
    """Build a stream_events wire message."""
    return {
        "type": "stream_events",
        "streamId": "my-stream",
        "seq": seq,
        "ts": 1700000000000 + seq,
        "events": events if events is not None else [{"seq": seq}],
    }


def _end_msg(seq: int) -> dict:
    """Build a stream_end wire message."""
    return {
        "type": "stream_end",
        "streamId": "my-stream",
        "seq": seq,
        "ts": 1700000000000 + seq,
    }


def _drain(client: StreamClient, count: int, timeout: float = 2.0) -> list:
    """Drain `count` InboundMessages from the client queue."""
    results = []
    for _ in range(count):
        msg = client._inbound_queue.get(timeout=timeout)
        if msg is None:
            break
        results.append(msg)
    return results


def _drain_until_done(client: StreamClient, timeout: float = 2.0) -> list:
    """Drain all InboundMessages until the sentinel (None) is received."""
    results = []
    while True:
        msg = client._inbound_queue.get(timeout=timeout)
        if msg is None:
            break
        results.append(msg)
    return results


class TestReorderBuffer:

    # 1. ordered passthrough: seq 0,1,2 -> yielded in order
    def test_ordered_passthrough(self):
        client = _make_client()
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_data_msg(1))
        client._handle_inbound_message(_data_msg(2))
        client._handle_inbound_message(_end_msg(3))

        msgs = _drain_until_done(client)
        assert len(msgs) == 3
        assert [m.seq for m in msgs] == [0, 1, 2]

    # 2. out-of-order reorder: seq 1,0,2 -> yielded 0,1,2
    def test_out_of_order_reorder(self):
        client = _make_client()
        client._handle_inbound_message(_data_msg(1))
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_data_msg(2))
        client._handle_inbound_message(_end_msg(3))

        msgs = _drain_until_done(client)
        assert len(msgs) == 3
        assert [m.seq for m in msgs] == [0, 1, 2]

    # 3. adjacent swap: seq 0,2,1,3 -> yielded 0,1,2,3
    def test_adjacent_swap(self):
        client = _make_client()
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_data_msg(2))
        client._handle_inbound_message(_data_msg(1))
        client._handle_inbound_message(_data_msg(3))
        client._handle_inbound_message(_end_msg(4))

        msgs = _drain_until_done(client)
        assert len(msgs) == 4
        assert [m.seq for m in msgs] == [0, 1, 2, 3]

    # 4. duplicate drop: same seq arrives twice -> yielded once
    def test_duplicate_drop(self):
        client = _make_client()
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_data_msg(0))  # duplicate
        client._handle_inbound_message(_data_msg(1))
        client._handle_inbound_message(_end_msg(2))

        msgs = _drain_until_done(client)
        assert len(msgs) == 2
        assert [m.seq for m in msgs] == [0, 1]

    # 5. gap with timeout: seq 0,2 arrive, 1 never arrives -> after timeout, yields 2
    def test_gap_with_timeout(self):
        # Use a short timeout to make the test fast
        client = _make_client(reorder_timeout_ms=50)
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_data_msg(2))
        # seq 1 never arrives

        # seq 0 should be yielded immediately
        msg0 = client._inbound_queue.get(timeout=1)
        assert msg0 is not None
        assert msg0.seq == 0

        # seq 2 should be yielded after timeout fires
        msg2 = client._inbound_queue.get(timeout=2)
        assert msg2 is not None
        assert msg2.seq == 2

        # Send stream_end to complete
        client._handle_inbound_message(_end_msg(3))
        sentinel = client._inbound_queue.get(timeout=1)
        assert sentinel is None

    # 6. stream_end before late data: seq 0, stream_end(seq=3), seq 1, seq 2
    #    -> yields 0,1,2 then completes
    def test_stream_end_before_late_data(self):
        client = _make_client()
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_end_msg(3))
        client._handle_inbound_message(_data_msg(1))
        client._handle_inbound_message(_data_msg(2))

        msgs = _drain_until_done(client)
        assert len(msgs) == 3
        assert [m.seq for m in msgs] == [0, 1, 2]

    # 7. stream_end with gap + timeout: seq 0, stream_end(seq=3), seq 2 arrive,
    #    1 never -> after timeout yields 2, completes
    def test_stream_end_with_gap_and_timeout(self):
        client = _make_client(reorder_timeout_ms=50)
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_end_msg(3))
        client._handle_inbound_message(_data_msg(2))
        # seq 1 never arrives

        msgs = _drain_until_done(client, timeout=2)
        assert len(msgs) == 2
        seqs = [m.seq for m in msgs]
        assert seqs == [0, 2]

    # 8. events format (seq starts at 1): seq 2,1,3 -> yielded 1,2,3
    def test_events_format_seq_starts_at_1(self):
        client = _make_client(format="events")
        client._handle_inbound_message(_event_msg(2))
        client._handle_inbound_message(_event_msg(1))
        client._handle_inbound_message(_event_msg(3))
        client._handle_inbound_message(_end_msg(4))

        msgs = _drain_until_done(client)
        assert len(msgs) == 3
        assert [m.seq for m in msgs] == [1, 2, 3]

    # 9. bytes first arrival out of order: seq 1,0,2 -> yielded 0,1,2
    #    (nextExpectedSeq=0, seq 1 buffered, seq 0 yields + flushes 1)
    def test_bytes_first_arrival_out_of_order(self):
        client = _make_client(format="bytes")
        client._handle_inbound_message(_data_msg(1))
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_data_msg(2))
        client._handle_inbound_message(_end_msg(3))

        msgs = _drain_until_done(client)
        assert len(msgs) == 3
        assert [m.seq for m in msgs] == [0, 1, 2]

    # 10. tail-gap: stream_end with lost final msgs
    #     seq 0, stream_end(seq=3), seq 1 and 2 never arrive
    #     -> after timeout, yields 0, completes (no deadlock)
    def test_tail_gap_stream_end_with_lost_final_msgs(self):
        client = _make_client(reorder_timeout_ms=50)
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_end_msg(3))
        # seq 1 and 2 never arrive

        msgs = _drain_until_done(client, timeout=2)
        assert len(msgs) == 1
        assert msgs[0].seq == 0

    # 11. on_inbound_done callback can call end() without deadlock
    def test_on_inbound_done_can_call_end_without_deadlock(self):
        """Regression: _fire_inbound_done must run outside _reorder_lock."""
        client = _make_client()

        done = threading.Event()

        def _callback():
            client.end()  # would deadlock if _reorder_lock is held
            done.set()

        client.on_inbound_done(_callback)

        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_end_msg(1))

        assert done.wait(timeout=2), "Deadlock: on_inbound_done callback could not call end()"

    # 12. malformed stream_end (no integer seq) warned and ignored
    def test_malformed_stream_end_without_seq_ignored(self):
        client = _make_client()
        client._handle_inbound_message(_data_msg(0))

        # Send malformed stream_end (no seq) — should be warned and ignored
        client._handle_inbound_message({
            "type": "stream_end",
            "streamId": "my-stream",
            "ts": 1700000000000,
        })

        # Stream should still be open — send more data and a valid stream_end
        client._handle_inbound_message(_data_msg(1))
        client._handle_inbound_message(_end_msg(2))

        msgs = _drain_until_done(client)
        assert len(msgs) == 2
        assert [m.seq for m in msgs] == [0, 1]

    # 13. reorder_timeout_ms=0 disables: messages arrive out of order
    #     -> yielded in arrival order, stream_end completes immediately
    def test_reorder_timeout_zero_disables(self):
        client = _make_client(reorder_timeout_ms=0)
        client._handle_inbound_message(_data_msg(1))
        client._handle_inbound_message(_data_msg(0))
        client._handle_inbound_message(_data_msg(2))
        client._handle_inbound_message(_end_msg(3))

        msgs = _drain_until_done(client)
        assert len(msgs) == 3
        # In arrival order, NOT sorted
        assert [m.seq for m in msgs] == [1, 0, 2]
