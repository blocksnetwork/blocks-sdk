"""
JSON-RPC 2.0 transport for PubNub Functions RPC gateway.

Port of ``rpc-client.ts``.

Provides:
- :func:`rpc_endpoint` -- builds the RPC gateway URL from a subscribe key
- :func:`call_rpc` -- sends a JSON-RPC 2.0 request with retry and error handling
- :class:`RpcError` -- structured error for JSON-RPC error responses
- :class:`BillingModeMismatchError` -- typed subclass for ``BillingModeMismatch``
"""

from __future__ import annotations

import json
import random
import ssl
import time
import urllib.error
import urllib.request
from typing import Any, Callable, Dict, Literal, Optional, TypeVar

import certifi

from .protocol_version import CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER
from .write_affinity import capture_affinity, inject_affinity

T = TypeVar("T")

# ============================================================================
# RPC header merge
# ============================================================================

# SDK-owned headers that caller-supplied ``rpc_headers`` may never override.
# Compared case-insensitively (via ``.lower()``) so no casing of a caller key
# can slip a forged Authorization / Content-Type / protocol-version header
# past the SDK-owned base. Parity with Node ``rpc-client``.
_PROTECTED_RPC_HEADERS = {
    "authorization",
    "content-type",
    PROTOCOL_VERSION_HEADER.lower(),
    "x-write-affinity",  # SDK-managed routing state; §14b no-fabrication
}


def _merge_rpc_headers(base: Dict[str, str], extra: Optional[Dict[str, str]]) -> Dict[str, str]:
    merged = {
        k: v for k, v in (extra or {}).items()
        if k.lower() not in _PROTECTED_RPC_HEADERS
    }
    merged.update(base)  # SDK-owned base wins
    return merged


# ============================================================================
# RPC Error
# ============================================================================


class RpcError(Exception):
    """Structured error for JSON-RPC error responses."""

    def __init__(
        self,
        rpc_message: str,
        code: Optional[int] = None,
        data: Any = None,
    ) -> None:
        super().__init__(f"[RPC] {rpc_message}")
        self.rpc_message = rpc_message
        self.code = code
        self.data = data


class BillingModeMismatchError(RpcError):
    """Raised when the backend rejects a SendMessage with ``BillingModeMismatch``.

    The backend emits this when ``params.billingMode`` does not match the
    target agent's registered billing mode. ``error.data`` carries
    ``{ expected, got }`` (both ``'free' | 'paid'``).

    The SDK does NOT auto-retry with the corrected mode and does NOT
    auto-correct from the registry. Callers fix their code (typically by
    constructing the ``TaskClient`` with the correct ``billing_mode``)
    and reissue the request.
    """

    def __init__(
        self,
        rpc_message: str,
        expected: Literal["free", "paid"],
        got: Literal["free", "paid"],
        code: Optional[int] = None,
        data: Any = None,
    ) -> None:
        # Preserve RpcError.code (the JSON-RPC numeric code carried by the
        # envelope, e.g. -32000) so callers see normal RpcError behavior on
        # the typed subclass — parity with Node's BillingModeMismatchError.
        super().__init__(rpc_message, code=code, data=data)
        self.expected: Literal["free", "paid"] = expected
        self.got: Literal["free", "paid"] = got


def _maybe_billing_mode_mismatch(err: RpcError) -> RpcError:
    """Promote a generic ``RpcError`` to ``BillingModeMismatchError`` when
    the JSON-RPC ``error.data`` carries ``code: 'BillingModeMismatch'``.

    Backend wire shape (per Phase 1 ``bmc-data``):

        error.data = {
          "code": "BillingModeMismatch",
          "details": { "expected": "free"|"paid", "got": "free"|"paid" }
        }

    Returns the original error unchanged when ``code`` is not present or
    not ``'BillingModeMismatch'`` so this helper is safe to apply on every
    error path.
    """
    data = err.data
    if not isinstance(data, dict):
        return err
    if data.get("code") != "BillingModeMismatch":
        return err
    details = data.get("details")
    if not isinstance(details, dict):
        return err
    expected = details.get("expected")
    got = details.get("got")
    if expected not in ("free", "paid") or got not in ("free", "paid"):
        return err
    return BillingModeMismatchError(
        err.rpc_message,
        expected=expected,
        got=got,
        code=err.code,
        data=data,
    )


