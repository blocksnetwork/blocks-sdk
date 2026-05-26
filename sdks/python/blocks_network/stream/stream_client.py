"""
StreamClient - The developer-facing API for the Stream SDK.

Handles PubNub client creation, token setup, UUID generation, channel
computation, direction routing, self-publish filtering, presence gating,
and inbound message consumption with multipart reassembly.

StreamClient is the public API. StreamBundle is the internal engine.
"""

from __future__ import annotations

import base64
import json
import logging
import os
import queue
import threading
import time
from dataclasses import dataclass
from typing import Any, Callable, Dict, FrozenSet, Iterator, List, Optional

from pubnub.pnconfiguration import PNConfiguration
from pubnub.pubnub import PubNub

from .descriptor import StreamDescriptor
from .stream_bundle import StreamBundle
from .types import InboundMessage, StreamBundleConfig
from .validate import validate_stream_id

logger = logging.getLogger(__name__)

# Multipart reassembly limits (aligned with the Node SDK)
_MULTIPART_TTL_S = 30        # 30 seconds
_MULTIPART_MAX_GROUPS = 64   # max buffered incomplete groups

# Neutral transport-category enum surfaced through the public
# ``StreamError.category`` field. Mirrors the Node SDK's
# ``TransportCategory`` union so cross-SDK consumers branching on
# ``category`` see the same labels regardless of runtime. The raw
# ``PN…Category`` strings (or pubnub-python enum members) emitted by
# the underlying SDK are mapped at the listener edge via
# ``_map_transport_category``.
StreamCategory = str  # Literal['connected','reconnected','network_down','network_issues','timeout','malformed_response','access_denied','bad_request','other']

_CATEGORY_MAP: Dict[str, str] = {
    "PNConnectedCategory": "connected",
    "PNReconnectedCategory": "reconnected",
    "PNNetworkDownCategory": "network_down",
    "PNNetworkIssuesCategory": "network_issues",
    "PNTimeoutCategory": "timeout",
    "PNMalformedResponseCategory": "malformed_response",
    "PNAccessDeniedCategory": "access_denied",
    "PNBadRequestCategory": "bad_request",
}

# Fatal neutral categories — force-terminate the stream because the PAM
# grant is gone and won't come back. Non-fatal error categories (network,
# timeout, etc.) fire on_error but leave the stream running so PubNub's
# retry machinery can recover. Mirrors Node's FATAL_TRANSPORT_CATEGORIES.
FATAL_STREAM_ERROR_CATEGORIES: FrozenSet[str] = frozenset({
    "access_denied",   # PAM revocation / token denied
    "bad_request",     # auth config / malformed grant
})


def _coerce_category_name(category: Any) -> str:
    """Normalize a PubNub status category to its canonical raw name.

    The installed pubnub-python SDK delivers ``status.category`` as a
    ``PNStatusCategory`` enum member (``enum.Enum``, not a ``str``
    subclass), not as a raw string — e.g. 403 subscribe failures come
    through as ``PNStatusCategory.PNAccessDeniedCategory``. The raw name
    (``"PNAccessDeniedCategory"``) is returned so callers can either
    feed it into ``_map_transport_category`` for the user-facing neutral
    label or compare it directly when they need the unmapped form.

    Returns the empty string for anything that isn't a string or an
    enum-like object exposing a string ``.name``.
    """
    if isinstance(category, str):
        return category
    name = getattr(category, "name", None)
    return name if isinstance(name, str) else ""


def _map_transport_category(raw: Any) -> str:
    """Map a raw ``PN…Category`` (string or pubnub-python enum) to the
    neutral ``StreamCategory`` value. Unknown / empty input maps to
    ``"other"``. Mirrors Node's ``mapTransportCategory`` in
    ``transport-categories.ts``.
    """
    name = _coerce_category_name(raw)
    return _CATEGORY_MAP.get(name, "other")


def _is_status_error(status: Any) -> bool:
    """Native PubNub error detection.

    Prefers ``PNStatus.is_error()`` (the documented native signal on the
    Python SDK's status object). Falls back to neutral-fatal-category
    membership when an older/alternate SDK shape lacks ``is_error``.

    Exported for classifier unit tests.
    """
    is_error_fn = getattr(status, "is_error", None)
    if callable(is_error_fn):
        try:
            return bool(is_error_fn())
        except Exception:
            # Defensive: if is_error() raises, fall through to category
            # membership rather than silently dropping the signal.
            pass
    return _is_fatal_category(getattr(status, "category", None))


def _is_fatal_category(category: Any) -> bool:
    """True if this category (raw or pre-mapped) is fatal.

    Accepts the same shapes as ``_map_transport_category``: a raw string,
    a ``PNStatusCategory`` enum member, or a pre-mapped neutral string.
    Pre-mapped neutral strings short-circuit through the FATAL set
    directly; raw / enum inputs are mapped first.
    """
    if isinstance(category, str) and category in _CATEGORY_MAP.values():
        return category in FATAL_STREAM_ERROR_CATEGORIES
    return _map_transport_category(category) in FATAL_STREAM_ERROR_CATEGORIES


