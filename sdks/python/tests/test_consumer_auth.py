"""Tests for blocks_network.consumer_auth -- ConsumerAuth with 3 modes."""

from __future__ import annotations

import json
import threading
import time
from io import BytesIO
import urllib.error
from unittest.mock import MagicMock, patch, call

import pytest

from blocks_network.auth_provider import AuthProvider
from blocks_network.consumer_auth import ConsumerAuth, TokenEndpointConfig, TokenResult


# ============================================================================
# Validation
# ============================================================================


class TestConsumerAuthValidation:
    def test_requires_exactly_one_mode(self):
        with pytest.raises(ValueError, match="Exactly one"):
            ConsumerAuth()

    def test_rejects_multiple_modes(self):
        with pytest.raises(ValueError, match="Exactly one"):
            ConsumerAuth(
                api_key="bk_test",
                token_endpoint="http://proxy/token",
                base_url="http://api",
            )

    def test_api_key_requires_base_url(self):
        with pytest.raises(ValueError, match="base_url is required"):
            ConsumerAuth(api_key="bk_test")


# ============================================================================
# Mode 1: API key
# ============================================================================


def _make_urlopen_response(body: dict) -> MagicMock:
    resp = MagicMock()
    encoded = json.dumps(body).encode("utf-8")
    resp.read.return_value = encoded
    resp.__enter__ = MagicMock(return_value=resp)
    resp.__exit__ = MagicMock(return_value=False)
    return resp


def _make_http_error(status: int, body: dict) -> urllib.error.HTTPError:
    payload = json.dumps(body).encode("utf-8")
    return urllib.error.HTTPError(
        url="http://api.test/error",
        code=status,
        msg="error",
        hdrs={},
        fp=BytesIO(payload),
    )


