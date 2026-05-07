"""
AuthProvider protocol and StaticAuthProvider for consumer/static JWT auth.

Provides a shared interface used by ALL authenticated SDK paths (RPC,
file upload, task-read-token helper). Static JWT strings are wrapped in
StaticAuthProvider; ConsumerAuth implements the same protocol with
transparent refresh.
"""

from __future__ import annotations

from typing import Optional, Protocol, runtime_checkable


@runtime_checkable
class AuthProvider(Protocol):
    """Protocol for objects that supply Authorization headers.

    Implementations must provide:
    - get_auth_header(): returns the current Authorization header value
      (e.g. "Bearer <jwt>"), or None if no token is available.
    - on_auth_failure(): called by the transport when a request receives
      a 401. Returns True if a fresh token was acquired (caller should
      retry the request), False if no refresh is possible.
    """

    def get_auth_header(self) -> Optional[str]: ...

    def on_auth_failure(self) -> bool: ...

    def ensure_ready(self) -> None: ...


class StaticAuthProvider:
    """Wraps a static JWT string. No refresh capability.

    on_auth_failure() always returns False because there is no mechanism
    to acquire a new token -- the 401 propagates to the caller.
    """

    def __init__(self, token: str) -> None:
        self._token = token

    def get_auth_header(self) -> str:
        return f"Bearer {self._token}"

    def on_auth_failure(self) -> bool:
        return False

    def ensure_ready(self) -> None:
        pass