@dataclass(frozen=True)
class StreamError:
    """Typed error payload fired to ``StreamClient.on_error(...)`` subscribers.

    Fires for every PubNub status event classified as an error by
    ``_is_status_error``. Consumers branch on ``fatal`` for
    must-terminate conditions (PAM revocation, bad grant) and on
    ``category`` for finer-grained UX.

    Attributes
    ----------
    category : str
        Neutral transport category (e.g., ``"access_denied"``,
        ``"network_issues"``). One of the values in
        ``StreamCategory``. Raw ``PN…Category`` strings are mapped via
        ``_map_transport_category`` before reaching this field.
    error : Any
        Raw PubNub error data (from ``error_data`` or ``error``).
    channel : str
        The stream channel the error applies to.
    timestamp : float
        Unix seconds timestamp when the error was observed.
    fatal : bool
        Whether the error triggered forced stream termination.
    """

    category: str
    error: Any
    channel: str
    timestamp: float
    fatal: bool

# Per-process UUID counter for the {agentName}-stream-{NNNN} convention
_uuid_counter = 0
_uuid_lock = threading.Lock()


def _reset_uuid_counter() -> None:
    """Reset the UUID counter (for testing)."""
    global _uuid_counter
    _uuid_counter = 0


def _is_valid_multipart_meta(mp: dict) -> bool:
    """Validate multipart metadata fields. Returns True if valid."""
    mp_id = mp.get("id")
    if not isinstance(mp_id, str) or not mp_id:
        return False
    part = mp.get("part")
    if not isinstance(part, int) or part < 1:
        return False
    total = mp.get("total")
    if not isinstance(total, int) or total < 2:
        return False
    if part > total:
        return False
    return True


def _resolve_config_int(
    option_value: Optional[int],
    env_var: str,
    default: int,
) -> int:
    """Resolve a config value: constructor option > env var > default."""
    if option_value is not None:
        return option_value
    env_val = os.environ.get(env_var, "")
    if env_val:
        try:
            return int(env_val)
        except ValueError:
            pass
    return default


def _resolve_config_bool(
    option_value: Optional[bool],
    env_var: str,
    default: bool,
) -> bool:
    """Resolve a boolean config value: constructor option > env var > default."""
    if option_value is not None:
        return option_value
    env_val = os.environ.get(env_var, "")
    if env_val:
        lower = env_val.lower()
        return lower in ("true", "on", "1")
    return default


class _MultipartBuffer:
    """Tracks parts of a multipart message for reassembly."""

    def __init__(
        self,
        total: int,
        seq: int,
        ts: int,
        msg_type: str,
        stream_id: Optional[str] = None,
    ) -> None:
        self.total = total
        self.parts: Dict[int, str] = {}
        self.seq = seq
        self.ts = ts
        self.msg_type = msg_type
        self.stream_id = stream_id
        self.created_at: float = time.time()


