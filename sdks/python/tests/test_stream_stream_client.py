"""
Tests for StreamClient.

Covers:
- Constructor creates PubNub client with correct token and UUID
- Constructor validates stream ID
- write() delegates to StreamBundle
- write() throws on ended stream
- write() throws on inbound-only stream
- end() flushes and destroys
- inbound iterator yields normalized messages
- inbound handles multipart reassembly
- inbound throws on outbound-only stream
- Self-publish filter set for bidirectional only
- Channel computed as stream.{agentName}.{streamId}
- UUID follows {agentName}-stream-{NNNN} convention
- Configuration hierarchy (constructor overrides env vars)
"""

from __future__ import annotations

import base64
import json
import os
import time
from typing import Any
from unittest.mock import patch, MagicMock, call

import pytest
from blocks_network.stream.stream_client import StreamClient, _reset_uuid_counter


@pytest.fixture(autouse=True)
def reset_counter():
    _reset_uuid_counter()
    yield
    _reset_uuid_counter()


# Track PubNub construction and method calls
mock_set_token = MagicMock()
mock_add_listener = MagicMock()
mock_remove_listener = MagicMock()
mock_unsubscribe_all = MagicMock()
mock_stop = MagicMock()


def _make_mock_pubnub():
    """Create a mock PubNub instance with builder-chain methods."""
    instance = MagicMock()
    instance.set_token = mock_set_token

    # Publish builder chain
    publish_sync = MagicMock()
    publish_should_store = MagicMock(return_value=MagicMock(sync=publish_sync))
    publish_meta = MagicMock(return_value=MagicMock(should_store=publish_should_store))
    publish_message = MagicMock(return_value=MagicMock(meta=publish_meta))
    publish_channel = MagicMock(return_value=MagicMock(message=publish_message))
    instance.publish.return_value = MagicMock(channel=publish_channel)

    # Subscribe builder chain
    subscribe_execute = MagicMock()
    subscribe_channels = MagicMock(return_value=MagicMock(execute=subscribe_execute))
    instance.subscribe.return_value = MagicMock(channels=subscribe_channels)

    # Unsubscribe builder chain
    unsubscribe_execute = MagicMock()
    unsubscribe_channels = MagicMock(return_value=MagicMock(execute=unsubscribe_execute))
    instance.unsubscribe.return_value = MagicMock(channels=unsubscribe_channels)

    # hereNow builder chain
    here_now_result = MagicMock()
    here_now_result.result.channels = []
    here_now_sync = MagicMock(return_value=here_now_result)
    here_now_channels = MagicMock(return_value=MagicMock(sync=here_now_sync))
    instance.here_now.return_value = MagicMock(channels=here_now_channels)

    instance.add_listener = mock_add_listener
    instance.remove_listener = mock_remove_listener
    instance.unsubscribe_all = mock_unsubscribe_all
    instance.stop = mock_stop

    return instance


class _FakeConfig:
    """Minimal PNConfiguration stand-in that tracks explicit assignments."""

    def __init__(self):
        self.subscribe_key = None
        self.publish_key = None
        self.user_id = None
        # filter_expression is intentionally absent — tests assert it was
        # explicitly set before PubNub construction.


@pytest.fixture(autouse=True)
def mock_pubnub():
    """Mock PubNub and PNConfiguration for all tests.

    The PubNub mock snapshots the config at construction time so tests
    can verify that filter_expression was set *before* client init —
    not after, which would be a no-op on the real SDK.
    """
    mock_set_token.reset_mock()
    mock_add_listener.reset_mock()
    mock_remove_listener.reset_mock()
    mock_unsubscribe_all.reset_mock()
    mock_stop.reset_mock()

    # Stores the config snapshot taken at PubNub() construction time
    config_snapshots: list[dict] = []

    def _pubnub_factory(config):
        # Snapshot filter_expression at construction time, mirroring the
        # real SDK's deep-copy behavior. If it was never set on the
        # _FakeConfig, getattr returns the sentinel None.
        config_snapshots.append({
            "filter_expression": getattr(config, "filter_expression", None),
        })
        return _make_mock_pubnub()

    fake_config = _FakeConfig()
    with patch("blocks_network.stream.stream_client.PubNub") as mock_cls, \
         patch("blocks_network.stream.stream_client.PNConfiguration", return_value=fake_config):
        mock_cls.side_effect = _pubnub_factory
        yield mock_cls, fake_config, config_snapshots


