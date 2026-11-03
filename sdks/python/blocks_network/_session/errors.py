"""Session errors, mirroring the Node SDK's: ``code`` is the backend or channel error code."""

from __future__ import annotations

from typing import Any, Callable, Dict, Optional

SESSIONS_DISABLED = "SESSIONS_DISABLED"
SESSION_PAID_AGENT_UNSUPPORTED = "SESSION_PAID_AGENT_UNSUPPORTED"
SESSION_NO_ROUTE = "SESSION_NO_ROUTE"
SESSION_NOT_FOUND = "SESSION_NOT_FOUND"
ROUTE_LOST = "ROUTE_LOST"


class SessionError(Exception):
    """Base class of every session error."""

    def __init__(self, code: str, message: Optional[str] = None) -> None:
        super().__init__(message or code)
        self.code = code


class SessionsDisabledError(SessionError):
    """Sessions are not enabled for this deployment, the caller's org, or the agent's org."""

    def __init__(self, message: Optional[str] = None) -> None:
        super().__init__(SESSIONS_DISABLED, message or "Sessions are not enabled")


class SessionPaidAgentError(SessionError):
    """Sessions serve free agents only."""

    def __init__(self, message: Optional[str] = None) -> None:
        super().__init__(
            SESSION_PAID_AGENT_UNSUPPORTED, message or "Sessions do not support paid agents"
        )


class SessionNoRouteError(SessionError):
    """The agent has no live, healthy route with capacity."""

    def __init__(self, message: Optional[str] = None) -> None:
        super().__init__(
            SESSION_NO_ROUTE, message or "The agent has no route available for a session"
        )


class SessionNotFoundError(SessionError):
    """Unknown session, or one the caller is not a party to."""

    def __init__(self, message: Optional[str] = None) -> None:
        super().__init__(SESSION_NOT_FOUND, message or "Session not found")


class SessionBackpressureError(SessionError):
    """``send()`` would exceed the session's pending-exchange bound."""

    def __init__(self, limit: int) -> None:
        super().__init__(
            "session_backpressure", f"A session holds at most {limit} pending exchanges"
        )


class SessionFailedError(SessionError):
    """The session failed; ``code`` is the failure reason, such as ``route_lost``."""

    def __init__(self, code: str) -> None:
        super().__init__(code, f"Session failed: {code}")


_HTTP_ERRORS: Dict[str, Callable[[Optional[str]], SessionError]] = {
    SESSIONS_DISABLED: SessionsDisabledError,
    SESSION_PAID_AGENT_UNSUPPORTED: SessionPaidAgentError,
    SESSION_NO_ROUTE: SessionNoRouteError,
    SESSION_NOT_FOUND: SessionNotFoundError,
}


def session_error_from_response(code: Any, message: Any) -> Optional[SessionError]:
    """Map a session endpoint's ``{code, error}`` refusal to its typed error, else ``None``."""
    build = _HTTP_ERRORS.get(code) if isinstance(code, str) else None
    return build(message if isinstance(message, str) else None) if build else None
