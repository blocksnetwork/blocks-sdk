"""Session wire vocabulary shared by the consumer and provider receivers."""

from __future__ import annotations

SESSION_OPEN = "session_open"
SESSION_READY = "session_ready"
EXCHANGE = "exchange"
EXCHANGE_CANCEL = "exchange_cancel"
EXCHANGE_PROGRESS = "exchange_progress"
EXCHANGE_ARTIFACT = "exchange_artifact"
EXCHANGE_OUTCOME = "exchange_outcome"
SESSION_CLOSE = "session_close"
SESSION_CLOSED = "session_closed"
SESSION_FAILED = "session_failed"
ROUTE_PROBE = "route_probe"

STATE_OPEN = "open"
STATE_READY = "ready"
STATE_CLOSING = "closing"
STATE_CLOSED = "closed"
STATE_FAILED = "failed"
STATE_CANCELED = "canceled"

TERMINAL_STATES = frozenset({STATE_CLOSED, STATE_FAILED, STATE_CANCELED})

OUTCOME_COMPLETED = "completed"
OUTCOME_FAILED = "failed"
OUTCOME_CANCELED = "canceled"
OUTCOME_STATES = frozenset({OUTCOME_COMPLETED, OUTCOME_FAILED, OUTCOME_CANCELED})

BACKEND_NOTICE_SEQ = 0
# Node reads seq as a JS number, so both SDKs stop at its largest exact integer.
MAX_SEQ = 2**53 - 1
MAX_EXCHANGE_ID_LENGTH = 64