class TestConsumerAuthApiKey:
    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_init_calls_consumer_token_endpoint(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response({
            "accessToken": "jwt-1",
            "refreshToken": "rt-1",
            "expiresIn": 60,
            "userId": "user-123",
        })

        auth = ConsumerAuth(api_key="bk_test", base_url="http://api.test")
        auth.init()

        assert auth.get_auth_header() == "Bearer jwt-1"
        assert auth.get_user_id() == "user-123"

        req = mock_urlopen.call_args[0][0]
        assert "/api/v1/auth/agent/consumer-token" in req.full_url
        body = json.loads(req.data.decode("utf-8"))
        assert body == {"apiKey": "bk_test"}

        auth.destroy()

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_refresh_uses_refresh_endpoint(self, mock_urlopen):
        # First call: consumer-token
        mock_urlopen.return_value = _make_urlopen_response({
            "accessToken": "jwt-1",
            "refreshToken": "rt-1",
            "expiresIn": 60,
            "userId": "user-123",
        })

        auth = ConsumerAuth(api_key="bk_test", base_url="http://api.test")
        auth.init()

        # Second call: refresh
        mock_urlopen.return_value = _make_urlopen_response({
            "accessToken": "jwt-2",
            "refreshToken": "rt-2",
            "expiresIn": 60,
            "userId": "user-123",
        })

        result = auth.on_auth_failure()
        assert result is True
        assert auth.get_auth_header() == "Bearer jwt-2"

        # Verify refresh endpoint was called
        req = mock_urlopen.call_args[0][0]
        assert "/api/v1/auth/agent/refresh" in req.full_url
        assert req.headers["Authorization"] == "Bearer bk_test"
        body = json.loads(req.data.decode("utf-8"))
        assert body["refreshToken"] == "rt-1"

        auth.destroy()

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_refresh_token_invalid_falls_back_to_consumer_token(self, mock_urlopen):
        mock_urlopen.side_effect = [
            _make_urlopen_response({
                "accessToken": "jwt-1",
                "refreshToken": "rt-1",
                "expiresIn": 60,
                "userId": "user-123",
            }),
            _make_http_error(
                401,
                {
                    "error": "Refresh token invalid or expired",
                    "code": "REFRESH_TOKEN_INVALID",
                },
            ),
            _make_urlopen_response({
                "accessToken": "jwt-2",
                "refreshToken": "rt-2",
                "expiresIn": 60,
                "userId": "user-123",
            }),
        ]

        auth = ConsumerAuth(api_key="bk_test", base_url="http://api.test")
        auth.init()

        assert auth.on_auth_failure() is True
        assert auth.get_auth_header() == "Bearer jwt-2"

        refresh_req = mock_urlopen.call_args_list[1][0][0]
        bootstrap_req = mock_urlopen.call_args_list[2][0][0]
        assert "/api/v1/auth/agent/refresh" in refresh_req.full_url
        assert "/api/v1/auth/agent/consumer-token" in bootstrap_req.full_url

        auth.destroy()


# ============================================================================
# Mode 2: Token endpoint
# ============================================================================


class TestConsumerAuthTokenEndpoint:
    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_init_calls_endpoint_with_empty_body(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response({
            "token": "proxy-jwt-1",
            "expiresIn": 120,
            "userId": "proxy-user",
        })

        auth = ConsumerAuth(token_endpoint="http://proxy.test/token")
        auth.init()

        assert auth.get_auth_header() == "Bearer proxy-jwt-1"
        assert auth.get_user_id() == "proxy-user"

        req = mock_urlopen.call_args[0][0]
        assert req.full_url == "http://proxy.test/token"
        body = json.loads(req.data.decode("utf-8"))
        assert body == {}

        auth.destroy()

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_refresh_calls_same_endpoint(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response({
            "token": "proxy-jwt-1",
            "expiresIn": 120,
            "userId": "proxy-user-1",
        })

        auth = ConsumerAuth(token_endpoint="http://proxy.test/token")
        auth.init()
        assert auth.get_user_id() == "proxy-user-1"

        # Refresh response omits userId — must preserve the original
        mock_urlopen.return_value = _make_urlopen_response({
            "token": "proxy-jwt-2",
            "expiresIn": 120,
        })

        assert auth.on_auth_failure() is True
        assert auth.get_auth_header() == "Bearer proxy-jwt-2"
        assert auth.get_user_id() == "proxy-user-1"  # preserved, not cleared

        auth.destroy()


class TestConsumerAuthTokenEndpointConfig:
    """Config-object form of ``token_endpoint`` (widened for cookie-auth proxies, etc.)."""

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_config_object_with_custom_headers(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response({
            "token": "csrf-jwt",
            "expiresIn": 120,
        })

        auth = ConsumerAuth(
            token_endpoint={
                "url": "http://proxy.test/token",
                "headers": {
                    "X-CSRF-Token": "csrf-abc",
                    "X-Session-Id": "sess-123",
                },
            }
        )
        auth.init()

        req = mock_urlopen.call_args[0][0]
        assert req.full_url == "http://proxy.test/token"
        # urllib normalizes header names via ``.capitalize()`` (per RFC 7230
        # case-insensitivity). We check against that form.
        assert req.headers["Content-type"] == "application/json"
        assert req.headers["X-csrf-token"] == "csrf-abc"
        assert req.headers["X-session-id"] == "sess-123"
        # Default body is still {}
        assert json.loads(req.data.decode("utf-8")) == {}

        auth.destroy()

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_config_object_with_cookie_header_cookie_auth_parity(self, mock_urlopen):
        """Python equivalent of Node's credentials: 'include' is Cookie header.

        Node's ``TokenEndpointConfig`` accepts ``credentials: 'include'`` because
        ``fetch`` natively forwards cookies. ``urllib`` has no equivalent, so
        Python callers pass the cookie value via ``headers={'Cookie': '...'}``.
        """
        mock_urlopen.return_value = _make_urlopen_response({
            "token": "cookie-jwt",
            "expiresIn": 60,
        })

        auth = ConsumerAuth(
            token_endpoint={
                "url": "http://proxy.test/token",
                "headers": {"Cookie": "session=abc; csrftoken=xyz"},
            }
        )
        auth.init()

        req = mock_urlopen.call_args[0][0]
        assert req.headers["Cookie"] == "session=abc; csrftoken=xyz"

        auth.destroy()

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_config_object_custom_body_serialized_as_json(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response({
            "token": "body-jwt",
            "expiresIn": 120,
        })

        auth = ConsumerAuth(
            token_endpoint={
                "url": "http://proxy.test/token",
                "body": {"sessionId": "sess-42", "clientId": "c-1"},
            }
        )
        auth.init()

        req = mock_urlopen.call_args[0][0]
        body = json.loads(req.data.decode("utf-8"))
        assert body == {"sessionId": "sess-42", "clientId": "c-1"}

        auth.destroy()

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_user_content_type_overrides_sdk_default(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response({
            "token": "x",
            "expiresIn": 60,
        })

        auth = ConsumerAuth(
            token_endpoint={
                "url": "http://proxy.test/token",
                "headers": {"Content-Type": "application/vnd.custom+json"},
            }
        )
        auth.init()

        req = mock_urlopen.call_args[0][0]
        assert req.headers["Content-type"] == "application/vnd.custom+json"

        auth.destroy()

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_refresh_path_uses_same_config(self, mock_urlopen):
        # init
        mock_urlopen.return_value = _make_urlopen_response({
            "token": "init-jwt",
            "expiresIn": 60,
        })

        auth = ConsumerAuth(
            token_endpoint={
                "url": "http://proxy.test/token",
                "headers": {"X-CSRF-Token": "csrf-xyz"},
                "body": {"scope": "task:read"},
            }
        )
        auth.init()

        # Refresh (on_auth_failure path)
        mock_urlopen.return_value = _make_urlopen_response({
            "token": "refresh-jwt",
            "expiresIn": 60,
        })

        assert auth.on_auth_failure() is True
        refresh_req = mock_urlopen.call_args[0][0]
        assert refresh_req.headers["X-csrf-token"] == "csrf-xyz"
        assert json.loads(refresh_req.data.decode("utf-8")) == {"scope": "task:read"}

        auth.destroy()

    def test_typed_dict_schema(self):
        """TokenEndpointConfig declares `url` as required, `headers` and
        `body` as optional, and deliberately omits `credentials`.

        Structural required-ness is enforced via the two-class
        inheritance pattern (``_TokenEndpointConfigRequired`` +
        ``total=False`` subclass), which is the Python 3.9-compatible
        way to mix required and optional keys in one TypedDict.
        """
        required = TokenEndpointConfig.__required_keys__
        optional = TokenEndpointConfig.__optional_keys__
        declared = required | optional

        assert "url" in required, (
            "url must be structurally required; runtime also rejects "
            "missing url with ValueError"
        )
        assert "headers" in optional
        assert "body" in optional
        assert "credentials" not in declared, (
            "Python TokenEndpointConfig must NOT declare `credentials` — "
            "urllib has no fetch-equivalent; consumers pass Cookie via headers."
        )

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_missing_url_raises_value_error(self, mock_urlopen):
        """A config dict without ``url`` raises a descriptive ValueError
        at acquisition time, not a bare KeyError.
        """
        # type: ignore[typeddict-item] -- deliberately malformed input
        auth = ConsumerAuth(token_endpoint={"headers": {"X-Custom": "y"}})  # type: ignore[typeddict-item]
        with pytest.raises(ValueError, match="url is required"):
            auth.init()
        # urlopen should never have been called — validation short-circuits
        mock_urlopen.assert_not_called()
        auth.destroy()


# ============================================================================
# Mode 3: Token provider
# ============================================================================


class TestConsumerAuthTokenProvider:
    def test_init_calls_provider_function(self):
        provider_fn = MagicMock(return_value=TokenResult(
            token="custom-jwt-1",
            expires_in=300,
            user_id="custom-user",
        ))

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        assert auth.get_auth_header() == "Bearer custom-jwt-1"
        assert auth.get_user_id() == "custom-user"
        provider_fn.assert_called_once()

        auth.destroy()

    def test_refresh_calls_provider_again(self):
        call_count = [0]

        def provider_fn():
            call_count[0] += 1
            return TokenResult(
                token=f"custom-jwt-{call_count[0]}",
                expires_in=300,
            )

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()
        assert auth.get_auth_header() == "Bearer custom-jwt-1"

        assert auth.on_auth_failure() is True
        assert auth.get_auth_header() == "Bearer custom-jwt-2"
        assert call_count[0] == 2

        auth.destroy()


# ============================================================================
# Protocol conformance
# ============================================================================


class TestConsumerAuthProtocol:
    def test_satisfies_auth_provider_protocol(self):
        auth = ConsumerAuth(
            token_provider=lambda: TokenResult(token="t", expires_in=60)
        )
        assert isinstance(auth, AuthProvider)
        auth.destroy()


# ============================================================================
# Proactive refresh
# ============================================================================


class TestProactiveRefresh:
    def test_proactive_refresh_fires_at_80_percent_ttl(self):
        call_count = [0]

        def provider_fn():
            call_count[0] += 1
            return TokenResult(
                token=f"jwt-{call_count[0]}",
                expires_in=1,  # 1 second TTL -> 0.8s refresh
            )

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()
        assert call_count[0] == 1

        # Wait for proactive refresh to fire (0.8s + buffer)
        time.sleep(1.5)

        assert call_count[0] >= 2
        assert auth.get_auth_header() == f"Bearer jwt-{call_count[0]}"

        auth.destroy()

    def test_proactive_refresh_retries_with_backoff(self):
        call_count = [0]
        fail_count = [0]

        def provider_fn():
            call_count[0] += 1
            if call_count[0] == 1:
                return TokenResult(token="jwt-init", expires_in=1)
            if fail_count[0] < 2:
                fail_count[0] += 1
                raise RuntimeError("network error")
            return TokenResult(token="jwt-recovered", expires_in=60)

        auth = ConsumerAuth(token_provider=provider_fn)

        # Patch sleep to avoid long waits in test
        with patch("blocks_network.consumer_auth.time.sleep"):
            auth.init()
            time.sleep(1.5)  # Real sleep for timer to fire

        # Give time for the retries to complete
        time.sleep(0.5)
        auth.destroy()

    def test_permanent_failure_calls_on_auth_error(self):
        call_count = [0]
        error_received = threading.Event()
        error_value = [None]

        def provider_fn():
            call_count[0] += 1
            if call_count[0] == 1:
                return TokenResult(token="jwt-init", expires_in=1)
            raise RuntimeError("permanent failure")

        def on_error(err):
            error_value[0] = err
            error_received.set()

        auth = ConsumerAuth(
            token_provider=provider_fn,
            on_auth_error=on_error,
        )

        with patch("blocks_network.consumer_auth.time.sleep"):
            auth.init()
            # Wait for the timer to fire and retries to exhaust
            error_received.wait(timeout=5)

        assert error_value[0] is not None
        assert "permanent failure" in str(error_value[0])

        auth.destroy()


# ============================================================================
# Reactive refresh (on_auth_failure)
# ============================================================================


class TestReactiveRefresh:
    def test_on_auth_failure_returns_true_on_success(self):
        call_count = [0]

        def provider_fn():
            call_count[0] += 1
            return TokenResult(token=f"jwt-{call_count[0]}", expires_in=300)

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        assert auth.on_auth_failure() is True
        assert auth.get_auth_header() == "Bearer jwt-2"
        auth.destroy()

    def test_on_auth_failure_returns_false_on_failure(self):
        call_count = [0]

        def provider_fn():
            call_count[0] += 1
            if call_count[0] == 1:
                return TokenResult(token="jwt-init", expires_in=300)
            raise RuntimeError("refresh failed")

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        assert auth.on_auth_failure() is False
        # Token remains stale
        assert auth.get_auth_header() == "Bearer jwt-init"
        auth.destroy()

    def test_on_auth_failure_returns_false_after_destroy(self):
        auth = ConsumerAuth(
            token_provider=lambda: TokenResult(token="jwt", expires_in=300)
        )
        auth.init()
        auth.destroy()

        assert auth.on_auth_failure() is False


# ============================================================================
# Concurrent on_auth_failure (thread safety)
# ============================================================================


class TestConcurrentRefresh:
    def test_concurrent_auth_failure_shares_result(self):
        call_count = [0]
        call_lock = threading.Lock()

        def provider_fn():
            with call_lock:
                call_count[0] += 1
            time.sleep(0.1)  # Simulate network delay
            return TokenResult(token=f"jwt-{call_count[0]}", expires_in=300)

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()
        initial_count = call_count[0]

        results = []
        threads = []
        for _ in range(5):
            def _run():
                r = auth.on_auth_failure()
                results.append(r)
            t = threading.Thread(target=_run)
            threads.append(t)

        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=5)

        # All 5 callers should get True, but only 1 extra provider call
        assert all(r is True for r in results)
        # The provider should have been called at most 2 times (init + 1 refresh)
        assert call_count[0] <= initial_count + 1

        auth.destroy()

    def test_concurrent_get_auth_header_during_refresh(self):
        """get_auth_header() should not block indefinitely during refresh."""
        call_count = [0]

        def provider_fn():
            call_count[0] += 1
            time.sleep(0.05)
            return TokenResult(token=f"jwt-{call_count[0]}", expires_in=300)

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        headers = []

        def _read_header():
            h = auth.get_auth_header()
            headers.append(h)

        def _refresh():
            auth.on_auth_failure()

        t_refresh = threading.Thread(target=_refresh)
        t_reads = [threading.Thread(target=_read_header) for _ in range(3)]

        t_refresh.start()
        for t in t_reads:
            t.start()
        t_refresh.join(timeout=5)
        for t in t_reads:
            t.join(timeout=5)

        # All reads should return a valid header (never None)
        assert all(h is not None for h in headers)
        assert all(h.startswith("Bearer ") for h in headers)

        auth.destroy()


# ============================================================================
# Destroy behavior
# ============================================================================


class TestDestroy:
    def test_destroy_cancels_timer(self):
        auth = ConsumerAuth(
            token_provider=lambda: TokenResult(token="jwt", expires_in=300)
        )
        auth.init()
        assert auth._timer is not None

        auth.destroy()
        # Timer should be cancelled
        assert auth._destroyed is True

    def test_token_readable_after_destroy(self):
        auth = ConsumerAuth(
            token_provider=lambda: TokenResult(token="jwt-final", expires_in=300)
        )
        auth.init()
        auth.destroy()

        # Token remains readable for active sessions
        assert auth.get_auth_header() == "Bearer jwt-final"

    def test_no_proactive_refresh_after_destroy(self):
        call_count = [0]

        def provider_fn():
            call_count[0] += 1
            return TokenResult(token=f"jwt-{call_count[0]}", expires_in=1)

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()
        auth.destroy()

        initial_count = call_count[0]
        time.sleep(2)  # Wait past when refresh would have fired

        assert call_count[0] == initial_count


# ============================================================================
# TaskClient.create() integration
# ============================================================================


class TestTaskClientCreateModes:
    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_mutual_exclusion_multiple_provider_modes(self, mock_cdm):
        from blocks_network.task_client import TaskClient

        mock_keyset = MagicMock()
        mock_keyset.subscribe_key = "sub-c-test"
        mock_keyset.publish_key = "pub-c-test"
        mock_cdm.return_value = MagicMock(
            playground=mock_keyset,
            network=mock_keyset,
            api=MagicMock(base_url="http://api.test"),
        )

        with pytest.raises(ValueError, match="Only one token provider mode"):
            TaskClient.create(
                billing_mode="free",
                api_key="bk_test",
                token_endpoint="http://proxy/token",
            )

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_with_token_provider_mode(self, mock_cdm):
        from blocks_network.task_client import TaskClient
        from blocks_network.consumer_auth import ConsumerAuth

        mock_keyset = MagicMock()
        mock_keyset.subscribe_key = "sub-c-test"
        mock_keyset.publish_key = "pub-c-test"
        mock_cdm.return_value = MagicMock(
            playground=mock_keyset,
            network=mock_keyset,
            api=MagicMock(base_url="http://api.test"),
        )

        provider_fn = MagicMock(return_value=TokenResult(
            token="custom-jwt",
            expires_in=300,
            user_id="user-1",
        ))

        client = TaskClient.create(
            billing_mode="free",
            token_provider=provider_fn,
        )

        assert client._auth_provider is not None
        assert client._auth_provider.get_auth_header() == "Bearer custom-jwt"
        assert client._consumer_auth is not None
        provider_fn.assert_called_once()

        client.destroy()


# ============================================================================
# TaskClient update_keys and destroy
# ============================================================================


class TestTaskClientUpdateKeys:
    def test_update_keys_only_changes_keys(self):
        from blocks_network.task_client import TaskClient

        client = TaskClient("sub-c-test", billing_mode="free", base_url="http://api.test")
        client.update_keys("sub-c-new", publish_key="pub-c-new")
        assert client._subscribe_key == "sub-c-new"
        assert client._publish_key == "pub-c-new"

    def test_update_keys_preserves_consumer_auth(self):
        from blocks_network.task_client import TaskClient

        provider_fn = MagicMock(return_value=TokenResult(
            token="consumer-jwt",
            expires_in=300,
        ))

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        client = TaskClient("sub-c-test", billing_mode="free", base_url="http://api.test",
                            auth_provider=auth)
        client._consumer_auth = auth

        client.update_keys("sub-c-new", publish_key="pub-c-new")
        assert client._auth_provider.get_auth_header() == "Bearer consumer-jwt"
        assert client._subscribe_key == "sub-c-new"
        assert client._publish_key == "pub-c-new"

        auth.destroy()


class TestTaskClientDestroy:
    def test_destroy_stops_consumer_auth_timer(self):
        from blocks_network.task_client import TaskClient

        provider_fn = MagicMock(return_value=TokenResult(
            token="jwt",
            expires_in=300,
        ))

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        client = TaskClient("sub-c-test", billing_mode="free", base_url="http://api.test",
                            auth_provider=auth)
        client._consumer_auth = auth

        client.destroy()
        assert auth._destroyed is True

    def test_destroy_keeps_token_readable(self):
        from blocks_network.task_client import TaskClient

        provider_fn = MagicMock(return_value=TokenResult(
            token="jwt-final",
            expires_in=300,
        ))

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        client = TaskClient("sub-c-test", billing_mode="free", base_url="http://api.test",
                            auth_provider=auth)
        client._consumer_auth = auth

        client.destroy()
        # Token remains readable for active sessions
        assert auth.get_auth_header() == "Bearer jwt-final"


# ============================================================================
# Token rotation verification
# ============================================================================


class TestTokenRotation:
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_rpc_uses_refreshed_token(self, mock_urlopen):
        """After token refresh, subsequent RPC calls use the new token."""
        from blocks_network.task_client import TaskClient

        call_count = [0]

        def provider_fn():
            call_count[0] += 1
            return TokenResult(token=f"jwt-{call_count[0]}", expires_in=300)

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        client = TaskClient("sub-c-test", billing_mode="free", base_url="http://api.test",
                            auth_provider=auth)

        # First RPC call
        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": {"task": {"taskId": "t1"}}}
        )

        client.get_task("t1")
        req1 = mock_urlopen.call_args[0][0]
        assert req1.headers["Authorization"] == "Bearer jwt-1"

        # Refresh
        auth.on_auth_failure()

        # Second RPC call
        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": {"task": {"taskId": "t1"}}}
        )

        client.get_task("t1")
        req2 = mock_urlopen.call_args[0][0]
        assert req2.headers["Authorization"] == "Bearer jwt-2"

        auth.destroy()


# ============================================================================
# TaskSession cancel/terminate with refreshed token
# ============================================================================


class TestTaskSessionAuthProvider:
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_cancel_uses_auth_provider(self, mock_urlopen):
        """TaskSession.cancel() should use auth_provider from rpc_config."""
        from blocks_network.task_session import TaskSession

        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": None}
        )

        provider_fn = MagicMock(return_value=TokenResult(
            token="session-jwt",
            expires_in=300,
        ))
        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        session = TaskSession(
            task_id="t1",
            owner_id="owner-1",
            read_token="read-token",
            status_channel="u.org.t1",
            agent_name="echo",
            pubnub=None,
            owns_subscribe_client=False,
            sdk_options={"subscribe_key": "sub-c-test", "publish_key": ""},
            rpc_config={
                "subscribe_key": "sub-c-test",
                "auth_provider": auth,
                "base_url": "http://api.test",
                "agent_auth": None,
            },
            skip_subscription=True,
            pre_closed_state="completed",
        )

        session.cancel()

        req = mock_urlopen.call_args[0][0]
        assert req.headers["Authorization"] == "Bearer session-jwt"

        auth.destroy()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_terminate_uses_refreshed_token(self, mock_urlopen):
        """After refresh, terminate() uses the new token."""
        from blocks_network.task_session import TaskSession

        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": None}
        )

        call_count = [0]

        def provider_fn():
            call_count[0] += 1
            return TokenResult(token=f"jwt-{call_count[0]}", expires_in=300)

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        session = TaskSession(
            task_id="t1",
            owner_id="owner-1",
            read_token="read-token",
            status_channel="u.org.t1",
            agent_name="echo",
            pubnub=None,
            owns_subscribe_client=False,
            sdk_options={"subscribe_key": "sub-c-test", "publish_key": ""},
            rpc_config={
                "subscribe_key": "sub-c-test",
                "auth_provider": auth,
                "base_url": "http://api.test",
                "agent_auth": None,
            },
            skip_subscription=True,
            pre_closed_state="completed",
        )

        # Refresh token
        auth.on_auth_failure()

        session.terminate()

        req = mock_urlopen.call_args[0][0]
        assert req.headers["Authorization"] == "Bearer jwt-2"

        auth.destroy()

    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_session_works_after_client_destroy(self, mock_urlopen):
        """Session.cancel() still works after TaskClient.destroy() -- stale token is readable."""
        from blocks_network.task_client import TaskClient
        from blocks_network.task_session import TaskSession

        mock_urlopen.return_value = _make_urlopen_response(
            {"jsonrpc": "2.0", "id": "x", "result": None}
        )

        provider_fn = MagicMock(return_value=TokenResult(
            token="jwt-stale",
            expires_in=300,
        ))

        auth = ConsumerAuth(token_provider=provider_fn)
        auth.init()

        rpc_config = {
            "subscribe_key": "sub-c-test",
            "auth_provider": auth,
            "base_url": "http://api.test",
            "agent_auth": None,
        }

        session = TaskSession(
            task_id="t1",
            owner_id="owner-1",
            read_token="read-token",
            status_channel="u.org.t1",
            agent_name="echo",
            pubnub=None,
            owns_subscribe_client=False,
            sdk_options={"subscribe_key": "sub-c-test", "publish_key": ""},
            rpc_config=rpc_config,
            skip_subscription=True,
            pre_closed_state="completed",
        )

        # Destroy the auth (simulating TaskClient.destroy())
        auth.destroy()

        # Session should still be able to cancel with stale token
        session.cancel()

        req = mock_urlopen.call_args[0][0]
        assert req.headers["Authorization"] == "Bearer jwt-stale"


# ============================================================================
# ensure_ready()
# ============================================================================


class TestConsumerAuthEnsureReady:
    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_ensure_ready_calls_init_on_first_invocation(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response({
            "accessToken": "jwt-lazy",
            "refreshToken": "rt-lazy",
            "expiresIn": 60,
            "userId": "user-lazy",
        })

        auth = ConsumerAuth(api_key="bk_lazy", base_url="http://localhost:3001")
        assert auth.get_auth_header() is None
        auth.ensure_ready()
        assert auth.get_auth_header() == "Bearer jwt-lazy"
        assert mock_urlopen.call_count == 1

        auth.destroy()

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_ensure_ready_is_noop_after_first_call(self, mock_urlopen):
        mock_urlopen.return_value = _make_urlopen_response({
            "accessToken": "jwt-once",
            "refreshToken": "rt-once",
            "expiresIn": 60,
            "userId": "user-once",
        })

        auth = ConsumerAuth(api_key="bk_once", base_url="http://localhost:3001")
        auth.ensure_ready()
        auth.ensure_ready()
        auth.ensure_ready()
        assert mock_urlopen.call_count == 1

        auth.destroy()

    @patch("blocks_network.consumer_auth.urllib.request.urlopen")
    def test_ensure_ready_concurrent_threads_single_init(self, mock_urlopen):
        """Multiple threads calling ensure_ready() concurrently only trigger one init()."""
        import threading

        mock_urlopen.return_value = _make_urlopen_response({
            "accessToken": "jwt-concurrent",
            "refreshToken": "rt-concurrent",
            "expiresIn": 60,
            "userId": "user-concurrent",
        })

        auth = ConsumerAuth(api_key="bk_conc", base_url="http://localhost:3001")
        barrier = threading.Barrier(5, timeout=5)
        errors: list = []

        def worker():
            try:
                barrier.wait()
                auth.ensure_ready()
            except Exception as e:
                errors.append(e)

        threads = [threading.Thread(target=worker) for _ in range(5)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=10)

        assert not errors, f"Threads raised: {errors}"
        assert auth.get_auth_header() == "Bearer jwt-concurrent"
        assert mock_urlopen.call_count == 1

        auth.destroy()
