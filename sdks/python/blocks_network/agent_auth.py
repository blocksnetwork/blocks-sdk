"""
AgentAuth -- API key-based authentication for agent instances.

Exchanges a long-lived API key (BLOCKS_API_KEY) for short-lived JWTs
via POST /api/v1/auth/agent/connect, handles transparent token refresh
with threading.Lock-based mutex to prevent concurrent refresh requests.

Cross-SDK parity with packages/node/src/runtime/agent-auth.ts.
"""

from __future__ import annotations

import json
import logging
import ssl
import threading
import urllib.error
import urllib.request
from typing import Any, Dict, List, Literal, Optional, Tuple, TypedDict

import certifi

from .protocol_version import CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER
from .write_affinity import capture_affinity, inject_affinity

logger = logging.getLogger(__name__)

# ============================================================================
# Error codes returned by the backend
# ============================================================================

ERROR_CODE_API_KEY_INVALID = "API_KEY_INVALID"
ERROR_CODE_REFRESH_TOKEN_INVALID = "REFRESH_TOKEN_INVALID"


# ============================================================================
# Connect payload type (TypedDict at the SDK <-> backend boundary)
# ============================================================================


class RegistrationPayload(TypedDict, total=False):
    """Wire shape for POST /api/v1/auth/agent/connect.

    Mirrors the Node SDK ``RegistrationPayload`` interface. Documented as
    a ``TypedDict`` so ``billingMode`` is typed at the SDK <-> backend
    boundary and not smuggled through a bare ``Dict[str, Any]``. All
    fields are optional in the structural type (``total=False``); the
    backend Zod schema validates required-ness. The SDK serializes a
    partial dict and strips ``None`` before sending.

    Wire fields use camelCase per IMPL §3.4. ``billingMode`` is REQUIRED
    on every real connect request (Phase 2 of the Billing Mode Contract
    initiative) and is populated by the SDK from the registry GET at
    boot. There is no provider-supplied override.
    """

    agentName: str
    instanceId: str
    billingMode: Literal["free", "paid"]
    listing: str
    expectedInstances: int
    concurrency: int
    maxPendingBacklog: int
    maxRunningTimeSec: int
    deviceOs: str
    sdkLanguage: str
    sdkVersion: str
    protocolVersions: List[str]
    preferredProtocolVersion: str
    cliVersion: str


# ============================================================================
# Exceptions
# ============================================================================


class AgentAuthFatalError(RuntimeError):
    """Raised when the API key is permanently invalid (revoked, expired, etc.).

    This is a fatal error -- the agent should shut down.
    """


# ============================================================================
# AgentAuth
# ============================================================================