def _make_client(**overrides):
    defaults = dict(
        subscribe_key="sub-key",
        publish_key="pub-key",
        token="test-token",
        agent_name="test_agent",
        stream_id="my-stream",
    )
    defaults.update(overrides)
    return StreamClient(**defaults)


class TestConstructor:

    def test_creates_pubnub_and_sets_token(self, mock_pubnub):
        mock_cls, _, _ = mock_pubnub
        client = _make_client()
        assert mock_cls.called
        mock_set_token.assert_called_with("test-token")

    def test_validates_stream_id(self):
        with pytest.raises(ValueError, match="Stream ID cannot be empty"):
            _make_client(stream_id="")
        with pytest.raises(ValueError, match="Stream ID contains invalid characters"):
            _make_client(stream_id="bad.id")

    def test_computes_channel(self):
        client = _make_client(agent_name="weather", stream_id="temp-out")
        assert client.channel == "stream.weather.temp-out"

    def test_uuid_convention(self):
        c1 = _make_client(agent_name="myAgent")
        assert c1.uuid == "myAgent-stream-0001"
        c2 = _make_client(agent_name="myAgent")
        assert c2.uuid == "myAgent-stream-0002"

    def test_uuid_increments(self):
        c1 = _make_client()
        c2 = _make_client()
        c3 = _make_client()
        assert c1.uuid.endswith("-0001")
        assert c2.uuid.endswith("-0002")
        assert c3.uuid.endswith("-0003")

    def test_requires_agent_name(self):
        with pytest.raises(ValueError, match="agent_name is required"):
            StreamClient(
                subscribe_key="sub",
                publish_key="pub",
                token="tok",
                stream_id="stream1",
            )

    def test_rejects_invalid_format(self):
        with pytest.raises(ValueError, match='Invalid stream format: "bogus"'):
            _make_client(format="bogus")

    def test_agent_name_env_var_does_not_provide_fallback(self):
        """Leaked AGENT_NAME env var must not serve as a fallback."""
        os.environ["AGENT_NAME"] = "env_agent"
        try:
            with pytest.raises(ValueError, match="agent_name is required"):
                StreamClient(
                    subscribe_key="sub",
                    publish_key="pub",
                    token="tok",
                    stream_id="stream1",
                )
        finally:
            os.environ.pop("AGENT_NAME", None)

    def test_missing_agent_name_error_does_not_mention_env(self):
        """Error for missing agent_name must not mention env var."""
        with pytest.raises(ValueError, match="agent_name is required") as exc_info:
            StreamClient(
                subscribe_key="sub",
                publish_key="pub",
                token="tok",
                stream_id="stream1",
            )
        assert "AGENT_NAME" not in str(exc_info.value)
        assert "env" not in str(exc_info.value).lower()


class TestSelfPublishFilter:

    def test_sets_filter_for_bidirectional(self, mock_pubnub):
        _, _, snapshots = mock_pubnub
        client = _make_client(direction="bidirectional", format="events")
        # The config snapshot taken at PubNub() construction must already
        # contain the filter — setting it after construction is a no-op
        # because the real SDK deep-copies config at init time.
        assert len(snapshots) == 1
        filt = snapshots[0]["filter_expression"]
        assert filt is not None
        assert f"meta.sender != '{client.uuid}'" == filt

    def test_no_filter_for_outbound(self, mock_pubnub):
        _, _, snapshots = mock_pubnub
        _make_client(direction="outbound")
        assert len(snapshots) == 1
        assert snapshots[0]["filter_expression"] is None

    def test_no_filter_for_inbound(self, mock_pubnub):
        _, _, snapshots = mock_pubnub
        _make_client(direction="inbound")
        assert len(snapshots) == 1
        assert snapshots[0]["filter_expression"] is None


