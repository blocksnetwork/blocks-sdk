"""Late-reader broadcast resilience test for shared streams
(SHARED_STREAM_LIFECYCLE_IMPL Code Changes §12b).

Scenario:

1. A writer-side StreamClient on a shared-affinity channel writes
   some data then calls ``end()``.
2. A later consumer-side StreamClient subscribes (built via
   ``from_descriptor``) — simulating a late reader joining the
   broadcast within PubNub's cache window.
3. Cached ``stream_data`` replays hit the late reader's inbound
   iterator; a cached ``stream_end`` would terminate the iterator
   prematurely.

Under the fix, the writer's ``end()`` suppresses the ``stream_end``
marker because affinity is ``shared``. The cache therefore contains
only data messages. The late reader's iterator stays alive: it yields
cached data then waits for more (never terminated by a stale marker).

Mirrors ``blocks-sdk/sdks/node/tests/shared-stream-late-reader.test.ts``.
"""

from __future__ import annotations

import queue
from typing import Any, Dict, List
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.stream.descriptor import StreamDescriptor
from blocks_network.stream.stream_client import StreamClient, _reset_uuid_counter


# ---------------------------------------------------------------------------
# PubNub mock — captures publishes AND registered listeners so tests
# can simulate cached replay on subscribe.
# ---------------------------------------------------------------------------


@pytest.fixture(autouse=True)
def _reset_counter() -> None:
    _reset_uuid_counter()
    yield
    _reset_uuid_counter()


def _make_mock_pubnub_and_state() -> tuple[MagicMock, Dict[str, Any]]:
    """Mock PubNub. Returns (instance, state-dict).

    state-dict keys:
      - ``published``: list[dict] — every publish message.
      - ``listeners``: list — registered listeners.
    """
    state: Dict[str, Any] = {"published": [], "listeners": []}
    instance = MagicMock()
    instance.set_token = MagicMock()

    def _make_chain() -> MagicMock:
        chain = MagicMock()
        record: Dict[str, Any] = {}

        def _channel(ch: str) -> MagicMock:
            record["channel"] = ch
            return chain

        def _message(msg: Any) -> MagicMock:
            record["message"] = msg
            return chain

        def _meta(m: Any) -> MagicMock:
            return chain

        def _should_store(v: Any) -> MagicMock:
            return chain

        def _use_post(v: Any) -> MagicMock:
            return chain

        def _sync() -> MagicMock:
            if isinstance(record.get("message"), dict):
                state["published"].append({
                    "channel": record.get("channel", ""),
                    "message": dict(record["message"]),
                })
            return MagicMock()

        chain.channel = _channel
        chain.message = _message
        chain.meta = _meta
        chain.should_store = _should_store
        chain.use_post = _use_post
        chain.sync = _sync
        return chain

    instance.publish.side_effect = lambda: _make_chain()
    instance.subscribe.return_value = _make_chain()
    instance.unsubscribe.return_value = _make_chain()

    here_now_result = MagicMock()
    here_now_result.result.channels = []
    here_now_sync = MagicMock(return_value=here_now_result)
    here_now_channels = MagicMock(return_value=MagicMock(sync=here_now_sync))
    instance.here_now.return_value = MagicMock(channels=here_now_channels)

    def _add_listener(listener: Any) -> None:
        state["listeners"].append(listener)

    instance.add_listener = MagicMock(side_effect=_add_listener)
    instance.remove_listener = MagicMock()
    instance.unsubscribe_all = MagicMock()
    instance.stop = MagicMock()
    return instance, state


class _FakeConfig:
    def __init__(self) -> None:
        self.subscribe_key = None
        self.publish_key = None
        self.user_id = None


@pytest.fixture
def pubnub_mock():
    """Yields list[(instance, state)] — one entry per constructed
    PubNub instance.
    """
    instances: List[tuple[MagicMock, Dict[str, Any]]] = []

    def _pn_factory(_config: Any) -> MagicMock:
        inst, state = _make_mock_pubnub_and_state()
        instances.append((inst, state))
        return inst

    fake_config = _FakeConfig()
    with patch("blocks_network.stream.stream_client.PubNub") as mock_cls, \
         patch(
             "blocks_network.stream.stream_client.PNConfiguration",
             return_value=fake_config,
         ):
        mock_cls.side_effect = _pn_factory
        yield instances