class StreamClient:
    """Developer-facing API for stream I/O.

    Handles PubNub client creation, token setup, UUID generation, channel
    computation, direction routing, self-publish filtering, presence gating,
    and inbound message consumption with multipart reassembly.

    Parameters
    ----------
    subscribe_key : str
        PubNub subscribe key.
    publish_key : str
        PubNub publish key.
    token : str
        Per-stream token (T7a from setup handshake).
    agent_name : str
        Agent name (required).
    stream_id : str
        Stream identifier.
    channel : str, optional
        Explicit channel name. Default: computed as stream.{agent_name}.{stream_id}.
    format : str
        Wire format ('bytes' or 'events'). Default: 'bytes'.
    direction : str
        Direction of data flow ('outbound', 'inbound', 'bidirectional').
        Default: 'outbound'.
    max_message_size : int, optional
        Max serialized message size. Default: 16384 (or STREAM_MAX_MESSAGE_SIZE).
    bundle_size_bytes : int, optional
        Flush buffer at this byte size. Default: 4096 (or STREAM_BUNDLE_SIZE).
    max_latency_ms : int, optional
        Flush buffer after this many ms. Default: 250 (or STREAM_MAX_LATENCY_MS).
    gating : bool, optional
        Presence gating. Default: True (or STREAM_GATING).
    affinity : str, optional
        ``'dedicated'`` or ``'shared'``. Gates whether ``end()`` publishes
        a ``stream_end`` marker: shared-affinity streams never publish
        the marker because they are cross-task broadcasts (per-task
        cleanup is refcount-internal). Defaults to ``'dedicated'`` when
        not provided (backward-compatible for callers that pre-date the
        shared-stream lifecycle change).
    """

    def __init__(
        self,
        *,
        subscribe_key: str,
        publish_key: str,
        token: str,
        agent_name: Optional[str] = None,
        stream_id: str,
        channel: Optional[str] = None,
        format: str = "bytes",
        direction: str = "outbound",
        max_message_size: Optional[int] = None,
        bundle_size_bytes: Optional[int] = None,
        max_latency_ms: Optional[int] = None,
        gating: Optional[bool] = None,
        reorder_timeout_ms: Optional[int] = None,
        affinity: str = "dedicated",
    ) -> None:
        # Resolve agent name
        resolved_agent_name = agent_name or ""
        if not resolved_agent_name:
            raise ValueError(
                "agent_name is required (pass in options)"
            )

        validate_stream_id(stream_id)

        self._format = format
        if self._format not in ("bytes", "events"):
            raise ValueError(
                f'Invalid stream format: "{self._format}". Must be "bytes" or "events".'
            )
        self._direction = direction
        # Shared-affinity streams must never publish a stream_end marker
        # on per-task cleanup (SDK_CONTRACT §8.4.1a carve-out). Store the
        # value once; `end()` consults it below. Invalid inputs fall back
        # to 'dedicated' (the safer default — wrongly suppressing the
        # marker is more surprising than wrongly publishing it). Log a
        # WARN on fallback so misconfigured hand-constructed clients
        # surface in logs rather than as silent correctness drift. See
        # QUESTIONS.md D6 (shared_stream_lifecycle). The parser-level
        # enum guard already rejects invalid affinity on the wire path.
        if affinity in ("dedicated", "shared"):
            self._affinity = affinity
        else:
            logger.warning(
                "stream_client_invalid_affinity_fallback",
                extra={
                    "event": "stream_client_invalid_affinity_fallback",
                    "stream_id": stream_id,
                    "received_affinity": affinity,
                    "fallback_affinity": "dedicated",
                },
            )
            self._affinity = "dedicated"

        # Generate UUID: {agentName}-stream-{NNNN}
        global _uuid_counter
        with _uuid_lock:
            _uuid_counter += 1
            counter = _uuid_counter
        self._uuid = f"{resolved_agent_name}-stream-{counter:04d}"

        # Use explicit channel or compute from agent_name + stream_id
        self._channel = channel if channel is not None else f"stream.{resolved_agent_name}.{stream_id}"

        # Resolve configuration with env var fallbacks
        resolved_max_message_size = _resolve_config_int(
            max_message_size, "STREAM_MAX_MESSAGE_SIZE", 16384
        )
        resolved_bundle_size_bytes = _resolve_config_int(
            bundle_size_bytes, "STREAM_BUNDLE_SIZE", 4096
        )
        resolved_max_latency_ms = _resolve_config_int(
            max_latency_ms, "STREAM_MAX_LATENCY_MS", 250
        )
        resolved_gating = _resolve_config_bool(gating, "STREAM_GATING", True)

        # Create PubNub client instance
        pn_config = PNConfiguration()
        pn_config.subscribe_key = subscribe_key
        pn_config.publish_key = publish_key
        pn_config.user_id = self._uuid

        # Self-publish filter for bidirectional streams — must be set
        # before PubNub() construction because the SDK deep-copies config.
        if self._direction == "bidirectional":
            pn_config.filter_expression = f"meta.sender != '{self._uuid}'"

        self._pubnub = PubNub(pn_config)
        self._pubnub.set_token(token)

        # Create StreamBundle for outbound/bidirectional
        can_write = self._direction in ("outbound", "bidirectional")
        if can_write:
            bundle_config = StreamBundleConfig(
                max_message_size=resolved_max_message_size,
                bundle_size_bytes=resolved_bundle_size_bytes,
                max_latency_ms=resolved_max_latency_ms,
                uuid=self._uuid,
            )
            self._bundle: Optional[StreamBundle] = StreamBundle(
                pubnub=self._pubnub,
                channel=self._channel,
                stream_id=stream_id,
                format=self._format,
                config=bundle_config,
                gated=resolved_gating,
            )
        else:
            self._bundle = None

        # Inbound state
        self._is_active = True
        self._end_callbacks: List[Callable[[], None]] = []
        self._inbound_done_callbacks: List[Callable[[], None]] = []
        self._inbound_done_fired = False
        self._error_callbacks: List[Callable[[StreamError], None]] = []
        self._inbound_queue: queue.Queue[Optional[InboundMessage]] = queue.Queue()
        self._inbound_done = False
        self._message_listener: Any = None
        self._multipart_buffers: Dict[str, _MultipartBuffer] = {}

        # Reorder buffer state -- protects against out-of-order PubNub delivery.
        # _next_expected_seq: 0 for bytes (matching stream-bundle.ts:87), 1 for events.
        # Initialized from format at construction time, NOT from first arrival.
        self._reorder_timeout_ms: int = (
            reorder_timeout_ms if reorder_timeout_ms is not None else 750
        )
        self._next_expected_seq: int = 0 if self._format == "bytes" else 1
        self._reorder_buffer: Dict[int, InboundMessage] = {}
        self._reorder_timer: Optional[threading.Timer] = None
        self._end_seq: Optional[int] = None
        # Lock protecting all reorder state: _next_expected_seq, _reorder_buffer,
        # _reorder_timer, _end_seq. Three threads may touch this concurrently:
        # PubNub listener, threading.Timer callback, and consumer calling end().
        self._reorder_lock = threading.Lock()

        # Subscribe for inbound/bidirectional
        can_read = self._direction in ("inbound", "bidirectional")
        if can_read:
            self._setup_inbound()

    # -- Properties ----------------------------------------------------------

    @property
    def is_active(self) -> bool:
        return self._is_active

    @property
    def channel(self) -> str:
        return self._channel

    @property
    def uuid(self) -> str:
        return self._uuid

    # -- Write / End ---------------------------------------------------------

    def write(self, data: Any) -> None:
        """Write data to the stream.
        Raises if the stream is ended or direction is inbound-only.
        """
        if not self._is_active:
            raise RuntimeError("Cannot write to an ended stream")
        if self._bundle is None:
            raise RuntimeError("Cannot write to an inbound-only stream")
        self._bundle.write(data)

    def end(self) -> None:
        """Flush remaining data, unsubscribe, and destroy the PubNub client."""
        if not self._is_active:
            return
        self._is_active = False

        # Best-effort flush: on fatal PAM revocation (the exact case that
        # force-ends the stream via the status callback), these publishes
        # will raise because the underlying token is dead. Swallow the
        # failure so the teardown below (iterator signal, listener removal,
        # destroy) still runs — otherwise consumer iterators hang waiting
        # for the stream_end marker that can never be published.
        if self._bundle is not None:
            try:
                self._bundle.end()
            except Exception as err:
                logger.warning(
                    "bundle.end() failed during end() (continuing teardown): %r",
                    err,
                )

        # Publish end marker if this side is the writer on a unidirectional
        # stream. Shared-affinity streams are cross-task broadcasts; the
        # producer's per-task cleanup (or a consumer-writer's end()) is
        # refcount-internal and MUST NOT publish a stream_end marker that
        # would end peer consumers' iterators mid-broadcast.
        # See SDK_CONTRACT §8.4.1a shared-affinity carve-out.
        if (
            self._direction != 'bidirectional'
            and self._bundle is not None
            and self._affinity != 'shared'
        ):
            try:
                self._bundle.publish_end_marker()
            except Exception as err:
                logger.warning(
                    "publish_end_marker() failed during end() (continuing teardown): %r",
                    err,
                )

        # Clear incomplete multipart buffers
        self._multipart_buffers.clear()

        # Clean up reorder state under lock
        with self._reorder_lock:
            if self._reorder_timer is not None:
                self._reorder_timer.cancel()
                self._reorder_timer = None
            self._reorder_buffer.clear()

        # Signal inbound iterator completion
        self._inbound_done = True
        self._inbound_queue.put(None)  # sentinel
        self._fire_inbound_done()

        # Clean up PubNub
        if self._message_listener is not None:
            self._pubnub.remove_listener(self._message_listener)
            self._message_listener = None
        self._pubnub.unsubscribe_all()
        self._pubnub.stop()

        # Invoke end callbacks
        for cb in self._end_callbacks:
            cb()
        self._end_callbacks.clear()

    def on_end(self, callback: Callable[[], None]) -> None:
        """Register a callback to be invoked when end() completes."""
        self._end_callbacks.append(callback)

    def on_inbound_done(self, cb: Callable[[], None]) -> None:
        """Register a callback to fire when the inbound iterator completes.

        Fires when the inbound side completes for any reason: stream_end
        marker, explicit end(), or error. Internal only -- used by
        TaskSession for auto-drain tracking.

        If the inbound side has already completed, the callback fires
        immediately.
        """
        if self._inbound_done_fired:
            cb()
            return
        self._inbound_done_callbacks.append(cb)

    def _fire_inbound_done(self) -> None:
        """Fire all inbound-done callbacks exactly once."""
        if self._inbound_done_fired:
            return
        self._inbound_done_fired = True
        for cb in self._inbound_done_callbacks:
            try:
                cb()
            except Exception:
                pass
        self._inbound_done_callbacks.clear()

    def on_error(self, callback: Callable[[StreamError], None]) -> None:
        """Register a callback to fire when the stream encounters a PubNub
        status error — PAM revocation, network failure, grant mismatch, etc.

        The callback receives a :class:`StreamError` with ``fatal=True``
        when the error caused forced stream termination (and
        :meth:`on_inbound_done` will fire shortly after so consumer
        iterators exit cleanly). Non-fatal errors fire with
        ``fatal=False`` and leave the stream running so PubNub's retry
        machinery can recover.

        Consumer exceptions from the callback are swallowed and logged
        via :mod:`logging`; they do not interrupt stream teardown.
        """
        self._error_callbacks.append(callback)

    def _fire_error(self, err: "StreamError") -> None:
        """Dispatch ``err`` to every registered ``on_error`` callback.

        Iterates over a snapshot of the list so a consumer that registers
        another callback mid-dispatch does not corrupt iteration.
        Consumer exceptions are logged (with traceback) but never
        re-raised: the dispatcher must not interrupt termination.
        """
        for cb in list(self._error_callbacks):
            try:
                cb(err)
            except Exception:
                logger.exception("StreamClient.on_error callback raised")

    # -- Inbound iterator ----------------------------------------------------

    @property
    def inbound(self) -> Iterator[InboundMessage]:
        """Iterator of inbound messages. Raises if direction is outbound-only.
        Handles multipart reassembly transparently.
        """
        if self._direction == "outbound":
            raise RuntimeError("Cannot read from an outbound-only stream")
        return self._inbound_iter()

    def _inbound_iter(self) -> Iterator[InboundMessage]:
        """Generator that yields InboundMessage objects from the queue."""
        while True:
            msg = self._inbound_queue.get()
            if msg is None:
                return
            yield msg

    # -- Inbound setup -------------------------------------------------------

    def _setup_inbound(self) -> None:
        client = self

        from pubnub.callbacks import SubscribeCallback

        class _MessageListener(SubscribeCallback):
            def status(self, pubnub: Any, status: Any) -> None:
                # Dispatch every error-classified status to on_error
                # subscribers, then force-terminate on fatal-category
                # errors so the consumer iterator exits rather than
                # hanging waiting for stream_end. All work wrapped in
                # try/except so a listener exception cannot destabilize
                # PubNub's internal event loop.
                try:
                    if not _is_status_error(status):
                        return
                    category = _map_transport_category(
                        getattr(status, "category", None),
                    )
                    fatal = _is_fatal_category(category)
                    error_data = (
                        getattr(status, "error_data", None)
                        or getattr(status, "errorData", None)
                        or getattr(status, "error", None)
                    )
                    err = StreamError(
                        category=category,
                        error=error_data,
                        channel=client._channel,
                        timestamp=time.time(),
                        fatal=fatal,
                    )
                    logger.warning(
                        "Stream subscribe status error: "
                        "category=%s fatal=%s channel=%s error=%r",
                        category, fatal, client._channel, error_data,
                    )
                    client._fire_error(err)
                    if fatal and client._is_active:
                        # Force-terminate so consumer iterator exits
                        # cleanly instead of hanging.
                        try:
                            client.end()
                        except Exception:
                            logger.exception(
                                "StreamClient.end() raised during forced"
                                " termination after fatal status error"
                            )
                except Exception:
                    logger.exception("Stream status handler raised")

            def presence(self, pubnub: Any, presence: Any) -> None:
                pass

            def message(self, pubnub: Any, event: Any) -> None:
                if event.channel != client._channel:
                    return
                client._handle_inbound_message(event.message)

        self._message_listener = _MessageListener()
        self._pubnub.add_listener(self._message_listener)
        # with_timetoken(1000) asks PubNub to replay everything still in the
        # channel's in-memory cache (per SDK_CONTRACT §10.4.1a). On data-plane
        # stream channels this is a short-term mitigation for the
        # publish-before-subscribe race; the reorder buffer's seq-based
        # dedup handles duplicate delivery from replay overlap. The durable
        # data-plane fix is stream presence gating (issue #496).
        self._pubnub.subscribe().channels([self._channel]).with_timetoken(1000).execute()

    def _handle_inbound_message(self, msg: Any) -> None:
        if not msg or not isinstance(msg, dict):
            return

        # Check for stream_end marker -- participates in reorder state machine
        if msg.get("type") == "stream_end":
            raw_seq = msg.get("seq")
            has_numeric_seq = isinstance(raw_seq, int) and not isinstance(raw_seq, bool)

            # Disable mode: complete immediately (buffer is always empty)
            if self._reorder_timeout_ms <= 0:
                self._inbound_done = True
                self._inbound_queue.put(None)
                self._fire_inbound_done()
                return

            # Malformed stream_end: missing numeric seq — warn and ignore
            if not has_numeric_seq:
                logging.warning(
                    "[StreamClient] stream_end missing numeric seq field; ignoring in reorder mode"
                )
                return

            end_seq_val = int(raw_seq)

            completed = False
            with self._reorder_lock:
                self._end_seq = end_seq_val
                if self._next_expected_seq >= self._end_seq:
                    completed = self._check_end_reached()
                else:
                    # Gap exists between _next_expected_seq and _end_seq.
                    # Start a gap timer even if _reorder_buffer is empty --
                    # handles the tail-gap case where final data messages
                    # are permanently lost.
                    self._start_reorder_timer()
            if completed:
                self._fire_inbound_done()
            return

        # Check for multipart
        multipart = msg.get("multipart")
        if multipart and isinstance(multipart, dict):
            self._handle_multipart_part(msg, multipart)
            return

        # Normal message -- normalize to InboundMessage
        inbound = self._normalize_message(msg)
        if inbound is not None:
            self._enqueue_reordered(inbound)

    def _evict_stale_groups(self) -> None:
        """Remove multipart groups older than TTL or when buffer exceeds cap."""
        now = time.time()
        stale_ids = [
            mp_id
            for mp_id, buf in self._multipart_buffers.items()
            if now - buf.created_at > _MULTIPART_TTL_S
        ]
        for mp_id in stale_ids:
            del self._multipart_buffers[mp_id]

        # If still over capacity, evict oldest groups first
        if len(self._multipart_buffers) > _MULTIPART_MAX_GROUPS:
            by_age = sorted(
                self._multipart_buffers.items(),
                key=lambda item: item[1].created_at,
            )
            to_remove = len(self._multipart_buffers) - _MULTIPART_MAX_GROUPS
            for mp_id, _ in by_age[:to_remove]:
                del self._multipart_buffers[mp_id]

    def _handle_multipart_part(
        self, message: dict, mp: dict
    ) -> None:
        # Validate multipart metadata
        if not _is_valid_multipart_meta(mp):
            return

        # Validate that data is a string
        data = message.get("data")
        if not isinstance(data, str):
            return

        # Evict stale/overflowing groups before processing
        self._evict_stale_groups()

        mp_id = mp["id"]
        part = mp["part"]
        total = mp["total"]

        msg_seq = message.get("seq", 0)
        msg_type = message.get("type", "stream_data")
        msg_stream_id = message.get("streamId")

        if mp_id in self._multipart_buffers:
            entry = self._multipart_buffers[mp_id]

            # Consistency check: total, seq, msg_type, and streamId must match
            if (
                entry.total != total
                or entry.seq != msg_seq
                or entry.msg_type != msg_type
                or entry.stream_id != msg_stream_id
            ):
                del self._multipart_buffers[mp_id]
                return

            # Duplicate detection
            if part in entry.parts:
                if entry.parts[part] == data:
                    # Idempotent duplicate -- ignore
                    return
                else:
                    # Conflicting duplicate -- drop the whole group
                    del self._multipart_buffers[mp_id]
                    return
        else:
            entry = _MultipartBuffer(
                total=total,
                seq=msg_seq,
                ts=message.get("ts", 0),
                msg_type=msg_type,
                stream_id=msg_stream_id,
            )
            self._multipart_buffers[mp_id] = entry

        entry.parts[part] = data

        # Check if all parts arrived
        if len(entry.parts) == entry.total:
            del self._multipart_buffers[mp_id]

            # Verify all keys 1..total are present (not just count)
            for i in range(1, entry.total + 1):
                if i not in entry.parts:
                    return  # Missing part -- drop silently

            # Reassemble: base64-decode each part, concatenate, parse JSON
            try:
                sorted_parts = [entry.parts[i] for i in range(1, entry.total + 1)]
                concatenated = b"".join(
                    base64.b64decode(p) for p in sorted_parts
                )
                reassembled = json.loads(concatenated.decode("utf-8"))
                inbound = self._normalize_message(reassembled)
                if inbound is not None:
                    self._enqueue_reordered(inbound)
            except Exception:
                pass  # Failed to reassemble -- drop silently

    def _normalize_message(self, message: dict) -> Optional[InboundMessage]:
        msg_type = message.get("type")

        if msg_type == "stream_data":
            seq = message.get("seq")
            if not isinstance(seq, int) or isinstance(seq, bool):
                return None
            chunks = message.get("chunks")
            if not isinstance(chunks, list) or len(chunks) == 0 or not all(isinstance(c, str) for c in chunks):
                return None
            encoding = message.get("encoding", "utf8")
            if encoding not in ("utf8", "base64"):
                return None
            return InboundMessage(
                data=chunks,
                seq=seq,
                ts=message.get("ts", 0),
                format="bytes",
                encoding=encoding,
            )

        if msg_type == "stream_events":
            seq = message.get("seq")
            if not isinstance(seq, int) or isinstance(seq, bool):
                return None
            events = message.get("events")
            if not isinstance(events, list) or len(events) == 0:
                return None
            return InboundMessage(
                data=events,
                seq=seq,
                ts=message.get("ts", 0),
                format="events",
                encoding="utf8",
            )

        # Unknown message type -- pass through as raw only if seq is a valid integer
        raw_seq = message.get("seq")
        if not isinstance(raw_seq, int) or isinstance(raw_seq, bool):
            return None
        return InboundMessage(
            data=message,
            seq=raw_seq,
            ts=message.get("ts", 0),
            format="raw",
            encoding="utf8",
        )

    def _enqueue_inbound(self, msg: InboundMessage) -> None:
        self._inbound_queue.put(msg)

    # -- Reorder buffer -------------------------------------------------------

    def _enqueue_reordered(self, msg: InboundMessage) -> None:
        """Route a normalized message through the reorder buffer.

        Disable mode (reorder_timeout_ms <= 0): bypass buffer entirely,
        yield in arrival order (legacy behavior).

        Must be called WITHOUT holding _reorder_lock -- this method
        acquires it internally.
        """
        if self._reorder_timeout_ms <= 0:
            self._enqueue_inbound(msg)
            return

        completed = False
        with self._reorder_lock:
            seq = msg.seq

            # Reject data past the stream boundary
            if self._end_seq is not None and seq >= self._end_seq:
                return

            # Duplicate detection: by seq number alone (first arrival wins).
            if seq < self._next_expected_seq:
                return  # already yielded
            if seq in self._reorder_buffer:
                return  # already buffered

            if seq == self._next_expected_seq:
                # In order: yield immediately and flush consecutive
                self._enqueue_inbound(msg)
                self._next_expected_seq += 1
                self._flush_consecutive()
                self._cancel_and_restart_timer_if_needed()
                completed = self._check_end_reached()
            else:
                # Out of order: buffer and start timeout (does NOT reset if
                # already running -- timeout measures from first gap, bounding
                # how long any single missing seq can block the stream)
                self._reorder_buffer[seq] = msg
                self._start_reorder_timer()
        if completed:
            self._fire_inbound_done()

    def _flush_consecutive(self) -> None:
        """Flush buffered messages that are consecutive from _next_expected_seq.

        Caller MUST hold _reorder_lock.
        """
        while self._next_expected_seq in self._reorder_buffer:
            buffered = self._reorder_buffer.pop(self._next_expected_seq)
            self._enqueue_inbound(buffered)
            self._next_expected_seq += 1

    def _start_reorder_timer(self) -> None:
        """Start a one-shot timer that fires _on_reorder_timeout.

        If a timer is already running, this is a no-op (timer measures
        from the first gap, not the latest out-of-order arrival).

        Caller MUST hold _reorder_lock.
        """
        if self._reorder_timer is not None:
            return  # already running
        self._reorder_timer = threading.Timer(
            self._reorder_timeout_ms / 1000.0,
            self._on_reorder_timeout,
        )
        self._reorder_timer.daemon = True
        self._reorder_timer.start()

    def _on_reorder_timeout(self) -> None:
        """Timer callback: skip missing seq(s), flush consecutive, check end.

        Acquires _reorder_lock internally. Fires callbacks after release.
        """
        completed = False
        with self._reorder_lock:
            self._reorder_timer = None
            if self._reorder_buffer:
                # Skip missing seq(s), advance to next buffered message
                min_buffered = min(self._reorder_buffer.keys())
                self._next_expected_seq = min_buffered
                self._flush_consecutive()
                self._cancel_and_restart_timer_if_needed()
                completed = self._check_end_reached()
            elif self._end_seq is not None and self._next_expected_seq < self._end_seq:
                # Tail-gap: stream_end was received but final data messages
                # are permanently lost (buffer is empty, nothing to flush).
                # Skip to _end_seq and complete the stream.
                self._next_expected_seq = self._end_seq
                completed = self._check_end_reached()
        if completed:
            self._fire_inbound_done()

    def _cancel_and_restart_timer_if_needed(self) -> None:
        """Cancel the current timer and start a fresh one if gaps remain.

        Called after flushing consecutive messages. If the gap that started
        the current timer has been resolved but a new gap exists, cancel
        the old timer and start a fresh one for the new gap.

        Caller MUST hold _reorder_lock.
        """
        if self._reorder_timer is not None:
            self._reorder_timer.cancel()
            self._reorder_timer = None
        if self._reorder_buffer:
            self._start_reorder_timer()

    def _check_end_reached(self) -> bool:
        """Complete the stream if _next_expected_seq has reached _end_seq.

        Returns True if the stream completed. Caller MUST fire
        _fire_inbound_done() AFTER releasing _reorder_lock to avoid
        deadlock (callbacks may call end() which acquires the lock).

        Caller MUST hold _reorder_lock.
        """
        if self._end_seq is not None and self._next_expected_seq >= self._end_seq:
            # Invariant: buffer should be empty at this point.
            if self._reorder_buffer:
                logging.warning(
                    "[StreamClient] reorder buffer not empty at stream end "
                    f"({len(self._reorder_buffer)} unexpected messages with seq >= {self._end_seq})"
                )
                self._reorder_buffer.clear()
            if self._reorder_timer is not None:
                self._reorder_timer.cancel()
                self._reorder_timer = None
            self._inbound_done = True
            self._inbound_queue.put(None)  # sentinel
            return True
        return False

    # -- Convenience iterators ------------------------------------------------

    def bytes(self) -> Iterator[bytes]:
        """Decoded byte iterator. Handles base64/utf8 encoding.

        Iterates over inbound messages and yields decoded ``bytes``
        objects. Base64-encoded chunks are decoded automatically based
        on the ``encoding`` field of each inbound message.
        """
        for msg in self.inbound:
            chunks = msg.data if isinstance(msg.data, list) else [msg.data]
            for chunk in chunks:
                if isinstance(chunk, (bytes, bytearray)):
                    yield bytes(chunk)
                elif msg.encoding == "base64":
                    yield base64.b64decode(chunk)
                else:
                    yield chunk.encode("utf-8")

    def events(self) -> Iterator[Any]:
        """Flattened event iterator.

        Iterates over inbound messages and yields individual events.
        Batched event arrays are flattened so each event is yielded
        separately.
        """
        for msg in self.inbound:
            items = msg.data if isinstance(msg.data, list) else [msg.data]
            for event in items:
                yield event

    def as_file(self) -> Any:
        """File-like wrapper for ``shutil.copyfileobj`` / subprocess pipe.

        Returns a ``BufferedReader`` wrapping a ``RawIOBase`` subclass
        that reads decoded bytes from :meth:`bytes`.
        """
        import io

        byte_iter = self.bytes()

        class _StreamFile(io.RawIOBase):
            def __init__(self) -> None:
                super().__init__()
                self._iter = byte_iter
                self._buf = b""

            def readable(self) -> bool:
                return True

            def readinto(self, b: bytearray) -> int:  # type: ignore[override]
                while len(self._buf) < len(b):
                    try:
                        self._buf += next(self._iter)
                    except StopIteration:
                        break
                n = min(len(b), len(self._buf))
                b[:n] = self._buf[:n]
                self._buf = self._buf[n:]
                return n

        return io.BufferedReader(_StreamFile())

    # -- Background thread helpers (Python-specific) --------------------------

    def consume_in_background(
        self, callback: Callable[[Any], None]
    ) -> threading.Thread:
        """Start a daemon thread that iterates events or bytes and calls
        *callback* for each item. Returns the started thread.

        Uses :meth:`events` for ``events`` format streams, :meth:`bytes`
        for ``bytes`` format streams.
        """
        def _run() -> None:
            try:
                if self._format == "events":
                    for event in self.events():
                        callback(event)
                else:
                    for chunk in self.bytes():
                        callback(chunk)
            except Exception:
                pass

        t = threading.Thread(target=_run, daemon=True)
        t.start()
        return t

    def write_periodic(
        self,
        interval_sec: float,
        generator: Callable[[int], Any],
        stop_event: Optional[threading.Event] = None,
    ) -> threading.Thread:
        """Start a daemon thread that calls ``generator(count)`` every
        *interval_sec* seconds and writes the result. Returns the thread.

        The thread stops when:
        - The stream becomes inactive (``is_active`` is ``False``).
        - *stop_event* is set (if provided).
        - *generator* or :meth:`write` raises an exception.
        """
        _stop = stop_event or threading.Event()

        def _run() -> None:
            count = 0
            while not _stop.is_set() and self.is_active:
                count += 1
                try:
                    self.write(generator(count))
                except Exception:
                    break
                _stop.wait(interval_sec)

        t = threading.Thread(target=_run, daemon=True)
        t.start()
        return t

    # -- Static factory ------------------------------------------------------

    @classmethod
    def from_descriptor(
        cls,
        descriptor: StreamDescriptor,
        *,
        subscribe_key: str,
        publish_key: str,
        max_message_size: Optional[int] = None,
        bundle_size_bytes: Optional[int] = None,
        max_latency_ms: Optional[int] = None,
        gating: Optional[bool] = None,
        reorder_timeout_ms: Optional[int] = None,
        consumer_user_id: Optional[str] = None,
    ) -> "StreamClient":
        """Create a StreamClient from a StreamDescriptor.

        This is the primary public entry point for descriptor-based construction.
        Used internally by StreamRef.open() and directly by advanced callers.

        Consumer gating policy: when local_direction includes writing and gating
        is not explicitly set, defaults to gating=False.

        ``consumer_user_id`` sets the UUID prefix for the consumer side of a
        bidirectional stream. When provided, it replaces descriptor.agent_name
        as the prefix so that provider and consumer UUIDs never collide even
        when their per-process counters are in sync. An empty string falls back
        to descriptor.agent_name (truthiness check).
        """
        local_dir = descriptor.local_direction
        can_write = local_dir in ("outbound", "bidirectional")
        resolved_gating = gating if gating is not None else (False if can_write else True)

        return cls(
            subscribe_key=subscribe_key,
            publish_key=publish_key,
            token=descriptor.token,
            agent_name=consumer_user_id or descriptor.agent_name,
            stream_id=descriptor.stream_id,
            channel=descriptor.channel,
            format=descriptor.format,
            direction=descriptor.local_direction,
            max_message_size=max_message_size,
            bundle_size_bytes=bundle_size_bytes,
            max_latency_ms=max_latency_ms,
            gating=resolved_gating,
            reorder_timeout_ms=reorder_timeout_ms,
            affinity=descriptor.affinity,
        )