class AgentAuth:
    """API key-based agent authentication with thread-safe refresh.

    Parameters
    ----------
    api_key:
        The long-lived API key (e.g. ``bk_...``).
    base_url:
        Backend base URL (e.g. ``https://api.example.com``).
    """

    def __init__(self, api_key: str, base_url: str) -> None:
        if not api_key:
            raise ValueError("api_key is required")
        if not base_url:
            raise ValueError("base_url is required")
        self._api_key = api_key
        self._base_url = base_url.rstrip("/")
        self._access_token: Optional[str] = None
        self._refresh_token: Optional[str] = None
        self._registration_payload: Optional[RegistrationPayload] = None
        self._lock = threading.Lock()

    # -- Public API ---------------------------------------------------------

    def init(self, registration_payload: Optional[RegistrationPayload] = None) -> Dict[str, Any]:
        """Connect the agent and obtain initial JWT + refresh token.

        Calls POST /api/v1/auth/agent/connect with the API key.
        The payload is stored for re-connection on refresh token
        invalidation.

        Parameters
        ----------
        registration_payload:
            Dict with agent connect fields (name, instanceId, scaling,
            etc.). Stored for re-connection fallback. If ``None``, reuses
            the previously stored payload.

        Returns
        -------
        dict
            The full connect response including agent data,
            ``accessToken``, ``refreshToken``, and ``expiresIn``.

        Raises
        ------
        AgentAuthFatalError
            If the API key is invalid, expired, or revoked.
        """
        if registration_payload is not None:
            self._registration_payload = registration_payload

        url = f"{self._base_url}/api/v1/auth/agent/connect"
        body_bytes = (
            json.dumps(self._registration_payload).encode("utf-8")
            if self._registration_payload
            else None
        )

        try:
            data = self._post(url, body=body_bytes, headers=self._api_key_headers())
        except _HttpError as exc:
            error_msg = (
                exc.body.get("error", f"HTTP {exc.status}")
                if isinstance(exc.body, dict)
                else f"HTTP {exc.status}"
            )
            raise AgentAuthFatalError(
                f"Agent connect failed: {error_msg}"
            ) from exc

        self._access_token = data["accessToken"]
        self._refresh_token = data["refreshToken"]
        return data

    def get_access_token(self) -> Optional[str]:
        """Return the current access token (JWT), or None if not initialized."""
        return self._access_token

    def get_api_key(self) -> str:
        """Return the API key."""
        return self._api_key

    def refresh(self) -> None:
        """Refresh the JWT.

        Uses ``threading.Lock`` so only one thread refreshes at a time;
        other threads wait for the lock holder to finish.
        """
        with self._lock:
            self._do_refresh()

    # -- Private helpers ----------------------------------------------------

    def _do_refresh(self) -> None:
        """Perform the actual refresh call.

        On ``REFRESH_TOKEN_INVALID``, falls back to re-connection via
        ``init()`` with the stored connect payload.
        On ``API_KEY_INVALID``, raises ``AgentAuthFatalError``.
        """
        url = f"{self._base_url}/api/v1/auth/agent/refresh"
        body = json.dumps({"refreshToken": self._refresh_token}).encode("utf-8")

        headers = self._api_key_headers()
        headers["Content-Type"] = "application/json"

        try:
            data = self._post(url, body=body, headers=headers)
            self._access_token = data["accessToken"]
            self._refresh_token = data["refreshToken"]
            return
        except _HttpError as exc:
            error_body = exc.body
            code = error_body.get("code", "") if isinstance(error_body, dict) else ""

            if code == ERROR_CODE_REFRESH_TOKEN_INVALID:
                # Refresh token invalid -- re-connect with stored payload
                self.init()
                return

            if code == ERROR_CODE_API_KEY_INVALID:
                raise AgentAuthFatalError(
                    f"API key invalid or revoked: {error_body.get('error', 'API_KEY_INVALID')}"
                ) from exc

            # Unknown error
            raise RuntimeError(
                f"Token refresh failed: {error_body.get('error', f'HTTP {exc.status}') if isinstance(error_body, dict) else f'HTTP {exc.status}'}"
            ) from exc

    def authenticated_request(
        self,
        url: str,
        method: str = "GET",
        body: Optional[bytes] = None,
        headers: Optional[Dict[str, str]] = None,
    ) -> Tuple[Dict[str, Any], int]:
        """Make an authenticated HTTP request with automatic 401 retry.

        Attaches the Bearer token and retries once on 401 after refreshing.
        Every HTTP call through this class (data-plane and auth-plane)
        echoes the cached ``X-Write-Affinity`` header on the outgoing
        request and captures it from the response, so write-affinity is
        never dropped across connect/refresh/data-plane transitions.

        Returns
        -------
        tuple of (parsed JSON body, HTTP status code)
        """
        token = self._access_token
        if not token:
            raise RuntimeError("AgentAuth not initialized -- call init() first")

        req_headers = dict(headers) if headers else {}
        req_headers["Authorization"] = f"Bearer {token}"
        req_headers.setdefault(PROTOCOL_VERSION_HEADER, CURRENT_PROTOCOL_VERSION)
        inject_affinity(req_headers)

        try:
            return self._request(
                url,
                method=method,
                body=body,
                headers=req_headers,
            )
        except _HttpError as exc:
            if exc.status != 401:
                raise

        # 401 -- refresh and retry once
        self.refresh()

        new_token = self._access_token
        if not new_token:
            raise RuntimeError("Failed to obtain access token after refresh")

        req_headers["Authorization"] = f"Bearer {new_token}"
        inject_affinity(req_headers)
        return self._request(
            url,
            method=method,
            body=body,
            headers=req_headers,
        )

    # -- HTTP helpers -------------------------------------------------------

    def _api_key_headers(self) -> Dict[str, str]:
        headers = {
            "Authorization": f"Bearer {self._api_key}",
            "Content-Type": "application/json",
            PROTOCOL_VERSION_HEADER: CURRENT_PROTOCOL_VERSION,
        }
        inject_affinity(headers)
        return headers

    def _post(
        self,
        url: str,
        body: Optional[bytes] = None,
        headers: Optional[Dict[str, str]] = None,
    ) -> Dict[str, Any]:
        """POST request returning parsed JSON. Raises _HttpError on non-2xx."""
        result, _status = self._request(url, method="POST", body=body, headers=headers)
        return result

    def _request(
        self,
        url: str,
        method: str = "GET",
        body: Optional[bytes] = None,
        headers: Optional[Dict[str, str]] = None,
    ) -> Tuple[Dict[str, Any], int]:
        """Low-level HTTP request returning (parsed JSON, status code).

        Always captures ``X-Write-Affinity`` from successful responses. The
        backend sets it on every successful mutation -- including the
        anonymous auth-plane endpoints (``/auth/agent/connect``,
        ``/auth/agent/refresh``) -- precisely so the SDK's first follow-up
        data-plane read hits primary after a fresh connect or refresh.
        """
        ssl_ctx = ssl.create_default_context(cafile=certifi.where())
        req = urllib.request.Request(
            url,
            data=body,
            headers=headers or {},
            method=method,
        )

        try:
            with urllib.request.urlopen(req, context=ssl_ctx, timeout=30) as resp:
                capture_affinity(resp)
                resp_body = resp.read().decode("utf-8")
                if not resp_body:
                    return {}, resp.status
                return json.loads(resp_body), resp.status
        except urllib.error.HTTPError as exc:
            error_body: Any = {}
            try:
                raw = exc.read().decode("utf-8") if exc.fp else ""
                if raw:
                    error_body = json.loads(raw)
            except (json.JSONDecodeError, ValueError):
                pass

            raise _HttpError(exc.code, error_body) from exc
        except urllib.error.URLError as exc:
            raise RuntimeError(f"Request failed: {exc.reason}") from exc


class _HttpError(Exception):
    """Internal exception carrying HTTP status and parsed body."""

    def __init__(self, status: int, body: Any = None) -> None:
        super().__init__(f"HTTP {status}")
        self.status = status
        self.body = body
