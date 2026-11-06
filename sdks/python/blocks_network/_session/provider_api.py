"""The provider's session endpoints: route registration, route token refresh, session lookup
and checkpoints. Requests carry the agent JWT."""

from __future__ import annotations

import json
import math
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, Optional, Sequence, Tuple
from urllib.parse import quote

from .._rate_limit import BlocksHttpError
from ..agent_auth import AgentAuthHttpError, _HttpError
from .errors import SESSION_PAID_AGENT_UNSUPPORTED, SESSIONS_DISABLED
from .token_schedule import is_refusal_status


TASK_ONLY_CODES = frozenset({SESSIONS_DISABLED, SESSION_PAID_AGENT_UNSUPPORTED})

# (method, url, JSON body) -> (status, parsed body, Retry-After seconds); raises only when no
# response arrived.
RequestFn = Callable[[str, str, Optional[Dict[str, Any]]], Tuple[int, Any, Optional[float]]]


class ProviderApiError(BlocksHttpError):
    def __init__(
        self, status: int, code: Optional[str], message: str, retry_after: Optional[float] = None
    ) -> None:
        super().__init__(status, message, retry_after)
        self.code = code


def error_code_of(err: BaseException) -> Optional[str]:
    return err.code if isinstance(err, ProviderApiError) else None


def is_credential_refusal(err: BaseException) -> bool:
    """The agent credential is refused: its own refresh was turned down, or the call still
    answered 401 after that refresh. A refresh that fails in transit is not a refusal."""
    if isinstance(err, AgentAuthHttpError):
        return is_refusal_status(err.status)
    return isinstance(err, ProviderApiError) and err.status == 401


@dataclass(frozen=True)
class RouteGrant:
    route_id: str
    route_channel: str
    pam_token: str
    ttl_minutes: float
    provider_transport_uuid: str
    # Backend notices are accepted from this UUID only, so a route without one is refused.
    backend_transport_uuid: str


@dataclass(frozen=True)
class RouteToken:
    pam_token: str
    ttl_minutes: float


@dataclass(frozen=True)
class SessionManifest:
    session_id: str
    route_id: str
    consumer_transport_uuid: str
    agent_name: str
    org_id: Optional[str]
    owner_id: str
    caller_claims: Dict[str, Any] = field(default_factory=dict)
    backend_transport_uuid: Optional[str] = None


def _is_text(value: Any) -> bool:
    return isinstance(value, str) and len(value) > 0


def _require_strings(body: Dict[str, Any], keys: Sequence[str], what: str) -> None:
    missing = [key for key in keys if not _is_text(body.get(key))]
    if missing:
        raise ValueError(f"{what} is missing {', '.join(missing)}")


def _parse_ttl(value: Any) -> float:
    is_number = isinstance(value, (int, float)) and not isinstance(value, bool)
    if not is_number or not math.isfinite(value) or value <= 0:
        raise ValueError("route token ttlMinutes must be a positive number")
    return value


def _parse_route_grant(body: Any) -> RouteGrant:
    if not isinstance(body, dict):
        raise ValueError("route response is not an object")
    _require_strings(
        body,
        ("routeId", "routeChannel", "pamToken", "providerTransportUuid", "backendTransportUuid"),
        "route",
    )
    return RouteGrant(
        route_id=body["routeId"],
        route_channel=body["routeChannel"],
        pam_token=body["pamToken"],
        ttl_minutes=_parse_ttl(body.get("ttlMinutes")),
        provider_transport_uuid=body["providerTransportUuid"],
        backend_transport_uuid=body["backendTransportUuid"],
    )


def _parse_route_token(body: Any) -> RouteToken:
    if not isinstance(body, dict):
        raise ValueError("route token response is not an object")
    _require_strings(body, ("pamToken",), "route token")
    return RouteToken(pam_token=body["pamToken"], ttl_minutes=_parse_ttl(body.get("ttlMinutes")))


def parse_manifest(value: Any) -> Optional[SessionManifest]:
    if not isinstance(value, dict):
        return None
    ids = ("sessionId", "routeId", "consumerTransportUuid", "agentName", "ownerId")
    if not all(_is_text(value.get(key)) for key in ids):
        return None
    org_id = value.get("orgId", "")
    if org_id is not None and not _is_text(org_id):
        return None
    claims = value.get("callerClaims")
    backend_uuid = value.get("backendTransportUuid")
    return SessionManifest(
        session_id=value["sessionId"],
        route_id=value["routeId"],
        consumer_transport_uuid=value["consumerTransportUuid"],
        agent_name=value["agentName"],
        org_id=org_id,
        owner_id=value["ownerId"],
        caller_claims=claims if isinstance(claims, dict) else {},
        backend_transport_uuid=backend_uuid if _is_text(backend_uuid) else None,
    )


class ProviderSessionApi:
    def __init__(self, base_url: str, request: RequestFn) -> None:
        self._base_url = base_url.rstrip("/")
        self._request_fn = request

    def register_route(self, instance_id: str, max_sessions: int) -> RouteGrant:
        body = {"instanceId": instance_id, "maxSessions": max_sessions}
        return _parse_route_grant(self._request("POST", "/api/v1/provider/routes", body))

    def refresh_route_token(self, route_id: str) -> RouteToken:
        path = f"/api/v1/provider/routes/{quote(route_id, safe='')}/token"
        return _parse_route_token(self._request("POST", path, {}))

    def get_session(self, session_id: str) -> SessionManifest:
        path = f"/api/v1/provider/sessions/{quote(session_id, safe='')}"
        manifest = parse_manifest(self._request("GET", path))
        if manifest is None:
            raise ValueError("session lookup returned an incomplete manifest")
        return manifest

    def post_checkpoint(self, batch: Dict[str, Any]) -> None:
        """Return only on the backend's acknowledgement of this batch; anything else raises."""
        ack = self._request("POST", "/api/v1/provider/checkpoints", batch)
        if (
            not isinstance(ack, dict)
            or ack.get("routeId") != batch["routeId"]
            or ack.get("idempotencyKey") != batch["idempotencyKey"]
        ):
            raise ValueError("checkpoint response is not the acknowledgement of this batch")

    def _request(self, method: str, path: str, body: Optional[Dict[str, Any]] = None) -> Any:
        status, parsed, retry_after = self._request_fn(method, f"{self._base_url}{path}", body)
        if 200 <= status < 300:
            return parsed
        fields = parsed if isinstance(parsed, dict) else {}
        code = fields.get("code") if _is_text(fields.get("code")) else None
        message = fields.get("error") if _is_text(fields.get("error")) else f"HTTP {status}"
        raise ProviderApiError(status, code, f"{method} {path} failed: {message}", retry_after)


def agent_auth_request(agent_auth: Any) -> RequestFn:
    """A :data:`RequestFn` over ``AgentAuth.authenticated_request``, which raises on non-2xx."""

    def request(
        method: str, url: str, body: Optional[Dict[str, Any]]
    ) -> Tuple[int, Any, Optional[float]]:
        data = None if body is None else json.dumps(body).encode("utf-8")
        headers = None if body is None else {"Content-Type": "application/json"}
        try:
            parsed, status = agent_auth.authenticated_request(
                url, method=method, body=data, headers=headers
            )
        except _HttpError as exc:
            return exc.status, exc.body, exc.retry_after
        return status, parsed, None

    return request
