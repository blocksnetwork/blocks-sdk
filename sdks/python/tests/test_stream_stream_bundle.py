"""
Tests for StreamBundle (internal transport engine).

Covers:
- Bytes format: single chunk, multiple chunks, flush on size, flush on time
- Events format: object events, $binary tag, flush thresholds
- Events format: raw string write throws error
- Binary encoding: bytes auto base64 in both formats
- Multipart: oversized payload splits correctly, part structure, data field base64
- Size tracking: len(data.encode('utf-8')), not len(data)
- Presence gating: temporarily disabled (writes always publish)
- meta.sender on every publish
- Sequence numbering: stream_data starts at 0, stream_events starts at 1
- store_in_history=False on all publishes
"""

from __future__ import annotations

import base64
import json
import threading
import time
from unittest.mock import MagicMock

import pytest
from blocks_network.stream.stream_bundle import (
    DEFAULT_MULTIPART_CONCURRENCY,
    StreamBundle,
)
from blocks_network.stream.types import StreamBundleConfig
from tests.stream_conftest import create_mock_pubnub


def _default_config(**overrides) -> StreamBundleConfig:
    defaults = dict(
        max_message_size=16384,
        bundle_size_bytes=4096,
        max_latency_ms=250,
        uuid="test_agent-stream-0001",
    )
    defaults.update(overrides)
    return StreamBundleConfig(**defaults)


class TestBytesFormat:

    def test_accumulates_without_publishing(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(bundle_size_bytes=1024, max_latency_ms=5000), False,
        )
        sb.write("hello ")
        sb.write("world")
        assert len(calls) == 0

    def test_flushes_on_bundle_size(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(bundle_size_bytes=256, max_latency_ms=60000), False,
        )
        sb.write("x" * 300)
        assert len(calls) == 1
        msg = calls[0]["message"]
        assert msg["type"] == "stream_data"
        assert msg["streamId"] == "s1"
        assert msg["encoding"] == "utf8"
        assert isinstance(msg["chunks"], list)

    def test_flushes_on_timer(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(bundle_size_bytes=1024 * 1024, max_latency_ms=50),
            False,
        )
        sb.write("small")
        assert len(calls) == 0

        # Wait for the timer to fire
        time.sleep(0.15)
        assert len(calls) == 1
        msg = calls[0]["message"]
        assert msg["type"] == "stream_data"
        assert msg["chunks"] == ["small"]

    def test_preserves_chunk_order(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(bundle_size_bytes=100000, max_latency_ms=50), False,
        )
        sb.write("first")
        sb.write("second")
        sb.write("third")

        time.sleep(0.15)
        assert len(calls) == 1
        assert calls[0]["message"]["chunks"] == ["first", "second", "third"]

    def test_binary_buffer_base64_encoding(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(max_latency_ms=50), False,
        )
        sb.write(b"\xff\xfe\xfd")
        time.sleep(0.15)

        assert len(calls) == 1
        msg = calls[0]["message"]
        assert msg["encoding"] == "base64"
        assert msg["chunks"][0] == base64.b64encode(b"\xff\xfe\xfd").decode("ascii")

    def test_binary_sets_entire_batch_to_base64(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(max_latency_ms=50), False,
        )
        sb.write("text data")
        sb.write(b"\x01\x02")
        time.sleep(0.15)

        assert len(calls) == 1
        assert calls[0]["message"]["encoding"] == "base64"


class TestEventsFormat:

    def test_buffers_object_events(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "events",
            _default_config(max_latency_ms=50), False,
        )
        sb.write({"temp": 72})
        sb.write({"temp": 73})
        time.sleep(0.15)

        assert len(calls) == 1
        msg = calls[0]["message"]
        assert msg["type"] == "stream_events"
        assert msg["encoding"] == "utf8"
        assert msg["events"] == [{"temp": 72}, {"temp": 73}]

    def test_wraps_binary_as_dollar_binary_tag(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "events",
            _default_config(max_latency_ms=50), False,
        )
        sb.write(b"\xde\xad")
        time.sleep(0.15)

        assert len(calls) == 1
        msg = calls[0]["message"]
        expected = {"$binary": base64.b64encode(b"\xde\xad").decode("ascii")}
        assert msg["events"][0] == expected

    def test_throws_on_raw_string(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "events",
            _default_config(), False,
        )
        with pytest.raises(
            RuntimeError,
            match='write\\(\\) does not accept raw strings in format: "events"',
        ):
            sb.write("raw string")

    def test_encoding_always_utf8(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "events",
            _default_config(max_latency_ms=50), False,
        )
        sb.write(b"\xff")
        time.sleep(0.15)

        assert calls[0]["message"]["encoding"] == "utf8"


