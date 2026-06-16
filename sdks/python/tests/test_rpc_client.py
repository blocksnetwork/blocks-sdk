"""Tests for blocks_network.rpc_client -- JSON-RPC transport."""

from __future__ import annotations

import json
from io import BytesIO
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.auth_provider import StaticAuthProvider
from blocks_network.rpc_client import (
    BillingModeMismatchError,
    RpcError,
    call_rpc,
    rpc_endpoint,
    with_retry,
)


# ============================================================================
# rpc_endpoint
# ============================================================================


class TestRpcEndpoint:
    def test_raises_without_base_url(self):
        with pytest.raises(ValueError, match="base_url is required"):
            rpc_endpoint("sub-c-abc123")

    def test_url_with_base_url(self):
        url = rpc_endpoint("sub-c-abc123", base_url="http://localhost:3001")
        assert url == "http://localhost:3001/api/v1/rpc"

    def test_url_with_base_url_trailing_slash(self):
        url = rpc_endpoint("sub-c-abc123", base_url="http://localhost:3001/")
        assert url == "http://localhost:3001/api/v1/rpc"


# ============================================================================
# call_rpc
# ============================================================================


def _make_urlopen_response(body: dict, status: int = 200) -> MagicMock:
    """Create a mock response for urllib.request.urlopen."""
    resp = MagicMock()
    encoded = json.dumps(body).encode("utf-8")
    resp.read.return_value = encoded
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return resp


class TestCallRpc:
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_sends_correct_json_rpc_envelope(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": {"ok": True}}
        )

        result = call_rpc("sub-c-test", "MyMethod", {"key": "val"}, base_url="http://localhost:3001")

        assert result == {"ok": True}

        # Verify the request was constructed correctly
        req = mock_urlopen.call_args[0][0]
        assert req.full_url == "http://localhost:3001/api/v1/rpc"
        assert req.method == "POST"

        body = json.loads(req.data.decode("utf-8"))
        assert body["jsonrpc"] == "2.0"
        assert body["method"] == "MyMethod"
        assert body["params"] == {"key": "val"}
        assert "id" in body

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_no_auth_header_by_default(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": None}
        )

        call_rpc("sub-c-test", "Ping", {}, base_url="http://localhost:3001")

        req = mock_urlopen.call_args[0][0]
        assert "Authorization" not in req.headers

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_auth_header_when_provider_supplies_token(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": None}
        )

        call_rpc(
            "sub-c-test",
            "Ping",
            {},
            base_url="http://localhost:3001",
            auth_provider=StaticAuthProvider("jwt-abc"),
        )

        req = mock_urlopen.call_args[0][0]
        assert req.headers["Authorization"] == "Bearer jwt-abc"

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_happy_path_result_extraction(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": {"taskId": "t-1", "queued": True}}
        )

        result = call_rpc("sub-c-test", "SendMessage", {"agentName": "echo"}, base_url="http://localhost:3001")

        assert result["taskId"] == "t-1"
        assert result["queued"] is True

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_json_rpc_error_raises_rpc_error(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {
                "jsonrpc": "2.0",
                "id": "x",
                "error": {
                    "code": -32600,
                    "message": "Invalid Request",
                    "data": {"message": "Missing agentName"},
                },
            }
        )

        with pytest.raises(RpcError) as exc_info:
            call_rpc("sub-c-test", "SendMessage", {}, base_url="http://localhost:3001")

        assert exc_info.value.rpc_message == "Missing agentName"
        assert exc_info.value.code == -32600

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_json_rpc_error_fallback_message(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {
                "jsonrpc": "2.0",
                "id": "x",
                "error": {"code": -32000, "message": "Server error"},
            }
        )

        with pytest.raises(RpcError) as exc_info:
            call_rpc("sub-c-test", "SendMessage", {}, base_url="http://localhost:3001")

        assert exc_info.value.rpc_message == "Server error"

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_uses_custom_base_url(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": {"ok": True}}
        )

        call_rpc("sub-c-test", "MyMethod", {"key": "val"}, base_url="http://localhost:8080")

        req = mock_urlopen.call_args[0][0]
        assert req.full_url == "http://localhost:8080/api/v1/rpc"

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_http_error_raises_rpc_error(self, mock_urlopen):
        import urllib.error

        mock_urlopen.side_effect = urllib.error.HTTPError(
            url="http://localhost:3001/api/v1/rpc", code=500, msg="ISE", hdrs={}, fp=BytesIO(b"")
        )

        with pytest.raises(RpcError) as exc_info:
            call_rpc("sub-c-test", "SendMessage", {}, base_url="http://localhost:3001")

        assert exc_info.value.rpc_message == "HTTP 500"
        assert exc_info.value.code == 500

    def test_raises_without_base_url(self):
        with pytest.raises(ValueError, match="base_url is required"):
            call_rpc("sub-c-test", "SendMessage", {})


# ============================================================================
# with_retry
# ============================================================================


