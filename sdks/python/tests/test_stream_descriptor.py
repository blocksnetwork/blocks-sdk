"""
Tests for StreamDescriptor, from_descriptor, and invert_direction.

Covers:
- invert_direction: all 3 cases + unknown throws
- from_descriptor: direction mapping from local_direction
- from_descriptor: consumer gating policy (gating defaults to False for writable)
- from_descriptor: explicit gating override
- from_descriptor: extracts fields from descriptor
- from_descriptor: format propagation (bytes and events)
- descriptor-opened writers publish correct wire format
- direct constructor format default unchanged
"""

from __future__ import annotations

from unittest.mock import patch, MagicMock

import pytest
from blocks_network.stream.descriptor import StreamDescriptor, invert_direction
from blocks_network.stream.stream_client import StreamClient, _reset_uuid_counter
from tests.stream_conftest import create_mock_pubnub


def _make_descriptor(**overrides) -> StreamDescriptor:
    defaults = dict(
        task_id="task-123",
        stream_id="test-stream",
        agent_name="weather",
        channel="stream.weather.test-stream",
        token="T7c-token",
        agent_direction="outbound",
        local_direction="inbound",
        format="bytes",
        affinity="dedicated",
    )
    defaults.update(overrides)
    return StreamDescriptor(**defaults)


BASE_OPTIONS = dict(subscribe_key="sub-key", publish_key="pub-key")


@pytest.fixture(autouse=True)
def reset_counter():
    _reset_uuid_counter()
    yield
    _reset_uuid_counter()


@pytest.fixture(autouse=True)
def mock_pubnub():
    # Container holding a reference to the calls list created by the most
    # recent PubNub() mock construction. Tests that need wire-format
    # assertions use publish_ref[0] after a write+flush cycle.
    publish_ref = [None]  # [List[dict]] -- set inside _make_instance

    with patch("blocks_network.stream.stream_client.PubNub") as mock_cls, \
         patch("blocks_network.stream.stream_client.PNConfiguration") as mock_config_cls:
        def _make_instance(config):
            instance, calls, _listeners, _subscriptions = create_mock_pubnub()
            publish_ref[0] = calls
            instance.set_token = MagicMock()
            instance.unsubscribe_all = MagicMock()
            instance.stop = MagicMock()
            return instance
        mock_cls.side_effect = _make_instance
        mock_config_cls.return_value = MagicMock()
        yield mock_cls, publish_ref


class TestInvertDirection:

    def test_inverts_outbound_to_inbound(self):
        assert invert_direction("outbound") == "inbound"

    def test_inverts_inbound_to_outbound(self):
        assert invert_direction("inbound") == "outbound"

    def test_keeps_bidirectional(self):
        assert invert_direction("bidirectional") == "bidirectional"

    def test_raises_on_unknown(self):
        with pytest.raises(ValueError, match="Unknown direction: unknown"):
            invert_direction("unknown")