class TestWrite:

    def test_throws_on_ended_stream(self):
        client = _make_client()
        client.end()
        with pytest.raises(RuntimeError, match="Cannot write to an ended stream"):
            client.write("fail")

    def test_throws_on_inbound_only(self):
        client = _make_client(direction="inbound")
        with pytest.raises(RuntimeError, match="Cannot write to an inbound-only stream"):
            client.write("fail")


class TestEnd:

    def test_sets_is_active_false(self):
        client = _make_client()
        assert client.is_active is True
        client.end()
        assert client.is_active is False

    def test_calls_on_end_callbacks(self):
        client = _make_client()
        called = []
        client.on_end(lambda: called.append(True))
        client.end()
        assert called == [True]

    def test_is_idempotent(self):
        client = _make_client()
        client.end()
        client.end()  # Should not throw

    def test_publishes_stream_end_marker_for_outbound(self):
        client = _make_client(direction="outbound", gating=False)
        client.write("data")
        client.end()

        # Verify stream_end was published via the bundle's _publish
        # Access the PubNub instance through the bundle
        pubnub = client._pubnub
        # Collect all publish calls and check for stream_end
        publish_calls = pubnub.publish.return_value.channel.return_value.message.call_args_list
        end_msgs = [
            c[0][0] for c in publish_calls
            if isinstance(c[0][0], dict) and c[0][0].get("type") == "stream_end"
        ]
        assert len(end_msgs) == 1
        msg = end_msgs[0]
        assert msg["type"] == "stream_end"
        assert msg["streamId"] == "my-stream"
        assert isinstance(msg["seq"], int)
        assert isinstance(msg["ts"], int)

    def test_does_not_publish_stream_end_marker_for_bidirectional(self):
        client = _make_client(direction="bidirectional", format="events", gating=False)
        client.write({"data": "test"})
        client.end()

        pubnub = client._pubnub
        publish_calls = pubnub.publish.return_value.channel.return_value.message.call_args_list
        end_msgs = [
            c[0][0] for c in publish_calls
            if isinstance(c[0][0], dict) and c[0][0].get("type") == "stream_end"
        ]
        assert len(end_msgs) == 0

    def test_does_not_publish_stream_end_marker_for_inbound(self):
        client = _make_client(direction="inbound")
        client.end()

        pubnub = client._pubnub
        publish_calls = pubnub.publish.return_value.channel.return_value.message.call_args_list
        end_msgs = [
            c[0][0] for c in publish_calls
            if isinstance(c[0][0], dict) and c[0][0].get("type") == "stream_end"
        ]
        assert len(end_msgs) == 0


