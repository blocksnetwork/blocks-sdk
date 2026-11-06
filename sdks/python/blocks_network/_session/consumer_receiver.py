"""Consumer side of the session receive pipeline: filter, dedup, order, settle."""

from __future__ import annotations

import logging
import threading
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional, Tuple

from . import wire
from ._callbacks import invoke, noop
from .counterpart import Counterparts, InboundKind, classify_inbound
from .scheduler import Cancellable, Scheduler, ThreadingScheduler
from .sequencing import InOrderBuffer
from .snapshot import SessionSnapshot, SnapshotExchange, merge_artifacts

logger = logging.getLogger(__name__)

GAP_RECOVERY_DELAY_S = 0.75

Message = Dict[str, Any]

_TERMINAL_MESSAGES = frozenset({wire.SESSION_CLOSED, wire.SESSION_FAILED})


def _text(message: Message, key: str) -> Optional[str]:
    """A wire field that must be a string; any other value may not even be hashable."""
    value = message.get(key)
    return value if isinstance(value, str) else None


def _exchange_id(message: Message) -> Optional[str]:
    return _text(message, "exchangeId")


@dataclass(frozen=True)
class ExchangeProgress:
    """A progress update on an exchange.

    .. warning:: Experimental: sessions are a preview and may change before general availability.
    """

    exchange_id: str
    message: Optional[str] = None
    progress: Optional[Any] = None


@dataclass(frozen=True)
class ExchangeArtifact:
    exchange_id: str
    artifact_ref: Any
    output_id: Optional[str] = None


@dataclass
class _Recovery:
    cutoffs: Dict[str, int]
    deferred: List[Message] = field(default_factory=list)
    record: Optional[SessionSnapshot] = None
    # The first local end asked for while the recovery is open or winding down.
    local_end: Optional[Tuple[str, Optional[str]]] = None


@dataclass(frozen=True)
class ExchangeOutcome:
    """The one outcome an exchange ends in; ``artifacts`` holds every artifact received for it.

    .. warning:: Experimental: sessions are a preview and may change before general availability.
    """

    exchange_id: str
    state: str
    error: Optional[str] = None
    reason: Optional[str] = None
    artifacts: List[Dict[str, Any]] = field(default_factory=list)


