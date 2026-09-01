"""Tests for blocks_network.auth_provider -- AuthProvider protocol and StaticAuthProvider."""

from __future__ import annotations

import json
from io import BytesIO
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.auth_provider import AuthProvider, StaticAuthProvider


class TestStaticAuthProvider:
    def test_get_auth_header_returns_bearer(self):
        provider = StaticAuthProvider("my-jwt-token")
        assert provider.get_auth_header() == "Bearer my-jwt-token"

    def test_on_auth_failure_returns_false(self):
        provider = StaticAuthProvider("my-jwt-token")
        assert provider.on_auth_failure() is False

    def test_satisfies_auth_provider_protocol(self):
        provider = StaticAuthProvider("test")
        assert isinstance(provider, AuthProvider)


class TestAuthProviderProtocol:
    def test_custom_implementation_satisfies_protocol(self):
        class CustomProvider:
            def get_auth_header(self):
                return "Bearer custom"

            def on_auth_failure(self):
                return True

            def ensure_ready(self):
                pass

        provider = CustomProvider()
        assert isinstance(provider, AuthProvider)
        assert provider.get_auth_header() == "Bearer custom"
        assert provider.on_auth_failure() is True

    def test_the_two_required_methods_are_enough(self):
        # The protocol is @runtime_checkable, so anything declared on it is
        # required by isinstance() too. ``ensure_ready`` was declared while the
        # docstring described it as optional, which made this assertion false and
        # locked out any provider written to the documented surface. Neither
        # optional hook may be declared on the protocol for that reason.
        class MinimalProvider:
            def get_auth_header(self):
                return "Bearer minimal"

            def on_auth_failure(self):
                return False

        provider = MinimalProvider()
        assert not hasattr(provider, "ensure_ready")
        assert not hasattr(provider, "get_last_auth_error")
        assert isinstance(provider, AuthProvider)


# ============================================================================
# RPC with AuthProvider
# ============================================================================


def _make_urlopen_response(body: dict, status: int = 200) -> MagicMock:
    resp = MagicMock()
    encoded = json.dumps(body).encode("utf-8")
    resp.read.return_value = encoded
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return resp


class TestRpcWithAuthProvider:
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_auth_provider_sets_header(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": None}
        )
        from blocks_network.rpc_client import call_rpc

        provider = StaticAuthProvider("provider-jwt")
        call_rpc("sub-c-test", "Ping", {}, base_url="http://localhost:3001",
                 auth_provider=provider)

        req = mock_urlopen.call_args[0][0]
        assert req.headers["Authorization"] == "Bearer provider-jwt"

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_no_auth_when_no_provider_or_token(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": None}
        )
        from blocks_network.rpc_client import call_rpc

        call_rpc("sub-c-test", "Ping", {}, base_url="http://localhost:3001")

        req = mock_urlopen.call_args[0][0]
        assert "Authorization" not in req.headers

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_401_with_static_auth_propagates_error(self, mock_urlopen):
        import urllib.error
        mock_urlopen.side_effect = urllib.error.HTTPError(
            "http://test", 401, "Unauthorized", {}, BytesIO(b"")
        )
        from blocks_network.rpc_client import call_rpc, RpcError

        provider = StaticAuthProvider("stale-jwt")
        with pytest.raises(RpcError, match="HTTP 401"):
            call_rpc("sub-c-test", "Ping", {}, base_url="http://localhost:3001",
                     auth_provider=provider)

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_401_with_refreshable_provider_retries(self, mock_urlopen):
        import urllib.error

        call_count = [0]

        def side_effect(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                raise urllib.error.HTTPError(
                    "http://test", 401, "Unauthorized", {}, BytesIO(b"")
                )
            return _make_urlopen_response(
                {"jsonrpc": "2.0", "id": "x", "result": {"ok": True}}
            )

        mock_urlopen.side_effect = side_effect

        from blocks_network.rpc_client import call_rpc

        class RefreshableProvider:
            def __init__(self):
                self.token = "old-jwt"
                self.refreshed = False

            def get_auth_header(self):
                return f"Bearer {self.token}"

            def on_auth_failure(self):
                self.token = "new-jwt"
                self.refreshed = True
                return True

        provider = RefreshableProvider()
        result = call_rpc("sub-c-test", "Ping", {}, base_url="http://localhost:3001",
                          auth_provider=provider)
        assert result == {"ok": True}
        assert provider.refreshed is True
        assert call_count[0] == 2


# ============================================================================
# File Upload with AuthProvider
# ============================================================================


class TestFileUploadWithAuthProvider:
    @patch("blocks_network.file_upload.urllib.request.urlopen")
    def test_auth_provider_sets_header_in_upload(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response(
            {"uploadId": "u1", "uploadUrl": "http://s3", "formFields": []}
        )
        from blocks_network.file_upload import _authenticated_json_post

        provider = StaticAuthProvider("upload-jwt")
        _authenticated_json_post(
            "http://localhost/api/v1/files/request-upload",
            {"role": "consumer-input"},
            auth_provider=provider,
        )

        req = mock_urlopen.call_args[0][0]
        assert req.headers["Authorization"] == "Bearer upload-jwt"

    @patch("blocks_network.file_upload.urllib.request.urlopen")
    def test_401_retry_in_upload(self, mock_urlopen):
        import urllib.error

        call_count = [0]

        def side_effect(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] == 1:
                raise urllib.error.HTTPError(
                    "http://test", 401, "Unauthorized", {}, BytesIO(b"")
                )
            return _make_urlopen_response({"uploadId": "u1"})

        mock_urlopen.side_effect = side_effect

        from blocks_network.file_upload import _authenticated_json_post

        class RefreshableProvider:
            def get_auth_header(self):
                return "Bearer refreshed-jwt"

            def on_auth_failure(self):
                return True

        result = _authenticated_json_post(
            "http://localhost/test",
            {"data": 1},
            auth_provider=RefreshableProvider(),
        )
        assert result == {"uploadId": "u1"}
        assert call_count[0] == 2
