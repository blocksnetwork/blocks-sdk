"""
ConsumerAuth -- consumer-side JWT authentication with transparent refresh.

Three token acquisition modes:
  Mode 1 (api_key): POST to /api/v1/auth/agent/consumer-token, refresh
    via /api/v1/auth/agent/refresh.
  Mode 2 (token_endpoint): POST with empty JSON body to a customer-owned
    proxy endpoint.
  Mode 3 (token_provider): call the provided sync function.

Thread safety:
  - threading.Lock protects _token, _user_id, _refresh_token, _expires_at.
  - threading.Timer daemon thread for proactive refresh at 80% TTL.
  - threading.Event for concurrent on_auth_failure() waiters -- only one
    refresh runs at a time, other callers wait on the Event.
  - Token provider / network calls happen OUTSIDE the lock.
"""

from __future__ import annotations

import json
import logging
import ssl
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any, Callable, Dict, Optional, TypedDict, Union

import certifi

from .write_affinity import capture_affinity, inject_affinity

logger = logging.getLogger(__name__)

# Backoff constants for proactive refresh retry
_BACKOFF_BASE = 5.0
_BACKOFF_MAX = 30.0
_MAX_RETRIES = 3
_ERROR_CODE_REFRESH_TOKEN_INVALID = "REFRESH_TOKEN_INVALID"
_ERROR_CODE_API_KEY_INVALID = "API_KEY_INVALID"

# Proactive refresh fires at this fraction of TTL
_REFRESH_TTL_FRACTION = 0.80


class AuthRefreshFailedError(RuntimeError):
    """Raised by :meth:`TaskClient.send_message` and :meth:`TaskClient.connect`
    when the underlying :class:`ConsumerAuth` is in a known-broken refresh
    state (proactive refresh permanently failed after 3 retries).

    The original failure is chained via PEP-3134 ``__cause__``. Cleared by a
    subsequent successful reactive refresh.
    """

    def __init__(self, cause: Exception) -> None:
        super().__init__(
            f"Consumer auth refresh permanently failed: {cause}"
        )


@dataclass
class TokenResult:
    """Result from a token acquisition call."""

    token: str
    expires_in: int  # seconds
    user_id: Optional[str] = None


class _TokenEndpointConfigRequired(TypedDict):
    """Required keys of :class:`TokenEndpointConfig` (see below)."""

    url: str


class TokenEndpointConfig(_TokenEndpointConfigRequired, total=False):
    """Configuration for Mode 2 (token endpoint) token acquisition.

    Accepts an object with fetch-init-like overrides. The config form is
    additive: SDK defaults (``method: POST``, ``Content-Type: application/json``,
    empty-object body) still apply; supplied fields override the defaults.

    Fields
    ------
    url:
        The URL to POST to. **Required** (enforced both structurally via
        :class:`_TokenEndpointConfigRequired` and at runtime in
        ``_acquire_token_endpoint``; missing ``url`` raises ``ValueError``).
    headers:
        Extra headers merged on top of the SDK defaults. Use
        ``headers={"Cookie": "..."}`` for cookie-based auth (the Python
        analogue of Node's ``credentials: 'include'`` — ``urllib`` has no
        direct equivalent of fetch's ``credentials`` option, so the
        Python ``TokenEndpointConfig`` deliberately omits that field).
    body:
        JSON-serializable body that replaces the default ``{}``. Passed
        through ``json.dumps``; non-serializable values raise at call
        time.

    Note on asymmetry with Node: Node's ``TokenEndpointConfig`` also
    accepts ``credentials: 'include' | 'same-origin' | 'omit'`` because
    ``fetch`` natively supports it. Python consumers needing cookie-based
    auth must pass the cookie value via ``headers={"Cookie": ...}``.
    """

    headers: Dict[str, str]
    body: Any


TokenEndpoint = Union[str, TokenEndpointConfig]
"""Either a bare URL string (legacy form) or a :class:`TokenEndpointConfig`."""