class _FakeAuthProvider:
    """Minimal auth provider: supplies a header and a refresh callback.

    Deliberately omits ``get_last_auth_error`` so ``preflight_auth_or_raise``
    is a no-op and the test exercises only the reactive-retry path.
    """

    def __init__(self, refresh_ok: bool = True) -> None:
        self._refresh_ok = refresh_ok
        self.on_auth_failure_calls = 0

    def get_auth_header(self) -> str:
        return "Bearer jwt-1"

    def on_auth_failure(self) -> bool:
        self.on_auth_failure_calls += 1
        return self._refresh_ok

    def ensure_ready(self) -> None:
        return None


def _rpc_error_body(data_code: str) -> dict:
    return {
        "jsonrpc": "2.0",
        "id": "x",
        "error": {
            "code": -32000,
            "message": "Embedded JWT liveness failure",
            "data": {"code": data_code},
        },
    }


class TestReactiveAuthRefresh:
    """Parity with Node ``rpc-client``: a JSON-RPC error whose ``data.code``
    is an embedded-JWT liveness code (not a transport 401) must trigger one
    reactive refresh + retry. Python previously only retried on transport
    401, so it missed every ``data.code`` revoke — including the already-
    documented ``EMBEDDED_JWT_REVOKED``.
    """

    @pytest.mark.parametrize(
        "data_code",
        [
            "EMBEDDED_JWT_REVOKED",
            "EMBEDDED_JWT_LOGOUT",
            "EMBEDDED_JWT_KILLED",
            "EMBEDDED_JWT_SCOPE_DRIFT",
            "AGENT_OUT_OF_SCOPE",
        ],
    )
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_liveness_code_refreshes_and_retries(self, mock_urlopen, data_code):
        provider = _FakeAuthProvider(refresh_ok=True)
        # First call: liveness revoke. Second call (after refresh): success.
        mock_urlopen.side_effect = [
            _make_urlopen_response(_rpc_error_body(data_code)),
            _make_urlopen_response({"jsonrpc": "2.0", "id": "x", "result": {"ok": True}}),
        ]
        result = call_rpc(
            "sub-c-test",
            "submitTask",
            {},
            base_url="http://localhost:3001",
            auth_provider=provider,
        )
        assert result == {"ok": True}
        assert provider.on_auth_failure_calls == 1
        assert mock_urlopen.call_count == 2

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_non_auth_data_code_does_not_refresh(self, mock_urlopen):
        provider = _FakeAuthProvider(refresh_ok=True)
        mock_urlopen.return_value = _make_urlopen_response(
            _rpc_error_body("SOME_OTHER_ERROR")
        )
        with pytest.raises(RpcError):
            call_rpc(
                "sub-c-test",
                "submitTask",
                {},
                base_url="http://localhost:3001",
                auth_provider=provider,
            )
        assert provider.on_auth_failure_calls == 0

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_revoke_but_refresh_fails_propagates(self, mock_urlopen):
        provider = _FakeAuthProvider(refresh_ok=False)
        mock_urlopen.return_value = _make_urlopen_response(
            _rpc_error_body("EMBEDDED_JWT_REVOKED")
        )
        with pytest.raises(RpcError):
            call_rpc(
                "sub-c-test",
                "submitTask",
                {},
                base_url="http://localhost:3001",
                auth_provider=provider,
            )
        assert provider.on_auth_failure_calls == 1


class TestWithRetry:
    @patch("blocks_network.rpc_client.time.sleep")
    def test_retries_on_transient_error(self, mock_sleep):
        call_count = 0

        def flaky():
            nonlocal call_count
            call_count += 1
            if call_count < 3:
                raise OSError("ECONNRESET")
            return "ok"

        result = with_retry(flaky, max_retries=3, base_delay=0.0)
        assert result == "ok"
        assert call_count == 3

    def test_no_retry_on_non_transient_error(self):
        call_count = 0

        def bad():
            nonlocal call_count
            call_count += 1
            raise ValueError("bad input")

        with pytest.raises(ValueError, match="bad input"):
            with_retry(bad, max_retries=3, base_delay=0.0)

        assert call_count == 1

    @patch("blocks_network.rpc_client.time.sleep")
    def test_raises_after_max_retries_exhausted(self, mock_sleep):
        def always_transient():
            raise OSError("ETIMEDOUT")

        with pytest.raises(OSError, match="ETIMEDOUT"):
            with_retry(always_transient, max_retries=2, base_delay=0.0)


# ============================================================================
# BillingModeMismatchError mapping at the RPC layer
# ============================================================================


