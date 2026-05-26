"""
TaskSession - Consumer-side task session with eager subscription.

Replaces SendMessageResult. Owns one task's channel subscription,
parsed task events, discovered streams, and cleanup. Auto-closes
on terminal event.
"""

from __future__ import annotations

import logging
import threading
from collections import OrderedDict
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional

from .stream import StreamClient, StreamDescriptor, invert_direction

from .channel_manager import task_channel
from .rpc_client import call_rpc
from .stream_ref import StreamRef
from .types import ArtifactRef

logger = logging.getLogger(__name__)


class TaskEvent:
    """A parsed task event from the task channel."""

    def __init__(self, data: Dict[str, Any]) -> None:
        self._data = data

    @property
    def type(self) -> str:
        return self._data.get("type", "")

    @property
    def task_id(self) -> str:
        return self._data.get("taskId", "")

    def get(self, key: str, default: Any = None) -> Any:
        return self._data.get(key, default)

    def __getitem__(self, key: str) -> Any:
        return self._data[key]

    def __contains__(self, key: str) -> bool:
        return key in self._data

    @property
    def message(self) -> Optional[str]:
        """Progress message text (progress events)."""
        return self._data.get("message")

    @property
    def progress(self) -> Optional[float]:
        """Numeric progress value (progress events)."""
        return self._data.get("progress")

    @property
    def state(self) -> Optional[str]:
        """Terminal state string (terminal events)."""
        return self._data.get("state")

    @property
    def artifact_ref(self) -> Optional["ArtifactRef"]:
        """Typed ArtifactRef (artifact events).

        Returns an :class:`ArtifactRef` instance when the raw data
        contains an ``artifactRef`` dict, or ``None`` otherwise.
        """
        raw = self._data.get("artifactRef")
        if raw is not None and isinstance(raw, dict):
            return ArtifactRef.from_dict(raw)
        if isinstance(raw, ArtifactRef):
            return raw
        return None

    @property
    def raw(self) -> Dict[str, Any]:
        """The raw event dict."""
        return self._data


# Unsubscribe callback type
Unsubscribe = Callable[[], None]


TERMINAL_STATES = frozenset({"completed", "failed", "canceled"})


@dataclass
class CallbackErrorContext:
    """Context passed to on_error handlers when a consumer callback raises."""

    entry_point: str  # 'taskSession' | 'subscribeToTask'
    callback_type: str  # 'onProgress' | 'onArtifact' | 'onTerminal' | 'onSystem' | 'onEvent' | 'onStream' | 'streamPredicate'
    event: Any = None  # TaskEvent | StreamRef | dict