class ConsumerAuth:
    """Consumer-side JWT authentication with transparent refresh.

    Implements the AuthProvider protocol. Manages JWT acquisition and
    refresh for three modes: api_key, token_endpoint, and token_provider.
    """

    def __init__(
        self,
        *,
        api_key: Optional[str] = None,
        token_endpoint: Optional[Union[str, TokenEndpointConfig]] = None,
        token_provider: Optional[Callable[[], TokenResult]] = None,
        base_url: Optional[str] = None,
        on_auth_error: Optional[Callable[[Exception], None]] = None,
    ) -> None:
        # Validate exactly one mode
        modes = sum(1 for m in (api_key, token_endpoint, token_provider) if m)
        if modes != 1:
            raise ValueError(
                "Exactly one of api_key, token_endpoint, or token_provider must be specified"
            )

        self._api_key = api_key
        self._token_endpoint = token_endpoint
        self._token_provider = token_provider
        self._base_url = base_url.rstrip("/") if base_url else None
        self._on_auth_error = on_auth_error

        if api_key and not self._base_url:
            raise ValueError("base_url is required for api_key mode")

        # Protected state (guarded by _lock)
        self._lock = threading.Lock()
        self._token: Optional[str] = None
        self._user_id: Optional[str] = None
        self._refresh_token_value: Optional[str] = None
        self._expires_at: float = 0.0
        self._destroyed: bool = False
        self._last_auth_error: Optional[AuthRefreshFailedError] = None

        # Proactive refresh timer (daemon thread)
        self._timer: Optional[threading.Timer] = None

        # Concurrent on_auth_failure() coordination:
        # When a refresh is in-flight, _refreshing is True and
        # _refresh_event is an Event that waiters block on.
        self._refreshing: bool = False
        self._refresh_event: Optional[threading.Event] = None
        self._refresh_success: bool = False
        self._ready: bool = False
        self._ready_lock = threading.Lock()

    # -- Public API ---------------------------------------------------------

    def init(self) -> None:
        """Acquire the initial token (synchronous, blocking).

        Must be called once before the auth provider is usable.
        Raises on failure so the caller can handle init errors.
        """
        result = self._acquire_token()
        self._store_result(result)
        self._schedule_refresh(result.expires_in)
        self._ready = True

    def get_auth_header(self) -> Optional[str]:
        """Return the current Authorization header value, or None."""
        with self._lock:
            if self._token:
                return f"Bearer {self._token}"
            return None

    def get_user_id(self) -> Optional[str]:
        """Return the userId from the last token acquisition, or None."""
        with self._lock:
            return self._user_id

    def get_last_auth_error(self) -> Optional["AuthRefreshFailedError"]:
        """Return the last permanent refresh failure, or None.

        Set when proactive refresh exhausts its 3 retries; cleared on a
        successful reactive refresh. Callers (``TaskClient.send_message`` /
        ``TaskClient.connect``) use this to fail fast before any
        authenticated request.
        """
        with self._lock:
            return self._last_auth_error

    def on_auth_failure(self) -> bool:
        """Trigger immediate refresh on 401.

        Only one refresh runs at a time. Concurrent callers wait on the
        same Event and share the result.

        Returns True if a fresh token was acquired (caller should retry).
        Returns False if refresh failed.
        """
        with self._lock:
            if self._destroyed:
                return False

            if self._refreshing:
                # Another thread is already refreshing -- wait for it
                event = self._refresh_event
            else:
                # We are the first caller -- start the refresh
                self._refreshing = True
                self._refresh_event = threading.Event()
                event = None

        if event is not None:
            # Wait for the in-flight refresh to complete
            event.wait(timeout=30)
            with self._lock:
                return self._refresh_success

        # Perform the refresh outside the lock
        try:
            result = self._acquire_token()
            self._store_result(result)
            self._schedule_refresh(result.expires_in)
            with self._lock:
                self._refresh_success = True
                self._refreshing = False
                # _last_auth_error is cleared atomically in _store_result
                # so concurrent get_last_auth_error() readers can never
                # see a stale fail-fast error once the new token is live.
                evt = self._refresh_event
                self._refresh_event = None
            if evt:
                evt.set()
            return True
        except Exception as err:
            logger.warning(
                "[ConsumerAuth] reactive refresh failed: %s", err,
            )
            with self._lock:
                self._refresh_success = False
                self._refreshing = False
                evt = self._refresh_event
                self._refresh_event = None
            if evt:
                evt.set()
            return False

    def ensure_ready(self) -> None:
        """Lazy initialization hook. Calls init() once; subsequent calls are no-ops.

        Thread-safe: uses _ready_lock (not _lock) to avoid deadlock since
        init() internally acquires _lock during token acquisition.
        """
        if self._ready:
            return
        with self._ready_lock:
            if self._ready:
                return
            self.init()
            self._ready = True

    def destroy(self) -> None:
        """Stop the refresh timer. Token remains readable for active sessions."""
        with self._lock:
            self._destroyed = True
        self._cancel_timer()

    # -- Private: token acquisition -----------------------------------------

    def _acquire_token(self) -> TokenResult:
        """Acquire a token based on the configured mode.

        Called outside the lock to avoid deadlock on blocking network calls.
        """
        try:
            if self._api_key:
                return self._acquire_api_key()
            elif self._token_endpoint:
                return self._acquire_token_endpoint()
            elif self._token_provider:
                return self._token_provider()
            raise RuntimeError("No token acquisition mode configured")
        except _ConsumerAuthHttpError as exc:
            raise RuntimeError(
                f"Consumer auth request failed: HTTP {exc.status}"
            ) from exc

    def _acquire_api_key(self) -> TokenResult:
        """Mode 1: exchange API key for consumer JWT."""
        # Check if we have a refresh token -- use refresh path
        with self._lock:
            refresh_tok = self._refresh_token_value
            has_existing = self._token is not None

        if has_existing and refresh_tok:
            return self._refresh_via_endpoint(refresh_tok)

        return self._bootstrap_api_key()

    def _bootstrap_api_key(self) -> TokenResult:
        """Initial acquisition via the consumer-token endpoint."""
        url = f"{self._base_url}/api/v1/auth/agent/consumer-token"
        payload = json.dumps({"apiKey": self._api_key}).encode("utf-8")
        data = self._http_post(url, payload, use_affinity=True)

        result = TokenResult(
            token=data["accessToken"],
            expires_in=data.get("expiresIn", 60),
            user_id=data.get("userId"),
        )
        with self._lock:
            self._refresh_token_value = data.get("refreshToken")
        return result

    def _refresh_via_endpoint(self, refresh_tok: str) -> TokenResult:
        """Refresh using the /auth/agent/refresh endpoint (API key mode)."""
        url = f"{self._base_url}/api/v1/auth/agent/refresh"
        payload = json.dumps({"refreshToken": refresh_tok}).encode("utf-8")
        headers = {
            "Authorization": f"Bearer {self._api_key}",
            "Content-Type": "application/json",
        }
        try:
            data = self._http_post(url, payload, headers=headers, use_affinity=True)
        except _ConsumerAuthHttpError as exc:
            body = exc.body if isinstance(exc.body, dict) else {}
            code = body.get("code", "")

            if code == _ERROR_CODE_REFRESH_TOKEN_INVALID:
                # Symmetric with provider AgentAuth: fall back to a fresh
                # bootstrap when refresh token rotation state is gone.
                return self._bootstrap_api_key()

            if code == _ERROR_CODE_API_KEY_INVALID:
                raise RuntimeError(
                    f"API key invalid or revoked: {body.get('error', _ERROR_CODE_API_KEY_INVALID)}"
                ) from exc

            raise RuntimeError(
                f"Consumer auth request failed: {body.get('error', f'HTTP {exc.status}')}"
            ) from exc

        result = TokenResult(
            token=data["accessToken"],
            expires_in=data.get("expiresIn", 60),
            user_id=data.get("userId"),
        )
        with self._lock:
            self._refresh_token_value = data.get("refreshToken")
        return result

    def _acquire_token_endpoint(self) -> TokenResult:
        """Mode 2: POST to customer-owned proxy endpoint.

        Accepts either a bare URL string (legacy form) or a
        :class:`TokenEndpointConfig` dict with optional ``headers`` and
        ``body`` overrides. SDK defaults (``Content-Type: application/json``,
        empty-object body) still apply; user-supplied values win on merge.
        """
        cfg = self._token_endpoint
        if isinstance(cfg, str):
            url = cfg
            extra_headers: Dict[str, str] = {}
            body: Any = {}
        else:
            # TokenEndpointConfig dict form. ``url`` is structurally
            # required via _TokenEndpointConfigRequired, but since the
            # instance is a plain dict at runtime we still validate
            # explicitly — a descriptive ValueError is friendlier than
            # a bare KeyError for callers who missed the type hint.
            cfg_url = cfg.get("url")  # type: ignore[union-attr]
            if not isinstance(cfg_url, str) or not cfg_url:
                raise ValueError(
                    "TokenEndpointConfig.url is required and must be a non-empty string"
                )
            url = cfg_url
            extra_headers = cfg.get("headers") or {}  # type: ignore[union-attr]
            body = cfg.get("body") if cfg.get("body") is not None else {}  # type: ignore[union-attr]

        # Merge: SDK defaults first, user-supplied overrides win.
        headers = {"Content-Type": "application/json", **extra_headers}
        payload = json.dumps(body).encode("utf-8")

        data = self._http_post(url, payload, headers=headers)

        return TokenResult(
            token=data["token"],
            expires_in=data.get("expiresIn", 60),
            user_id=data.get("userId"),
        )

    # -- Private: state management ------------------------------------------

    def _store_result(self, result: TokenResult) -> None:
        """Atomically update token state under the lock.

        Clearing ``_last_auth_error`` here (rather than in a separate
        critical section in ``on_auth_failure``) closes the thread
        interleaving where a concurrent RPC/file-upload caller could
        read a stale fail-fast error after a successful reactive
        refresh has already applied the new token. Mirrors Node's
        ``_applyTokenResult``.
        """
        with self._lock:
            self._token = result.token
            if result.user_id is not None:
                self._user_id = result.user_id
            self._expires_at = time.monotonic() + result.expires_in
            self._last_auth_error = None

    # -- Private: proactive refresh -----------------------------------------

    def _schedule_refresh(self, expires_in: int) -> None:
        """Schedule a proactive refresh at 80% of the token TTL."""
        self._cancel_timer()
        with self._lock:
            if self._destroyed:
                return
        delay = max(1.0, expires_in * _REFRESH_TTL_FRACTION)
        timer = threading.Timer(delay, self._proactive_refresh)
        timer.daemon = True
        timer.start()
        self._timer = timer

    def _proactive_refresh(self) -> None:
        """Proactive refresh callback with exponential backoff retry."""
        with self._lock:
            if self._destroyed:
                return

        for attempt in range(_MAX_RETRIES):
            try:
                result = self._acquire_token()
                self._store_result(result)
                self._schedule_refresh(result.expires_in)
                return
            except Exception as err:
                if attempt == _MAX_RETRIES - 1:
                    # Permanent failure
                    logger.warning(
                        "[ConsumerAuth] proactive refresh failed after %d retries: %s",
                        _MAX_RETRIES, err,
                    )
                    permanent = AuthRefreshFailedError(err)
                    permanent.__cause__ = err
                    permanent.__suppress_context__ = True
                    with self._lock:
                        self._last_auth_error = permanent
                    if self._on_auth_error:
                        try:
                            self._on_auth_error(err)
                        except Exception:
                            pass
                    return
                delay = min(_BACKOFF_BASE * (2 ** attempt), _BACKOFF_MAX)
                time.sleep(delay)

    def _cancel_timer(self) -> None:
        """Cancel the proactive refresh timer if one is scheduled."""
        timer = self._timer
        if timer is not None:
            timer.cancel()
            self._timer = None

    # -- Private: HTTP helper -----------------------------------------------

    def _http_post(
        self,
        url: str,
        payload: bytes,
        headers: Optional[Dict[str, str]] = None,
        *,
        use_affinity: bool = False,
    ) -> Dict[str, Any]:
        """POST JSON and return parsed response. Raises on non-2xx.

        ``use_affinity`` must be True only for Blocks-backend endpoints. Mode-2
        token-endpoint traffic goes to a customer-owned proxy and must not
        exchange ``X-Write-Affinity`` in either direction.
        """
        ssl_ctx = ssl.create_default_context(cafile=certifi.where())
        req_headers: Dict[str, str] = {"Content-Type": "application/json"}
        if headers:
            req_headers.update(headers)
        if use_affinity:
            inject_affinity(req_headers)

        req = urllib.request.Request(
            url, data=payload, headers=req_headers, method="POST"
        )

        try:
            with urllib.request.urlopen(req, context=ssl_ctx, timeout=30) as resp:
                if use_affinity:
                    capture_affinity(resp)
                body = resp.read().decode("utf-8")
                return json.loads(body) if body else {}
        except urllib.error.HTTPError as exc:
            error_body: Any = {}
            try:
                raw = exc.read().decode("utf-8") if exc.fp else ""
                if raw:
                    try:
                        error_body = json.loads(raw)
                    except json.JSONDecodeError:
                        error_body = raw
            except Exception:
                pass
            raise _ConsumerAuthHttpError(exc.code, error_body) from exc
        except urllib.error.URLError as exc:
            raise RuntimeError(
                f"Consumer auth request failed: {exc.reason}"
            ) from exc


class _ConsumerAuthHttpError(Exception):
    """Internal exception carrying HTTP status and parsed body."""

    def __init__(self, status: int, body: Any = None) -> None:
        super().__init__(f"HTTP {status}")
        self.status = status
        self.body = body
