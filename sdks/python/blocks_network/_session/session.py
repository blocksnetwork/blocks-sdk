"""The consumer's session: one channel with one agent, carrying many exchanges."""

from __future__ import annotations

import logging
import math
import queue
import threading
import time
import uuid
from concurrent.futures import Future
from concurrent.futures import TimeoutError as FutureTimeout
from dataclasses import dataclass
from typing import Any, Callable, Dict, List, Optional, Sequence, Tuple, Union, cast

from ..protocol_version import CURRENT_PROTOCOL_VERSION
from ..task_client import SendMessageRequestPart
from . import wire
from ._callbacks import invoke
from .consumer_receiver import (
    ConsumerReceiver,
    ExchangeArtifact,
    ExchangeOutcome,
    ExchangeProgress,
)
from .consumer_token import ConsumerTokenRotation
from .counterpart import consumer_counterparts
from .errors import SessionBackpressureError, SessionError, SessionFailedError
from .futures import spawn
from .http import BEACON_CLOSED, BEACON_OPENED, OpenedSession, SessionHttp, SessionSnapshot
from .scheduler import Scheduler

logger = logging.getLogger(__name__)

# In flight plus queued, per session.
MAX_PENDING_EXCHANGES = 8
# How long close() waits for the counterpart's session_closed before ending locally.
SESSION_CLOSE_TIMEOUT_S = 5.0
SESSION_READY_TIMEOUT_S = 30.0
# The backend cancels a session still unready after 5 minutes, so a wait must end well before.
MAX_SESSION_READY_TIMEOUT_S = 120.0
HISTORY_PAGE = 100
# A recovery replays at most this many pages; the snapshot then settles what history did not hold.
HISTORY_MAX_PAGES = 10
# For all pages together; replay gives up and replays nothing, so the snapshot settles alone.
HISTORY_TIMEOUT_S = 10.0

NOT_READY = "session_not_ready"
ACCESS_REVOKED = "session_access_revoked"
PUBLISH_FAILED = "publish_failed"

SessionInput = Union[str, Sequence[Union[SendMessageRequestPart, Dict[str, Any]]]]
Unsubscribe = Callable[[], None]

# Set while this thread runs an application callback; see Session.close().
_callback_context = threading.local()


def _in_callback() -> bool:
    return getattr(_callback_context, "depth", 0) > 0


def _invoke_app(callback: Callable[[Any], None], value: Any) -> None:
    _callback_context.depth = getattr(_callback_context, "depth", 0) + 1
    try:
        invoke(callback, value)
    finally:
        _callback_context.depth -= 1


def _wire_part(part: Any) -> Dict[str, Any]:
    if isinstance(part, SendMessageRequestPart):
        if part.file is not None:
            raise TypeError("file parts are not supported on sessions yet")
        wire_part: Dict[str, Any] = {}
        if part.part_id is not None:
            wire_part["partId"] = part.part_id
        if part.text is not None:
            wire_part["text"] = part.text
        if part.content_type is not None:
            wire_part["contentType"] = part.content_type
        return wire_part
    if isinstance(part, dict):
        if part.get("file") is not None:
            raise TypeError("file parts are not supported on sessions yet")
        return dict(part)
    raise TypeError("session input parts must be SendMessageRequestPart or dict")


def to_request_parts(value: SessionInput) -> List[Dict[str, Any]]:
    """A string is sent as one text part."""
    if isinstance(value, str):
        return [{"partId": "text", "text": value}]
    if not isinstance(value, (list, tuple)):
        raise TypeError("session input must be a string or a list of parts")
    return [_wire_part(part) for part in value]


@dataclass(frozen=True)
class ReadyDeadline:
    """How long an open waits, from its request to ``session_ready``, and when that wait ends."""

    timeout: float
    at: float

    def remaining(self) -> float:
        return max(0.0, self.at - time.monotonic())


def ready_deadline(timeout: Any = SESSION_READY_TIMEOUT_S) -> ReadyDeadline:
    if (
        not isinstance(timeout, (int, float))
        or isinstance(timeout, bool)
        or not math.isfinite(timeout)
        or not 0 < timeout <= MAX_SESSION_READY_TIMEOUT_S
    ):
        raise ValueError(
            "ready_timeout must be a number of seconds greater than 0 and at most "
            f"{MAX_SESSION_READY_TIMEOUT_S:g}"
        )
    return ReadyDeadline(timeout, time.monotonic() + timeout)


