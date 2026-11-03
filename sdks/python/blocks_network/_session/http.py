"""The consumer's session endpoints on the Blocks backend, and the request helper they share."""

from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import TimeoutError as FutureTimeout
from dataclasses import dataclass
from typing import Any, Dict, Optional, Tuple

from ..auth_provider import preflight_auth_or_raise
from ..protocol_version import CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER
from ..rpc_client import _merge_rpc_headers
from ..tls import https_context
from ..write_affinity import capture_affinity, inject_affinity
from . import wire
from .errors import SessionError, session_error_from_response
from .futures import spawn
from .snapshot import SessionSnapshot, parse_snapshot
from .token_schedule import is_refusal_status

REQUEST_TIMEOUT_S = 30.0
SNAPSHOT_TIMEOUT_S = 10.0

BEACON_OPENED = "opened"
BEACON_CLOSED = "closed"

_KNOWN_STATES = frozenset(
    {wire.STATE_OPEN, wire.STATE_READY, wire.STATE_CLOSING, *wire.TERMINAL_STATES}
)
_URL_SCHEMES = frozenset({"http", "https"})


class SessionHttpError(Exception):
    """A session endpoint answered with an HTTP error that carries no session code."""

    def __init__(self, status: int, message: str) -> None:
        super().__init__(message)
        self.status = status


def is_refusal(err: BaseException) -> bool:
    """True when the backend answered and turned the call down, as opposed to failing in transit."""
    if isinstance(err, SessionError):
        return True
    return isinstance(err, SessionHttpError) and is_refusal_status(err.status)


@dataclass(frozen=True)
class OpenedSession:
    """What ``POST /api/v1/sessions`` returns: the session, its channel, and the consumer's credential."""

    session_id: str
    session_channel: str
    pam_token: str
    ttl_minutes: float
    consumer_transport_uuid: str
    provider_transport_uuid: str
    backend_transport_uuid: str
    # Set when the backend answered a repeated idempotency key with the session it already opened.
    idempotent: bool = False
    state: Optional[str] = None


@dataclass(frozen=True)
class SessionToken:
    pam_token: str
    ttl_minutes: float


_OPENED_STRINGS = (
    ("sessionId", "session_id"),
    ("sessionChannel", "session_channel"),
    ("pamToken", "pam_token"),
    ("consumerTransportUuid", "consumer_transport_uuid"),
    ("providerTransportUuid", "provider_transport_uuid"),
    ("backendTransportUuid", "backend_transport_uuid"),
)


def _is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def parse_opened_session(body: Any) -> OpenedSession:
    value = body if isinstance(body, dict) else {}
    invalid = [
        wire_name
        for wire_name, _ in _OPENED_STRINGS
        if not isinstance(value.get(wire_name), str) or not value.get(wire_name)
    ]
    if not _is_number(value.get("ttlMinutes")):
        invalid.append("ttlMinutes")
    idempotent = value.get("idempotent")
    if idempotent is not None and not isinstance(idempotent, bool):
        invalid.append("idempotent")
    # A replay's state decides whether it may be joined, so an unknown one is refused, not guessed.
    if idempotent is True and value.get("state") not in _KNOWN_STATES:
        invalid.append("state")
    if invalid:
        raise ValueError(f"POST /api/v1/sessions returned an invalid session: {', '.join(invalid)}")
    state = value.get("state")
    return OpenedSession(
        **{field: value[wire_name] for wire_name, field in _OPENED_STRINGS},
        ttl_minutes=value["ttlMinutes"],
        idempotent=idempotent is True,
        state=state if isinstance(state, str) else None,
    )


def parse_session_token(body: Any) -> SessionToken:
    value = body if isinstance(body, dict) else {}
    pam_token = value.get("pamToken")
    ttl_minutes = value.get("ttlMinutes")
    if not isinstance(pam_token, str) or not pam_token:
        raise ValueError("session token response has no pamToken")
    if not _is_number(ttl_minutes) or not ttl_minutes > 0 or ttl_minutes == float("inf"):
        raise ValueError("session token ttlMinutes must be a positive number")
    return SessionToken(pam_token, ttl_minutes)


def _parse_refusal(text: str) -> Dict[str, Any]:
    try:
        parsed = json.loads(text)
    except ValueError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def _send(
    url: str, data: Optional[bytes], headers: Dict[str, str], method: str, timeout: float
) -> Tuple[int, bytes]:
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        # session_request() admits http and https URLs only.
        with urllib.request.urlopen(  # nosec B310
            request, context=https_context(), timeout=timeout
        ) as response:
            capture_affinity(response)
            return response.status, response.read()
    except urllib.error.HTTPError as err:
        return err.code, err.read() or b""


