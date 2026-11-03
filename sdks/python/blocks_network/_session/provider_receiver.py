"""Provider side of the session receive pipeline: filter, dedup, gap logging, probe echo."""

from __future__ import annotations

import logging
import threading
from dataclasses import dataclass
from typing import Any, Callable, Dict, List, Optional

from . import wire
from ._callbacks import invoke, noop
from .counterpart import Counterparts, InboundKind, classify_inbound
from .sequencing import SeqTracker, SeqVerdict

logger = logging.getLogger(__name__)

Message = Dict[str, Any]

_ACCEPTS_EXCHANGES = frozenset({wire.STATE_READY})
_CLOSABLE = frozenset({wire.STATE_OPEN, wire.STATE_READY})


@dataclass(frozen=True)
class BackendNotice:
    """A backend ``session_closed`` / ``session_failed``; ``detail`` is its reason or error."""

    state: str
    detail: Optional[str]


@dataclass(frozen=True)
class StartedExchange:
    exchange_id: str
    request_parts: List[Dict[str, Any]]


def _is_exchange_id(value: Any) -> bool:
    return isinstance(value, str) and 0 < len(value) <= wire.MAX_EXCHANGE_ID_LENGTH


def _started_exchange(value: Any) -> Optional[StartedExchange]:
    if not isinstance(value, dict):
        return None
    exchange_id, request_parts = value.get("exchangeId"), value.get("requestParts")
    if not _is_exchange_id(exchange_id) or not isinstance(request_parts, list):
        return None
    return StartedExchange(exchange_id, request_parts)


class ProviderReceiver:
    """Turns consumer and backend messages into session events for the provider runtime.

    Unlike the consumer, the provider does not hold messages behind a ``seq`` gap: it logs
    the gap and keeps processing. A consumer ``session_close`` moves the session to
    ``closing`` and fires ``on_close_requested``; the runtime cancels the in-flight
    exchange, publishes ``session_closed``, then calls :meth:`terminate`.
    """

    def __init__(
        self,
        *,
        counterparts: Counterparts,
        state: str = wire.STATE_OPEN,
        on_state_change: Callable[[str], None] = noop,
        on_exchange: Callable[[StartedExchange], None] = noop,
        on_cancel: Callable[[str], None] = noop,
        on_close_requested: Callable[[None], None] = noop,
        on_probe: Callable[[str], None] = noop,
        on_backend_notice: Callable[[BackendNotice], None] = noop,
    ) -> None:
        self._counterparts = counterparts
        self._on_state_change = on_state_change
        self._on_exchange = on_exchange
        self._on_cancel = on_cancel
        self._on_close_requested = on_close_requested
        self._on_probe = on_probe
        self._on_backend_notice = on_backend_notice
        self._lock = threading.RLock()
        self._state = state
        self._seen = SeqTracker()
        self._handlers: Dict[str, Callable[[Message], None]] = {
            wire.SESSION_OPEN: self._on_session_open,
            wire.EXCHANGE: self._start_exchange,
            wire.EXCHANGE_CANCEL: self._on_exchange_cancel,
            wire.SESSION_CLOSE: self._on_session_close,
            wire.SESSION_CLOSED: lambda m: self._on_notice(wire.STATE_CLOSED, m.get("reason")),
            wire.SESSION_FAILED: lambda m: self._on_notice(wire.STATE_FAILED, m.get("error")),
            wire.ROUTE_PROBE: self._on_route_probe,
        }

    @property
    def state(self) -> str:
        return self._state

    def deliver(self, publisher: Optional[str], message: Message) -> bool:
        """Return True when the message passes counterpart filtering and dedup.

        Once the session is terminal only backend notices still pass.
        """
        inbound = classify_inbound(self._counterparts, publisher, message)
        if inbound.kind is InboundKind.DROP:
            return False
        with self._lock:
            if inbound.kind is InboundKind.SEQUENCED and (
                self._is_terminal() or not self._observe_seq(publisher, inbound.seq)
            ):
                return False
            self._handlers[message["type"]](message)
            return True

    def terminate(self, state: str) -> None:
        """End the session once; later calls and notices change nothing."""
        with self._lock:
            if not self._is_terminal():
                self._set_state(state)

    def _observe_seq(self, publisher: str, seq: int) -> bool:
        verdict = self._seen.observe(publisher, seq)
        if verdict is SeqVerdict.AFTER_GAP:
            logger.warning("session seq gap from %s: received %d", publisher, seq)
        return verdict is not SeqVerdict.DUPLICATE

    def _is_terminal(self) -> bool:
        return self._state in wire.TERMINAL_STATES

    def _set_state(self, state: str) -> None:
        self._state = state
        invoke(self._on_state_change, state)

    def _start_exchange(self, exchange: Any) -> None:
        started = _started_exchange(exchange)
        if self._state in _ACCEPTS_EXCHANGES and started is not None:
            invoke(self._on_exchange, started)

    def _on_session_open(self, message: Message) -> None:
        if self._state != wire.STATE_OPEN:
            return
        self._set_state(wire.STATE_READY)
        if "exchange" in message:
            self._start_exchange(message["exchange"])

    def _on_exchange_cancel(self, message: Message) -> None:
        exchange_id = message.get("exchangeId")
        if not self._is_terminal() and _is_exchange_id(exchange_id):
            invoke(self._on_cancel, exchange_id)

    def _on_session_close(self, _: Message) -> None:
        if self._state in _CLOSABLE:
            self._set_state(wire.STATE_CLOSING)
            invoke(self._on_close_requested, None)

    def _on_notice(self, state: str, detail: Any) -> None:
        if self._is_terminal():
            return
        self._set_state(state)
        notice = BackendNotice(state, detail if isinstance(detail, str) else None)
        invoke(self._on_backend_notice, notice)

    def _on_route_probe(self, message: Message) -> None:
        nonce = message.get("nonce")
        if isinstance(nonce, str):
            invoke(self._on_probe, nonce)