def ready_timeout_error(deadline: ReadyDeadline) -> SessionError:
    # The session ends canceled when its ready deadline passes, so the open fails with that code.
    return SessionError("session_canceled", f"session was not ready within {deadline.timeout:g} s")


def _terminal_error(state: str, error: Optional[str]) -> SessionError:
    if state == wire.STATE_FAILED:
        return SessionFailedError(error or "session_failed")
    return SessionError(f"session_{state}", f"Session {state} before it was ready")


def _category(status: Any) -> str:
    category = getattr(status, "category", None)
    name = getattr(category, "name", category)
    return name if isinstance(name, str) else ""


class SessionRecoveryError(RuntimeError):
    """Recovery read neither history nor the snapshot; ``errors`` holds both causes.

    .. warning:: Experimental: sessions are a preview and may change before general availability.
    """

    def __init__(self, history_error: BaseException, snapshot_error: BaseException) -> None:
        super().__init__(
            f"session recovery failed: history ({history_error}) and snapshot "
            f"({snapshot_error}) both unavailable"
        )
        self.errors = [history_error, snapshot_error]


class Exchange:
    """One correlated input inside a session; it ends in exactly one outcome.

    .. warning:: Experimental: sessions are a preview and may change before general availability.
    """

    def __init__(
        self,
        exchange_id: str,
        request_cancel: Callable[[str], "Future[None]"],
        delivery_lock: "threading.RLock",
    ) -> None:
        self.exchange_id = exchange_id
        self._request_cancel = request_cancel
        # Live progress and artifacts are delivered under this lock.
        self._delivery_lock = delivery_lock
        self._lock = threading.Lock()
        self._settled = threading.Event()
        self._outcome: Optional[ExchangeOutcome] = None
        self._artifacts: List[Any] = []
        self._progress_cbs: List[Callable[[ExchangeProgress], None]] = []
        self._artifact_cbs: List[Callable[[Any], None]] = []

    @property
    def settled(self) -> bool:
        return self._settled.is_set()

    def on_progress(self, callback: Callable[[ExchangeProgress], None]) -> Unsubscribe:
        with self._lock:
            self._progress_cbs.append(callback)
        return lambda: self._remove(self._progress_cbs, callback)

    def on_artifact(self, callback: Callable[[Any], None]) -> Unsubscribe:
        """Also replays the artifacts already received, before any that arrive later."""
        with self._delivery_lock:
            with self._lock:
                received = list(self._artifacts)
                self._artifact_cbs.append(callback)
            for ref in received:
                _invoke_app(callback, ref)
        return lambda: self._remove(self._artifact_cbs, callback)

    def wait_for_outcome(self, timeout: Optional[float] = None) -> ExchangeOutcome:
        """Block until the one outcome; raise ``TimeoutError`` only when ``timeout`` passes first."""
        if not self._settled.wait(timeout):
            raise TimeoutError(f"exchange {self.exchange_id} did not settle within {timeout} s")
        return cast(ExchangeOutcome, self._outcome)

    def cancel(self) -> None:
        """Ask the provider to cancel; the outcome is still delivered."""
        if not self.settled:
            self._request_cancel(self.exchange_id).result()

    def _remove(self, callbacks: List[Any], callback: Any) -> None:
        with self._lock:
            if callback in callbacks:
                callbacks.remove(callback)

    def _progress(self, progress: ExchangeProgress) -> None:
        with self._lock:
            callbacks = list(self._progress_cbs)
        for callback in callbacks:
            _invoke_app(callback, progress)

    def _artifact(self, ref: Any) -> None:
        with self._lock:
            self._artifacts.append(ref)
            callbacks = list(self._artifact_cbs)
        for callback in callbacks:
            _invoke_app(callback, ref)

    def _settle(self, outcome: ExchangeOutcome) -> None:
        with self._lock:
            if self._outcome is not None:
                return
            self._outcome = outcome
            self._progress_cbs.clear()
            self._artifact_cbs.clear()
        self._settled.set()