class ConsumerReceiver:
    """Turns raw session-channel messages into state changes, progress and outcomes.

    ``recover`` is called, outside the lock, when a ``seq`` gap outlasts
    ``GAP_RECOVERY_DELAY_S``. It may feed history back through :meth:`deliver` with
    ``replayed=True``; any ``seq`` still missing below what was held when the timer fired
    is then skipped. A gap that opens during recovery gets its own window.

    A full recovery brackets that replay with :meth:`begin_recovery`,
    :meth:`history_replayed` and :meth:`finish_recovery`, which applies the backend's
    record in one order (see :meth:`finish_recovery`).
    """

    def __init__(
        self,
        *,
        counterparts: Counterparts,
        recover: Callable[[], None],
        scheduler: Optional[Scheduler] = None,
        state: str = wire.STATE_OPEN,
        on_state_change: Callable[[str], None] = noop,
        on_progress: Callable[[ExchangeProgress], None] = noop,
        on_artifact: Callable[[ExchangeArtifact], None] = noop,
        on_outcome: Callable[[ExchangeOutcome], None] = noop,
    ) -> None:
        self._counterparts = counterparts
        self._scheduler = scheduler or ThreadingScheduler()
        self._recover = recover
        self._on_state_change = on_state_change
        self._on_progress = on_progress
        self._on_artifact = on_artifact
        self._on_outcome = on_outcome
        # Reentrant: callbacks run under the lock and may call back in, e.g. track_exchange().
        self._lock = threading.RLock()
        self._state = state
        self._error: Optional[str] = None
        self._ending = False
        # A local end waits for an open recovery to wind down, but no new work may start meanwhile.
        self._end_requested = False
        self._recovery: Optional[_Recovery] = None
        # Set while finish_recovery runs, so a callback it fires cannot re-enter it.
        self._finishing: Optional[_Recovery] = None
        self._order: InOrderBuffer[Message] = InOrderBuffer(self._dispatch)
        self._gap_timers: Dict[str, Tuple[object, Cancellable]] = {}
        self._artifacts_by_unsettled: Dict[str, List[Dict[str, Any]]] = {}
        # Recorded outcomes waiting for their exchange's messages still held behind a live gap.
        self._recorded_later: Dict[str, SnapshotExchange] = {}
        self._handlers: Dict[str, Callable[[Message], None]] = {
            wire.SESSION_READY: self._on_session_ready,
            wire.SESSION_CLOSED: self._on_session_closed,
            wire.SESSION_FAILED: self._on_session_failed,
            wire.EXCHANGE_PROGRESS: self._on_exchange_progress,
            wire.EXCHANGE_ARTIFACT: self._on_exchange_artifact,
            wire.EXCHANGE_OUTCOME: self._on_exchange_outcome,
        }

    @property
    def state(self) -> str:
        return self._state

    @property
    def error(self) -> Optional[str]:
        """The failure reason once the session is ``failed``."""
        return self._error

    @property
    def is_terminal(self) -> bool:
        return self._state in wire.TERMINAL_STATES

    @property
    def is_ending(self) -> bool:
        """True from the moment the session starts ending, before its state turns terminal."""
        return self._ended or self._end_requested

    @property
    def _ended(self) -> bool:
        return self._ending or self.is_terminal

    @property
    def delivery_lock(self) -> "threading.RLock":
        """Held while callbacks run; a caller replaying to a callback takes it to stay in order."""
        return self._lock

    @property
    def has_gap(self) -> bool:
        """True while a message waits behind a ``seq`` gap; history must replay from before it."""
        with self._lock:
            return self._order.holding

    def track_exchange(self, exchange_id: str) -> None:
        with self._lock:
            if self._state not in wire.TERMINAL_STATES:
                self._artifacts_by_unsettled.setdefault(exchange_id, [])

    def forget_exchange(self, exchange_id: str) -> None:
        """Stop tracking an exchange settled outside the channel, such as one whose publish failed."""
        with self._lock:
            self._artifacts_by_unsettled.pop(exchange_id, None)
            self._recorded_later.pop(exchange_id, None)

    def begin_closing(self) -> None:
        """Enter ``closing`` once the consumer has asked to close; a later ready no longer applies."""
        with self._lock:
            if self._state not in wire.TERMINAL_STATES and self._state != wire.STATE_CLOSING:
                self._set_state(wire.STATE_CLOSING)

    def terminate(self, state: str, error: Optional[str] = None) -> None:
        """End the session, settling unsettled exchanges locally. A no-op once ending.

        An open recovery ends first, so a terminal message it deferred, or a terminal state
        it recorded, wins over this one; among local ends the first wins, including over one
        a callback starts while the recovery winds down. A held artifact reaches its
        exchange before the state turns terminal; any other held message is dropped, an
        outcome included, as the messages before it never came.
        """
        with self._lock:
            recovery = self._recovery or self._finishing
            if recovery is None:
                self._end(state, error)
                return
            if recovery.local_end is None:
                recovery.local_end = (state, error)
            self._end_requested = True
            self.finish_recovery()

    def _end(self, state: str, error: Optional[str] = None) -> None:
        # The first end wins, even over one a callback starts while this one hands
        # artifacts over.
        if self._ended:
            return
        self._ending = True
        held_artifacts = [
            m for m in self._order.held_items() if m.get("type") == wire.EXCHANGE_ARTIFACT
        ]
        self._order.dispose()
        for _, handle in self._gap_timers.values():
            handle.cancel()
        self._gap_timers.clear()
        for message in held_artifacts:
            self._on_exchange_artifact(message)
        failed = state == wire.STATE_FAILED
        self._error = error if failed else None
        # Exchanges settle before the state change is reported, so a state callback finds
        # them settled; the state itself is terminal first, so a settle callback cannot send.
        self._state = state
        for exchange_id in list(self._artifacts_by_unsettled):
            if exchange_id not in self._artifacts_by_unsettled:
                continue
            recorded = self._recorded_later.get(exchange_id)
            if recorded is not None:
                self._settle_from_record(recorded)
            elif failed:
                self._settle(exchange_id, wire.OUTCOME_FAILED, error=error)
            else:
                self._settle(exchange_id, wire.OUTCOME_CANCELED)
        invoke(self._on_state_change, state)

    def begin_recovery(self) -> None:
        """Start a recovery: until :meth:`finish_recovery`, a terminal message is deferred.

        The gaps open now are among those the recovery closes.
        """
        with self._lock:
            if self._recovery is not None:
                raise RuntimeError("a session recovery is already open")
            self._recovery = _Recovery(cutoffs=self._order.held_cutoffs())

    def history_replayed(self) -> None:
        """Add the gaps the replay revealed to those open when recovery began.

        A gap below a held message history returned closes too, whether that message
        arrived first by replay or live. A gap a live message opened since, with nothing
        above it from history, keeps its own timer, as its missing message may be in flight.
        """
        with self._lock:
            if self._recovery is None:
                return
            cutoffs = self._recovery.cutoffs
            for publisher, seq in self._order.replayed_cutoffs().items():
                cutoffs[publisher] = max(seq, cutoffs.get(publisher, 0))

    def finish_recovery(self, snapshot: Optional[SessionSnapshot] = None) -> None:
        """End a recovery in one order, so no step cuts another short.

        Skip the gaps history did not fill, settle the outcomes the backend recorded, apply
        the deferred terminal messages, then follow the recorded state. Without a snapshot
        only the skip and the deferred messages apply. A session about to end skips every
        gap, as no live message will fill one in time.
        """
        with self._lock:
            recovery = self._recovery
            if recovery is None or self._finishing is not None:
                return
            self._finishing = recovery
            try:
                self._finish(recovery, snapshot)
            finally:
                self._finishing = None

    def _finish(self, recovery: _Recovery, snapshot: Optional[SessionSnapshot]) -> None:
        matches = snapshot is not None and snapshot.session_id == self._counterparts.session_id
        if snapshot is not None and not matches:
            logger.warning("session snapshot names another session; ignored")
        record = snapshot if matches else None
        # An outcome released from behind a gap takes the recorded file refs too: the
        # artifact message that carried one may be the message history lost.
        recovery.record = record
        ending = bool(recovery.deferred) or (
            record is not None and record.state in wire.TERMINAL_STATES
        )
        # A requested local end applies even if a step raises, or the session would refuse
        # sends without ever ending.
        try:
            try:
                self._skip_through(self._order.held_cutoffs() if ending else recovery.cutoffs)
            finally:
                self._recovery = None
            if record is not None:
                self._apply_recorded_outcomes(record)
            for message in recovery.deferred:
                self._dispatch(message)
            if record is not None:
                self._apply_recorded_state(record)
        finally:
            if recovery.local_end is not None:
                self._end(*recovery.local_end)

    def _skip_through(self, cutoffs: Dict[str, int]) -> None:
        for publisher, through in cutoffs.items():
            released = self._order.skip_gaps(publisher, through)
            # What is still held waits behind a newer gap, which gets its own window.
            if released and publisher in self._gap_timers:
                self._gap_timers.pop(publisher)[1].cancel()
            self._sync_gap_timer(publisher)

    def _apply_recorded_outcomes(self, snapshot: SessionSnapshot) -> None:
        """An exchange with a message still held behind a gap left open for live delivery
        settles once that message is released, so a held artifact is not dropped by an
        exchange already settled."""
        if self._ended:
            return
        held = {_exchange_id(m) for m in self._order.held_items()}
        for recorded in snapshot.exchanges:
            exchange_id = recorded.exchange_id
            if (
                exchange_id not in self._artifacts_by_unsettled
                or recorded.state not in wire.OUTCOME_STATES
            ):
                continue
            if exchange_id in held:
                self._recorded_later[exchange_id] = recorded
            else:
                self._settle_from_record(recorded)

    def _settle_recorded_if_released(self, exchange_id: Any) -> None:
        recorded = self._recorded_later.get(exchange_id)
        if recorded is None or self._order.holds_any(lambda m: _exchange_id(m) == exchange_id):
            return
        if exchange_id in self._artifacts_by_unsettled:
            self._settle_from_record(recorded)

    def _settle_from_record(self, recorded: SnapshotExchange) -> None:
        self._artifacts_by_unsettled[recorded.exchange_id] = merge_artifacts(
            self._artifacts_by_unsettled[recorded.exchange_id], recorded.artifact_refs
        )
        self._settle(recorded.exchange_id, recorded.state, error=recorded.error)

    def _apply_recorded_state(self, snapshot: SessionSnapshot) -> None:
        if self._state in wire.TERMINAL_STATES:
            return
        if snapshot.state == wire.STATE_READY:
            if self._state == wire.STATE_OPEN:
                self._set_state(wire.STATE_READY)
        elif snapshot.state == wire.STATE_FAILED:
            self._end(wire.STATE_FAILED, snapshot.error)
        elif snapshot.state in wire.TERMINAL_STATES:
            self._end(snapshot.state)

    def deliver(self, publisher: Optional[str], message: Message, replayed: bool = False) -> bool:
        """Return True when the message passes counterpart filtering and dedup, even if held.

        ``replayed`` marks a message read from history rather than received live. Once the
        session is ending only backend notices still pass.
        """
        inbound = classify_inbound(self._counterparts, publisher, message)
        if inbound.kind is InboundKind.DROP:
            return False
        with self._lock:
            if inbound.kind is InboundKind.NOTICE:
                self._dispatch(message)
                return True
            if self._ended or not self._order.accept(
                publisher, inbound.seq, message, replayed
            ):
                return False
            self._sync_gap_timer(publisher)
            return True

    def _sync_gap_timer(self, publisher: str) -> None:
        gapped = self._order.has_gap(publisher) and not self._ended
        if gapped and publisher not in self._gap_timers:
            token = object()
            handle = self._scheduler.call_later(
                GAP_RECOVERY_DELAY_S, lambda: self._on_gap_timeout(publisher, token)
            )
            self._gap_timers[publisher] = (token, handle)
        elif not gapped and publisher in self._gap_timers:
            self._gap_timers.pop(publisher)[1].cancel()

    def _on_gap_timeout(self, publisher: str, token: object) -> None:
        with self._lock:
            current = self._gap_timers.get(publisher)
            if current is None or current[0] is not token:
                return
            del self._gap_timers[publisher]
            through = self._order.highest_held(publisher)
        if through is None:
            return
        try:
            self._recover()
        except Exception:
            logger.warning("session recover() failed; skipping the seq gap", exc_info=True)
        with self._lock:
            self._order.skip_gaps(publisher, through)
            self._sync_gap_timer(publisher)

    def _dispatch(self, message: Message) -> None:
        if self._state in wire.TERMINAL_STATES:
            return
        if self._recovery is not None and message.get("type") in _TERMINAL_MESSAGES:
            self._recovery.deferred.append(message)
            return
        handler = self._handlers.get(message.get("type"))
        if handler is not None:
            handler(message)
        # Any released message of an exchange, even one ignored here, may end its recorded
        # outcome's wait.
        self._settle_recorded_if_released(_exchange_id(message))

    def _set_state(self, state: str) -> None:
        self._state = state
        invoke(self._on_state_change, state)

    def _on_session_ready(self, _: Message) -> None:
        if self._state == wire.STATE_OPEN:
            self._set_state(wire.STATE_READY)

    def _on_session_closed(self, _: Message) -> None:
        self._end(wire.STATE_CLOSED)

    def _on_session_failed(self, message: Message) -> None:
        self._end(wire.STATE_FAILED, _text(message, "error"))

    def _on_exchange_progress(self, message: Message) -> None:
        exchange_id = _exchange_id(message)
        if exchange_id in self._artifacts_by_unsettled:
            progress = ExchangeProgress(
                exchange_id=exchange_id,
                message=message.get("message"),
                progress=message.get("progress"),
            )
            invoke(self._on_progress, progress)

    def _on_exchange_artifact(self, message: Message) -> None:
        exchange_id = _exchange_id(message)
        artifacts = self._artifacts_by_unsettled.get(exchange_id)
        artifact_ref = message.get("artifactRef")
        if artifacts is not None and artifact_ref is not None:
            artifacts.append(artifact_ref)
            output_id = message.get("outputId")
            artifact = ExchangeArtifact(
                exchange_id, artifact_ref, output_id if isinstance(output_id, str) else None
            )
            invoke(self._on_artifact, artifact)

    def _on_exchange_outcome(self, message: Message) -> None:
        exchange_id = _exchange_id(message)
        if (
            exchange_id in self._artifacts_by_unsettled
            and _text(message, "state") in wire.OUTCOME_STATES
        ):
            record = self._recovery.record if self._recovery is not None else None
            recorded = next(
                (x for x in (record.exchanges if record else []) if x.exchange_id == exchange_id),
                self._recorded_later.get(exchange_id),
            )
            if recorded is not None:
                self._artifacts_by_unsettled[exchange_id] = merge_artifacts(
                    self._artifacts_by_unsettled[exchange_id], recorded.artifact_refs
                )
            self._settle(
                exchange_id,
                message.get("state"),
                error=_text(message, "error"),
                reason=_text(message, "reason"),
            )

    def _settle(
        self,
        exchange_id: str,
        state: str,
        *,
        error: Optional[str] = None,
        reason: Optional[str] = None,
    ) -> None:
        artifacts = self._artifacts_by_unsettled.pop(exchange_id)
        self._recorded_later.pop(exchange_id, None)
        outcome = ExchangeOutcome(exchange_id, state, error, reason, artifacts)
        invoke(self._on_outcome, outcome)
