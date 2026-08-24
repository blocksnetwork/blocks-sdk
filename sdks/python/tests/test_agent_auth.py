"""
Tests for blocks_network.agent_auth -- AgentAuth class.

Tests mock urllib.request.urlopen to simulate backend responses.
"""

from __future__ import annotations

import json
import threading
from io import BytesIO
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.agent_auth import (
    AgentAuth,
    AgentAuthFatalError,
    ERROR_CODE_AGENT_FORCED_OFFLINE,
    ERROR_CODE_API_KEY_INVALID,
    ERROR_CODE_REFRESH_TOKEN_INVALID,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _mock_registration_response(
    access_token: str = "jwt-abc",
    refresh_token: str = "rt-xyz",
    expires_in: int = 60,
    agent_name: str = "echo",
):
    """Create a mock urlopen that returns a registration response with tokens."""
    resp_data = {
        "agentName": agent_name,
        "name": agent_name,
        "accessToken": access_token,
        "refreshToken": refresh_token,
        "expiresIn": expires_in,
    }
    resp_bytes = json.dumps(resp_data).encode("utf-8")

    def mock_urlopen(req, **kwargs):
        resp = MagicMock()
        resp.read.return_value = resp_bytes
        resp.status = 201
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        return resp

    return mock_urlopen


def _mock_token_response(
    access_token: str = "jwt-abc",
    refresh_token: str = "rt-xyz",
    expires_in: int = 60,
):
    """Create a mock urlopen that returns a token refresh response."""
    resp_data = {
        "accessToken": access_token,
        "refreshToken": refresh_token,
        "expiresIn": expires_in,
    }
    resp_bytes = json.dumps(resp_data).encode("utf-8")

    def mock_urlopen(req, **kwargs):
        resp = MagicMock()
        resp.read.return_value = resp_bytes
        resp.status = 200
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        return resp

    return mock_urlopen


def _mock_error_response(status: int, body: dict):
    """Create a mock urlopen that raises an HTTPError."""
    import urllib.error

    def mock_urlopen(req, **kwargs):
        body_bytes = json.dumps(body).encode("utf-8")
        raise urllib.error.HTTPError(
            req.full_url, status, "Error", {}, BytesIO(body_bytes),
        )

    return mock_urlopen


# ---------------------------------------------------------------------------
# Constructor validation
# ---------------------------------------------------------------------------


class TestAgentAuthInit:
    def test_requires_api_key(self) -> None:
        with pytest.raises(ValueError, match="api_key is required"):
            AgentAuth(api_key="", base_url="http://localhost:8080")

    def test_requires_base_url(self) -> None:
        with pytest.raises(ValueError, match="base_url is required"):
            AgentAuth(api_key="bk_test", base_url="")

    def test_strips_trailing_slash(self) -> None:
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080/")
        assert auth._base_url == "http://localhost:8080"


# ---------------------------------------------------------------------------
# init() -- connects agent and obtains JWT via /auth/agent/connect
# ---------------------------------------------------------------------------


class TestAgentAuthExchange:
    def test_init_calls_auth_agent_connect(self) -> None:
        """init() sends POST to /api/v1/auth/agent/connect with connect payload."""
        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["url"] = req.full_url
            captured["headers"] = dict(req.headers)
            captured["method"] = req.get_method()
            captured["body"] = json.loads(req.data.decode("utf-8")) if req.data else {}
            return _mock_registration_response()(req, **kwargs)

        auth = AgentAuth(api_key="bk_testkey", base_url="http://localhost:8080")
        payload = {"agentName": "echo", "instanceId": "AG-echo-1"}
        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            result = auth.init(registration_payload=payload)

        assert auth.get_access_token() == "jwt-abc"
        assert auth._refresh_token == "rt-xyz"
        assert captured["url"] == "http://localhost:8080/api/v1/auth/agent/connect"
        assert captured["headers"]["Authorization"] == "Bearer bk_testkey"
        assert captured["method"] == "POST"
        assert captured["body"]["agentName"] == "echo"
        assert captured["body"]["instanceId"] == "AG-echo-1"
        # init() returns the full response
        assert result["accessToken"] == "jwt-abc"
        assert result["agentName"] == "echo"

    def test_init_stores_connect_payload(self) -> None:
        """init() stores the connect payload for re-connection."""
        auth = AgentAuth(api_key="bk_testkey", base_url="http://localhost:8080")
        payload = {"agentName": "echo", "instanceId": "AG-echo-1"}
        with patch("urllib.request.urlopen", side_effect=_mock_registration_response()):
            auth.init(registration_payload=payload)

        assert auth._registration_payload == payload

    def test_init_reuses_stored_payload(self) -> None:
        """init() without payload reuses the previously stored one."""
        captured_bodies = []

        def mock_urlopen(req, **kwargs):
            if req.data:
                captured_bodies.append(json.loads(req.data.decode("utf-8")))
            return _mock_registration_response()(req, **kwargs)

        auth = AgentAuth(api_key="bk_testkey", base_url="http://localhost:8080")
        payload = {"agentName": "echo"}
        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            auth.init(registration_payload=payload)
            auth.init()  # No payload -- reuses stored

        assert len(captured_bodies) == 2
        assert captured_bodies[0] == payload
        assert captured_bodies[1] == payload

    def test_init_raises_fatal_on_api_key_invalid(self) -> None:
        """init() raises AgentAuthFatalError when the API key is invalid."""
        auth = AgentAuth(api_key="bk_invalid", base_url="http://localhost:8080")
        with patch(
            "urllib.request.urlopen",
            side_effect=_mock_error_response(
                401,
                {"error": "API key invalid", "code": ERROR_CODE_API_KEY_INVALID},
            ),
        ):
            with pytest.raises(AgentAuthFatalError, match="API key invalid or revoked"):
                auth.init(registration_payload={"agentName": "echo"})

    def test_init_raises_non_fatal_on_transient_failure(self) -> None:
        """A transient connect failure raises a plain RuntimeError.

        A 5xx / no-fatal-code response must NOT raise AgentAuthFatalError,
        so it stays non-blocking and the caller does not terminate the
        process. BLOCKS-553.
        """
        auth = AgentAuth(api_key="bk_ok", base_url="http://localhost:8080")
        with patch(
            "urllib.request.urlopen",
            side_effect=_mock_error_response(503, {"error": "Service Unavailable"}),
        ):
            with pytest.raises(RuntimeError, match="Agent connect failed") as exc_info:
                auth.init(registration_payload={"agentName": "echo"})
            assert not isinstance(exc_info.value, AgentAuthFatalError)

    def test_get_api_key(self) -> None:
        auth = AgentAuth(api_key="bk_mykey", base_url="http://localhost:8080")
        assert auth.get_api_key() == "bk_mykey"

    def test_get_access_token_before_init(self) -> None:
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        assert auth.get_access_token() is None


# ---------------------------------------------------------------------------
# refresh() -- updates tokens
# ---------------------------------------------------------------------------


class TestAgentAuthRefresh:
    def test_refresh_updates_tokens(self) -> None:
        """refresh() updates the access token and refresh token."""
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        # Manually set initial tokens
        auth._access_token = "old-jwt"
        auth._refresh_token = "old-rt"

        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["url"] = req.full_url
            body = json.loads(req.data.decode("utf-8")) if req.data else {}
            captured["body"] = body
            captured["headers"] = dict(req.headers)
            return _mock_token_response("new-jwt", "new-rt")(req, **kwargs)

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            auth.refresh()

        assert auth.get_access_token() == "new-jwt"
        assert auth._refresh_token == "new-rt"
        assert captured["url"] == "http://localhost:8080/api/v1/auth/agent/refresh"
        assert captured["body"]["refreshToken"] == "old-rt"
        assert captured["headers"]["Authorization"] == "Bearer bk_test"

    def test_refresh_falls_back_to_re_connection(self) -> None:
        """When refresh returns REFRESH_TOKEN_INVALID, falls back to re-connection via init()."""
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        auth._access_token = "old-jwt"
        auth._refresh_token = "old-rt"
        auth._registration_payload = {"agentName": "echo", "instanceId": "AG-echo-1"}

        call_count = {"n": 0}
        captured_urls = []

        def mock_urlopen(req, **kwargs):
            call_count["n"] += 1
            captured_urls.append(req.full_url)
            if call_count["n"] == 1:
                # First call: refresh endpoint returns REFRESH_TOKEN_INVALID
                return _mock_error_response(
                    401,
                    {"error": "Refresh token invalid", "code": ERROR_CODE_REFRESH_TOKEN_INVALID},
                )(req, **kwargs)
            else:
                # Second call: re-connection via /auth/agent/connect succeeds
                return _mock_registration_response("reinit-jwt", "reinit-rt")(req, **kwargs)

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            auth.refresh()

        assert auth.get_access_token() == "reinit-jwt"
        assert auth._refresh_token == "reinit-rt"
        assert call_count["n"] == 2
        # First call was refresh, second was re-connection
        assert "/auth/agent/refresh" in captured_urls[0]
        assert "/auth/agent/connect" in captured_urls[1]

    def test_refresh_re_connection_sends_stored_payload(self) -> None:
        """Re-connection on REFRESH_TOKEN_INVALID sends the stored connect payload."""
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        auth._access_token = "old-jwt"
        auth._refresh_token = "old-rt"
        auth._registration_payload = {"agentName": "echo", "instanceId": "AG-echo-1"}

        captured_body = {}

        def mock_urlopen(req, **kwargs):
            if "/auth/agent/connect" in req.full_url:
                if req.data:
                    captured_body.update(json.loads(req.data.decode("utf-8")))
                return _mock_registration_response()(req, **kwargs)
            # refresh endpoint
            return _mock_error_response(
                401,
                {"error": "Refresh token invalid", "code": ERROR_CODE_REFRESH_TOKEN_INVALID},
            )(req, **kwargs)

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            auth.refresh()

        assert captured_body["agentName"] == "echo"
        assert captured_body["instanceId"] == "AG-echo-1"

    def test_refresh_raises_fatal_on_api_key_invalid(self) -> None:
        """When refresh returns API_KEY_INVALID, raises AgentAuthFatalError."""
        auth = AgentAuth(api_key="bk_revoked", base_url="http://localhost:8080")
        auth._access_token = "old-jwt"
        auth._refresh_token = "old-rt"

        with patch(
            "urllib.request.urlopen",
            side_effect=_mock_error_response(
                401,
                {"error": "API key revoked", "code": ERROR_CODE_API_KEY_INVALID},
            ),
        ):
            with pytest.raises(AgentAuthFatalError, match="API key invalid or revoked"):
                auth.refresh()

    def test_refresh_raises_fatal_on_forced_offline_without_re_connection(self) -> None:
        """When refresh returns AGENT_FORCED_OFFLINE, raises AgentAuthFatalError.

        Must NOT fall back to re-connection via init() -- a re-register would
        bypass the administrator ban.
        """
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        auth._access_token = "old-jwt"
        auth._refresh_token = "old-rt"
        auth._registration_payload = {"agentName": "echo", "instanceId": "AG-echo-1"}

        captured_urls = []

        def mock_urlopen(req, **kwargs):
            captured_urls.append(req.full_url)
            return _mock_error_response(
                403,
                {
                    "error": "Agent has been forced offline by an administrator",
                    "code": ERROR_CODE_AGENT_FORCED_OFFLINE,
                },
            )(req, **kwargs)

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            # The SDK owns the human sentence; it must NOT echo the backend's
            # `error` (itself a full sentence) or it would double-render.
            with pytest.raises(
                AgentAuthFatalError,
                match=(
                    r"^Agent forced offline by an administrator\. "
                    r"It must be re-enabled before it can reconnect\.$"
                ),
            ):
                auth.refresh()

        # Only the refresh endpoint was hit -- no re-connection to /connect.
        assert len(captured_urls) == 1
        assert "/auth/agent/refresh" in captured_urls[0]
        assert not any("/auth/agent/connect" in u for u in captured_urls)

    def test_init_raises_fatal_on_forced_offline(self) -> None:
        """init() raises a forced-offline AgentAuthFatalError on AGENT_FORCED_OFFLINE."""
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        with patch(
            "urllib.request.urlopen",
            side_effect=_mock_error_response(
                403,
                {
                    "error": "Agent has been forced offline by an administrator",
                    "code": ERROR_CODE_AGENT_FORCED_OFFLINE,
                },
            ),
        ):
            with pytest.raises(
                AgentAuthFatalError,
                match=(
                    r"^Agent forced offline by an administrator\. "
                    r"It must be re-enabled before it can reconnect\.$"
                ),
            ):
                auth.init(registration_payload={"agentName": "echo"})

    def test_refresh_is_thread_safe(self) -> None:
        """Multiple threads calling refresh() do not cause races."""
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        auth._access_token = "old-jwt"
        auth._refresh_token = "old-rt"

        call_count = {"n": 0}
        lock = threading.Lock()

        def mock_urlopen(req, **kwargs):
            with lock:
                call_count["n"] += 1
            return _mock_token_response(f"jwt-{call_count['n']}", f"rt-{call_count['n']}")(req, **kwargs)

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            threads = [threading.Thread(target=auth.refresh) for _ in range(5)]
            for t in threads:
                t.start()
            for t in threads:
                t.join()

        # Due to threading.Lock, calls are serialized -- each gets a fresh token
        assert call_count["n"] >= 1
        # The final token should be set (not None)
        assert auth.get_access_token() is not None
        assert auth._refresh_token is not None


# ---------------------------------------------------------------------------
# authenticated_request() -- automatic 401 retry
# ---------------------------------------------------------------------------


class TestAuthenticatedRequest:
    def test_attaches_bearer_token(self) -> None:
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        auth._access_token = "my-jwt"
        auth._refresh_token = "my-rt"

        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["headers"] = dict(req.headers)
            resp = MagicMock()
            resp.read.return_value = b'{"ok": true}'
            resp.status = 200
            resp.__enter__ = lambda s: s
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            result, status = auth.authenticated_request("http://localhost:8080/api/v1/registry/agents")

        assert captured["headers"]["Authorization"] == "Bearer my-jwt"
        assert status == 200
        assert result == {"ok": True}

    def test_retries_on_401(self) -> None:
        """First request gets 401, refresh succeeds, retry succeeds."""
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        auth._access_token = "expired-jwt"
        auth._refresh_token = "my-rt"

        call_count = {"n": 0}

        def mock_urlopen(req, **kwargs):
            call_count["n"] += 1
            url = req.full_url

            if "/auth/agent/refresh" in url:
                # Refresh endpoint
                return _mock_token_response("fresh-jwt", "fresh-rt")(req, **kwargs)

            if call_count["n"] == 1:
                # First registry call -- 401
                import urllib.error
                raise urllib.error.HTTPError(
                    url, 401, "Unauthorized", {}, BytesIO(b'{"error":"token expired"}'),
                )

            # Retry succeeds
            resp = MagicMock()
            resp.read.return_value = b'{"registered": true}'
            resp.status = 200
            resp.__enter__ = lambda s: s
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            result, status = auth.authenticated_request(
                "http://localhost:8080/api/v1/registry/agents",
                method="POST",
                body=b'{"agentName":"echo"}',
                headers={"Content-Type": "application/json"},
            )

        assert result == {"registered": True}
        assert status == 200
        assert auth.get_access_token() == "fresh-jwt"

    def test_raises_if_not_initialized(self) -> None:
        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        with pytest.raises(RuntimeError, match="not initialized"):
            auth.authenticated_request("http://localhost:8080/api/test")

    def test_captures_and_injects_write_affinity(self) -> None:
        """agent_auth data-plane calls must capture and echo X-Write-Affinity.

        Without this wiring, Python agents would always read from the
        replica because the agent_auth branch bypasses affinity.
        """
        import time as _time

        from blocks_network.write_affinity import reset_affinity

        reset_affinity()
        try:
            auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
            auth._access_token = "my-jwt"
            auth._refresh_token = "my-rt"

            future = str(int(_time.time()) + 60)
            sent_headers: list[dict[str, str]] = []

            def mock_urlopen(req, **kwargs):
                sent_headers.append(dict(req.headers))
                resp = MagicMock()
                resp.read.return_value = b"{}"
                resp.status = 200
                resp.getheader = lambda name, default=None: (
                    future if name == "X-Write-Affinity" else default
                )
                resp.__enter__ = lambda s: s
                resp.__exit__ = MagicMock(return_value=False)
                return resp

            with patch("urllib.request.urlopen", side_effect=mock_urlopen):
                auth.authenticated_request("http://localhost:8080/api/v1/test")
                auth.authenticated_request("http://localhost:8080/api/v1/test")

            assert len(sent_headers) == 2
            assert not any(k.lower() == "x-write-affinity" for k in sent_headers[0])
            second = {k.lower(): v for k, v in sent_headers[1].items()}
            assert second["x-write-affinity"] == future
        finally:
            reset_affinity()

    def test_connect_and_refresh_inject_write_affinity(self) -> None:
        """init()/refresh() must echo cached X-Write-Affinity outbound.

        Parity with Node agent-auth, which injects on both _doConnect and
        _doRefresh. A Python agent that refreshes shortly after a data-plane
        write must not drop the affinity window.
        """
        import time as _time

        from blocks_network.write_affinity import (
            capture_affinity,
            reset_affinity,
        )

        reset_affinity()
        try:
            future = str(int(_time.time()) + 60)

            # Seed the module-level affinity state as if a prior response set it.
            class _Seed:
                @staticmethod
                def getheader(name, default=None):
                    return future if name == "X-Write-Affinity" else default

            capture_affinity(_Seed())

            auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
            sent_headers: list[dict[str, str]] = []

            def make_mock(body: bytes):
                def _mock(req, **kwargs):
                    sent_headers.append(dict(req.headers))
                    resp = MagicMock()
                    resp.read.return_value = body
                    resp.status = 200
                    resp.getheader = lambda name, default=None: default
                    resp.__enter__ = lambda s: s
                    resp.__exit__ = MagicMock(return_value=False)
                    return resp

                return _mock

            with patch(
                "urllib.request.urlopen",
                side_effect=make_mock(
                    b'{"accessToken":"jwt","refreshToken":"rt","expiresIn":60}',
                ),
            ):
                auth.init(registration_payload={"agentName": "echo"})

            with patch(
                "urllib.request.urlopen",
                side_effect=make_mock(
                    b'{"accessToken":"jwt2","refreshToken":"rt2","expiresIn":60}',
                ),
            ):
                auth.refresh()

            assert len(sent_headers) == 2
            connect_headers = {k.lower(): v for k, v in sent_headers[0].items()}
            refresh_headers = {k.lower(): v for k, v in sent_headers[1].items()}
            assert connect_headers["x-write-affinity"] == future
            assert refresh_headers["x-write-affinity"] == future
        finally:
            reset_affinity()


# ---------------------------------------------------------------------------
# No references to /agent/token or /registry/agents
# ---------------------------------------------------------------------------


class TestNoLegacyEndpoints:
    def test_init_uses_auth_agent_connect(self) -> None:
        """Verify init() uses /auth/agent/connect, not /agent/token or /registry/agents."""
        captured_urls = []

        def mock_urlopen(req, **kwargs):
            captured_urls.append(req.full_url)
            return _mock_registration_response()(req, **kwargs)

        auth = AgentAuth(api_key="bk_test", base_url="http://localhost:8080")
        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            auth.init(registration_payload={"agentName": "test"})

        assert len(captured_urls) == 1
        assert "/agent/token" not in captured_urls[0]
        assert "/registry/agents" not in captured_urls[0]
        assert "/auth/agent/connect" in captured_urls[0]