class TestInbound:

    def test_throws_on_outbound_only(self):
        client = _make_client(direction="outbound")
        with pytest.raises(RuntimeError, match="Cannot read from an outbound-only stream"):
            _ = client.inbound

    def test_yields_normalized_stream_data(self):
        client = _make_client(direction="inbound")

        # Simulate incoming message by calling handle directly
        client._handle_inbound_message({
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
            "encoding": "utf8",
            "chunks": ["hello", "world"],
        })

        it = iter(client.inbound)
        # Get the message from the queue
        msg = client._inbound_queue.get(timeout=1)
        assert msg is not None
        assert msg.format == "bytes"
        assert msg.data == ["hello", "world"]
        assert msg.seq == 0
        assert msg.encoding == "utf8"

    def test_yields_normalized_stream_events(self):
        client = _make_client(direction="inbound")

        client._handle_inbound_message({
            "type": "stream_events",
            "streamId": "my-stream",
            "seq": 1,
            "ts": 1700000000000,
            "encoding": "utf8",
            "events": [{"temp": 72}],
        })

        msg = client._inbound_queue.get(timeout=1)
        assert msg is not None
        assert msg.format == "events"
        assert msg.data == [{"temp": 72}]
        assert msg.seq == 1

    def test_handles_multipart_reassembly(self):
        client = _make_client(direction="inbound")

        # Create an original message and split it
        original = {
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
            "encoding": "utf8",
            "chunks": ["reassembled content"],
        }
        serialized = json.dumps(original)
        raw_bytes = serialized.encode("utf-8")
        mid = len(raw_bytes) // 2
        part1 = raw_bytes[:mid]
        part2 = raw_bytes[mid:]

        # Send parts out of order (part 2 first)
        client._handle_inbound_message({
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
            "multipart": {"id": "mp-123-0", "part": 2, "total": 2},
            "data": base64.b64encode(part2).decode("ascii"),
        })

        client._handle_inbound_message({
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
            "multipart": {"id": "mp-123-0", "part": 1, "total": 2},
            "data": base64.b64encode(part1).decode("ascii"),
        })

        msg = client._inbound_queue.get(timeout=1)
        assert msg is not None
        assert msg.format == "bytes"
        assert msg.data == ["reassembled content"]

    def test_passes_through_raw_messages(self):
        client = _make_client(direction="inbound")

        client._handle_inbound_message({
            "type": "unknown_type",
            "data": "raw content",
            "seq": 5,
            "ts": 1700000000000,
        })

        msg = client._inbound_queue.get(timeout=1)
        assert msg is not None
        assert msg.format == "raw"

    def test_signals_done_when_ended(self):
        client = _make_client(direction="inbound")
        client.end()
        # Sentinel should be in queue
        msg = client._inbound_queue.get(timeout=1)
        assert msg is None  # sentinel

    def test_completes_iterator_on_stream_end(self):
        client = _make_client(direction="inbound")

        # Send stream_end marker
        client._handle_inbound_message({
            "type": "stream_end",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
        })

        assert client._inbound_done is True
        # Sentinel should be in queue
        msg = client._inbound_queue.get(timeout=1)
        assert msg is None  # sentinel

    def test_stream_end_not_yielded_as_data(self):
        client = _make_client(direction="inbound")

        # Send a normal data message first
        client._handle_inbound_message({
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
            "encoding": "utf8",
            "chunks": ["before-end"],
        })

        # Send stream_end marker
        client._handle_inbound_message({
            "type": "stream_end",
            "streamId": "my-stream",
            "seq": 1,
            "ts": 1700000000001,
        })

        # First message should be the data
        msg1 = client._inbound_queue.get(timeout=1)
        assert msg1 is not None
        assert msg1.format == "bytes"
        assert msg1.data == ["before-end"]

        # Second should be the sentinel (stream_end consumed internally)
        msg2 = client._inbound_queue.get(timeout=1)
        assert msg2 is None  # sentinel, not a data message


class TestConfigurationHierarchy:

    def test_uses_env_var_defaults(self):
        os.environ["STREAM_MAX_MESSAGE_SIZE"] = "8192"
        os.environ["STREAM_BUNDLE_SIZE"] = "2048"
        os.environ["STREAM_MAX_LATENCY_MS"] = "500"
        os.environ["STREAM_GATING"] = "off"

        client = _make_client()
        assert client.is_active

    def test_constructor_overrides_env_vars(self):
        os.environ["STREAM_GATING"] = "on"

        client = _make_client(gating=False)
        assert client.is_active
        # Write should go through even with 0 occupancy (gating disabled)
        client.write("test")

    def test_uses_built_in_defaults(self):
        client = _make_client()
        assert client.is_active


