"""One provider session: runs its exchanges one at a time and publishes what they produce."""

from __future__ import annotations

import logging
import queue
import threading
import time
from collections import OrderedDict, deque
from concurrent.futures import Future
from dataclasses import dataclass, field
from typing import Any, Callable, Deque, Dict, List, Optional, Set

from ..artifacts import build_artifact_ref, should_inline_artifact
from ..config import ARTIFACT_INLINE_LIMIT_BYTES
from ..protocol_version import CURRENT_PROTOCOL_VERSION
from ..types import RequestPart
from . import wire
from .counterpart import provider_counterparts
from .provider_api import SessionManifest
from .provider_checkpoints import CheckpointEvent
from .provider_receiver import BackendNotice, ProviderReceiver, StartedExchange
from .provider_transport import RouteClient
from .provider_types import ExchangeContext, SessionContext, SessionHandlers
from .scheduler import Cancellable, Scheduler

logger = logging.getLogger(__name__)

# In flight plus queued, per session.
MAX_PENDING_EXCHANGES = 8
# Settled exchange IDs remembered per session, so a re-sent exchange is not run twice.
MAX_SETTLED_EXCHANGE_IDS = 256
STATUS_THROTTLE_S = 1.0
# The longest ready wait a consumer may set, so no consumer is still waiting when it expires.
SESSION_OPEN_TIMEOUT_S = 120.0
_PUBLISH_ATTEMPTS = 2
_DEFAULT_MIME_TYPE = "application/octet-stream"


def spawn_daemon(fn: Callable[[], None]) -> None:
    def run() -> None:
        try:
            fn()
        except Exception:
            logger.exception("session provider worker %s raised", getattr(fn, "__qualname__", fn))

    threading.Thread(target=run, daemon=True).start()


def _resolved(value: bool) -> "Future[bool]":
    future: "Future[bool]" = Future()
    future.set_result(value)
    return future


class _Outbox:
    """Runs publishes one at a time, in submission order, on a daemon thread."""

    def __init__(self) -> None:
        self._jobs: "queue.Queue[Optional[Callable[[], None]]]" = queue.Queue()
        self._worker: Optional[threading.Thread] = None

    def submit(self, job: Callable[[], bool]) -> "Future[bool]":
        future: "Future[bool]" = Future()

        def run() -> None:
            try:
                future.set_result(job())
            except Exception as exc:
                future.set_exception(exc)

        self._jobs.put(run)
        if self._worker is None:
            self._worker = threading.Thread(target=self._work, daemon=True)
            self._worker.start()
        return future

    def stop(self) -> None:
        self._jobs.put(None)

    def _work(self) -> None:
        while True:
            job = self._jobs.get()
            if job is None:
                return
            job()


class _StatusThrottle:
    """Emits under the session's lock, so a status cannot be queued behind its outcome."""

    def __init__(
        self,
        emit: Callable[[str], None],
        scheduler: Scheduler,
        clock: Callable[[], float],
        lock: threading.RLock,
    ) -> None:
        self._emit = emit
        self._scheduler = scheduler
        self._clock = clock
        self._lock = lock
        self._last: Optional[float] = None
        self._pending: Optional[str] = None
        self._timer: Optional[Cancellable] = None

    def report(self, message: str) -> None:
        with self._lock:
            now = self._clock()
            wait = 0.0 if self._last is None else self._last + STATUS_THROTTLE_S - now
            if wait > 0 or self._timer is not None:
                self._pending = message
                if self._timer is None:
                    self._timer = self._scheduler.call_later(max(wait, 0.0), self.flush)
                return
            self._last = now
            self._emit(message)

    def flush(self) -> None:
        with self._lock:
            self._cancel_timer()
            message, self._pending = self._pending, None
            if message is None:
                return
            self._last = self._clock()
            self._emit(message)

    def discard(self) -> None:
        with self._lock:
            self._cancel_timer()
            self._pending = None

    def _cancel_timer(self) -> None:
        if self._timer is not None:
            self._timer.cancel()
            self._timer = None