# ============================================================================
# RPC Endpoint
# ============================================================================


def rpc_endpoint(subscribe_key: str, base_url: Optional[str] = None) -> str:
    """Build the RPC gateway URL from a backend root URL."""
    if not base_url:
        raise ValueError(
            "[RPC] base_url is required. Provide it via CDM config or pass base_url explicitly."
        )
    return f"{base_url.rstrip('/')}/api/v1/rpc"


# ============================================================================
# Retry Helper
# ============================================================================

_TRANSIENT_MARKERS = ("ENOTFOUND", "ETIMEDOUT", "ECONNRESET", "NetworkIssues")

# Backend AppError codes (carried in JSON-RPC ``error.data.code``, where the
# transport code is a generic JSON-RPC number rather than 401) that mean the
# JWT's underlying embedded session was rotated/superseded and a refresh will
# recover. Parity with Node ``rpc-client.ts``
# ``AUTH_REFRESH_RETRYABLE_RPC_DATA_CODES``. Raised in auth middleware BEFORE
# the task is processed, so retrying once cannot double-submit:
#   - EMBEDDED_JWT_REVOKED: refresh-token rotation race -> refresh yields the
#     rotated token and the retry succeeds.
#   - EMBEDDED_JWT_LOGOUT: refresh re-checks the logout watermark and 401s,
#     clearing the dead session at once.
#   - EMBEDDED_JWT_KILLED: refresh drops killed agents; survivors re-mint a
#     narrowed token, all-killed 401s and clears.
#   - EMBEDDED_JWT_SCOPE_DRIFT: refresh re-mints scoped to the current
#     refresh-token row so the new JWT matches and the retry succeeds.
#   - AGENT_OUT_OF_SCOPE: one of several scoped agents had its grant
#     revoked; refresh narrows the token to the still-reachable subset so
#     requests to the surviving agents recover (all-revoked 401s and clears).
_AUTH_REFRESH_RETRYABLE_RPC_DATA_CODES = frozenset(
    {
        "EMBEDDED_JWT_REVOKED",
        "EMBEDDED_JWT_LOGOUT",
        "EMBEDDED_JWT_KILLED",
        "EMBEDDED_JWT_SCOPE_DRIFT",
        "AGENT_OUT_OF_SCOPE",
    }
)


def _is_auth_refresh_retryable(err: RpcError) -> bool:
    """Whether an ``RpcError`` should trigger one reactive refresh + retry.

    Matches either a transport 401, or an embedded-auth liveness code carried
    in the JSON-RPC ``error.data.code`` (the transport ``code`` being a generic
    JSON-RPC code is why a plain ``code == 401`` check misses these).
    """
    if err.code == 401:
        return True
    data = err.data
    return (
        isinstance(data, dict)
        and data.get("code") in _AUTH_REFRESH_RETRYABLE_RPC_DATA_CODES
    )


def _is_transient(err: BaseException) -> bool:
    """Return ``True`` if the error looks like a transient network issue."""
    err_str = str(err)
    return any(marker in err_str for marker in _TRANSIENT_MARKERS)


def with_retry(
    fn: Callable[[], T],
    max_retries: int = 3,
    base_delay: float = 0.5,
) -> T:
    """Retry *fn* with exponential back-off on transient network errors."""
    last_error: Optional[BaseException] = None
    for attempt in range(max_retries):
        try:
            return fn()
        except Exception as err:
            last_error = err
            if not _is_transient(err) or attempt == max_retries - 1:
                raise
            delay = base_delay * (2 ** attempt) + random.random() * 0.1
            time.sleep(delay)
    raise last_error  # type: ignore[misc]


# ============================================================================
# callRpc
# ============================================================================