class TaskSession:
    """Consumer-side task session with eager subscription.

    Subscribes to the task channel using T4 and parses stream_started
    events into StreamRef objects with consumer-local direction.

    Provides event callbacks, stream discovery helpers, and auto-close
    on terminal event.

    When ``pre_closed_state`` is set to a terminal state string, the
    session starts already closed with no PubNub subscription. This is
    used for terminal idempotent hits where the task has already
    reached a final state and no further events will arrive.
    """

    # Maximum number of PubNub timetokens retained for replay dedup.
    # Parity with Node's TaskSession.SEEN_TIMETOKENS_MAX.
    _SEEN_TIMETOKENS_MAX = 200

    def __init__(
        self,
        *,
        task_id: str,
        owner_id: str,
        read_token: Optional[str],
        status_channel: Optional[str] = None,
        agent_name: str,
        pubnub: Any = None,
        owns_subscribe_client: bool = False,
        sdk_options: Dict[str, Any],
        rpc_config: Optional[Dict[str, Any]] = None,
        idempotent: Optional[bool] = None,
        queued: Optional[bool] = None,
        push_config_id: Optional[str] = None,
        auto_drain: bool = True,
        drain_window_s: Optional[float] = None,
        pre_closed_state: Optional[str] = None,
        # P1-2: connect() support
        state: Optional[str] = None,
        skip_subscription: bool = False,
        preloaded_streams: Optional[Dict[str, StreamRef]] = None,
        preloaded_artifacts: Optional[List[ArtifactRef]] = None,
        preloaded_events: Optional[List[Dict[str, Any]]] = None,
        external_subscription: Optional[Dict[str, Any]] = None,
    ) -> None:
        self._task_id = task_id
        self._owner_id = owner_id
        self._read_token = read_token
        self._agent_name = agent_name
        self._pubnub = pubnub
        self._owns_subscribe_client = owns_subscribe_client
        self._sdk_options = {**sdk_options, "consumer_user_id": owner_id}
        self._rpc_config = rpc_config
        self._status_channel = status_channel or task_channel(task_id, owner_id)

        # RPC response metadata from send_message()
        self._idempotent = idempotent
        self._queued = queued
        self._push_config_id = push_config_id

        # Pre-closed terminal state (for terminal idempotent hits)
        self._pre_closed_state = pre_closed_state

        # Explicit state (for connect() sessions)
        self._state = state

        # Skip-subscription mode (terminal connect() sessions)
        self._skip_subscription = skip_subscription

        # Auto-drain state
        self._auto_drain = auto_drain
        self._terminal_received = False
        self._drain_timer: Optional[threading.Timer] = None
        # Default drain window raised from 2.0s to 30.0s to give already-open
        # streams enough time to finish draining naturally after a terminal
        # event. Only applies to streams opened while the task was still
        # active; unopened streams on a terminal session raise
        # StreamUnavailableError per the merged t7c baseline.
        self._drain_window_s = drain_window_s if drain_window_s is not None else 30.0
        self._open_stream_clients: set = set()

        # Dedup: bounded seen-timetoken set to suppress duplicate dispatch when
        # PubNub's cache replay overlaps live delivery (SDK_CONTRACT §10.4.1a).
        # Uses OrderedDict for guaranteed FIFO eviction -- a plain set's
        # iteration order is an implementation detail and not a public
        # contract across Python versions.
        self._seen_timetokens: "OrderedDict[str, bool]" = OrderedDict()

        # Event callbacks
        self._progress_cbs: List[Callable[[TaskEvent], None]] = []
        self._artifact_cbs: List[Callable[[TaskEvent], None]] = []
        self._terminal_cbs: List[Callable[[TaskEvent], None]] = []
        self._event_cbs: List[Callable[[TaskEvent], None]] = []
        self._stream_cbs: List[Callable[[StreamRef], None]] = []

        # P1-3: Error callbacks
        self._error_cbs: List[Callable[[Exception, CallbackErrorContext], None]] = []

        # Stream tracking
        self._streams: Dict[str, StreamRef] = {}

        # Artifact tracking (P1-2: accumulated from history + live events)
        self._artifacts: List[ArtifactRef] = []

        # History event tracking for connect(). Live events arrive through callbacks.
        self._history_events: List[TaskEvent] = []
        self._history_timetokens: set = set()

        # Waiters for stream discovery
        self._waiter_lock = threading.Lock()
        self._stream_waiters: List[Dict[str, Any]] = []
        self._artifact_lock = threading.RLock()

        self._listener: Any = None

        # Pre-populate from connect() history.
        # Re-wrap preloaded StreamRefs with on_open hooks so auto-drain
        # tracks clients opened from preloaded streams the same way it
        # tracks clients opened from live-discovered streams.
        if preloaded_streams:
            for sid, ref in preloaded_streams.items():
                hooked = StreamRef(
                    ref.descriptor, self._sdk_options,
                    on_open=self._make_stream_on_open(),
                    session_state=lambda: self._state,
                )
                self._streams[sid] = hooked
        if preloaded_artifacts:
            self._artifacts.extend(preloaded_artifacts)
        if preloaded_events:
            for raw in preloaded_events:
                self._history_events.append(TaskEvent(raw))

        if pre_closed_state is not None:
            # Pre-closed session: no subscription, already terminal
            self._closed = True
        elif skip_subscription:
            # Terminal connect() session: hold client, skip subscribe
            self._closed = False
        elif external_subscription is not None:
            # Active connect() session: subscription already active
            self._closed = False
            ext = external_subscription
            self._listener = ext.get("listener")
            self._status_channel = ext.get("channel", self._status_channel)
            # Hand dispatch ref back to connect() via on_ready
            on_ready = ext.get("on_ready")
            if on_ready is not None:
                on_ready(self._handle_event)
        else:
            self._closed = False
            self._setup_subscription()

    def __enter__(self) -> "TaskSession":
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    @property
    def task_id(self) -> str:
        return self._task_id

    @property
    def owner_id(self) -> str:
        return self._owner_id

    @property
    def read_token(self) -> Optional[str]:
        return self._read_token

    @property
    def status_channel(self) -> str:
        return self._status_channel

    @property
    def idempotent(self) -> Optional[bool]:
        """Whether the task submission was idempotent (RPC metadata)."""
        return self._idempotent

    @property
    def queued(self) -> Optional[bool]:
        """Whether the task was queued (RPC metadata)."""
        return self._queued

    @property
    def push_config_id(self) -> Optional[str]:
        """Push config ID from task submission (RPC metadata)."""
        return self._push_config_id

    @property
    def state(self) -> Optional[str]:
        """Task state. For connect() sessions, reflects status at connect time.
        For pre-closed sessions, reflects the terminal state."""
        return self._state or self._pre_closed_state

    @property
    def is_closed(self) -> bool:
        return self._closed

    def _setup_subscription(self) -> None:
        channel = self._status_channel
        session = self

        from pubnub.callbacks import SubscribeCallback

        class _Listener(SubscribeCallback):
            def status(self, pubnub: Any, status: Any) -> None:
                pass

            def presence(self, pubnub: Any, presence: Any) -> None:
                pass

            def message(self, pubnub: Any, event: Any) -> None:
                evt_channel = getattr(event, "channel", None)
                if evt_channel != channel:
                    return
                msg = getattr(event, "message", None)
                if not isinstance(msg, dict) or "type" not in msg:
                    return
                tt = getattr(event, "timetoken", None)
                session._handle_event(msg, str(tt) if tt is not None else None)

        self._listener = _Listener()
        self._pubnub.add_listener(self._listener)
        # with_timetoken(1000) asks PubNub to replay everything still in the
        # channel's in-memory cache (per SDK_CONTRACT §10.4.1a). Using 0
        # would mean "initial subscribe, no catch-up" and leaves the
        # publish-before-subscribe race unfixed.
        self._pubnub.subscribe().channels([channel]).with_timetoken(1000).execute()

    def _route_callback_error(
        self, error: Exception, callback_type: str, event: Any
    ) -> None:
        """Route a callback error to on_error handlers or warn log."""
        ctx = CallbackErrorContext(
            entry_point="taskSession",
            callback_type=callback_type,
            event=event,
        )
        if self._error_cbs:
            for ecb in list(self._error_cbs):
                try:
                    ecb(error, ctx)
                except Exception:
                    pass  # prevent infinite loop
        else:
            logger.warning(
                "[TaskSession] callback error in %s: %s",
                callback_type, error,
            )

    def _handle_event(
        self,
        raw: Dict[str, Any],
        timetoken: Optional[str] = None,
    ) -> None:
        if self._closed:
            return

        # Dedup: cache replay + live delivery can surface the same message
        # twice. Drop repeats by PubNub timetoken before any dispatch.
        # Bounded to the last _SEEN_TIMETOKENS_MAX entries to cap memory.
        # Events without a timetoken (e.g. synthetic test fixtures, pre-existing
        # call sites that don't thread it) bypass dedup and dispatch unchanged.
        if timetoken is not None:
            if timetoken in self._seen_timetokens:
                return
            self._seen_timetokens[timetoken] = True
            if len(self._seen_timetokens) > self._SEEN_TIMETOKENS_MAX:
                # OrderedDict.popitem(last=False) is FIFO eviction (oldest).
                self._seen_timetokens.popitem(last=False)

        event = TaskEvent(raw)

        # Catch-all
        for cb in list(self._event_cbs):
            try:
                cb(event)
            except Exception as err:
                self._route_callback_error(err, "onEvent", event)

        # Typed dispatch
        if event.type == "progress":
            for cb in list(self._progress_cbs):
                try:
                    cb(event)
                except Exception as err:
                    self._route_callback_error(err, "onProgress", event)
            # Check for stream_started
            if raw.get("streamEvent") == "stream_started" and raw.get("streams"):
                self._handle_stream_started(raw)

        elif event.type == "artifact":
            with self._artifact_lock:
                artifact_data = raw.get("artifactRef")
                if isinstance(artifact_data, dict):
                    self._artifacts.append(ArtifactRef.from_dict(artifact_data))
                artifact_cbs = list(self._artifact_cbs)

            for cb in artifact_cbs:
                try:
                    cb(event)
                except Exception as err:
                    self._route_callback_error(err, "onArtifact", event)

        elif event.type == "terminal":
            # Update session state FIRST so callbacks (and any ref.open() calls
            # they make) see the terminal state, not the stale 'running' value.
            self._state = event.state
            for cb in list(self._terminal_cbs):
                try:
                    cb(event)
                except Exception as err:
                    self._route_callback_error(err, "onTerminal", event)
            if self._auto_drain:
                self._start_auto_drain()
            else:
                self.close()

    def _make_stream_on_open(self) -> Callable:
        """Create an on_open hook that registers a stream client for auto-drain tracking."""
        def _on_open(client: StreamClient) -> None:
            self._open_stream_clients.add(client)

            def _on_inbound_done() -> None:
                self._open_stream_clients.discard(client)
                if client.is_active:
                    try:
                        client.end()
                    except Exception:
                        pass  # cleanup exception, stay silent
                if self._terminal_received and len(self._open_stream_clients) == 0:
                    if self._drain_timer is not None:
                        self._drain_timer.cancel()
                        self._drain_timer = None
                    self.close()

            client.on_inbound_done(_on_inbound_done)
        return _on_open

    def _handle_stream_started(self, raw: Dict[str, Any]) -> None:
        streams_map = raw.get("streams")
        if not isinstance(streams_map, dict):
            return

        # declaredStream is a top-level field on the stream_started event
        declared_stream_key = raw.get("declaredStream")

        for stream_id, entry in streams_map.items():
            if not isinstance(entry, dict):
                continue
            if stream_id in self._streams:
                continue

            agent_direction = entry.get("direction", "outbound")
            local_direction = invert_direction(agent_direction)
            fmt = entry.get("format")

            if fmt not in ("bytes", "events"):
                continue

            affinity = entry.get("affinity")
            if affinity not in ("dedicated", "shared"):
                # affinity became schema-required in 4.7.0. Silent drop
                # would leave a consumer missing a stream with no log.
                # Warn loudly so a malformed live event is diagnosable.
                logger.warning(
                    'live stream_started: dropping stream "%s" for task "%s" -- '
                    "invalid or missing affinity (got %r)",
                    stream_id, self._task_id, affinity,
                )
                continue

            descriptor = StreamDescriptor(
                task_id=self._task_id,
                stream_id=stream_id,
                agent_name=self._agent_name,
                channel=entry.get("channel", ""),
                token=entry.get("token", ""),
                agent_direction=agent_direction,
                local_direction=local_direction,
                format=fmt,
                affinity=affinity,
                metadata=entry.get("metadata"),
                declared_stream=declared_stream_key if isinstance(declared_stream_key, str) else None,
            )

            ref = StreamRef(
                descriptor,
                self._sdk_options,
                on_open=self._make_stream_on_open(),
                session_state=lambda: self._state,
            )
            self._streams[stream_id] = ref

            # Notify stream callbacks
            for cb in list(self._stream_cbs):
                try:
                    cb(ref)
                except Exception as err:
                    self._route_callback_error(err, "onStream", ref)

            # Resolve matching waiters
            self._resolve_waiters(ref)

    def _start_auto_drain(self) -> None:
        if self._closed:
            return
        self._terminal_received = True

        if len(self._open_stream_clients) == 0:
            self.close()
            return

        def _drain_timeout() -> None:
            self._drain_timer = None
            for client in list(self._open_stream_clients):
                if client.is_active:
                    try:
                        client.end()
                    except Exception:
                        pass  # cleanup exception, stay silent
            self.close()

        self._drain_timer = threading.Timer(self._drain_window_s, _drain_timeout)
        self._drain_timer.daemon = True
        self._drain_timer.start()

    def _resolve_waiters(self, ref: StreamRef) -> None:
        with self._waiter_lock:
            remaining: List[Dict[str, Any]] = []
            for waiter in self._stream_waiters:
                matched = False
                stream_id = waiter.get("stream_id")
                predicate = waiter.get("predicate")

                if stream_id is not None:
                    matched = (
                        ref.descriptor.declared_stream == stream_id
                        or ref.descriptor.stream_id == stream_id
                    )
                elif predicate is not None:
                    try:
                        matched = predicate(ref)
                    except Exception as err:
                        self._route_callback_error(err, "streamPredicate", ref)
                else:
                    # No stream_id and no predicate: match any
                    matched = True

                if matched:
                    waiter["event"].set()
                    waiter["result"] = ref
                else:
                    remaining.append(waiter)

            self._stream_waiters = remaining

    # -- Public event subscription API --

    def on_progress(self, cb: Callable[[TaskEvent], None]) -> Unsubscribe:
        self._progress_cbs.append(cb)
        return lambda: self._progress_cbs.remove(cb) if cb in self._progress_cbs else None

    def on_artifact(self, cb: Callable[[TaskEvent], None]) -> Unsubscribe:
        with self._artifact_lock:
            self._artifact_cbs.append(cb)
            snapshot = list(self._artifacts)
            for ref in snapshot:
                event = TaskEvent({
                    "type": "artifact",
                    "taskId": self._task_id,
                    "artifactRef": ref.to_dict(),
                })
                try:
                    cb(event)
                except Exception as err:
                    self._route_callback_error(err, "onArtifact", event)

        def _unsubscribe() -> None:
            with self._artifact_lock:
                if cb in self._artifact_cbs:
                    self._artifact_cbs.remove(cb)

        return _unsubscribe

    def on_terminal(self, cb: Callable[[TaskEvent], None]) -> Unsubscribe:
        self._terminal_cbs.append(cb)
        terminal_state = self._state or self._pre_closed_state
        if terminal_state and terminal_state in TERMINAL_STATES:
            event = TaskEvent({
                "type": "terminal",
                "taskId": self._task_id,
                "state": terminal_state,
            })
            try:
                cb(event)
            except Exception as err:
                self._route_callback_error(err, "onTerminal", event)
        return lambda: self._terminal_cbs.remove(cb) if cb in self._terminal_cbs else None

    def wait_for_terminal(self, timeout: float = 60) -> TaskEvent:
        """Block until a terminal event arrives and return it.

        This is a convenience wrapper around :meth:`on_terminal` for the
        common "submit and wait for result" consumer pattern::

            session = client.send_message(agent_name="echo", ...)
            terminal = session.wait_for_terminal(timeout=30)
            print(terminal.raw["state"])  # "completed" | "failed" | ...

        For already-terminal sessions (pre-closed idempotent hits or
        terminal connect() sessions), resolves immediately with a
        synthetic terminal event.

        Raises ``TimeoutError`` if no terminal event arrives within
        *timeout* seconds.
        """
        # Resolve immediately for already-terminal sessions
        terminal_state = self._pre_closed_state or self._state
        if terminal_state and terminal_state in TERMINAL_STATES:
            return TaskEvent({
                "type": "terminal",
                "taskId": self._task_id,
                "state": terminal_state,
            })

        import threading

        evt = threading.Event()
        result: List[Optional[TaskEvent]] = [None]

        def _cb(event: TaskEvent) -> None:
            result[0] = event
            evt.set()

        unsub = self.on_terminal(_cb)
        try:
            if not evt.wait(timeout=timeout):
                raise TimeoutError(
                    f"Timed out waiting for terminal event ({timeout}s)"
                )
            return result[0]  # type: ignore[return-value]
        finally:
            unsub()

    def on_event(self, cb: Callable[[TaskEvent], None]) -> Unsubscribe:
        self._event_cbs.append(cb)
        return lambda: self._event_cbs.remove(cb) if cb in self._event_cbs else None

    def on_error(
        self, cb: Callable[[Exception, CallbackErrorContext], None]
    ) -> Unsubscribe:
        """Register a handler for callback errors.

        When a consumer callback raises, the error is routed to all
        registered on_error handlers. If no handlers are registered,
        the error is logged at WARNING level.
        """
        self._error_cbs.append(cb)
        return lambda: self._error_cbs.remove(cb) if cb in self._error_cbs else None

    # -- Stream discovery API --

    def on_stream(self, cb: Callable[[StreamRef], None]) -> Unsubscribe:
        self._stream_cbs.append(cb)
        # Fire for already-known streams
        for ref in self._streams.values():
            try:
                cb(ref)
            except Exception as err:
                self._route_callback_error(err, "onStream", ref)
        return lambda: self._stream_cbs.remove(cb) if cb in self._stream_cbs else None

    def list_streams(self) -> List[StreamRef]:
        return list(self._streams.values())

    def open_all_streams(
        self,
        *,
        reorder_timeout_ms: Optional[int] = None,
    ) -> List[StreamClient]:
        """Open every readable stream known to this session synchronously.

        Returns a list of :class:`StreamClient`\\ s in insertion order
        (matching :meth:`list_streams`). Outbound-only streams are
        skipped; streams that fail to open (already ended, terminal
        session, etc.) are skipped silently. Calling twice returns the
        same client objects for already-opened streams via
        :meth:`StreamRef.open` idempotence.

        This is an active-session convenience. Under the merged t7c
        baseline, :meth:`StreamRef.open` raises
        :class:`StreamUnavailableError` for never-opened streams on a
        terminal session -- call this method while the task is still
        running if the goal is to observe every stream.
        """
        clients: List[StreamClient] = []
        for ref in self._streams.values():
            direction = ref.descriptor.local_direction
            if direction not in ("inbound", "bidirectional"):
                continue
            try:
                clients.append(ref.open(reorder_timeout_ms=reorder_timeout_ms))
            except Exception:
                # Terminal session, already-ended ref, or other open
                # failure: skip silently. Callers can inspect
                # list_streams() to see which refs exist and branch on
                # ref.is_open / session.state for richer diagnostics.
                continue
        return clients

    def list_artifacts(self) -> List[ArtifactRef]:
        """Return all artifact refs seen so far (from history and live events)."""
        return list(self._artifacts)

    def list_events(self) -> List[TaskEvent]:
        """Return all history events from connect() in arrival order."""
        return list(self._history_events)

    def _append_history_event(self, raw: Dict[str, Any], timetoken: Optional[str] = None) -> None:
        """Called by connect() to append buffer-drain events to the history snapshot."""
        if timetoken:
            if timetoken in self._history_timetokens:
                return
            self._history_timetokens.add(timetoken)
        self._history_events.append(TaskEvent(raw))

    def wait_for_stream(
        self,
        stream_id: Optional[str] = None,
        timeout: Optional[float] = None,
    ) -> StreamRef:
        """Wait for a stream to be announced.

        If stream_id is given, waits for that specific stream.
        If stream_id is None and exactly one stream exists, returns it.
        If stream_id is None and multiple streams exist, raises.

        This is a blocking call (Python is synchronous). Returns the
        StreamRef when the stream is discovered.

        Raises RuntimeError if the session is closed before the stream
        is discovered, or if timeout expires.
        """
        if self._closed:
            raise RuntimeError("TaskSession is closed")

        # Check already-known streams (by declared_stream first, then stream_id)
        if stream_id is not None:
            # Check declared_stream match first
            for ref in self._streams.values():
                if ref.descriptor.declared_stream == stream_id:
                    return ref
            # Fall back to runtime stream_id key
            existing = self._streams.get(stream_id)
            if existing is not None:
                return existing
        else:
            if len(self._streams) == 1:
                return next(iter(self._streams.values()))
            if len(self._streams) > 1:
                raise RuntimeError(
                    f"Multiple streams exist ({len(self._streams)}). "
                    f"Use wait_for_stream(stream_id) or "
                    f"wait_for_stream_where(predicate) to select one."
                )

        # In skip_subscription mode, no live events will arrive
        if self._skip_subscription:
            raise RuntimeError(
                "No matching stream found. This is a terminal task session "
                "with no live subscription -- no future stream "
                "announcements will arrive."
            )

        # Wait for future stream announcement
        evt = threading.Event()
        waiter: Dict[str, Any] = {
            "event": evt,
            "stream_id": stream_id,
            "predicate": None,
            "result": None,
        }
        with self._waiter_lock:
            self._stream_waiters.append(waiter)

        if not evt.wait(timeout=timeout):
            # Timeout or session closed
            with self._waiter_lock:
                if waiter in self._stream_waiters:
                    self._stream_waiters.remove(waiter)
            if self._closed:
                raise RuntimeError("TaskSession closed")
            raise TimeoutError("Timed out waiting for stream")

        result = waiter.get("result")
        if result is None:
            raise RuntimeError("TaskSession closed")
        return result

    def wait_for_stream_where(
        self,
        predicate: Callable[[StreamRef], bool],
        timeout: Optional[float] = None,
    ) -> StreamRef:
        """Wait for a stream matching a predicate.

        This is a blocking call. Returns the first StreamRef matching
        the predicate.
        """
        if self._closed:
            raise RuntimeError("TaskSession is closed")

        # Check already-known streams
        for ref in self._streams.values():
            try:
                if predicate(ref):
                    return ref
            except Exception as err:
                self._route_callback_error(err, "streamPredicate", ref)

        # In skip_subscription mode, no live events will arrive
        if self._skip_subscription:
            raise RuntimeError(
                "No matching stream found. This is a terminal task session "
                "with no live subscription -- no future stream "
                "announcements will arrive."
            )

        # Wait for future stream announcement
        evt = threading.Event()
        waiter: Dict[str, Any] = {
            "event": evt,
            "stream_id": None,
            "predicate": predicate,
            "result": None,
        }
        with self._waiter_lock:
            self._stream_waiters.append(waiter)

        if not evt.wait(timeout=timeout):
            with self._waiter_lock:
                if waiter in self._stream_waiters:
                    self._stream_waiters.remove(waiter)
            if self._closed:
                raise RuntimeError("TaskSession closed")
            raise TimeoutError("Timed out waiting for matching stream")

        result = waiter.get("result")
        if result is None:
            raise RuntimeError("TaskSession closed")
        return result

    # -- Artifact download --

    def download_artifact(self, ref: ArtifactRef) -> "DownloadedArtifact":
        """Download an artifact using this session's PubNub client.

        For pre-closed sessions (no PubNub client), lazily creates a
        temporary read-capable client from stored token and keys, then
        destroys it after the download.
        """
        from .artifacts import download_artifact, DownloadedArtifact

        if self._pubnub is not None:
            return download_artifact(ref, self._pubnub)

        # No active client -- create a temporary one
        from .pubnub_client import create_pubnub_client
        import uuid

        temp_pn = create_pubnub_client(
            subscribe_key=self._sdk_options.get("subscribe_key", ""),
            publish_key=self._sdk_options.get("publish_key"),
            user_id=f"blocks-dl-{uuid.uuid4().hex[:12]}",
            subscribe_retry_unbounded=False,
        )
        if self._read_token:
            temp_pn.set_token(self._read_token)
        try:
            return download_artifact(ref, temp_pn)
        finally:
            try:
                temp_pn.stop()
            except Exception:
                pass  # cleanup exception, stay silent

    # -- Artifact save helper --

    def save_artifacts(self, directory: str) -> List[str]:
        """Download all artifacts and save them to *directory*.

        Creates the directory (and parents) if it does not exist.
        Returns the list of written file paths.
        """
        from pathlib import Path

        dir_path = Path(directory).resolve()
        dir_path.mkdir(parents=True, exist_ok=True)
        paths: List[str] = []
        for i, ref in enumerate(self.list_artifacts()):
            downloaded = self.download_artifact(ref)
            raw_name = downloaded.file_name or f"artifact-{i}"
            # Sanitize: use only the final path component to prevent traversal
            safe_name = Path(raw_name).name or f"artifact-{i}"
            file_path = (dir_path / safe_name).resolve()
            if not str(file_path).startswith(str(dir_path) + "/") and file_path != dir_path:
                raise ValueError(
                    f"Artifact filename {raw_name!r} resolves outside target directory"
                )
            file_path.write_bytes(downloaded.data)
            paths.append(str(file_path))
        return paths

    # -- Task lifecycle --

    def cancel(self) -> None:
        """Cancel this task via JSON-RPC ``CancelTask``."""
        if not self._rpc_config:
            raise RuntimeError(
                "TaskSession was not created with RPC config; "
                "use TaskClient.cancel_task() directly"
            )
        call_rpc(
            self._rpc_config["subscribe_key"],
            "CancelTask",
            {"taskId": self._task_id},
            base_url=self._rpc_config.get("base_url"),
            agent_auth=self._rpc_config.get("agent_auth"),
            auth_provider=self._rpc_config.get("auth_provider"),
        )

    def terminate(self) -> None:
        """Terminate this task via JSON-RPC ``TerminateTask``."""
        if not self._rpc_config:
            raise RuntimeError(
                "TaskSession was not created with RPC config; "
                "use TaskClient.terminate_task() directly"
            )
        call_rpc(
            self._rpc_config["subscribe_key"],
            "TerminateTask",
            {"taskId": self._task_id},
            base_url=self._rpc_config.get("base_url"),
            agent_auth=self._rpc_config.get("agent_auth"),
            auth_provider=self._rpc_config.get("auth_provider"),
        )

    # -- Cleanup --

    def close(self) -> None:
        """Close the session: end stream clients, unsubscribe, reject waiters, release resources.

        Idempotent. Ends all open StreamClient instances before cleaning
        up the task channel subscription.
        """
        if self._closed:
            return
        self._closed = True

        # End all open stream clients
        for client in list(self._open_stream_clients):
            if client.is_active:
                try:
                    client.end()
                except Exception:
                    pass  # cleanup exception, stay silent
        self._open_stream_clients.clear()

        # Cancel pending drain timer
        if self._drain_timer is not None:
            self._drain_timer.cancel()
            self._drain_timer = None

        # Reject all pending waiters
        with self._waiter_lock:
            for waiter in self._stream_waiters:
                waiter["event"].set()
                # result stays None -- callers will get RuntimeError
            self._stream_waiters.clear()

        # Unsubscribe from task channel (skip if no PubNub, e.g. pre-closed,
        # or if skip_subscription was set — we never subscribed)
        if self._pubnub is not None:
            if self._listener is not None:
                self._pubnub.remove_listener(self._listener)
                self._listener = None
            if not self._skip_subscription:
                try:
                    self._pubnub.unsubscribe().channels([self._status_channel]).execute()
                except Exception:
                    pass  # channel may not have been subscribed

            # Destroy session-owned PubNub client
            if self._owns_subscribe_client:
                try:
                    self._pubnub.stop()
                except Exception:
                    pass  # cleanup exception, stay silent

        # Clear callbacks
        self._progress_cbs.clear()
        self._artifact_cbs.clear()
        self._terminal_cbs.clear()
        self._event_cbs.clear()
        self._stream_cbs.clear()
        self._error_cbs.clear()