@dataclass(frozen=True)
class SessionTransport:
    pubnub: Any
    http: SessionHttp
    scheduler: Scheduler
    clock: Callable[[], float]


@dataclass(frozen=True)
class SessionLifecycle:
    """Lets the owning client track a session from creation until its connection is released."""

    on_created: Callable[["Session"], None]
    on_released: Callable[["Session"], None]


_STOP = object()


class Session:
    """One session with an agent: many exchanges over one channel.

    Returned by :meth:`BlocksClient.open_session`. Callbacks run on SDK threads; one that
    calls :meth:`close` gets a close that runs in the background.

    .. warning:: Experimental: sessions are a preview, refused with ``SESSIONS_DISABLED``
       until enabled for your org, and may change before general availability.
    """

    def __init__(
        self, opened: OpenedSession, transport: SessionTransport, lifecycle: SessionLifecycle
    ) -> None:
        self._opened = opened
        self._transport = transport
        self._lifecycle = lifecycle
        self._lock = threading.Lock()
        self._changed = threading.Condition(self._lock)
        self._seq = 0
        self._pending: Dict[str, Exchange] = {}
        self._state_cbs: List[Callable[[str], None]] = []
        self._publishes: "queue.Queue[Any]" = queue.Queue()
        self._publisher: Optional[threading.Thread] = None
        self._connected = False
        self._open_published = False
        # Set by close() alone; the state is also closing when the counterpart ends the session.
        self._close_requested = False
        self._closing = False
        self._closed = threading.Event()
        self._released = False
        self._recovering = threading.Lock()
        # The newest timetoken received with no gap open; history replays from here.
        self._last_timetoken: Optional[int] = None
        self._receiver = ConsumerReceiver(
            counterparts=consumer_counterparts(
                session_id=opened.session_id,
                provider_uuid=opened.provider_transport_uuid,
                backend_uuid=opened.backend_transport_uuid,
            ),
            recover=self.recover,
            scheduler=transport.scheduler,
            on_state_change=self._state_changed,
            on_progress=self._on_progress,
            on_artifact=self._on_artifact,
            on_outcome=self._settle,
        )
        self._token = ConsumerTokenRotation(
            session_id=opened.session_id,
            refresh=lambda: transport.http.refresh_token(opened.session_id),
            set_token=transport.pubnub.set_token,
            resume=transport.pubnub.reconnect,
            on_revoked=lambda: self._receiver.terminate(wire.STATE_FAILED, ACCESS_REVOKED),
            scheduler=transport.scheduler,
            clock=transport.clock,
        )
        self._listener = self._make_listener()

    @classmethod
    def _open(
        cls,
        opened: OpenedSession,
        transport: SessionTransport,
        lifecycle: SessionLifecycle,
        *,
        ready: ReadyDeadline,
        first_parts: Optional[List[Dict[str, Any]]] = None,
    ) -> Tuple["Session", Optional[Exchange]]:
        """Subscribe, publish ``session_open`` (carrying ``first_parts`` inline when given),
        and return once the provider reports the session ready."""
        session = cls(opened, transport, lifecycle)
        lifecycle.on_created(session)
        try:
            session._subscribe()
            # session_open goes out only once the subscription is up, so nothing is missed.
            if not session._wait_for(lambda: session._connected or session._terminal(), ready):
                raise ready_timeout_error(ready)
            session._raise_if_terminal()
            exchange = None if first_parts is None else session._track()
            fields: Dict[str, Any] = {"type": wire.SESSION_OPEN}
            if exchange is not None:
                fields["exchange"] = {
                    "exchangeId": exchange.exchange_id,
                    "requestParts": first_parts,
                }
            with session._lock:
                # A close that read _open_published before this tells only the backend.
                if session._close_requested:
                    raise SessionError("session_canceled", "The session was closed while opening")
                published = session._publish_locked(fields)
                # From here a close() queues session_close behind session_open.
                session._open_published = True
            if not session._wait_for(lambda: published.done() or session._terminal(), ready):
                raise ready_timeout_error(ready)
            session._raise_if_terminal()
            published.result()
            session._beacon(BEACON_OPENED)
            if not session._wait_for(lambda: session.state != wire.STATE_OPEN, ready):
                raise ready_timeout_error(ready)
            # session_ready can land while a close (from destroy(), say) publishes session_close.
            if session._close_requested:
                raise SessionError("session_canceled", "The session was closed while opening")
            session._raise_if_terminal()
            return session, exchange
        except BaseException:
            # Not waited for: the deadline bounds the open, and close() is bounded on its own.
            session._close_in_background()
            raise

    @property
    def session_id(self) -> str:
        return self._opened.session_id

    @property
    def state(self) -> str:
        return self._receiver.state

    def send(self, value: SessionInput) -> Exchange:
        """Publish one exchange.

        Raises :class:`SessionBackpressureError` rather than queue beyond the pending bound,
        and ``SessionError('session_not_ready')`` unless the session is ready and not ending.
        """
        parts = to_request_parts(value)
        with self._lock:
            # Ending covers a terminal still handing held artifacts over, whose callbacks see ready.
            ending = self._close_requested or self._receiver.is_ending
            if self.state != wire.STATE_READY or ending:
                shown = wire.STATE_CLOSING if ending and not self._terminal() else self.state
                raise SessionError(NOT_READY, f"Cannot send on a session that is {shown}")
            if len(self._pending) >= MAX_PENDING_EXCHANGES:
                raise SessionBackpressureError(MAX_PENDING_EXCHANGES)
            exchange = self._track_locked()
        if not self._track_in_receiver(exchange):
            return exchange
        with self._lock:
            # A close() since the check above queued session_close first; it settles this
            # exchange canceled, which must not reach the provider after the close.
            if self._close_requested:
                return exchange
            published = self._publish_locked(
                {"type": wire.EXCHANGE, "exchangeId": exchange.exchange_id, "requestParts": parts}
            )
        published.add_done_callback(lambda done: self._on_exchange_published(exchange, done))
        return exchange

    def on_state_change(self, callback: Callable[[str], None]) -> Unsubscribe:
        with self._lock:
            self._state_cbs.append(callback)

        def unsubscribe() -> None:
            with self._lock:
                if callback in self._state_cbs:
                    self._state_cbs.remove(callback)

        return unsubscribe

    def recover(self) -> None:
        """Catch up after a gap or a reconnect.

        Replays the session channel's history through the usual filtering and dedup, then
        applies the backend's record of the session. Runs on its own when a ``seq`` gap
        persists and after the subscription reconnects. Recoveries run one after another.
        Raises :class:`SessionRecoveryError` only when history and the record both fail.
        """
        with self._recovering:
            self._recover_once()

    def _request_close(self) -> None:
        """Mark the close asked for, so an open waiting for ready fails, before close() runs."""
        with self._changed:
            self._close_requested = True
            self._changed.notify_all()

    def close(self) -> None:
        """Idempotent. Returns once the session is terminal and its connection released.

        Called from a session callback, it starts the close and returns at once: waiting
        there would hold the thread that delivers ``session_closed``.
        """
        with self._lock:
            self._close_requested = True
            self._changed.notify_all()
            start = not self._closing
            self._closing = True
        if _in_callback():
            if start:
                spawn(self._run_close, "blocks-session-close")
        elif start:
            self._run_close()
        else:
            self._closed.wait()

    def __enter__(self) -> "Session":
        return self

    def __exit__(self, *_: Any) -> None:
        self.close()

    # -- internals ----------------------------------------------------------------------

    def _terminal(self) -> bool:
        return self._receiver.is_terminal

    def _publishing_stopped(self) -> bool:
        # Ending covers a terminal still handing held artifacts over, while the state reads ready.
        return self._released or self._receiver.is_ending

    def _raise_if_terminal(self) -> None:
        if self._terminal():
            raise _terminal_error(self.state, self._receiver.error)

    def _wait_for(self, predicate: Callable[[], bool], deadline: ReadyDeadline) -> bool:
        with self._changed:
            return self._changed.wait_for(predicate, timeout=deadline.remaining())

    def _notify(self) -> None:
        with self._changed:
            self._changed.notify_all()

    def _close_in_background(self) -> None:
        def run() -> None:
            try:
                self.close()
            except Exception:
                logger.warning("session close after a failed open failed", exc_info=True)

        spawn(run, "blocks-session-close")

    def _run_close(self) -> None:
        with self._lock:
            open_published = self._open_published
        try:
            receiver = self._receiver
            if not receiver.is_terminal and not open_published:
                # The provider never heard of this session; only the backend needs telling.
                receiver.terminate(wire.STATE_CANCELED)
                self._beacon(BEACON_CLOSED)
            elif not receiver.is_terminal:
                if self.state != wire.STATE_OPEN:
                    receiver.begin_closing()
                with self._lock:
                    sent = self._publish_locked({"type": wire.SESSION_CLOSE})
                # Bounded: it queues behind any publish still in flight, which may never finish.
                published = self._succeeds_within(sent, SESSION_CLOSE_TIMEOUT_S)
                # Re-read: session_ready may have arrived while session_close was in flight.
                if self.state == wire.STATE_OPEN:
                    receiver.terminate(wire.STATE_CANCELED)
                else:
                    receiver.begin_closing()
                    if published:
                        with self._changed:
                            self._changed.wait_for(self._terminal, timeout=SESSION_CLOSE_TIMEOUT_S)
                    receiver.terminate(wire.STATE_CLOSED)
                self._beacon(BEACON_CLOSED)
        finally:
            self._release()
            self._closed.set()

    @staticmethod
    def _succeeds_within(sent: "Future[None]", timeout: float) -> bool:
        try:
            sent.result(timeout=timeout)
            return True
        except FutureTimeout:
            return False
        except Exception as err:
            logger.warning("session publish failed: %s", err)
            return False

    def _recover_once(self) -> None:
        if self._released or self._terminal():
            return
        # Taken now: a live message accepted during recovery must not move the replay point
        # past what was missed before recovery began.
        with self._lock:
            replay_from = self._last_timetoken
        history_error: Optional[BaseException] = None
        snapshot_error: Optional[BaseException] = None
        snapshot: Optional[SessionSnapshot] = None
        self._receiver.begin_recovery()
        try:
            try:
                self._replay_history(replay_from)
            except Exception as err:
                history_error = err
            self._receiver.history_replayed()
            try:
                snapshot = self._transport.http.get_session(self._opened.session_id)
            except Exception as err:
                snapshot_error = err
        finally:
            if not self._released:
                self._receiver.finish_recovery(snapshot)
        if history_error is not None and snapshot_error is not None:
            raise SessionRecoveryError(history_error, snapshot_error)
        if history_error is not None:
            logger.warning(
                "session history replay failed; recovered from the snapshot alone: %s",
                history_error,
            )
        if snapshot_error is not None:
            logger.warning(
                "session snapshot read failed; recovered from history alone: %s", snapshot_error
            )

    def _replay_history(self, replay_from: Optional[int]) -> None:
        expired = threading.Event()
        fetching = spawn(
            lambda: self._fetch_history(replay_from, expired), "blocks-session-history"
        )
        try:
            fetched = fetching.result(timeout=HISTORY_TIMEOUT_S)
        except FutureTimeout:
            expired.set()
            raise TimeoutError(
                f"session history did not answer within {HISTORY_TIMEOUT_S:g} s"
            ) from None
        for timetoken, publisher, message in fetched:
            if self._released or self._terminal():
                return
            if publisher is not None:
                self._receive(publisher, message, timetoken, replayed=True)

    def _fetch_history(
        self, replay_from: Optional[int], expired: threading.Event
    ) -> List[Tuple[int, Optional[str], Any]]:
        """Page back from now to the last timetoken seen, oldest first; ``end`` is inclusive and
        dedup drops the repeat. Stops paging once ``expired`` is set."""
        channel = self._opened.session_channel
        fetched: List[Tuple[int, Optional[str], Any]] = []
        start: Optional[int] = None
        for _ in range(HISTORY_MAX_PAGES):
            if expired.is_set():
                break
            builder = (
                self._transport.pubnub.fetch_messages()
                .channels([channel])
                .maximum_per_channel(HISTORY_PAGE)
                # Counterpart filtering needs each entry's publisher.
                .include_uuid(True)
            )
            if replay_from is not None:
                builder = builder.end(replay_from)
            if start is not None:
                builder = builder.start(start)
            result = getattr(builder.sync(), "result", None)
            items = (getattr(result, "channels", None) or {}).get(channel) or []
            page = [
                (int(item.timetoken), getattr(item, "uuid", None), item.message) for item in items
            ]
            fetched.extend(page)
            if len(page) < HISTORY_PAGE:
                break
            start = min(timetoken for timetoken, _, _ in page)
        fetched.sort(key=lambda entry: entry[0])
        return fetched

    def _receive(
        self, publisher: str, message: Any, timetoken: Any, replayed: bool = False
    ) -> None:
        # A dropped message (the consumer's own echo, a stranger, a duplicate) is no evidence
        # that everything before it arrived, and one held behind a gap must not move the replay
        # point past the message it waits for.
        accepted = self._receiver.deliver(publisher, message, replayed)
        if accepted and not self._receiver.has_gap:
            self._note_timetoken(timetoken)

    def _note_timetoken(self, timetoken: Any) -> None:
        try:
            value = int(timetoken)
        except (TypeError, ValueError):
            return
        with self._lock:
            if self._last_timetoken is None or value > self._last_timetoken:
                self._last_timetoken = value

    def _track(self) -> Exchange:
        with self._lock:
            exchange = self._track_locked()
        self._track_in_receiver(exchange)
        return exchange

    def _track_in_receiver(self, exchange: Exchange) -> bool:
        """Track the exchange in the receiver; False when the session ended first.

        A terminal state reached before the receiver tracked the exchange settled every other
        one but not this, so it is settled here the same way.
        """
        self._receiver.track_exchange(exchange.exchange_id)
        if not self._terminal():
            return True
        failed = self.state == wire.STATE_FAILED
        self._settle(
            ExchangeOutcome(
                exchange.exchange_id,
                wire.OUTCOME_FAILED if failed else wire.OUTCOME_CANCELED,
                self._receiver.error if failed else None,
            )
        )
        return False

    def _track_locked(self) -> Exchange:
        exchange = Exchange(
            str(uuid.uuid4()),
            lambda exchange_id: self._publish(
                {"type": wire.EXCHANGE_CANCEL, "exchangeId": exchange_id}
            ),
            self._receiver.delivery_lock,
        )
        self._pending[exchange.exchange_id] = exchange
        return exchange

    def _on_exchange_published(self, exchange: Exchange, done: "Future[None]") -> None:
        err = done.exception()
        if err is None:
            return
        logger.warning("exchange publish failed (exchange %s): %s", exchange.exchange_id, err)
        self._receiver.forget_exchange(exchange.exchange_id)
        self._settle(ExchangeOutcome(exchange.exchange_id, wire.OUTCOME_FAILED, PUBLISH_FAILED))

    def _settle(self, outcome: ExchangeOutcome) -> None:
        with self._lock:
            exchange = self._pending.pop(outcome.exchange_id, None)
        if exchange is not None:
            exchange._settle(outcome)

    def _on_progress(self, progress: ExchangeProgress) -> None:
        exchange = self._pending.get(progress.exchange_id)
        if exchange is not None:
            exchange._progress(progress)

    def _on_artifact(self, artifact: ExchangeArtifact) -> None:
        exchange = self._pending.get(artifact.exchange_id)
        if exchange is not None:
            exchange._artifact(artifact.artifact_ref)

    def _state_changed(self, state: str) -> None:
        self._notify()
        with self._lock:
            callbacks = list(self._state_cbs)
        for callback in callbacks:
            _invoke_app(callback, state)
        if not self._terminal():
            return
        # A terminal session publishes nothing more; close() releases it once it has run.
        with self._lock:
            release = not self._closing
            self._closing = True
        if release:

            def run() -> None:
                self._release()
                self._closed.set()

            spawn(run, "blocks-session-release")

    def _make_listener(self) -> Any:
        from pubnub.callbacks import SubscribeCallback

        session = self

        class _Listener(SubscribeCallback):
            def message(self, _pubnub: Any, event: Any) -> None:
                publisher = getattr(event, "publisher", None)
                if event.channel == session._opened.session_channel and publisher is not None:
                    session._receive(publisher, event.message, getattr(event, "timetoken", None))

            def presence(self, _pubnub: Any, _event: Any) -> None:
                pass

            def status(self, _pubnub: Any, status: Any) -> None:
                session._on_status(_category(status))

        return _Listener()

    def _on_status(self, category: str) -> None:
        if category in ("PNConnectedCategory", "PNReconnectedCategory"):
            self._token.on_connected()
            with self._changed:
                self._connected = True
                self._changed.notify_all()
        # A blip resumes the same subscription; what was published meanwhile comes from history.
        if category == "PNReconnectedCategory":
            spawn(self._recover_after_reconnect, "blocks-session-recover")
        # A denial the one refresh cannot cure fails the session, which fails an open.
        if category == "PNAccessDeniedCategory":
            self._token.on_denied()

    def _recover_after_reconnect(self) -> None:
        try:
            self.recover()
        except Exception as err:
            logger.warning("session recovery after reconnect failed: %s", err)

    def _subscribe(self) -> None:
        """Under the lock, so a release either follows it and tears it down, or precedes it
        and it does nothing."""
        pubnub = self._transport.pubnub
        with self._lock:
            if self._released:
                return
            pubnub.set_token(self._opened.pam_token)
            self._token.start(self._opened.ttl_minutes)
            pubnub.add_listener(self._listener)
            pubnub.subscribe().channels([self._opened.session_channel]).execute()

    def _publish(self, fields: Dict[str, Any]) -> "Future[None]":
        with self._lock:
            return self._publish_locked(fields)

    def _publish_locked(self, fields: Dict[str, Any]) -> "Future[None]":
        """Queue one publish; the caller holds the lock, so seq order is queue order.

        Serialized so the provider receives this sender's seqs in order.
        """
        session_id = self._opened.session_id
        self._seq += 1
        message = {
            **fields,
            "protocolVersion": CURRENT_PROTOCOL_VERSION,
            "sessionId": session_id,
            "seq": self._seq,
            "ts": int(time.time() * 1000),
        }
        future: "Future[None]" = Future()
        if self._publishing_stopped():
            future.set_exception(SessionError(NOT_READY, "The session is no longer open"))
            return future
        self._publishes.put((message, future))
        if self._publisher is None:
            self._publisher = threading.Thread(
                target=self._run_publishes, name="blocks-session-publish", daemon=True
            )
            self._publisher.start()
        return future

    def _run_publishes(self) -> None:
        meta = {"sessionId": self._opened.session_id, "protocolVersion": CURRENT_PROTOCOL_VERSION}
        while True:
            job = self._publishes.get()
            if job is _STOP:
                return
            message, future = job
            # Checked when the publish runs: one queued behind a stalled publish may outlive
            # the session, which publishes nothing more once it starts ending.
            if self._publishing_stopped():
                future.set_exception(SessionError(NOT_READY, "The session is no longer open"))
                continue
            try:
                (
                    self._transport.pubnub.publish()
                    .channel(self._opened.session_channel)
                    .message(message)
                    .meta(meta)
                    .should_store(True)
                    .sync()
                )
            except Exception as err:
                future.set_exception(err)
            else:
                future.set_result(None)
            self._notify()

    def _beacon(self, beacon: str) -> None:
        def send() -> None:
            try:
                self._transport.http.send_beacon(self.session_id, beacon)
            except Exception as err:
                logger.debug("session beacon %s failed: %s", beacon, err)

        spawn(send, "blocks-session-beacon")

    def _release(self) -> None:
        with self._lock:
            if self._released:
                return
            self._released = True
        self._token.stop()
        self._publishes.put(_STOP)
        pubnub = self._transport.pubnub
        for step in (
            lambda: pubnub.remove_listener(self._listener),
            pubnub.unsubscribe_all,
            pubnub.stop,
        ):
            try:
                step()
            except Exception:
                logger.debug("session transport release step failed", exc_info=True)
        self._lifecycle.on_released(self)
