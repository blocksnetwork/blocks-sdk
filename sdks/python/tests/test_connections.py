"""Node mirror: sdks/node/tests/connections.test.ts. Stubs urlopen so the real AgentAuth runs."""

from __future__ import annotations

import json
import threading
import time
import urllib.error
from io import BytesIO
from typing import Any, Dict, List, Optional, Tuple
from unittest.mock import MagicMock, patch

import pytest

from blocks_network import (
    AgentAuth,
    ConnectionAccessError,
    ConnectionRequiredError,
    ConnectionToken,
)
from blocks_network.agent_instance import start_agent_instance
from blocks_network.protocol_version import PROTOCOL_VERSION_HEADER
from blocks_network.types import AgentInstanceOptions, TaskContext

from tests.conftest import minimal_card
from tests.test_agent_instance_p3 import _make_mock_pubnub, _simulate_start_task

BASE_URL = "http://backend.test"
TOKEN_URL = f"{BASE_URL}/api/v1/connections/token"

SALESFORCE_TOKEN = {
    "accessToken": "sf-access",
    "tokenType": "Bearer",
    "expiresAt": "2026-10-21T00:00:00Z",
    "scopes": ["api", "refresh_token"],
    "extra": {"instance_url": "https://acme.my.salesforce.com"},
}


class _Backend:
    def __init__(self, token_status: int = 200, token_body: Any = None) -> None:
        self.token_status = token_status
        self.token_body = SALESFORCE_TOKEN if token_body is None else token_body
        self.requests: List[Any] = []

    def __call__(self, req: Any, **_kwargs: Any) -> Any:
        self.requests.append(req)
        if req.full_url.endswith("/auth/agent/connect"):
            return self._response(201, {"accessToken": "agent-jwt", "refreshToken": "rt"})
        if req.full_url == TOKEN_URL:
            return self._response(self.token_status, self.token_body)
        raise AssertionError(f"unexpected request to {req.full_url}")

    @staticmethod
    def _response(status: int, body: Any) -> Any:
        raw = body if isinstance(body, bytes) else json.dumps(body).encode("utf-8")
        if status >= 400:
            raise urllib.error.HTTPError(TOKEN_URL, status, "Error", {}, BytesIO(raw))
        resp = MagicMock()
        resp.read.return_value = raw
        resp.status = status
        resp.headers = {}
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        return resp

    def token_requests(self) -> List[Tuple[Any, Dict[str, Any]]]:
        return [
            (r, json.loads(r.data)) for r in self.requests if r.full_url == TOKEN_URL
        ]


def _patch_urlopen(backend: _Backend) -> Any:
    return patch("blocks_network.agent_auth.urllib.request.urlopen", side_effect=backend)


def _ctx(backend: _Backend) -> TaskContext:
    auth = AgentAuth("bk_test", BASE_URL)
    with _patch_urlopen(backend):
        auth.init({"agentName": "test_agent"})
    return TaskContext(task_id="task-123", _agent_auth=auth, _base_url=BASE_URL)


def _run_handler(options: Dict[str, Any], body) -> Any:
    pn = _make_mock_pubnub()
    outcome: Dict[str, Any] = {}
    done = threading.Event()

    def handler(_task, ctx):
        try:
            outcome["value"] = body(ctx)
        except Exception as exc:
            outcome["error"] = exc
        finally:
            done.set()

    with patch("blocks_network.agent_instance.create_pubnub_client", return_value=_make_mock_pubnub()):
        instance = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(), pubnub=pn, agent_name="test_agent", handler=handler, **options
            )
        )
        try:
            time.sleep(0.2)
            _simulate_start_task(pn, task_id="task-1")
            assert done.wait(timeout=2.0)
        finally:
            instance["stop"]()
    return outcome


class TestRuntimeWiring:
    def test_handler_ctx_fetches_token_for_its_task_with_agent_bearer(self) -> None:
        backend = _Backend()

        def connected_auth(*args: Any, **kwargs: Any) -> AgentAuth:
            auth = AgentAuth(*args, **kwargs)
            auth.init({"agentName": "test_agent"})
            return auth

        with patch("blocks_network.agent_instance.AgentAuth", side_effect=connected_auth):
            with _patch_urlopen(backend):
                outcome = _run_handler(
                    {"base_url": f"{BASE_URL}/"},
                    lambda ctx: ctx.connections.get_token("salesforce"),
                )

        assert "error" not in outcome, outcome.get("error")
        assert outcome["value"] == ConnectionToken(
            access_token="sf-access",
            token_type="Bearer",
            expires_at="2026-10-21T00:00:00Z",
            scopes=["api", "refresh_token"],
            extra={"instance_url": "https://acme.my.salesforce.com"},
        )
        [(req, body)] = backend.token_requests()
        assert req.get_method() == "POST"
        assert req.get_header("Authorization") == "Bearer agent-jwt"
        assert req.has_header(PROTOCOL_VERSION_HEADER.capitalize())
        assert body == {"taskId": "task-1", "integration": "salesforce"}

    def test_connections_without_base_url_raises_clear_error(self) -> None:
        outcome = _run_handler({}, lambda ctx: ctx.connections)

        assert isinstance(outcome.get("error"), RuntimeError)
        assert "require authentication" in str(outcome["error"])


class TestGetToken:
    @pytest.mark.parametrize("force_refresh", [True, False])
    def test_force_refresh_sent_when_passed(self, force_refresh: bool) -> None:
        backend = _Backend()
        ctx = _ctx(backend)

        with _patch_urlopen(backend):
            ctx.connections.get_token("github", force_refresh=force_refresh)

        [(_req, body)] = backend.token_requests()
        assert body == {"taskId": "task-123", "integration": "github", "forceRefresh": force_refresh}

    def test_auth_required_raises_connection_required_with_details(self) -> None:
        backend = _Backend(
            403,
            {
                "code": "AuthRequired",
                "details": {
                    "integration": "salesforce",
                    "reason": "not_connected",
                    "connectUrl": "https://app.test/connect/salesforce",
                },
            },
        )
        ctx = _ctx(backend)

        with _patch_urlopen(backend), pytest.raises(ConnectionRequiredError) as info:
            ctx.connections.get_token("salesforce")

        assert info.value.integration == "salesforce"
        assert info.value.reason == "not_connected"
        assert info.value.connect_url == "https://app.test/connect/salesforce"

    @pytest.mark.parametrize(
        ("status", "body", "expected_code"),
        [
            (403, {"code": "AuthRequired"}, "AuthRequired"),
            (403, {"code": "AuthRequired", "details": {"integration": "salesforce"}}, "AuthRequired"),
            (403, {"code": "Forbidden"}, "Forbidden"),
            (404, {"code": "NotFound"}, "NotFound"),
            (500, b"not json", None),
        ],
        ids=["auth-required-no-details", "auth-required-partial-details", "forbidden", "not-found", "non-json"],
    )
    def test_other_failures_raise_connection_access_error(
        self, status: int, body: Any, expected_code: Optional[str]
    ) -> None:
        backend = _Backend(status, body)
        ctx = _ctx(backend)

        with _patch_urlopen(backend), pytest.raises(ConnectionAccessError) as info:
            ctx.connections.get_token("salesforce")

        assert info.value.status == status
        assert info.value.code == expected_code