class TestFromDescriptor:

    def test_creates_inbound_client(self):
        desc = _make_descriptor(local_direction="inbound")
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)

        assert client.is_active
        assert client.channel == "stream.weather.test-stream"
        with pytest.raises(RuntimeError, match="Cannot write to an inbound-only stream"):
            client.write("test")

    def test_creates_outbound_client(self):
        desc = _make_descriptor(
            agent_direction="inbound", local_direction="outbound"
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)

        assert client.is_active
        with pytest.raises(RuntimeError, match="Cannot read from an outbound-only stream"):
            _ = client.inbound

    def test_creates_bidirectional_client(self):
        desc = _make_descriptor(
            agent_direction="bidirectional", local_direction="bidirectional"
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)

        assert client.is_active
        # Can write
        client.write({"text": "hello"})
        # Can read
        assert client.inbound is not None

    def test_defaults_gating_false_for_outbound(self):
        desc = _make_descriptor(
            agent_direction="inbound", local_direction="outbound"
        )
        # No explicit gating -- should default to False for writable
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)
        # Write should go through (gating:False means writes not gated)
        client.write("hello")
        assert client.is_active

    def test_defaults_gating_false_for_bidirectional(self):
        desc = _make_descriptor(
            agent_direction="bidirectional", local_direction="bidirectional"
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)
        client.write({"text": "hello"})
        assert client.is_active

    def test_defaults_gating_true_for_inbound(self):
        desc = _make_descriptor(local_direction="inbound")
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)
        assert client.is_active

    def test_respects_explicit_gating_true_for_writable(self):
        desc = _make_descriptor(
            agent_direction="inbound", local_direction="outbound"
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS, gating=True)
        assert client.is_active
        # With gating:True and 0 occupancy, write is silently discarded
        client.write("test")

    def test_respects_explicit_gating_false_for_inbound(self):
        desc = _make_descriptor(local_direction="inbound")
        client = StreamClient.from_descriptor(
            desc, **BASE_OPTIONS, gating=False
        )
        assert client.is_active

    def test_extracts_fields_from_descriptor(self):
        desc = _make_descriptor(
            stream_id="my-custom-id",
            agent_name="video_proc",
            channel="stream.video_proc.my-custom-id",
            token="my-token-123",
            local_direction="inbound",
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)
        assert client.channel == "stream.video_proc.my-custom-id"
        assert "video_proc-stream-" in client.uuid

    def test_honors_descriptor_channel(self):
        desc = _make_descriptor(
            stream_id="my-stream",
            agent_name="test_agent",
            channel="custom.channel.name",
            local_direction="inbound",
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)
        # Must use the descriptor's channel, not stream.test_agent.my-stream
        assert client.channel == "custom.channel.name"

    def test_works_with_minimal_fields(self):
        desc = StreamDescriptor(
            task_id="task-1",
            stream_id="stream-1",
            agent_name="test",
            channel="stream.test.stream-1",
            token="token-1",
            agent_direction="outbound",
            local_direction="inbound",
            format="bytes",
            affinity="dedicated",
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)
        assert client.is_active

    def test_works_with_all_fields_including_metadata(self):
        desc = StreamDescriptor(
            task_id="task-1",
            stream_id="stream-1",
            agent_name="test",
            channel="stream.test.stream-1",
            token="token-1",
            agent_direction="outbound",
            local_direction="inbound",
            format="bytes",
            affinity="dedicated",
            metadata={"resolution": "1080p", "codec": "h264"},
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)
        assert client.is_active

    def test_passes_through_transport_config(self):
        desc = _make_descriptor(local_direction="inbound")
        client = StreamClient.from_descriptor(
            desc,
            **BASE_OPTIONS,
            max_message_size=8192,
            bundle_size_bytes=2048,
            max_latency_ms=100,
        )
        assert client.is_active

    # -- Format propagation tests ------------------------------------------------

    def test_creates_bytes_format_client_from_descriptor(self, mock_pubnub):
        desc = _make_descriptor(
            agent_direction="inbound",
            local_direction="outbound",
            format="bytes",
            affinity="dedicated",
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)

        # Bytes format accepts raw strings
        client.write("hello")
        assert client.is_active

    def test_creates_events_format_client_from_descriptor(self, mock_pubnub):
        desc = _make_descriptor(
            agent_direction="inbound",
            local_direction="outbound",
            format="events",
            affinity="dedicated",
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)

        # Events format accepts objects
        client.write({"text": "hello"})
        assert client.is_active

    def test_descriptor_events_writers_reject_raw_strings(self, mock_pubnub):
        desc = _make_descriptor(
            agent_direction="inbound",
            local_direction="outbound",
            format="events",
            affinity="dedicated",
        )
        client = StreamClient.from_descriptor(desc, **BASE_OPTIONS)

        with pytest.raises(RuntimeError, match='write\\(\\) does not accept raw strings in format: "events"'):
            client.write("raw string")

    def test_descriptor_events_writers_publish_stream_events(self, mock_pubnub):
        _, publish_ref = mock_pubnub
        desc = _make_descriptor(
            agent_direction="inbound",
            local_direction="outbound",
            format="events",
            affinity="dedicated",
        )
        client = StreamClient.from_descriptor(
            desc, **BASE_OPTIONS, bundle_size_bytes=1,
        )
        client.write({"action": "greet"})

        calls = publish_ref[0]
        assert len(calls) >= 1
        msg = calls[0]["message"]
        assert msg["type"] == "stream_events"
        assert msg["events"] == [{"action": "greet"}]

    def test_descriptor_bytes_writers_publish_stream_data(self, mock_pubnub):
        _, publish_ref = mock_pubnub
        desc = _make_descriptor(
            agent_direction="inbound",
            local_direction="outbound",
            format="bytes",
            affinity="dedicated",
        )
        client = StreamClient.from_descriptor(
            desc, **BASE_OPTIONS, bundle_size_bytes=1,
        )
        client.write("hello bytes")

        calls = publish_ref[0]
        assert len(calls) >= 1
        msg = calls[0]["message"]
        assert msg["type"] == "stream_data"
        assert msg["chunks"] == ["hello bytes"]

    def test_direct_constructor_default_format_is_bytes(self, mock_pubnub):
        _, publish_ref = mock_pubnub
        client = StreamClient(
            subscribe_key="sub-key",
            publish_key="pub-key",
            token="test-token",
            agent_name="test_agent",
            stream_id="my-stream",
            direction="outbound",
            bundle_size_bytes=1,
            gating=False,
        )

        # Bytes format accepts raw strings (would throw if events)
        client.write("text data")

        calls = publish_ref[0]
        assert len(calls) >= 1
        msg = calls[0]["message"]
        assert msg["type"] == "stream_data"