def call_rpc(
    subscribe_key: str,
    method: str,
    params: Dict[str, Any],
    base_url: Optional[str] = None,
    agent_auth: Any = None,
    auth_provider: Any = None,
    rpc_headers: Optional[Dict[str, str]] = None,
) -> Any:
    """Send a JSON-RPC 2.0 request to the PubNub Functions RPC gateway.

    Returns the ``result`` field from the JSON-RPC response.
    Raises :class:`RpcError` on HTTP or JSON-RPC errors.

    When ``agent_auth`` is provided, uses its ``authenticated_request()``
    for automatic 401 retry with token refresh.

    When ``auth_provider`` is provided, uses its ``get_auth_header()``
    for the Authorization header and ``on_auth_failure()`` for 401 retry.
    """
    url = rpc_endpoint(subscribe_key, base_url)
    request_id = f"rpc-{int(time.time() * 1000)}-{random.randbytes(4).hex()}"

    rpc_payload = json.dumps(
        {"jsonrpc": "2.0", "id": request_id, "method": method, "params": params}
    ).encode("utf-8")

    def _parse_rpc_response(data: Any) -> Any:
        if isinstance(data, dict) and data.get("error"):
            err = data["error"]
            message = (
                (err.get("data") or {}).get("message")
                or err.get("message")
                or "Unknown RPC error"
            )
            base_err = RpcError(message, err.get("code"), err.get("data"))
            raise _maybe_billing_mode_mismatch(base_err)
        return data.get("result") if isinstance(data, dict) else data

    # Pre-flight: when the auth provider has a recorded permanent-refresh
    # error, attempt one reactive recovery. On failure the typed
    # AuthRefreshFailedError is raised before any network attempt so
    # consumers don't see an opaque 401 from the doomed RPC.
    from .auth_provider import preflight_auth_or_raise
    preflight_auth_or_raise(auth_provider)

    if auth_provider is not None and hasattr(auth_provider, "ensure_ready"):
        auth_provider.ensure_ready()

    if agent_auth is not None:
        def _do_auth_request() -> Any:
            resp_data, _status = agent_auth.authenticated_request(
                url,
                method="POST",
                body=rpc_payload,
                headers=_merge_rpc_headers(
                    {
                        "Content-Type": "application/json",
                        PROTOCOL_VERSION_HEADER: CURRENT_PROTOCOL_VERSION,
                    },
                    rpc_headers,
                ),
            )
            return _parse_rpc_response(resp_data)

        return with_retry(_do_auth_request)

    def _build_headers() -> Dict[str, str]:
        base: Dict[str, str] = {
            "Content-Type": "application/json",
            PROTOCOL_VERSION_HEADER: CURRENT_PROTOCOL_VERSION,
        }
        if auth_provider is not None:
            auth_header = auth_provider.get_auth_header()
            if auth_header:
                base["Authorization"] = auth_header
        hdrs = _merge_rpc_headers(base, rpc_headers)
        inject_affinity(hdrs)
        return hdrs

    def _execute_request(hdrs: Dict[str, str]) -> Any:
        ssl_ctx = ssl.create_default_context(cafile=certifi.where())
        req = urllib.request.Request(url, data=rpc_payload, headers=hdrs, method="POST")

        try:
            with urllib.request.urlopen(req, context=ssl_ctx, timeout=30) as resp:
                capture_affinity(resp)
                body = resp.read().decode("utf-8")
                data = json.loads(body) if body else {}
        except urllib.error.HTTPError as exc:
            raise RpcError(f"HTTP {exc.code}", exc.code) from exc
        except urllib.error.URLError as exc:
            raise RpcError(str(exc.reason)) from exc

        return _parse_rpc_response(data)

    def _do_request() -> Any:
        hdrs = _build_headers()
        try:
            return _execute_request(hdrs)
        except RpcError as err:
            # Reactive refresh: retry once if auth_provider can refresh. Fires
            # on a transport 401 OR an embedded-auth liveness code carried in
            # error.data.code (parity with Node rpc-client).
            if _is_auth_refresh_retryable(err) and auth_provider is not None:
                if auth_provider.on_auth_failure():
                    return _execute_request(_build_headers())
            raise

    return with_retry(_do_request)
