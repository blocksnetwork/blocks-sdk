"""
StreamBundle - Internal transport engine for the Stream SDK.

Accumulates write() calls into buffered bundles and publishes them to
PubNub stream channels. Handles both wire formats (stream_data and
stream_events), multipart splitting for oversized payloads, binary
encoding, presence gating, and meta.sender on every publish.

This class is internal to the Stream SDK package. The public API is
StreamClient, which owns a StreamBundle instance.

Wire formats:
  stream_data:   { type, streamId, seq, ts, encoding, chunks }
  stream_events: { type, streamId, seq, ts, encoding: "utf8", events }

Sequence numbering:
  stream_data starts at seq 0
  stream_events starts at seq 1
"""

from __future__ import annotations

import base64
import json
import logging
import math
import threading
import time
from concurrent.futures import Future, ThreadPoolExecutor
from typing import Any, Callable, List, Optional

from .types import ENVELOPE_RESERVE, StreamBundleConfig
from ..protocol_version import CURRENT_PROTOCOL_VERSION


logger = logging.getLogger(__name__)


def _now_ms() -> int:
    """Current Unix timestamp in milliseconds."""
    return int(time.time() * 1000)


# Cap on concurrent in-flight publishes per multipart group.
#
# Python uses a larger pool than the Node SDK (Node: 4). Rationale:
# each Python worker blocks on ``httpx.Client.send`` inside
# ``pubnub.publish()...sync()`` — the Python-level per-publish
# overhead (JSON encode, auth header build, status wrap) is higher
# than Node's event-loop scheduling, so a wider pool is needed to
# hit the same wall-clock for a multipart group. 8 workers on a
# 13-part segment completes in ~2 rounds × RTT (~200-300 ms) which
# fits comfortably inside the 1.8 s real-time video publish budget.
# Still well under the DNS/connection-pool saturation point
# observed with unbounded fan-out. Cross-SDK parity is on the
# contract (both cap multipart fan-out) rather than on the exact
# integer.
DEFAULT_MULTIPART_CONCURRENCY = 8

# Per-publish retry policy. Mirrors the Node SDK's
# ``StreamBundle.publishMessage`` retry loop. Absorbs transient
# DNS / network blips that would otherwise drop a multipart part and
# break consumer reassembly.
_PUBLISH_MAX_ATTEMPTS = 3
_PUBLISH_BACKOFF_BASE_SEC = 0.1  # 100 ms, 200 ms between attempts