def session_request(
    base_url: str,
    path: str,
    body: Any = None,
    *,
    auth_provider: Any = None,
    headers: Optional[Dict[str, str]] = None,
    method: str = "POST",
    timeout: float = REQUEST_TIMEOUT_S,
) -> Any:
    """Call a session endpoint and return its JSON body, or ``None`` for an empty one.

    Raises the typed session error for a refusal that carries a session code, else
    :class:`SessionHttpError`. A transport failure raises as ``urllib`` raises it.
    """
    preflight_auth_or_raise(auth_provider)
    if auth_provider is not None and hasattr(auth_provider, "ensure_ready"):
        auth_provider.ensure_ready()

    url = f"{base_url.rstrip('/')}{path}"
    if urllib.parse.urlsplit(url).scheme.lower() not in _URL_SCHEMES:
        raise ValueError(f"session backend URL must use http or https: {base_url!r}")
    data = json.dumps(body).encode("utf-8") if method == "POST" else None

    def attempt() -> Tuple[int, bytes]:
        base: Dict[str, str] = {PROTOCOL_VERSION_HEADER: CURRENT_PROTOCOL_VERSION}
        if method == "POST":
            base["Content-Type"] = "application/json"
        auth = auth_provider.get_auth_header() if auth_provider is not None else None
        if auth:
            base["Authorization"] = auth
        merged = _merge_rpc_headers(base, headers)
        inject_affinity(merged)
        return _send(url, data, merged, method, timeout)

    status, raw = attempt()
    if status == 401 and auth_provider is not None and auth_provider.on_auth_failure():
        status, raw = attempt()
    if 200 <= status < 300:
        if status == 204 or not raw:
            return None
        try:
            return json.loads(raw)
        except ValueError:
            return None
    text = raw.decode("utf-8", errors="replace")
    refusal = _parse_refusal(text)
    raise session_error_from_response(refusal.get("code"), refusal.get("error")) or SessionHttpError(
        status, f"{method} {path} failed: HTTP {status}{f' {text}' if text else ''}"
    )


def _session_path(session_id: str, suffix: str = "") -> str:
    return f"/api/v1/sessions/{urllib.parse.quote(session_id, safe='')}{suffix}"


class SessionHttp:
    """The consumer's session endpoints."""

    def __init__(
        self,
        base_url: str,
        auth_provider: Any = None,
        headers: Optional[Dict[str, str]] = None,
    ) -> None:
        self._base_url = base_url
        self._auth_provider = auth_provider
        self._headers = headers

    def _request(self, path: str, body: Any = None, method: str = "POST") -> Any:
        return session_request(
            self._base_url,
            path,
            body,
            auth_provider=self._auth_provider,
            headers=self._headers,
            method=method,
        )

    def open_session(self, agent_name: str, idempotency_key: Optional[str] = None) -> OpenedSession:
        body: Dict[str, Any] = {"agentName": agent_name}
        if idempotency_key is not None:
            body["idempotencyKey"] = idempotency_key
        return parse_opened_session(self._request("/api/v1/sessions", body))

    def refresh_token(self, session_id: str) -> SessionToken:
        return parse_session_token(self._request(_session_path(session_id, "/token"), {}))

    def send_beacon(self, session_id: str, beacon: str) -> None:
        self._request(_session_path(session_id, "/beacons"), {"beacon": beacon})

    def get_session(self, session_id: str, timeout: Optional[float] = None) -> SessionSnapshot:
        """Give up after ``timeout`` seconds, auth preparation included, so a stalled read
        cannot hold a recovery open."""
        limit = SNAPSHOT_TIMEOUT_S if timeout is None else timeout
        reading = spawn(
            lambda: self._request(_session_path(session_id), method="GET"), "blocks-session-snapshot"
        )
        try:
            body = reading.result(timeout=limit)
        except FutureTimeout:
            raise TimeoutError(f"session snapshot did not answer within {limit} s") from None
        snapshot = parse_snapshot(body)
        if snapshot.session_id != session_id:
            raise ValueError(
                "GET /api/v1/sessions/{sessionId} returned the snapshot of another session "
                f"({snapshot.session_id})"
            )
        return snapshot
