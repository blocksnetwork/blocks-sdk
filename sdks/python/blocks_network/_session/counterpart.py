"""Which publishers a session receiver accepts, and the envelope checks that precede dispatch."""

from __future__ import annotations

import enum
from dataclasses import dataclass
from typing import Any, FrozenSet, Mapping, Optional

from . import wire
from .sequencing import is_sender_seq

_BACKEND_TO_CONSUMER = frozenset({wire.SESSION_CLOSED, wire.SESSION_FAILED})
_BACKEND_TO_PROVIDER = frozenset({wire.SESSION_CLOSED, wire.SESSION_FAILED, wire.ROUTE_PROBE})
_CONSUMER_TO_PROVIDER = frozenset(
    {wire.SESSION_OPEN, wire.EXCHANGE, wire.EXCHANGE_CANCEL, wire.SESSION_CLOSE}
)


@dataclass(frozen=True)
class Counterparts:
    """``accepted`` maps a publisher UUID to its allowed types; ``None`` allows every type."""

    session_id: str
    backend_uuid: str
    accepted: Mapping[str, Optional[FrozenSet[str]]]

    def accepts(self, publisher: Optional[str], message_type: object) -> bool:
        if publisher not in self.accepted:
            return False
        allowed = self.accepted[publisher]
        return allowed is None or message_type in allowed

    def is_backend(self, publisher: Optional[str]) -> bool:
        return publisher == self.backend_uuid


def consumer_counterparts(
    *, session_id: str, provider_uuid: str, backend_uuid: str
) -> Counterparts:
    return Counterparts(
        session_id=session_id,
        backend_uuid=backend_uuid,
        accepted={provider_uuid: None, backend_uuid: _BACKEND_TO_CONSUMER},
    )


def provider_counterparts(
    *, session_id: str, consumer_uuid: str, backend_uuid: str
) -> Counterparts:
    return Counterparts(
        session_id=session_id,
        backend_uuid=backend_uuid,
        accepted={consumer_uuid: _CONSUMER_TO_PROVIDER, backend_uuid: _BACKEND_TO_PROVIDER},
    )


class InboundKind(enum.Enum):
    DROP = "drop"
    NOTICE = "notice"
    SEQUENCED = "sequenced"


@dataclass(frozen=True)
class Inbound:
    kind: InboundKind
    seq: int = wire.BACKEND_NOTICE_SEQ


_DROP = Inbound(InboundKind.DROP)
_NOTICE = Inbound(InboundKind.NOTICE)


def classify_inbound(counterparts: Counterparts, publisher: Optional[str], message: Any) -> Inbound:
    """Drop a message, act on it now (backend notices), or sequence it by ``seq``."""
    if not isinstance(message, dict):
        return _DROP
    message_type = message.get("type")
    if not isinstance(message_type, str) or not counterparts.accepts(publisher, message_type):
        return _DROP
    # A route probe is not a session message: its envelope has no sessionId or seq. Only
    # the provider accepts one, and only from the backend.
    if message_type == wire.ROUTE_PROBE:
        return _NOTICE if counterparts.is_backend(publisher) else _DROP
    if message.get("sessionId") != counterparts.session_id:
        return _DROP
    seq = message.get("seq")
    if counterparts.is_backend(publisher):
        is_notice_seq = (
            isinstance(seq, int) and not isinstance(seq, bool) and seq == wire.BACKEND_NOTICE_SEQ
        )
        return _NOTICE if is_notice_seq else _DROP
    return Inbound(InboundKind.SEQUENCED, seq) if is_sender_seq(seq) else _DROP
