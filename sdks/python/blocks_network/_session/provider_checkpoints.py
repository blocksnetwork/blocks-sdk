"""One checkpoint loop per route: lifecycle facts batched to the backend, and the heartbeat."""

from __future__ import annotations

import enum
import logging
import threading
import time
import uuid
from collections import deque
from typing import Any, Callable, Deque, Dict, List, Optional

from .errors import ROUTE_LOST
from .provider_api import ProviderApiError, error_code_of, is_credential_refusal
from .scheduler import Cancellable, Scheduler
from .token_schedule import rate_limited_wait_s

logger = logging.getLogger(__name__)

CHECKPOINT_FLUSH_S = 2.0
CHECKPOINT_MAX_EVENTS = 64
CHECKPOINT_MAX_OPEN_SESSIONS = 256
_MAX_PROBE_ECHOES = 256
_MAX_ID_LENGTH = 256
# Malformed (400) or oversized (413): the backend refuses the batch the same way on every replay.
_UNREPLAYABLE_STATUSES = frozenset({400, 413})
_SESSION_ENDS = frozenset({"session_closed", "session_failed"})

# Lifecycle facts only: never request parts or inline artifact data.
CheckpointEvent = Dict[str, Any]
CheckpointBatch = Dict[str, Any]
Spawn = Callable[[Callable[[], None]], None]


class _Delivery(enum.Enum):
    ACKNOWLEDGED = "acknowledged"
    DROPPED = "dropped"
    RETRY = "retry"


class CheckpointLoop:
    """Flushes every :data:`CHECKPOINT_FLUSH_S`, at :data:`CHECKPOINT_MAX_EVENTS` events, and at
    once on a session's end. An empty batch is the heartbeat. A batch the backend has not
    acknowledged is retained and replayed, with the same key, before any later one.
    """

    def __init__(
        self,
        *,
        route_id: str,
        post: Callable[[CheckpointBatch], None],
        open_sessions: Callable[[], List[str]],
        on_route_lost: Callable[[], None],
        scheduler: Scheduler,
        spawn: Spawn,
    ) -> None:
        self._route_id = route_id
        self._post = post
        self._open_sessions = open_sessions
        self._on_route_lost = on_route_lost
        self._scheduler = scheduler
        self._spawn = spawn
        # A leaf lock: nothing is called out to while it is held.
        self._lock = threading.Lock()
        self._pending: List[CheckpointEvent] = []
        self._echoes: List[str] = []
        self._unacked: Deque[CheckpointBatch] = deque()
        self._timer: Optional[Cancellable] = None
        self._flushing = False
        self._flush_again = False
        self._drained = threading.Event()
        self._stopped = False
        # No batch is posted before this monotonic time: the last failure carried a Retry-After.
        self._held_until = 0.0

    def start(self) -> None:
        with self._lock:
            if not self._stopped:
                self._timer = self._scheduler.call_later(CHECKPOINT_FLUSH_S, self._tick)

    def stop(self) -> None:
        with self._lock:
            self._stopped = True
            if self._timer is not None:
                self._timer.cancel()

    def record(self, event: CheckpointEvent) -> None:
        with self._lock:
            if self._stopped:
                return
            self._pending.append(event)
            due = event["type"] in _SESSION_ENDS or len(self._pending) >= CHECKPOINT_MAX_EVENTS
        if due:
            self.flush()

    def echo_probe(self, nonce: Any) -> None:
        """Echoed in the next batch; a nonce the backend could not accept is dropped."""
        if not isinstance(nonce, str) or not 0 < len(nonce) <= _MAX_ID_LENGTH:
            return
        with self._lock:
            if self._stopped or nonce in self._echoes or len(self._echoes) >= _MAX_PROBE_ECHOES:
                return
            self._echoes.append(nonce)

    def flush(self) -> threading.Event:
        """Start a drain, or have the running one go round again; the event is set once done."""
        with self._lock:
            if self._flushing:
                self._flush_again = True
                return self._drained
            self._flushing = True
            self._drained = threading.Event()
            drained = self._drained
        self._spawn(self._drain)
        return drained

    def _tick(self) -> None:
        with self._lock:
            if self._stopped:
                return
            self._timer = self._scheduler.call_later(CHECKPOINT_FLUSH_S, self._tick)
        self.flush()

    def _drain(self) -> None:
        try:
            while True:
                self._cut_batches()
                self._send_in_order()
                with self._lock:
                    if self._flush_again and not self._stopped:
                        self._flush_again = False
                        continue
                    # Released with the decision, so a flush after it starts a new drain.
                    self._flushing = False
                    drained = self._drained
                    break
        except Exception:
            logger.exception("checkpoint drain failed")
            with self._lock:
                self._flushing = False
                self._flush_again = False
                drained = self._drained
        drained.set()

    def _cut_batches(self) -> None:
        open_sessions = self._open_sessions()
        with self._lock:
            while self._pending or self._echoes:
                events = self._pending[:CHECKPOINT_MAX_EVENTS]
                del self._pending[:CHECKPOINT_MAX_EVENTS]
                echoes, self._echoes = self._echoes, []
                self._unacked.append(self._batch(open_sessions, events, echoes))

    def _send_in_order(self) -> None:
        with self._lock:
            idle = not self._unacked
        if idle:
            # A failed heartbeat is not retained: the next tick sends a fresh one.
            self._deliver(self._batch(self._open_sessions(), [], []))
            return
        while True:
            with self._lock:
                if not self._unacked or self._stopped:
                    return
                batch = self._unacked[0]
            if self._deliver(batch) is _Delivery.RETRY:
                return
            with self._lock:
                if self._unacked and self._unacked[0] is batch:
                    self._unacked.popleft()

    def _deliver(self, batch: CheckpointBatch) -> _Delivery:
        if self._stopped or time.monotonic() < self._held_until:
            return _Delivery.RETRY
        try:
            self._post(batch)
            return _Delivery.ACKNOWLEDGED
        except Exception as exc:
            credential_refused = is_credential_refusal(exc)
            if credential_refused or error_code_of(exc) == ROUTE_LOST:
                # Retrying a refused credential every tick is a storm; re-registering backs off.
                if credential_refused:
                    logger.error(
                        "agent credential refused; ending the route %s: %s", self._route_id, exc
                    )
                self.stop()
                self._on_route_lost()
                return _Delivery.RETRY
            if isinstance(exc, ProviderApiError) and exc.status in _UNREPLAYABLE_STATUSES:
                logger.error(
                    "checkpoint batch refused; dropping it (route %s, key %s): %s",
                    self._route_id,
                    batch["idempotencyKey"],
                    exc,
                )
                return _Delivery.DROPPED
            wait_s = rate_limited_wait_s(exc)
            if wait_s > 0:
                self._held_until = time.monotonic() + wait_s
            logger.warning(
                "checkpoint failed; retrying on the next tick (route %s, %d retained): %s",
                self._route_id,
                len(self._unacked),
                exc,
            )
            return _Delivery.RETRY

    def _batch(
        self, open_sessions: List[str], events: List[CheckpointEvent], echoes: List[str]
    ) -> CheckpointBatch:
        return {
            "routeId": self._route_id,
            "idempotencyKey": str(uuid.uuid4()),
            "openSessions": list(open_sessions),
            "events": events,
            "probeEchoes": echoes,
        }