def _all_end_markers(instances: List[tuple[MagicMock, Dict[str, Any]]]) -> List[Dict[str, Any]]:
    """Return all ``stream_end`` publishes across every PubNub instance."""
    out: List[Dict[str, Any]] = []
    for _inst, state in instances:
        for p in state["published"]:
            if p["message"].get("type") == "stream_end":
                out.append(p)
    return out


def _simulate_cached_message(
    instances: List[tuple[MagicMock, Dict[str, Any]]],
    instance_index: int,
    channel: str,
    msg: Dict[str, Any],
) -> None:
    """Invoke the registered listener(s) on a specific PubNub instance
    with a cached message, simulating PubNub's replay delivery."""
    inst, state = instances[instance_index]
    for listener in state["listeners"]:
        if hasattr(listener, "message"):
            event = MagicMock()
            event.channel = channel
            event.message = msg
            listener.message(inst, event)


class TestSharedStreamLateReader:
    """§12b: writer-side end() on shared stream does not publish
    stream_end, and a late-subscribing reader does not exit on a cached
    marker."""

    def test_shared_writer_end_no_marker_and_late_reader_iterator_stays_live(
        self, pubnub_mock,
    ) -> None:
        channel = "stream.late_reader_test.shared_down"

        # --- Phase 1: shared-affinity writer does its thing and ends. ---
        writer = StreamClient(
            subscribe_key="sub-key",
            publish_key="pub-key",
            token="T7a-writer",
            agent_name="late_reader_test",
            stream_id="shared_down",
            channel=channel,
            direction="outbound",
            format="bytes",
            affinity="shared",
        )
        writer.write("chunk-1")
        writer.write("chunk-2")
        writer.end()

        # Core invariant: no stream_end marker on the wire.
        assert _all_end_markers(pubnub_mock) == []

        # --- Phase 2: late reader subscribes within the cache window. ---
        desc = StreamDescriptor(
            task_id="task-late-reader",
            stream_id="shared_down",
            agent_name="late_reader_test",
            channel=channel,
            token="T7c-late-reader",
            agent_direction="outbound",
            local_direction="inbound",
            format="bytes",
            affinity="shared",
            declared_stream="shared_down",
        )
        late_reader = StreamClient.from_descriptor(
            desc, subscribe_key="sub-key", publish_key="pub-key",
        )
        assert late_reader.is_active is True

        # The last constructed PubNub instance is the late reader's.
        late_reader_pn_index = len(pubnub_mock) - 1

        # --- Phase 3: replay what PubNub's cache would have contained.
        # Under the fix the cache holds only data — no terminator.
        _simulate_cached_message(
            pubnub_mock, late_reader_pn_index, channel, {
                "type": "stream_data",
                "streamId": "shared_down",
                "seq": 0,
                "ts": 1700000000000,
                "encoding": "utf8",
                "chunks": ["chunk-1"],
            },
        )
        _simulate_cached_message(
            pubnub_mock, late_reader_pn_index, channel, {
                "type": "stream_data",
                "streamId": "shared_down",
                "seq": 1,
                "ts": 1700000000001,
                "encoding": "utf8",
                "chunks": ["chunk-2"],
            },
        )

        # --- Critical assertion: the inbound queue has NOT received a
        # sentinel (None) from a stale stream_end marker. The two
        # cached data messages are queued; there is no terminator. ---
        msg1 = late_reader._inbound_queue.get(timeout=1.0)
        assert msg1 is not None
        assert msg1.format == "bytes"
        assert msg1.data == ["chunk-1"]

        msg2 = late_reader._inbound_queue.get(timeout=1.0)
        assert msg2 is not None
        assert msg2.format == "bytes"
        assert msg2.data == ["chunk-2"]

        # No terminator queued. The next .get(timeout=..) should time
        # out rather than return None (which would indicate stream_end
        # was replayed from the cache).
        with pytest.raises(queue.Empty):
            late_reader._inbound_queue.get(timeout=0.05)

        # Clean up.
        late_reader.end()

    def test_dedicated_writer_end_publishes_marker_regression_gate(
        self, pubnub_mock,
    ) -> None:
        """Contrast: a dedicated-affinity writer DOES publish the
        marker; preserves the dedicated-stream contract."""
        writer = StreamClient(
            subscribe_key="sub-key",
            publish_key="pub-key",
            token="T7a-writer",
            agent_name="late_reader_test",
            stream_id="ded_down",
            channel="stream.late_reader_test.ded_down",
            direction="outbound",
            format="bytes",
            affinity="dedicated",
        )
        writer.write("chunk-1")
        writer.end()

        assert len(_all_end_markers(pubnub_mock)) == 1