class TestSequenceNumbering:

    def test_stream_data_starts_at_seq_0(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(bundle_size_bytes=10, max_latency_ms=60000), False,
        )
        sb.write("x" * 20)
        assert len(calls) == 1
        assert calls[0]["message"]["seq"] == 0

    def test_stream_events_starts_at_seq_1(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "events",
            _default_config(bundle_size_bytes=10, max_latency_ms=60000), False,
        )
        sb.write({"event": "test"})
        assert len(calls) == 1
        assert calls[0]["message"]["seq"] == 1

    def test_sequence_increments_per_flush(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(bundle_size_bytes=256, max_latency_ms=60000), False,
        )
        # First batch
        sb.write("a" * 300)
        # Second batch
        sb.write("b" * 300)

        assert len(calls) == 2
        assert calls[0]["message"]["seq"] == 0
        assert calls[1]["message"]["seq"] == 1


class TestMetaSender:

    def test_includes_meta_sender_on_bytes_publish(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(max_latency_ms=50, uuid="my-agent-stream-0001"),
            False,
        )
        sb.write("hello")
        time.sleep(0.15)

        assert len(calls) == 1
        assert calls[0]["meta"]["sender"] == "my-agent-stream-0001"
        assert calls[0]["meta"]["protocolVersion"] == "2026-05-01"

    def test_includes_meta_sender_on_events_publish(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "events",
            _default_config(max_latency_ms=50, uuid="ev-agent-stream-0002"),
            False,
        )
        sb.write({"data": "test"})
        time.sleep(0.15)

        assert len(calls) == 1
        assert calls[0]["meta"]["sender"] == "ev-agent-stream-0002"
        assert calls[0]["meta"]["protocolVersion"] == "2026-05-01"


class TestStoreInHistory:

    def test_store_in_history_false_on_bytes(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(max_latency_ms=50), False,
        )
        sb.write("hello")
        time.sleep(0.15)

        assert calls[0]["store_in_history"] is False

    def test_store_in_history_false_on_events(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "events",
            _default_config(max_latency_ms=50), False,
        )
        sb.write({"event": "test"})
        time.sleep(0.15)

        assert calls[0]["store_in_history"] is False


class TestMultipart:

    def test_rejects_max_message_size_at_or_below_envelope_reserve(self):
        pubnub, _, _, _ = create_mock_pubnub()
        with pytest.raises(ValueError, match=r"max_message_size \(512\) must be greater than ENVELOPE_RESERVE"):
            StreamBundle(
                pubnub, "stream.test.s1", "s1", "bytes",
                _default_config(max_message_size=512), False,
            )
        with pytest.raises(ValueError, match="must be greater than ENVELOPE_RESERVE"):
            StreamBundle(
                pubnub, "stream.test.s1", "s1", "bytes",
                _default_config(max_message_size=100), False,
            )

    def test_splits_oversized_bytes_payload(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(
                max_message_size=600, bundle_size_bytes=100000, max_latency_ms=50
            ),
            False,
        )
        sb.write("x" * 1000)
        time.sleep(0.15)

        # Should have published multiple parts
        assert len(calls) > 1

        for call in calls:
            msg = call["message"]
            assert "multipart" in msg
            assert msg["multipart"]["id"].startswith("mp-")
            assert msg["multipart"]["part"] >= 1
            assert msg["multipart"]["total"] > 1
            assert isinstance(msg["data"], str)  # base64
            assert msg["seq"] == 0  # All parts share the same seq

        # Parts numbered 1 through total
        parts = [c["message"]["multipart"]["part"] for c in calls]
        total = calls[0]["message"]["multipart"]["total"]
        assert len(parts) == total
        for i in range(1, total + 1):
            assert i in parts

    def test_splits_oversized_events_payload(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "events",
            _default_config(
                max_message_size=600, bundle_size_bytes=100000, max_latency_ms=50
            ),
            False,
        )
        sb.write({"data": "y" * 1000})
        time.sleep(0.15)

        assert len(calls) > 1
        msg = calls[0]["message"]
        assert msg["type"] == "stream_events"
        assert "multipart" in msg
        # Events format seq starts at 1
        assert msg["seq"] == 1

    def test_multipart_parts_use_meta_sender(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(
                max_message_size=600,
                bundle_size_bytes=100000,
                max_latency_ms=50,
                uuid="mp-agent-stream-0001",
            ),
            False,
        )
        sb.write("z" * 1000)
        time.sleep(0.15)

        for call in calls:
            assert call["meta"]["sender"] == "mp-agent-stream-0001"
            assert call["meta"]["protocolVersion"] == "2026-05-01"

    def test_multipart_parts_store_in_history_false(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(
                max_message_size=600, bundle_size_bytes=100000, max_latency_ms=50
            ),
            False,
        )
        sb.write("z" * 1000)
        time.sleep(0.15)

        for call in calls:
            assert call["store_in_history"] is False

    def test_multipart_can_be_reassembled(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(
                max_message_size=600, bundle_size_bytes=100000, max_latency_ms=50
            ),
            False,
        )
        original_content = "hello-world-test-" * 80
        sb.write(original_content)
        time.sleep(0.15)

        # Reassemble
        parts = sorted(calls, key=lambda c: c["message"]["multipart"]["part"])
        buffers = [base64.b64decode(p["message"]["data"]) for p in parts]
        reassembled = b"".join(buffers).decode("utf-8")
        parsed = json.loads(reassembled)

        assert parsed["type"] == "stream_data"
        assert parsed["chunks"] == [original_content]


