"""
AuthProvider protocol and StaticAuthProvider for consumer/static JWT auth.

Provides a shared interface used by ALL authenticated SDK paths (RPC,
file upload, task-read-token helper). Static JWT strings are wrapped in
StaticAuthProvider; ConsumerAuth implements the same protocol with
transparent refresh.
"""

from __future__ import annotations

from typing import Any, Optional, Protocol, runtime_checkable


@runtime_checkable
class AuthProvider(Protocol):
    """Protocol for objects that supply Authorization headers.

    Implementations must provide:
    - get_auth_header(): returns the current Authorization header value
      (e.g. "Bearer <jwt>"), or None if no token is available.
    - on_auth_failure(): called by the transport when a request receives
      a 401. Returns True if a fresh token was acquired (caller should
      retry the request), False if no refresh is possible.

    Implementations may optionally provide:
    - get_last_auth_error(): returns the recorded permanent-refresh error
      when the provider is in a known-broken state (e.g. ConsumerAuth
      proactive refresh exhausted its retries), or None otherwise. The
      transport calls this before issuing any authenticated request and
      raises the error directly so consumers see the typed
      ``AuthRefreshFailedError`` instead of an opaque downstream 401.
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


def preflight_auth_or_raise(provider: Optional[Any]) -> None:
    """Pre-flight gate used by authenticated SDK paths.

    Mirrors Node's ``preflightAuthOrThrow`` in ``runtime/auth-provider.ts``.

    Behavior:
    - No provider, no ``get_last_auth_error``, or no recorded error ->
      returns silently.
    - Error recorded -> attempts one reactive refresh via
      ``on_auth_failure()``. On success the error clears atomically with
      the new token apply (``ConsumerAuth._store_result``) and this
      returns silently so the caller proceeds with the refreshed token.
      On failure the error stays recorded and this raises it so the
      caller sees the typed ``AuthRefreshFailedError`` instead of an
      opaque downstream 401.

    The two-phase shape (record -> retry -> raise) preserves the
    documented reactive recovery path: a transient outage that exhausts
    proactive retries and then resolves still lets the next authenticated
    call recover, instead of permanently wedging the client until it is
    rebuilt.
    """
    if provider is None:
        return
    if not hasattr(provider, "get_last_auth_error"):
        return
    initial = provider.get_last_auth_error()
    if initial is None:
        return
    provider.on_auth_failure()
    remaining = provider.get_last_auth_error()
    if remaining is not None:
        raise remaining
