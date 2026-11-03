"""End-user OAuth access tokens for a task; Node parity: sdks/node/src/runtime/connections.ts."""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any, Dict, List, Optional, Tuple

from .agent_auth import _HttpError

if TYPE_CHECKING:
    from .agent_auth import AgentAuth

CONNECTIONS_TOKEN_PATH = "/api/v1/connections/token"
AUTH_REQUIRED_CODE = "AuthRequired"


@dataclass
class ConnectionToken:
    access_token: str
    token_type: str
    expires_at: str
    scopes: List[str] = field(default_factory=list)
    extra: Dict[str, Any] = field(default_factory=dict)


class ConnectionRequiredError(Exception):
    """The end user must connect or approve the integration at ``connect_url``."""

    def __init__(self, integration: str, reason: str, connect_url: str) -> None:
        super().__init__(f"Integration connection required: {integration} ({reason})")
        self.integration = integration
        self.reason = reason
        self.connect_url = connect_url


class ConnectionAccessError(Exception):
    """The backend refused or failed the token request."""

    def __init__(self, status: int, code: Optional[str] = None) -> None:
        suffix = f" ({code})" if code else ""
        super().__init__(f"Connection access error: {status}{suffix}")
        self.status = status
        self.code = code


class ConnectionsClient:
    def __init__(self, agent_auth: "AgentAuth", base_url: str, task_id: str) -> None:
        self._agent_auth = agent_auth
        self._url = f"{base_url.rstrip('/')}{CONNECTIONS_TOKEN_PATH}"
        self._task_id = task_id

    def get_token(
        self, integration: str, *, force_refresh: Optional[bool] = None
    ) -> ConnectionToken:
        """Raises :class:`ConnectionRequiredError` to connect or approve, else :class:`ConnectionAccessError`."""
        body: Dict[str, Any] = {"taskId": self._task_id, "integration": integration}
        if force_refresh is not None:
            body["forceRefresh"] = force_refresh

        try:
            data, _status = self._agent_auth.authenticated_request(
                self._url,
                method="POST",
                body=json.dumps(body).encode("utf-8"),
                headers={"Content-Type": "application/json"},
            )
        except _HttpError as exc:
            raise _error_from_response(exc.status, exc.body) from exc

        return ConnectionToken(
            access_token=data["accessToken"],
            token_type=data["tokenType"],
            expires_at=data["expiresAt"],
            scopes=data.get("scopes") or [],
            extra=data.get("extra") or {},
        )


def _error_from_response(status: int, body: Any) -> Exception:
    error_body = body if isinstance(body, dict) else {}
    code = error_body.get("code")
    code = code if isinstance(code, str) else None
    details = error_body.get("details")

    if status == 403 and code == AUTH_REQUIRED_CODE:
        fields = parse_auth_required_details(details)
        if fields is not None:
            return ConnectionRequiredError(*fields)

    return ConnectionAccessError(status, code)


def parse_auth_required_details(details: Any) -> Optional[Tuple[str, str, str]]:
    """Return ``(integration, reason, connect_url)``, or ``None`` when malformed."""
    if not isinstance(details, dict):
        return None
    fields = (details.get("integration"), details.get("reason"), details.get("connectUrl"))
    if not all(isinstance(v, str) for v in fields):
        return None
    return fields  # type: ignore[return-value]


def create_connections_client(
    agent_auth: "AgentAuth", base_url: str, task_id: str
) -> ConnectionsClient:
    return ConnectionsClient(agent_auth, base_url, task_id)