# Presence gating is temporarily disabled (TODO(presence-gating)).
# These tests verify the disabled state. When re-enabled, restore
# the original assertions that validate discard-on-zero-occupancy,
# -pnpres subscription, and hereNow seeding.
class TestPresenceGatingDisabled:

    def test_publishes_even_when_gated_occupancy_zero(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(max_latency_ms=50), True,
        )
        # With gating disabled, writes publish regardless of occupancy
        sb.write("gated write")
        time.sleep(0.15)
        assert len(calls) == 1

    def test_does_not_subscribe_to_pnpres_when_gated(self):
        pubnub, _, _, subscriptions = create_mock_pubnub()
        StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(), True,
        )
        assert "stream.test.s1-pnpres" not in subscriptions

    def test_does_not_subscribe_when_not_gated(self):
        pubnub, _, _, subscriptions = create_mock_pubnub()
        StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(), False,
        )
        assert "stream.test.s1-pnpres" not in subscriptions


class TestSizeTracking:

    def test_measures_bytes_not_characters(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        # emoji is 4 bytes in UTF-8. 512 emojis = 2048 bytes > 1024
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(bundle_size_bytes=1024, max_latency_ms=60000), False,
        )
        emoji = "\U0001F600"  # 4 bytes
        sb.write(emoji * 512)  # 2048 bytes
        assert len(calls) == 1


class TestEnd:

    def test_flushes_remaining_data(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(max_latency_ms=60000), False,
        )
        sb.write("buffered")
        sb.end()
        assert len(calls) == 1
        assert calls[0]["message"]["chunks"] == ["buffered"]

    def test_throws_on_write_after_end(self):
        pubnub, _, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(), False,
        )
        sb.end()
        with pytest.raises(RuntimeError, match="Cannot write to a closed stream"):
            sb.write("fail")

    def test_end_is_idempotent(self):
        pubnub, _, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(), False,
        )
        sb.end()
        sb.end()  # Should not throw

    def test_invokes_on_end_callback(self):
        pubnub, _, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(), False,
        )
        called = []
        sb.on_end = lambda: called.append(True)
        sb.end()
        assert called == [True]

    def test_no_publishes_on_empty_buffer(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(), False,
        )
        sb.end()
        assert len(calls) == 0