@dataclass
class _Running:
    exchange_id: str
    status: _StatusThrottle
    cancel_event: threading.Event = field(default_factory=threading.Event)
    settled: bool = False


def _as_bytes(data: Any) -> bytes:
    if isinstance(data, str):
        return data.encode("utf-8")
    if isinstance(data, (bytes, bytearray)):
        return bytes(data)
    raise TypeError("artifact data must be str or bytes")


def _artifacts_of(result: Any) -> List[Dict[str, Any]]:
    artifacts = result.get("artifacts") if isinstance(result, dict) else None
    return [a for a in artifacts if isinstance(a, dict)] if isinstance(artifacts, list) else []


def _request_parts(parts: List[Any]) -> List[Any]:
    return [RequestPart.from_dict(p) if isinstance(p, dict) else p for p in parts]


def _artifact_ref_of(part: Any) -> Any:
    if isinstance(part, dict):
        return part.get("artifactRef")
    return getattr(part, "artifact_ref", None)


class ProviderSession:
    """``_lock`` is never held across a handler or a publish wait; both run off the subscribe
    thread."""

    def __init__(
        self,
        *,
        manifest: SessionManifest,
        channel: str,
        client: RouteClient,
        handlers: SessionHandlers,
        record: Callable[[CheckpointEvent], None],
        on_ended: Callable[[], None],
        scheduler: Scheduler,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self._channel = channel
        self._client = client
        self._handlers = handlers
        self._record_event = record
        self._on_ended = on_ended
        self._scheduler = scheduler
        self._clock = clock
        self.session_id = manifest.session_id
        self.ctx = self._context(manifest)
        self._lock = threading.RLock()
        self._outbox = _Outbox()
        self._queue: Deque[StartedExchange] = deque()
        self._settled_ids: "OrderedDict[str, None]" = OrderedDict()
        self._outcomes_in_flight: "Set[Future[bool]]" = set()
        self._outcome_undelivered = False
        self._current: Optional[_Running] = None
        self._seq = 0
        self._ready = False
        self._open_pending = False
        self._open_timer: Optional[Cancellable] = None
        self._refusals_in_flight = 0
        self._deferred_close: Optional[str] = None
        self._open_returned = False
        self._ending = False
        self._silenced = False
        self._close_handled = False
        self._finished = False
        self._end_reason = ""
        self._receiver = self._receiver_for(manifest)

    def _context(self, manifest: SessionManifest) -> SessionContext:
        return SessionContext(
            session_id=manifest.session_id,
            agent_name=manifest.agent_name,
            org_id=manifest.org_id,
            owner_id=manifest.owner_id,
            caller_claims=manifest.caller_claims,
            close=lambda: self._close("provider_closed"),
        )

    def _receiver_for(self, manifest: SessionManifest) -> ProviderReceiver:
        return ProviderReceiver(
            counterparts=provider_counterparts(
                session_id=manifest.session_id,
                consumer_uuid=manifest.consumer_transport_uuid,
                backend_uuid=manifest.backend_transport_uuid or "",
            ),
            on_state_change=self._on_state_change,
            on_exchange=self._enqueue,
            on_cancel=self._cancel_exchange,
            on_close_requested=lambda _: self._close_soon("consumer_closed"),
            on_backend_notice=self._on_backend_notice,
        )

    @property
    def ending(self) -> bool:
        """An ending session still receives (a backend notice during closing must land) but
        holds no seat."""
        return self._ending

    def receive(self, publisher: Optional[str], message: Any) -> bool:
        return self._receiver.deliver(publisher, message)

    def reject_at_capacity(self) -> None:
        self._fail_soon("provider_at_capacity")

    def end_locally(self, reason: str, state: str = wire.STATE_FAILED) -> None:
        """End without publishing: cancel the in-flight exchange and run ``on_session_close``."""
        with self._lock:
            if self._silenced:
                return
            self._ending = True
            self._silenced = True
            self._end_reason = reason
            self._queue.clear()
            current = self._current
            if current is not None:
                current.settled = True
                current.status.discard()
                current.cancel_event.set()
        spawn_daemon(lambda: self._after_local_end(reason, state))

    def _after_local_end(self, reason: str, state: str) -> None:
        self._receiver.terminate(state)
        self._run_session_close(reason)
        self._finish()

    def _on_backend_notice(self, notice: BackendNotice) -> None:
        self.end_locally(notice.detail or notice.state, notice.state)

    def _on_state_change(self, state: str) -> None:
        if state != wire.STATE_READY:
            return
        with self._lock:
            self._open_pending = True
            # A close waits for the handler, so one that never returns would hold the seat.
            self._open_timer = self._scheduler.call_later(
                SESSION_OPEN_TIMEOUT_S, self._on_open_timeout
            )
        spawn_daemon(self._open)

    def _on_open_timeout(self) -> None:
        with self._lock:
            # The timer may fire just as on_session_open returns and cancels it.
            if not self._open_pending:
                return
            logger.error(
                "on_session_open did not return in %ss; failing session %s",
                SESSION_OPEN_TIMEOUT_S,
                self.session_id,
            )
            self._fail_soon("provider_error")

    def _open(self) -> None:
        with self._lock:
            # A notice or route loss may have ended it before this worker started.
            if self._ending:
                self._cancel_open_timer()
                self._open_pending = False
                return
        try:
            if self._handlers.on_session_open is not None:
                self._handlers.on_session_open(self.ctx)
        except Exception:
            with self._lock:
                self._cancel_open_timer()
                self._open_pending = False
            logger.exception("on_session_open raised; failing session %s", self.session_id)
            self._fail("provider_error")
            return
        ready: "Optional[Future[bool]]" = None
        with self._lock:
            self._cancel_open_timer()
            self._open_pending = False
            self._open_returned = True
            ending, deferred, reason = self._ending, self._deferred_close, self._end_reason
            if not ending and deferred is None:
                # Queued with the checks above, so a close arriving now publishes after it.
                event = {"type": wire.SESSION_READY, "sessionId": self.session_id}
                ready = self._send(wire.SESSION_READY, {}, on_delivered=lambda: self._record(event))
        # Before the deferred close, which does nothing once a notice or the timeout ended it.
        if ending:
            self._run_session_close(reason)
            return
        if deferred is not None:
            self._close(deferred)
            return
        if ready is None or not ready.result():
            self._fail("provider_error")
            return
        with self._lock:
            self._ready = True
        self._pump()

    def _fail(self, error: str) -> None:
        if self._begin_fail(error):
            self._complete_fail(error)

    def _fail_soon(self, error: str) -> None:
        """Fail now, but run the handlers and publishes off this thread."""
        if self._begin_fail(error):
            spawn_daemon(lambda: self._complete_fail(error))

    def _begin_fail(self, error: str) -> bool:
        with self._lock:
            if self._ending:
                return False
            self._ending = True
            self._end_reason = error
            # Unstarted exchanges are the consumer's to settle with the session's error.
            if not self._ready:
                self._queue.clear()
            return True

    def _complete_fail(self, error: str) -> None:
        self._cancel_all()
        self._run_session_close(error)
        self._send(wire.SESSION_FAILED, {"error": error}).result()
        self._record({"type": wire.SESSION_FAILED, "sessionId": self.session_id, "error": error})
        self._receiver.terminate(wire.STATE_FAILED)
        self._finish()

    def _close(self, reason: str) -> None:
        if self._begin_close(reason):
            self._complete_close(reason)

    def _close_soon(self, reason: str) -> None:
        if self._begin_close(reason):
            spawn_daemon(lambda: self._complete_close(reason))

    def _begin_close(self, reason: str) -> bool:
        with self._lock:
            if self._ending:
                return False
            # on_session_close must follow a returned on_session_open.
            if self._open_pending:
                if self._deferred_close is None:
                    self._deferred_close = reason
                return False
            self._ending = True
            self._end_reason = reason
            return True

    def _complete_close(self, reason: str) -> None:
        if not self._cancel_all():
            with self._lock:
                # A backend notice ended it meanwhile, and its own handling reports that reason.
                if self._silenced:
                    return
                # An outcome the consumer never got makes this a failure, not a close.
                self._end_reason = "provider_error"
            self._complete_fail("provider_error")
            return
        self._run_session_close(reason)
        self._send(wire.SESSION_CLOSED, {"reason": reason}).result()
        self._record({"type": wire.SESSION_CLOSED, "sessionId": self.session_id, "reason": reason})
        self._receiver.terminate(wire.STATE_CLOSED)
        self._finish()

    def _enqueue(self, exchange: StartedExchange) -> None:
        with self._lock:
            if self._ending:
                return
            if self._is_known_exchange(exchange.exchange_id):
                logger.warning(
                    "dropping a re-sent exchange %s in session %s",
                    exchange.exchange_id,
                    self.session_id,
                )
                return
            pending = len(self._queue) + (1 if self._current is not None else 0)
            if pending >= MAX_PENDING_EXCHANGES:
                self._refuse(exchange.exchange_id)
                return
            self._queue.append(exchange)
        self._pump()

    def _is_known_exchange(self, exchange_id: str) -> bool:
        return (
            (self._current is not None and self._current.exchange_id == exchange_id)
            or any(queued.exchange_id == exchange_id for queued in self._queue)
            or exchange_id in self._settled_ids
        )

    def _remember_settled(self, exchange_id: str) -> None:
        self._settled_ids[exchange_id] = None
        if len(self._settled_ids) > MAX_SETTLED_EXCHANGE_IDS:
            self._settled_ids.popitem(last=False)

    def _refuse(self, exchange_id: str) -> None:
        """A consumer outrunning its refusals fails the session (``provider_error``), so every
        exchange still settles without the outbox growing."""
        with self._lock:
            if self._refusals_in_flight >= MAX_PENDING_EXCHANGES:
                overflow = True
            else:
                overflow = False
                self._refusals_in_flight += 1
                sent = self._begin_outcome(exchange_id, wire.OUTCOME_FAILED, "session_backpressure")
        if overflow:
            logger.warning(
                "consumer keeps exceeding the pending-exchange bound; failing session %s",
                self.session_id,
            )
            self._fail_soon("provider_error")
            return

        def finish() -> None:
            try:
                self._finish_outcome(sent)
            finally:
                with self._lock:
                    self._refusals_in_flight -= 1

        spawn_daemon(finish)

    def _pump(self) -> None:
        with self._lock:
            if not self._ready or self._ending or self._current is not None or not self._queue:
                return
            exchange = self._queue.popleft()
            running = _Running(
                exchange_id=exchange.exchange_id,
                status=_StatusThrottle(
                    lambda message: self._progress(exchange.exchange_id, message),
                    self._scheduler,
                    self._clock,
                    self._lock,
                ),
            )
            self._current = running
        spawn_daemon(lambda: self._run_then_pump(running, exchange.request_parts))

    def _progress(self, exchange_id: str, message: str) -> None:
        self._send(wire.EXCHANGE_PROGRESS, {"exchangeId": exchange_id, "message": message})

    def _cancel_exchange(self, exchange_id: str) -> None:
        with self._lock:
            if self._current is not None and self._current.exchange_id == exchange_id:
                self._current.cancel_event.set()
                return
            queued = next((q for q in self._queue if q.exchange_id == exchange_id), None)
            if queued is None:
                return
            self._queue.remove(queued)
            sent = self._begin_outcome(exchange_id, wire.OUTCOME_CANCELED, None)
        spawn_daemon(lambda: self._finish_outcome(sent))

    def _cancel_all(self) -> bool:
        """Settle the in-flight exchange and every queued one as canceled, without waiting for
        handlers. False when any outcome, including one already publishing, was not delivered."""
        with self._lock:
            publishing = list(self._outcomes_in_flight)
            settling = []
            current = self._current
            if current is not None:
                current.cancel_event.set()
                if not current.settled:
                    current.settled = True
                    current.status.discard()
                    settling.append(current.exchange_id)
            settling.extend(queued.exchange_id for queued in self._queue)
            self._queue.clear()
            sent = [
                self._begin_outcome(exchange_id, wire.OUTCOME_CANCELED, None)
                for exchange_id in settling
            ]
        delivered = all(
            [future.result() for future in publishing]
            + [self._finish_outcome(future) for future in sent]
        )
        return delivered and not self._outcome_undelivered

    def _run_then_pump(self, running: _Running, request_parts: List[Any]) -> None:
        try:
            self._run(running, request_parts)
        finally:
            with self._lock:
                if self._current is running:
                    self._current = None
            self._pump()

    def _run(self, running: _Running, request_parts: List[Any]) -> None:
        cancelled = running.cancel_event.is_set
        with self._lock:
            # A close or notice may have settled it before this worker started.
            if running.settled:
                return
        try:
            exchange = self._exchange_context(running, request_parts)
            result = self._handlers.on_exchange(self.ctx, exchange)
            if cancelled():
                self._settle(running, wire.OUTCOME_CANCELED)
                return
            for artifact in _artifacts_of(result):
                if cancelled():
                    break
                self._publish_artifact(running, artifact)
            self._settle(running, wire.OUTCOME_CANCELED if cancelled() else wire.OUTCOME_COMPLETED)
        except Exception:
            if cancelled():
                self._settle(running, wire.OUTCOME_CANCELED)
                return
            logger.exception(
                "on_exchange raised (session %s, exchange %s)",
                self.session_id,
                running.exchange_id,
            )
            self._settle(running, wire.OUTCOME_FAILED, "handler_error")

    def _settle(self, running: _Running, state: str, error: Optional[str] = None) -> None:
        with self._lock:
            if running.settled:
                return
            running.settled = True
            if state == wire.OUTCOME_CANCELED:
                running.status.discard()
            else:
                running.status.flush()
            sent = self._begin_outcome(running.exchange_id, state, error)
        self._finish_outcome(sent)

    def _begin_outcome(self, exchange_id: str, state: str, error: Optional[str]) -> "Future[bool]":
        """Remembered as settled before the publish, so a duplicate arriving meanwhile is known."""
        with self._lock:
            self._remember_settled(exchange_id)
            fields: Dict[str, Any] = {"exchangeId": exchange_id, "state": state}
            event: CheckpointEvent = {
                "type": wire.EXCHANGE_OUTCOME,
                "sessionId": self.session_id,
                "exchangeId": exchange_id,
                "state": state,
                "artifactRefs": [],
            }
            if error:
                fields["error"] = error
                event["error"] = error
            # Recorded on delivery, in publish order, so a close that follows cannot drop it.
            sent = self._send(
                wire.EXCHANGE_OUTCOME, fields, on_delivered=lambda: self._record(event)
            )
            self._outcomes_in_flight.add(sent)
            sent.add_done_callback(self._outcome_done)
            return sent

    def _outcome_done(self, sent: "Future[bool]") -> None:
        with self._lock:
            self._outcomes_in_flight.discard(sent)
            if not sent.result():
                self._outcome_undelivered = True

    def _finish_outcome(self, sent: "Future[bool]") -> bool:
        if sent.result():
            return True
        # Recording an outcome the consumer never got would settle it in the record alone.
        self._fail_soon("provider_error")
        return False

    def _exchange_context(self, running: _Running, request_parts: List[Any]) -> ExchangeContext:
        def report_status(message: str) -> None:
            with self._lock:
                if not running.settled:
                    running.status.report(message)

        def publish_artifact(
            data: Any,
            *,
            mime_type: Optional[str] = None,
            file_name: Optional[str] = None,
            output_id: Optional[str] = None,
        ) -> None:
            if running.settled:
                raise RuntimeError(f"exchange {running.exchange_id} has already settled")
            artifact = {
                "data": data,
                "mimeType": mime_type or _DEFAULT_MIME_TYPE,
                "fileName": file_name,
                "outputId": output_id,
            }
            self._publish_artifact(running, artifact)

        def download_input_artifact(part: Any) -> bytes:
            ref = _artifact_ref_of(part)
            if ref is None:
                raise ValueError("Request part has no artifactRef")
            return self._client.download_artifact(ref)

        return ExchangeContext(
            exchange_id=running.exchange_id,
            request_parts=_request_parts(request_parts),
            cancel_event=running.cancel_event,
            report_status=report_status,
            publish_artifact=publish_artifact,
            download_input_artifact=download_input_artifact,
        )

    def _publish_artifact(self, running: _Running, artifact: Dict[str, Any]) -> None:
        data = _as_bytes(artifact.get("data"))
        if not should_inline_artifact(len(data)):
            raise ValueError(
                f"session artifacts are limited to {ARTIFACT_INLINE_LIMIT_BYTES} bytes; "
                f"got {len(data)}"
            )
        ref = build_artifact_ref(
            data=data,
            mime_type=artifact.get("mimeType") or _DEFAULT_MIME_TYPE,
            file_name=artifact.get("fileName"),
        )
        fields: Dict[str, Any] = {"exchangeId": running.exchange_id, "artifactRef": ref}
        if artifact.get("outputId"):
            fields["outputId"] = artifact["outputId"]
        # Checked and queued together, so an artifact cannot follow its exchange's outcome.
        with self._lock:
            if running.settled:
                return
            sent = self._send(wire.EXCHANGE_ARTIFACT, fields)
        if not sent.result():
            raise RuntimeError(f"artifact publish failed for exchange {running.exchange_id}")

    def _send(
        self,
        message_type: str,
        fields: Dict[str, Any],
        on_delivered: Optional[Callable[[], None]] = None,
    ) -> "Future[bool]":
        """Publish in call order; ``seq`` is assigned when the publish is requested. The future
        resolves True once delivered."""
        with self._lock:
            if self._silenced:
                return _resolved(False)
            self._seq += 1
            message = {
                "type": message_type,
                "protocolVersion": CURRENT_PROTOCOL_VERSION,
                "sessionId": self.session_id,
                "seq": self._seq,
                "ts": int(time.time() * 1000),
                **fields,
            }
            return self._outbox.submit(lambda: self._deliver(message, on_delivered))

    def _deliver(
        self, message: Dict[str, Any], on_delivered: Optional[Callable[[], None]] = None
    ) -> bool:
        meta = {"sessionId": self.session_id, "protocolVersion": CURRENT_PROTOCOL_VERSION}
        for attempt in range(1, _PUBLISH_ATTEMPTS + 1):
            # Rechecked per attempt: a backend notice may have ended the session meanwhile.
            if self._silenced:
                return False
            try:
                self._client.publish(self._channel, message, meta)
            except Exception as exc:
                if attempt < _PUBLISH_ATTEMPTS:
                    continue
                logger.warning(
                    "session publish failed (session %s, %s seq %s): %s",
                    self.session_id,
                    message["type"],
                    message["seq"],
                    exc,
                )
            else:
                if on_delivered is not None:
                    on_delivered()
                return True
        return False

    def _record(self, event: CheckpointEvent) -> None:
        if not self._silenced:
            self._record_event(event)

    def _run_session_close(self, reason: str) -> None:
        with self._lock:
            if not self._open_returned or self._close_handled:
                return
            self._close_handled = True
        if self._handlers.on_session_close is None:
            return
        try:
            self._handlers.on_session_close(self.ctx, reason)
        except Exception:
            logger.exception("on_session_close raised (session %s)", self.session_id)

    def _cancel_open_timer(self) -> None:
        if self._open_timer is not None:
            self._open_timer.cancel()
            self._open_timer = None

    def _finish(self) -> None:
        with self._lock:
            if self._finished:
                return
            self._finished = True
            self._silenced = True
            self._cancel_open_timer()
        self._outbox.stop()
        self._on_ended()