class TestBillingModeMismatchMapping:
    """Backend ``BillingModeMismatch`` JSON-RPC errors map to the typed subclass.

    Backend wire shape (per ``bmc-data`` Phase 1 IMPL_REPORT):

        error.data = {
          "code": "BillingModeMismatch",
          "details": { "expected": "free"|"paid", "got": "free"|"paid" }
        }

    The mapping lives in the rpc-client layer (Python's ``call_rpc``)
    rather than in ``task-client``, mirroring Q5's "extend RpcError"
    decision. This means any RPC method that returns a
    ``BillingModeMismatch`` flows through the typed subclass — tests
    asserting on it can target either layer.
    """

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_maps_to_typed_subclass(self, mock_urlopen):
        body = {
            "jsonrpc": "2.0",
            "id": "x",
            "error": {
                "code": -32000,
                "message": (
                    "Billing mode mismatch: caller declared 'free', agent is 'paid'. "
                    "Read the agent's billingMode from the registry "
                    "(Node: (await getAgent(name)).billingMode; Python: get_agent(agent_name).billing_mode) "
                    "and pass it into TaskClient.create."
                ),
                "data": {
                    "code": "BillingModeMismatch",
                    "details": {"expected": "paid", "got": "free"},
                },
            },
        }
        encoded = json.dumps(body).encode("utf-8")
        resp = MagicMock()
        resp.read.return_value = encoded
        resp.__enter__ = MagicMock(return_value=resp)
        resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = resp

        with pytest.raises(BillingModeMismatchError) as exc_info:
            call_rpc(
                "sub-c-test",
                "SendMessage",
                {"agentName": "echo", "billingMode": "free"},
                base_url="http://localhost:3001",
            )

        # Subclass identity preserved
        assert isinstance(exc_info.value, BillingModeMismatchError)
        # ...and is also an RpcError (cross-language parity hook)
        assert isinstance(exc_info.value, RpcError)
        assert exc_info.value.expected == "paid"
        assert exc_info.value.got == "free"
        # JSON-RPC numeric code from the envelope is preserved on the
        # typed subclass — parity with Node's BillingModeMismatchError.
        assert exc_info.value.code == -32000

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_does_not_map_when_code_missing(self, mock_urlopen):
        """An RPC error WITHOUT data.code stays a plain RpcError."""
        body = {
            "jsonrpc": "2.0",
            "id": "x",
            "error": {"code": -32600, "message": "Invalid Request", "data": {}},
        }
        encoded = json.dumps(body).encode("utf-8")
        resp = MagicMock()
        resp.read.return_value = encoded
        resp.__enter__ = MagicMock(return_value=resp)
        resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = resp

        with pytest.raises(RpcError) as exc_info:
            call_rpc(
                "sub-c-test",
                "SendMessage",
                {},
                base_url="http://localhost:3001",
            )

        assert not isinstance(exc_info.value, BillingModeMismatchError)

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_does_not_map_for_unrelated_code(self, mock_urlopen):
        """Other backend error codes don't accidentally produce mismatch error."""
        body = {
            "jsonrpc": "2.0",
            "id": "x",
            "error": {
                "code": -32000,
                "message": "balance",
                "data": {
                    "code": "InsufficientBalance",
                    "details": {"required": "0.10", "available": "0.00"},
                },
            },
        }
        encoded = json.dumps(body).encode("utf-8")
        resp = MagicMock()
        resp.read.return_value = encoded
        resp.__enter__ = MagicMock(return_value=resp)
        resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = resp

        with pytest.raises(RpcError) as exc_info:
            call_rpc(
                "sub-c-test",
                "SendMessage",
                {},
                base_url="http://localhost:3001",
            )

        assert not isinstance(exc_info.value, BillingModeMismatchError)
        assert exc_info.value.data["code"] == "InsufficientBalance"

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_does_not_retry_on_mismatch(self, mock_urlopen):
        """Per IMPL §6 Python 7: SDK does NOT auto-retry on BillingModeMismatch."""
        body = {
            "jsonrpc": "2.0",
            "id": "x",
            "error": {
                "code": -32000,
                "message": "mismatch",
                "data": {
                    "code": "BillingModeMismatch",
                    "details": {"expected": "free", "got": "paid"},
                },
            },
        }
        encoded = json.dumps(body).encode("utf-8")
        resp = MagicMock()
        resp.read.return_value = encoded
        resp.__enter__ = MagicMock(return_value=resp)
        resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = resp

        with pytest.raises(BillingModeMismatchError):
            call_rpc(
                "sub-c-test",
                "SendMessage",
                {},
                base_url="http://localhost:3001",
            )

        # Exactly ONE attempt. with_retry only retries on TRANSIENT network
        # errors; RpcError subclasses propagate immediately.
        assert mock_urlopen.call_count == 1


class TestBillingModeMismatchErrorClass:
    """Direct tests on the exception class shape (cross-language parity)."""

    def test_extends_rpc_error(self):
        assert issubclass(BillingModeMismatchError, RpcError)

    def test_attributes(self):
        err = BillingModeMismatchError(
            "boom",
            expected="paid",
            got="free",
            code=-32000,
            data={"code": "BillingModeMismatch"},
        )
        assert err.expected == "paid"
        assert err.got == "free"
        assert err.rpc_message == "boom"
        assert err.code == -32000
        assert err.data == {"code": "BillingModeMismatch"}

    def test_importable_from_package(self):
        from blocks_network import BillingModeMismatchError as ImportedError

        assert ImportedError is BillingModeMismatchError