class TestPublishEndMarker:

    def test_publishes_correct_wire_shape(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(uuid="marker-agent-0001"), False,
        )
        sb.publish_end_marker()

        assert len(calls) == 1
        msg = calls[0]["message"]
        assert msg["type"] == "stream_end"
        assert msg["streamId"] == "s1"
        assert isinstance(msg["seq"], int)
        assert isinstance(msg["ts"], int)
        # No data, chunks, events, or encoding fields
        assert "data" not in msg
        assert "chunks" not in msg
        assert "events" not in msg
        assert "encoding" not in msg
        # meta.sender, meta.protocolVersion and store_in_history via _publish
        assert calls[0]["meta"]["sender"] == "marker-agent-0001"
        assert calls[0]["meta"]["protocolVersion"] == "2026-05-01"
        assert calls[0]["store_in_history"] is False
        # Stream publishes MUST use POST so multipart payloads end up
        # in the request body (gzipped) instead of the URL path. See
        # stream_bundle.py::_publish and Node SDK parity commit.
        assert calls[0]["use_post"] is True

    def test_increments_seq_correctly_after_data(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(bundle_size_bytes=10, max_latency_ms=60000), False,
        )
        # Write enough to trigger a flush (seq 0)
        sb.write("x" * 20)
        assert len(calls) == 1
        assert calls[0]["message"]["seq"] == 0

        # publish_end_marker should use the next seq (1)
        sb.publish_end_marker()
        assert len(calls) == 2
        assert calls[1]["message"]["seq"] == 1

    def test_does_not_throw_on_publish_failure(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        # Make every publish() call raise. ``_publish`` retries up to
        # ``_PUBLISH_MAX_ATTEMPTS`` times before giving up; assertion
        # is that exhaustion is silent (no exception escapes).
        pubnub.publish.side_effect = Exception("Network error")

        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(), False,
        )
        # Should not raise. Takes ~300 ms wall-clock (100 ms + 200 ms backoff).
        sb.publish_end_marker()
        # Exactly 3 attempts -- no over-retry, no under-retry.
        assert pubnub.publish.call_count == 3
        sb.end()

    # Regression guard for the retry/backoff logic in ``_publish``.
    # Transient network failures should be absorbed by a later attempt
    # instead of silently dropping the stream message.
    def test_retries_transient_publish_failure_and_succeeds_on_retry(self):
        pubnub, calls, _, _ = create_mock_pubnub()
        original_side_effect = pubnub.publish.side_effect
        # First publish attempt raises; the rest delegate to the
        # default mock builder so the retry succeeds normally.
        attempt_counter = {"n": 0}

        def first_call_raises_then_normal():
            attempt_counter["n"] += 1
            if attempt_counter["n"] == 1:
                raise Exception("Transient network error")
            return original_side_effect()

        pubnub.publish.side_effect = first_call_raises_then_normal

        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            _default_config(), False,
        )
        sb.publish_end_marker()
        # Two publish() calls: failed first attempt + successful retry.
        assert pubnub.publish.call_count == 2
        # Exactly one successful publish was recorded.
        assert len(calls) == 1
        sb.end()

    # Regression guard for bounded multipart concurrency. Unbounded
    # parallel fan-out (the prior model) saturated DNS / connection
    # pools in the video_stream use case; bounded fan-out (now via
    # per-bundle ThreadPoolExecutor) caps in-flight publishes at
    # ``DEFAULT_MULTIPART_CONCURRENCY``.
    def test_caps_concurrent_multipart_publishes_at_default(self):
        in_flight = [0]
        max_in_flight = [0]
        counter_lock = threading.Lock()
        recorded_parts: list[int] = []

        # Custom builder that delays sync() so multipart workers
        # actually overlap. Without the artificial delay, the GIL +
        # mock's instant return would let one thread complete before
        # the next started, masking concurrency violations.
        class _ConcurrencyTrackingBuilder:
            def channel(self, _ch): return self
            def message(self, msg): self._msg = msg; return self
            def meta(self, _m): return self
            def should_store(self, _s): return self
            def use_post(self, _p): return self

            def sync(self):
                with counter_lock:
                    in_flight[0] += 1
                    max_in_flight[0] = max(max_in_flight[0], in_flight[0])
                    recorded_parts.append(self._msg["multipart"]["part"])
                time.sleep(0.05)  # let other workers race
                with counter_lock:
                    in_flight[0] -= 1
                return MagicMock()

        pubnub = MagicMock()
        pubnub.publish.side_effect = _ConcurrencyTrackingBuilder
        pubnub.subscribe.side_effect = lambda: MagicMock()
        pubnub.unsubscribe.side_effect = lambda: MagicMock()
        pubnub.add_listener.side_effect = lambda _l: None
        pubnub.remove_listener.side_effect = lambda _l: None

        sb = StreamBundle(
            pubnub, "stream.test.s1", "s1", "bytes",
            # max_message_size just above ENVELOPE_RESERVE (512) +
            # tiny bundle_size_bytes forces a multipart split as soon
            # as we write. Yields ~80 parts for the 5000-byte payload.
            _default_config(max_message_size=600, bundle_size_bytes=80),
            False,
        )
        sb.write("z" * 5000)
        # Returns once all multipart futures have resolved.

        assert len(recorded_parts) > 4, (
            f"expected >4 parts to actually exercise concurrency; got {len(recorded_parts)}"
        )
        assert max_in_flight[0] <= DEFAULT_MULTIPART_CONCURRENCY, (
            f"concurrency cap broken: max_in_flight={max_in_flight[0]} > "
            f"{DEFAULT_MULTIPART_CONCURRENCY}"
        )
        # Sanity: we *are* using parallelism, not accidentally serial.
        assert max_in_flight[0] > 1, (
            f"expected concurrent publishes; got serial (max_in_flight={max_in_flight[0]})"
        )
        sb.end()