class TestMultipartValidation:
    """Tests for hardened multipart ingest: validation, consistency,
    duplicate detection, stale eviction, and reassembly edge cases."""

    # -- Helper to build a multipart wire message --------------------------

    @staticmethod
    def _mp_msg(
        mp_id: str = "mp-1",
        part: int = 1,
        total: int = 2,
        data: Any = None,
        seq: int = 0,
        ts: int = 1700000000000,
        msg_type: str = "stream_data",
        stream_id: str = "my-stream",
    ) -> dict:
        """Build a multipart envelope dict for testing."""
        if data is None:
            data = base64.b64encode(b"chunk").decode("ascii")
        msg: dict = {
            "type": msg_type,
            "streamId": stream_id,
            "seq": seq,
            "ts": ts,
            "multipart": {"id": mp_id, "part": part, "total": total},
            "data": data,
        }
        return msg

    # 1. Valid out-of-order reassembly still works
    def test_valid_out_of_order_reassembly(self):
        client = _make_client(direction="inbound")

        original = {
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
            "encoding": "utf8",
            "chunks": ["reassembled content"],
        }
        raw = json.dumps(original).encode("utf-8")
        mid = len(raw) // 2

        client._handle_inbound_message({
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
            "multipart": {"id": "mp-ooo", "part": 2, "total": 2},
            "data": base64.b64encode(raw[mid:]).decode("ascii"),
        })
        client._handle_inbound_message({
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
            "multipart": {"id": "mp-ooo", "part": 1, "total": 2},
            "data": base64.b64encode(raw[:mid]).decode("ascii"),
        })

        msg = client._inbound_queue.get(timeout=1)
        assert msg is not None
        assert msg.data == ["reassembled content"]

    # 2. part > total is dropped silently
    def test_part_greater_than_total_dropped(self):
        client = _make_client(direction="inbound")
        client._handle_inbound_message(self._mp_msg(part=3, total=2))
        assert client._inbound_queue.empty()
        assert len(client._multipart_buffers) == 0

    # 3. part == 0 is dropped silently
    def test_part_zero_dropped(self):
        client = _make_client(direction="inbound")
        client._handle_inbound_message(self._mp_msg(part=0, total=2))
        assert client._inbound_queue.empty()
        assert len(client._multipart_buffers) == 0

    # 4. Malformed "complete" set with wrong part numbers
    def test_malformed_complete_set_dropped(self):
        """total=2, parts {3,1} -- len(parts)==2 but key 2 missing.
        With validation, part=3 is rejected outright (part > total).
        So we only get part 1 buffered, never completing."""
        client = _make_client(direction="inbound")

        # part=3 with total=2 is invalid metadata, will be dropped
        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-bad", part=3, total=2,
            data=base64.b64encode(b"a").decode("ascii"),
        ))
        # part=1 with total=2 is valid, will be buffered but incomplete
        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-bad", part=1, total=2,
            data=base64.b64encode(b"b").decode("ascii"),
        ))

        assert client._inbound_queue.empty()

    # 5. Inconsistent total for same multipart.id
    def test_inconsistent_total_drops_group(self):
        client = _make_client(direction="inbound")

        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-inc", part=1, total=3,
        ))
        assert "mp-inc" in client._multipart_buffers

        # Second part claims total=2 instead of 3
        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-inc", part=2, total=2,
        ))
        # Group should be dropped
        assert "mp-inc" not in client._multipart_buffers
        assert client._inbound_queue.empty()

    # 6. Inconsistent seq or type for same multipart.id
    def test_inconsistent_seq_drops_group(self):
        client = _make_client(direction="inbound")

        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-seq", part=1, total=3, seq=5,
        ))
        assert "mp-seq" in client._multipart_buffers

        # Second part has different seq
        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-seq", part=2, total=3, seq=6,
        ))
        assert "mp-seq" not in client._multipart_buffers
        assert client._inbound_queue.empty()

    def test_inconsistent_type_drops_group(self):
        client = _make_client(direction="inbound")

        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-type", part=1, total=3, msg_type="stream_data",
        ))
        assert "mp-type" in client._multipart_buffers

        # Second part has different type
        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-type", part=2, total=3, msg_type="stream_events",
        ))
        assert "mp-type" not in client._multipart_buffers
        assert client._inbound_queue.empty()

    # 7. Non-string data field
    def test_non_string_data_dropped(self):
        client = _make_client(direction="inbound")

        # data is a number
        msg_num = self._mp_msg(mp_id="mp-num", part=1, total=2)
        msg_num["data"] = 42
        client._handle_inbound_message(msg_num)
        assert client._inbound_queue.empty()
        assert "mp-num" not in client._multipart_buffers

        # data is an object
        msg_obj = self._mp_msg(mp_id="mp-obj", part=1, total=2)
        msg_obj["data"] = {"nested": True}
        client._handle_inbound_message(msg_obj)
        assert client._inbound_queue.empty()
        assert "mp-obj" not in client._multipart_buffers

    # 8. Stale groups are evicted
    def test_stale_groups_evicted(self):
        client = _make_client(direction="inbound")

        # Create a partial group
        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-stale", part=1, total=3,
        ))
        assert "mp-stale" in client._multipart_buffers

        # Backdate the group's created_at past the TTL
        from blocks_network.stream.stream_client import _MULTIPART_TTL_S
        client._multipart_buffers["mp-stale"].created_at = (
            time.time() - _MULTIPART_TTL_S - 1
        )

        # Send a new multipart message to trigger eviction
        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-new", part=1, total=2,
        ))

        # Stale group should be gone
        assert "mp-stale" not in client._multipart_buffers
        # New group should be present
        assert "mp-new" in client._multipart_buffers

    # 9. Normal messages still flow after incomplete multipart
    def test_normal_messages_after_incomplete_multipart(self):
        client = _make_client(direction="inbound")

        # Send part 1 of 2 (incomplete multipart)
        client._handle_inbound_message(self._mp_msg(
            mp_id="mp-partial", part=1, total=2,
        ))
        assert client._inbound_queue.empty()

        # Send a normal stream_data message
        client._handle_inbound_message({
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 1,
            "ts": 1700000000000,
            "encoding": "utf8",
            "chunks": ["normal content"],
        })

        msg = client._inbound_queue.get(timeout=1)
        assert msg is not None
        assert msg.format == "bytes"
        assert msg.data == ["normal content"]


