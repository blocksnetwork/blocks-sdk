"""The backend's record of a session, read during recovery, and how recorded refs merge."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, List, Optional

from . import wire

_SNAPSHOT_STATES = frozenset(
    {wire.STATE_OPEN, wire.STATE_READY, wire.STATE_CLOSING, *wire.TERMINAL_STATES}
)


@dataclass(frozen=True)
class SnapshotExchange:
    """One settled exchange as recorded; ``artifact_refs`` holds file-type refs only."""

    exchange_id: str
    state: str
    error: Optional[str] = None
    artifact_refs: List[Any] = field(default_factory=list)


@dataclass(frozen=True)
class SessionSnapshot:
    session_id: str
    state: str
    reason: Optional[str] = None
    error: Optional[str] = None
    exchanges: List[SnapshotExchange] = field(default_factory=list)


def _text(value: Any) -> Optional[str]:
    return value if isinstance(value, str) else None


def _is_one_of(value: Any, allowed: frozenset) -> bool:
    return isinstance(value, str) and value in allowed


def _is_recorded_exchange(value: Any) -> bool:
    return (
        isinstance(value, dict)
        and isinstance(value.get("exchangeId"), str)
        and _is_one_of(value.get("state"), wire.OUTCOME_STATES)
        and isinstance(value.get("artifactRefs"), list)
        and all(_is_file_ref(ref) for ref in value["artifactRefs"])
    )


def _is_file_ref(ref: Any) -> bool:
    """A recorded ref is a file ref, matched to received ones on its ``fileId``."""
    return isinstance(ref, dict) and isinstance(ref.get("fileId"), str)


def parse_snapshot(body: Any) -> SessionSnapshot:
    """Parse ``GET /api/v1/sessions/{sessionId}``; raise ``ValueError`` on any other shape."""
    value = body if isinstance(body, dict) else {}
    exchanges = value.get("exchanges")
    if (
        not isinstance(value.get("sessionId"), str)
        or not _is_one_of(value.get("state"), _SNAPSHOT_STATES)
        or not isinstance(exchanges, list)
        or not all(_is_recorded_exchange(x) for x in exchanges)
    ):
        raise ValueError("GET /api/v1/sessions/{sessionId} returned an invalid snapshot")
    return SessionSnapshot(
        session_id=value["sessionId"],
        state=value["state"],
        reason=_text(value.get("reason")),
        error=_text(value.get("error")),
        exchanges=[
            SnapshotExchange(x["exchangeId"], x["state"], _text(x.get("error")), x["artifactRefs"])
            for x in exchanges
        ],
    )


def _file_id(ref: Any) -> Optional[str]:
    file_id = ref.get("fileId") if isinstance(ref, dict) else None
    return file_id if isinstance(file_id, str) else None


def merge_artifacts(received: List[Any], recorded: List[Any]) -> List[Any]:
    """The artifacts already received, then each recorded one not among them.

    A recorded ref is a file ref, whose keys may come back in another order, so file refs
    match on ``fileId``; a recorded ref without one is kept.
    """
    seen = {file_id for file_id in map(_file_id, received) if file_id is not None}
    return [*received, *(ref for ref in recorded if _file_id(ref) not in seen)]