class StreamBundle:
    """Thread-safe buffered stream publisher.

    Parameters
    ----------
    pubnub:
        A configured PubNub client instance.
    channel:
        The PubNub channel to publish on
        (stream.{agentName}.{streamId}).
    stream_id:
        Logical stream identifier.
    format:
        Wire format ('bytes' or 'events').
    config:
        Bundling configuration.
    gated:
        Enable presence-gated publishing.
    """

    def __init__(
        self,
        pubnub: Any,
        channel: str,
        stream_id: str,
        format: str,
        config: StreamBundleConfig,
        gated: bool,
    ) -> None:
        if config.max_message_size <= ENVELOPE_RESERVE:
            raise ValueError(
                f"max_message_size ({config.max_message_size}) must be greater "
                f"than ENVELOPE_RESERVE ({ENVELOPE_RESERVE})"
            )
        self._pubnub = pubnub
        self._channel = channel
        self._stream_id = stream_id
        self._format = format
        self._max_message_size = config.max_message_size
        self._bundle_size_bytes = config.bundle_size_bytes
        self._max_latency_ms = config.max_latency_ms
        self._uuid = config.uuid
        self._gated = gated

        # Presence gating state
        self._occupancy: int = 0
        self._presence_listener: Any = None

        # Byte-format buffer
        self._buffer: List[str] = []
        self._buffer_bytes: int = 0
        self._current_batch_has_binary: bool = False

        # Event-format buffer
        self._event_buffer: List[Any] = []
        self._event_buffer_size: int = 0

        # stream_data starts at seq 0, stream_events starts at seq 1
        self._seq: int = 1 if format == "events" else 0
        self._closed: bool = False
        self._flush_timer: Optional[threading.Timer] = None
        self._lock = threading.Lock()

        # Bounded-concurrency pool for multipart publishes. Workers
        # only call ``self._pubnub.publish()...sync()`` -- they do
        # NOT touch StreamBundle mutable state, so no recursive lock
        # acquisition. Lifecycle: created here, shutdown in ``end()``.
        self._publish_executor: Optional[ThreadPoolExecutor] = ThreadPoolExecutor(
            max_workers=DEFAULT_MULTIPART_CONCURRENCY,
            thread_name_prefix=f"stream-publish-{stream_id}",
        )

        # on_end callback
        self.on_end: Optional[Callable[[], None]] = None

        # TODO(presence-gating): Temporarily disabled. The current
        # implementation silently discards writes when occupancy == 0,
        # which races against consumer subscription timing and causes
        # data loss on pipe tasks. Re-enable after fixing the race.
        # if self._gated:
        #     self._setup_presence_gating()

    @property
    def is_active(self) -> bool:
        with self._lock:
            return not self._closed

    @property
    def consumer_count(self) -> int:
        return self._occupancy if self._gated else 0

    # -- Public API ----------------------------------------------------------

    def write(self, data: Any) -> None:
        """Buffered write. Appends data to the internal buffer.
        Flushing happens on size or time threshold.
        """
        with self._lock:
            if self._closed:
                raise RuntimeError("Cannot write to a closed stream")

        # TODO(presence-gating): Disabled — see constructor comment.
        # if self._gated and self._occupancy == 0:
        #     return

        if self._format == "events":
            self._write_event(data)
        else:
            self._write_bytes(data)

    def publish_end_marker(self) -> None:
        """Publish a stream_end marker on the data channel.
        Uses the next seq value after the final data flush.
        Swallows publish errors silently (terminal fallback is the safety net).
        """
        with self._lock:
            message = {
                "type": "stream_end",
                "protocolVersion": CURRENT_PROTOCOL_VERSION,
                "streamId": self._stream_id,
                "seq": self._seq,
                "ts": _now_ms(),
            }
            self._seq += 1
        try:
            self._publish(message)
        except Exception:
            pass  # Best-effort; swallow errors

    def end(self) -> None:
        """Flush remaining data, clean up presence, invoke on_end callback."""
        with self._lock:
            if self._closed:
                return
            self._closed = True
            self._cancel_timer_locked()

            # Flush remaining buffered data (may submit to the
            # multipart pool below; pool is still alive at this point)
            if self._format == "events":
                if self._event_buffer:
                    self._flush_events_locked()
            else:
                if self._buffer:
                    self._flush_locked()

        # Clean up presence tracking
        self._teardown_presence_gating()

        # Drain the multipart publish pool. Final flush above already
        # waited on its own futures; this just releases worker threads.
        if self._publish_executor is not None:
            self._publish_executor.shutdown(wait=True)
            self._publish_executor = None

        if self.on_end is not None:
            self.on_end()

    # -- Presence gating -----------------------------------------------------

    def _setup_presence_gating(self) -> None:
        pres_channel = self._channel + "-pnpres"
        bundle_ref = self

        from pubnub.callbacks import SubscribeCallback

        class _PresenceListener(SubscribeCallback):
            def status(self, pubnub: Any, status: Any) -> None:
                pass

            def presence(self, pubnub: Any, presence: Any) -> None:
                pass

            def message(self, pubnub: Any, event: Any) -> None:
                msg = getattr(event, "message", None)
                if (
                    event.channel == pres_channel
                    and isinstance(msg, dict)
                    and isinstance(msg.get("occupancy"), int)
                ):
                    bundle_ref._occupancy = msg["occupancy"]

        self._presence_listener = _PresenceListener()
        self._pubnub.add_listener(self._presence_listener)
        self._pubnub.subscribe().channels([pres_channel]).execute()

        # Seed occupancy from hereNow
        try:
            result = self._pubnub.here_now().channels([self._channel]).sync()
            for ch in result.result.channels or []:
                if ch.channel_name == self._channel:
                    self._occupancy = ch.occupancy
                    break
        except Exception:
            pass  # Presence events will correct

    def _teardown_presence_gating(self) -> None:
        if self._presence_listener is not None:
            self._pubnub.remove_listener(self._presence_listener)
            self._presence_listener = None
        if self._gated:
            try:
                self._pubnub.unsubscribe().channels(
                    [self._channel + "-pnpres"]
                ).execute()
            except (KeyError, Exception):
                pass  # Channel may not be in subscription registry

    # -- Byte-format helpers -------------------------------------------------

    def _write_bytes(self, data: Any) -> None:
        if isinstance(data, bytes):
            chunk = base64.b64encode(data).decode("ascii")
            has_binary = True
        elif isinstance(data, str):
            chunk = data
            has_binary = False
        else:
            chunk = str(data)
            has_binary = False

        chunk_bytes = len(chunk.encode("utf-8"))

        with self._lock:
            if self._closed:
                raise RuntimeError("Cannot write to a closed stream")

            # TODO(presence-gating): Disabled — see constructor comment.
            # if self._gated and self._occupancy == 0:
            #     return

            self._buffer.append(chunk)
            self._buffer_bytes += chunk_bytes
            if has_binary:
                self._current_batch_has_binary = True

            if self._buffer_bytes >= self._bundle_size_bytes:
                self._flush_locked()
            else:
                self._ensure_timer_locked()

    def _flush_locked(self) -> None:
        """Build and publish a stream_data bundle.
        Must be called while _lock is held.
        """
        if not self._buffer:
            return

        self._cancel_timer_locked()

        has_binary = self._current_batch_has_binary
        encoding = "base64" if has_binary else "utf8"
        message = {
            "type": "stream_data",
            "protocolVersion": CURRENT_PROTOCOL_VERSION,
            "streamId": self._stream_id,
            "seq": self._seq,
            "ts": _now_ms(),
            "encoding": encoding,
            "chunks": list(self._buffer),
        }
        self._seq += 1

        self._buffer.clear()
        self._buffer_bytes = 0
        self._current_batch_has_binary = False

        # Check size limit -- split if oversized
        serialized = json.dumps(message)
        if len(serialized.encode("utf-8")) > self._max_message_size:
            self._publish_multipart(message)
        else:
            self._publish(message)

    # -- Event-format helpers ------------------------------------------------

    def _write_event(self, data: Any) -> None:
        if isinstance(data, str):
            raise RuntimeError(
                'write() does not accept raw strings in format: "events". '
                'Pass an object (e.g., {"text": "..."}) or use format: "bytes".'
            )

        if isinstance(data, bytes):
            encoded = base64.b64encode(data).decode("ascii")
            entry: Any = {"$binary": encoded}
            entry_size = math.ceil(len(data) * 4 / 3) + 20
        else:
            entry = data
            entry_size = len(json.dumps(data).encode("utf-8"))

        with self._lock:
            if self._closed:
                raise RuntimeError("Cannot write to a closed stream")

            # TODO(presence-gating): Disabled — see constructor comment.
            # if self._gated and self._occupancy == 0:
            #     return

            self._event_buffer.append(entry)
            self._event_buffer_size += entry_size

            if self._event_buffer_size >= self._bundle_size_bytes:
                self._flush_events_locked()
            else:
                self._ensure_timer_locked()

    def _flush_events_locked(self) -> None:
        """Build and publish a stream_events bundle.
        Must be called while _lock is held.
        """
        if not self._event_buffer:
            return

        self._cancel_timer_locked()

        message = {
            "type": "stream_events",
            "protocolVersion": CURRENT_PROTOCOL_VERSION,
            "streamId": self._stream_id,
            "seq": self._seq,
            "ts": _now_ms(),
            "encoding": "utf8",
            "events": list(self._event_buffer),
        }
        self._seq += 1

        self._event_buffer.clear()
        self._event_buffer_size = 0

        serialized = json.dumps(message)
        if len(serialized.encode("utf-8")) > self._max_message_size:
            self._publish_multipart(message)
        else:
            self._publish(message)

    # -- Publish helpers -----------------------------------------------------

    def _publish(self, message: dict) -> None:
        """Best-effort publish to the stream channel with meta.sender,
        meta.protocolVersion, and store_in_history=False.

        ``use_post(True)`` sends the message in the HTTP body (gzipped)
        instead of the URL path. Multipart stream frames can be tens
        of KB each; GET-based publishes saturate connection pools and
        TLS buffers once ~15 concurrent large URLs fly per segment
        (observed with video/fMP4 streams on the Node side, same
        contract here). Parity with ``agent_instance.py`` lines 189
        and 605, which already use POST for control-plane publishes.

        Retries up to ``_PUBLISH_MAX_ATTEMPTS`` times with exponential
        backoff (100 ms, 200 ms) on any exception. Absorbs transient
        DNS / network blips that would otherwise drop a multipart part
        and break consumer reassembly. On final failure the error is
        logged once; the call still returns normally so callers can
        continue with subsequent publishes (best-effort semantics).
        """
        last_err: Optional[BaseException] = None
        for attempt in range(1, _PUBLISH_MAX_ATTEMPTS + 1):
            try:
                self._pubnub.publish().channel(self._channel).message(
                    message
                ).meta(
                    {
                        "sender": self._uuid,
                        "protocolVersion": CURRENT_PROTOCOL_VERSION,
                    }
                ).should_store(
                    False
                ).use_post(True).sync()
                return
            except Exception as e:
                last_err = e
                if attempt < _PUBLISH_MAX_ATTEMPTS:
                    time.sleep(_PUBLISH_BACKOFF_BASE_SEC * (2 ** (attempt - 1)))
        logger.error(
            "[StreamBundle] Failed to publish stream message after %d attempts: %s",
            _PUBLISH_MAX_ATTEMPTS,
            last_err,
        )

    def _publish_multipart(self, message: dict) -> None:
        """Split an oversized message into multiple base64-encoded parts.

        Splitting is byte-safe: converts to bytes, splits on byte
        boundaries, then base64-encodes each part.

        Publishes run with bounded concurrency via the bundle's
        per-instance ``ThreadPoolExecutor`` (see
        ``DEFAULT_MULTIPART_CONCURRENCY``). Part order on the wire is
        irrelevant: the consumer reassembles by ``multipart.part``
        index. If the executor has been torn down (defensive: should
        not happen during normal flush), publishes fall back to serial
        execution on the calling thread.
        """
        serialized = json.dumps(message)
        data_bytes = serialized.encode("utf-8")
        part_size = (self._max_message_size - ENVELOPE_RESERVE) * 3 // 4
        total_parts = math.ceil(len(data_bytes) / part_size)
        multipart_id = f"mp-{_now_ms()}-{message.get('seq', self._seq)}"

        parts: List[dict] = []
        for i in range(total_parts):
            part_bytes = data_bytes[i * part_size : (i + 1) * part_size]
            parts.append(
                {
                    "type": message.get("type", "stream_data"),
                    "protocolVersion": CURRENT_PROTOCOL_VERSION,
                    "streamId": self._stream_id,
                    "seq": message.get("seq", self._seq),
                    "ts": message.get("ts", _now_ms()),
                    "multipart": {
                        "id": multipart_id,
                        "part": i + 1,
                        "total": total_parts,
                    },
                    "data": base64.b64encode(part_bytes).decode("ascii"),
                }
            )

        executor = self._publish_executor
        if executor is None:
            # Fallback: executor torn down (e.g., publish after end()).
            for part in parts:
                self._publish(part)
            return

        futures: List[Future] = [executor.submit(self._publish, p) for p in parts]
        for f in futures:
            # ``_publish`` swallows its own exceptions, so ``.result()``
            # should never raise. Catch defensively for unexpected
            # executor-level errors so one bad part can't abort the
            # remainder of the multipart group.
            try:
                f.result()
            except Exception as e:
                logger.error("[StreamBundle] Unexpected multipart future error: %s", e)

    # -- Timer helpers -------------------------------------------------------

    def _ensure_timer_locked(self) -> None:
        """Start (or leave running) the latency flush timer.
        Must be called while _lock is held.
        """
        if self._flush_timer is not None:
            return
        interval_sec = self._max_latency_ms / 1000.0
        self._flush_timer = threading.Timer(interval_sec, self._timer_fired)
        self._flush_timer.daemon = True
        self._flush_timer.start()

    def _cancel_timer_locked(self) -> None:
        """Cancel the pending flush timer if any.
        Must be called while _lock is held.
        """
        if self._flush_timer is not None:
            self._flush_timer.cancel()
            self._flush_timer = None

    def _timer_fired(self) -> None:
        """Callback invoked by the latency timer thread."""
        with self._lock:
            self._flush_timer = None
            if not self._closed:
                if self._format == "events" and self._event_buffer:
                    self._flush_events_locked()
                elif self._buffer:
                    self._flush_locked()