class TestOnInboundDone:
    """Tests for the on_inbound_done callback mechanism."""

    def test_fires_when_stream_end_received(self):
        client = _make_client(direction="inbound")

        fired = []
        client.on_inbound_done(lambda: fired.append(True))

        client._handle_inbound_message({
            "type": "stream_end",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
        })

        assert fired == [True]

    def test_fires_when_end_called_explicitly(self):
        client = _make_client(direction="inbound")

        fired = []
        client.on_inbound_done(lambda: fired.append(True))

        client.end()

        assert fired == [True]

    def test_fires_immediately_if_registered_after_completion(self):
        client = _make_client(direction="inbound")

        # Complete the iterator via stream_end
        client._handle_inbound_message({
            "type": "stream_end",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
        })

        # Register callback after completion
        fired = []
        client.on_inbound_done(lambda: fired.append(True))

        assert fired == [True]

    def test_does_not_fire_on_normal_data_messages(self):
        client = _make_client(direction="inbound")

        fired = []
        client.on_inbound_done(lambda: fired.append(True))

        client._handle_inbound_message({
            "type": "stream_data",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
            "encoding": "utf8",
            "chunks": ["hello"],
        })

        assert fired == []

    def test_fires_only_once_even_if_both_stream_end_and_end_occur(self):
        client = _make_client(direction="inbound")

        fired = []
        client.on_inbound_done(lambda: fired.append(True))

        # First: stream_end fires the callback
        client._handle_inbound_message({
            "type": "stream_end",
            "streamId": "my-stream",
            "seq": 0,
            "ts": 1700000000000,
        })

        assert fired == [True]

        # Second: end() should NOT fire it again
        client.end()

        assert fired == [True]
