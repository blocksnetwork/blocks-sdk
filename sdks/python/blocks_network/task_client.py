"""
TaskClient -- send tasks to other agents via the PubNub Functions RPC gateway.

Port of ``task-client.ts``.

Provides:
- :func:`subscribe_to_task` -- standalone subscribe to real-time task events
- :class:`TaskClient` -- main client for sending messages and managing tasks
"""

from __future__ import annotations

import logging
import os
import threading
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any, Callable, Dict, List, Literal, Optional, Union

from .agent_registry import get_agent
from .auth_provider import AuthProvider, preflight_auth_or_raise
from .channel_manager import task_channel
from .pubnub_compat import patch_pubnub_file_message
from .rpc_client import BillingModeMismatchError, call_rpc
from .terminal_delivery_tracker import TerminalDeliveryTracker
from .task_session import (
    TERMINAL_STATES,
    CallbackErrorContext,
    TaskSession,
)
from .types import ArtifactRef

if TYPE_CHECKING:
    from .consumer_auth import TokenEndpointConfig

patch_pubnub_file_message()

logger = logging.getLogger(__name__)


# ============================================================================
# Exceptions
# ============================================================================


class AnonTaskAccessDenied(Exception):
    """Raised when the anonymous task-read-token endpoint returns 403.

    Indicates the caller's fingerprint does not match the submitter
    fingerprint recorded on the task, or the task no longer satisfies
    the public+free gate required by the anonymous read path. The
    error message intentionally embeds ``403`` so callers that match
    the shared 403 fallback path see the same behavior as an
    authenticated non-owner.
    """


# ============================================================================
# Dataclasses
# ============================================================================


@dataclass
class SendMessageRequestPart:
    """A single request part for :meth:`TaskClient.send_message`.

    Supports text-only parts, inline file parts (small files base64-encoded),
    and uploaded file parts (large files via pre-signed URL flow).
    """

    part_id: Optional[str] = None
    text: Optional[str] = None
    content_type: Optional[str] = None
    file: Optional[bytes] = None
    file_name: Optional[str] = None


@dataclass
class SendMessageParams:
    """Parameters for :meth:`TaskClient.send_message`.

    Each entry in ``request_parts`` may include an optional ``partId``
    field mapping the part to a declared ``io.inputs[].id`` in the
    target agent's card.

    ``idempotency_key`` is an optional caller-supplied string for
    duplicate detection. If the same key is submitted twice by the
    same authenticated user, the server returns the existing task
    instead of creating a new one.

    ``consumer_public_key`` is included in ``extensions.blocks`` on the
    wire when set. Required by agents whose card declares
    ``security.encryption.consumerKeyRequired: true``.
    """

    agent_name: str = ""
    request_parts: List[Any] = field(default_factory=list)
    idempotency_key: Optional[str] = None
    owner_id: str = ""
    task_kind: Optional[str] = None
    duration: Optional[int] = None
    consumer_public_key: Optional[str] = None
    # Request live streaming for this task (request tasks only). ``True``
    # streams token output if the agent supports it; ``False`` suppresses
    # streaming (status updates + final result only). Omitting it (``None``)
    # applies the server default, which is no streaming — pass ``stream=True``
    # to opt in. Ignored for pipe tasks (pipe streaming is capability-driven).
    # Sent as ``extensions.blocks.stream``.
    stream: Optional[bool] = None
    push_notification_config: Optional[Dict[str, Any]] = None
    retry_policy: Optional[Dict[str, Any]] = None
    auto_drain: Optional[bool] = None
    # Duration in seconds the session waits for already-open streams to
    # finish draining naturally after a terminal event. Defaults to 30.0
    # seconds. Ignored when ``auto_drain`` is False.
    #
    # Only applies to streams that were opened while the task was still
    # active. Unopened streams on a terminal session raise
    # ``StreamUnavailableError`` per the merged t7c baseline.
    drain_window_s: Optional[float] = None


@dataclass
class TaskEventCallbacks:
    """Callbacks for task event dispatch."""

    on_progress: Optional[Callable[[Dict[str, Any]], None]] = None
    on_artifact: Optional[Callable[[Dict[str, Any]], None]] = None
    on_terminal: Optional[Callable[[Dict[str, Any]], None]] = None
    on_cancel_requested: Optional[Callable[[Dict[str, Any]], None]] = None
    on_system: Optional[Callable[[Dict[str, Any]], None]] = None
    on_event: Optional[Callable[[Dict[str, Any]], None]] = None
    on_error: Optional[Callable[[Exception, CallbackErrorContext], None]] = None


@dataclass
class TaskInfo:
    """Information about a task returned from RPC."""

    task_id: str = ""
    agent_name: Optional[str] = None
    owner: Optional[str] = None
    state: Optional[str] = None
    created_time: Optional[str] = None
    updated_time: Optional[str] = None
    extra: Dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "TaskInfo":
        """Deserialize from camelCase dict."""
        known_keys = {
            "taskId", "agentName", "owner", "state", "createdTime", "updatedTime",
        }
        extra = {k: v for k, v in d.items() if k not in known_keys}
        return cls(
            task_id=d.get("taskId", ""),
            agent_name=d.get("agentName"),
            owner=d.get("owner"),
            state=d.get("state"),
            created_time=d.get("createdTime"),
            updated_time=d.get("updatedTime"),
            extra=extra,
        )


@dataclass
class ListTasksParams:
    """Parameters for :meth:`TaskClient.list_tasks`."""

    owner_id: Optional[str] = None
    agent_name: Optional[str] = None
    state: Optional[str] = None
    limit: Optional[int] = None
    cursor: Optional[str] = None


@dataclass
class ListTasksResult:
    """Result from :meth:`TaskClient.list_tasks`."""

    tasks: List[TaskInfo] = field(default_factory=list)
    next: Optional[str] = None
    total_count: Optional[int] = None


# ============================================================================
# TaskSubscription
# ============================================================================


class TaskSubscription:
    """Wraps an unsubscribe callable for cleanup."""

    def __init__(self, unsubscribe_fn: Callable[[], None]) -> None:
        self._unsubscribe_fn = unsubscribe_fn

    def unsubscribe(self) -> None:
        self._unsubscribe_fn()


# ============================================================================
# Standalone subscribe helper
# ============================================================================


def _route_subscribe_error(
    error: Exception,
    callback_type: str,
    event: Any,
    on_error: Optional[Callable[[Exception, CallbackErrorContext], None]],
) -> None:
    """Route a callback error in subscribe_to_task to on_error or warn log."""
    ctx = CallbackErrorContext(
        entry_point="subscribeToTask",
        callback_type=callback_type,
        event=event,
    )
    if on_error is not None:
        try:
            on_error(error, ctx)
        except Exception:
            pass  # prevent infinite loop
    else:
        logger.warning(
            "[subscribeToTask] callback error in %s: %s",
            callback_type, error,
        )


def subscribe_to_task(
    pubnub: Any,
    task_id: str,
    owner_id: str,
    callbacks: TaskEventCallbacks,
) -> TaskSubscription:
    """Subscribe to real-time task events on channel ``u.{ownerId}.{taskId}``.

    Dispatches to typed callbacks based on event type:
    - ``"progress"`` -> on_progress
    - ``"artifact"`` -> on_artifact
    - ``"terminal"`` -> on_terminal (deduplicated; first-terminal-wins)
    - ``"cancel_requested"`` -> on_cancel_requested
    - ``"system"``   -> on_system
    - (any)          -> on_event (catch-all)

    Returns a :class:`TaskSubscription` with ``unsubscribe()`` for cleanup.
    """
    channel = task_channel(task_id, owner_id)
    # Per-subscription tracker so on_terminal fires at most
    # once even if the wire delivers two terminals (scanner force-cancel
    # + agent's delayed terminal).
    terminal_tracker = TerminalDeliveryTracker()
    # cancel_requested fires zero-or-once per subscription.
    # Tracked via a 1-element list (mutable closure-captured state without
    # the `nonlocal` boilerplate).
    cancel_requested_state: List[bool] = [False]

    # Build listener compatible with PubNub Python SDK (SubscribeCallback subclass)
    # with ImportError fallback for tests (same pattern as agent_instance.py:594-625).
    listener: Any = None
    try:
        from pubnub.callbacks import SubscribeCallback

        class _TaskListener(SubscribeCallback):
            def message(self, pubnub_instance: Any, event: Any) -> None:
                _dispatch_event(
                    event,
                    channel,
                    callbacks,
                    terminal_tracker,
                    cancel_requested_state,
                )

            def presence(self, pubnub_instance: Any, event: Any) -> None:
                pass

            def status(self, pubnub_instance: Any, event: Any) -> None:
                pass

            def file_message(self, file_message: Any) -> None:
                pass

        listener = _TaskListener()
    except ImportError:
        listener = {
            "message": lambda event: _dispatch_event(
                event,
                channel,
                callbacks,
                terminal_tracker,
                cancel_requested_state,
            ),
        }

    pubnub.add_listener(listener)
    # with_timetoken(1000) asks PubNub to replay everything still in the
    # channel's in-memory cache (per the SDK contract). Using 0 would
    # mean "initial subscribe, no catch-up" and leaves the
    # publish-before-subscribe race unfixed.
    #
    # Note: this standalone helper does not dedup replayed messages.
    # Consumers who care about exactly-once delivery should use
    # TaskSession (via TaskClient.connect / send_message), which tracks
    # seen timetokens in its _handle_event layer.
    pubnub.subscribe().channels([channel]).with_timetoken(1000).execute()

    def _unsubscribe() -> None:
        pubnub.remove_listener(listener)
        pubnub.unsubscribe().channels([channel]).execute()

    return TaskSubscription(_unsubscribe)


def _dispatch_event(
    event: Any,
    channel: str,
    callbacks: TaskEventCallbacks,
    terminal_tracker: Optional[TerminalDeliveryTracker] = None,
    cancel_requested_state: Optional[List[bool]] = None,
) -> None:
    """Dispatch a PubNub message event to typed callbacks.

    ``terminal_tracker`` and ``cancel_requested_state`` are optional for
    backward compatibility with direct test callers. When omitted, fresh
    per-call instances are created so the dedup guarantees are preserved
    per call (single dispatch paths like unit tests don't need cross-call
    dedup).
    """
    if terminal_tracker is None:
        terminal_tracker = TerminalDeliveryTracker()
    if cancel_requested_state is None:
        cancel_requested_state = [False]
    # Support both PubNub SDK event objects and plain dicts
    evt_channel = getattr(event, "channel", None) or (
        event.get("channel") if isinstance(event, dict) else None
    )
    if evt_channel != channel:
        return

    msg = getattr(event, "message", None) or (
        event.get("message") if isinstance(event, dict) else None
    )
    if not msg or not isinstance(msg, dict) or "type" not in msg:
        return

    on_error = callbacks.on_error

    # Catch-all
    if callbacks.on_event:
        try:
            callbacks.on_event(msg)
        except Exception as err:
            _route_subscribe_error(err, "onEvent", msg, on_error)

    # Typed dispatch
    msg_type = msg["type"]
    if msg_type == "progress" and callbacks.on_progress:
        try:
            callbacks.on_progress(msg)
        except Exception as err:
            _route_subscribe_error(err, "onProgress", msg, on_error)
    elif msg_type == "artifact" and callbacks.on_artifact:
        try:
            callbacks.on_artifact(msg)
        except Exception as err:
            _route_subscribe_error(err, "onArtifact", msg, on_error)
    elif msg_type == "terminal":
        # Dedup terminal — first-terminal-wins. A duplicate
        # wire terminal (scanner force-cancel + agent's delayed
        # terminal) is silently dropped before any callback fires.
        def _deliver_terminal(e: dict) -> None:
            if callbacks.on_terminal:
                try:
                    callbacks.on_terminal(e)
                except Exception as err:
                    _route_subscribe_error(err, "onTerminal", e, on_error)

        terminal_tracker.try_deliver(msg, _deliver_terminal)
    elif msg_type == "cancel_requested":
        # Backend acknowledgment of a cooperative cancel.
        # Two suppression gates: terminal-already-delivered (causality)
        # and cancel_requested-already-delivered (duplicate wire emission,
        # e.g. PubNub cache replay).
        if terminal_tracker.is_delivered:
            return
        if cancel_requested_state[0]:
            return
        cancel_requested_state[0] = True
        if callbacks.on_cancel_requested:
            try:
                callbacks.on_cancel_requested(msg)
            except Exception as err:
                _route_subscribe_error(err, "onCancelRequested", msg, on_error)
    elif msg_type == "system" and callbacks.on_system:
        try:
            callbacks.on_system(msg)
        except Exception as err:
            _route_subscribe_error(err, "onSystem", msg, on_error)


# ============================================================================
# TaskClient
# ============================================================================


class TaskClient:
    """Client for sending tasks to other agents via JSON-RPC.

    Parameters
    ----------
    billing_mode:
        Required. Caller-owned billing mode of the target agent
        (``'free'`` or ``'paid'``). Threaded into every SendMessage RPC
        params dict on the wire as camelCase ``billingMode``. The
        backend rejects mismatches with a ``BillingModeMismatch`` error
        carrying ``expected``/``got``; the SDK surfaces that as
        :class:`BillingModeMismatchError` and does NOT auto-retry or
        auto-correct.
    subscribe_key:
        PubNub subscribe key.
    publish_key:
        Optional PubNub publish key (for stream I/O via StreamRef.open).
    auth_provider:
        Optional low-level auth provider for advanced/internal use.
    pubnub:
        Shared PubNub instance for low-level ``subscribe_to_task()``.
    create_pubnub:
        Shared factory for low-level ``subscribe_to_task()``. Creates once, caches.
    create_session_pubnub:
        Per-session factory for ``send_message()`` -> ``TaskSession`` eager
        subscriptions. Must return a fresh PubNub client per call so each
        session gets its own token-isolated instance. Not used by
        ``subscribe_to_task()``.
    default_owner_id:
        Default owner ID for send_message calls.
    """

    def __init__(
        self,
        subscribe_key: str,
        billing_mode: Literal["free", "paid"],
        publish_key: Optional[str] = None,
        pubnub: Any = None,
        create_pubnub: Optional[Callable[[], Any]] = None,
        create_session_pubnub: Optional[Callable[[], Any]] = None,
        default_owner_id: Optional[str] = None,
        base_url: Optional[str] = None,
        agent_auth: Any = None,
        auth_provider: Optional[AuthProvider] = None,
        anon_fingerprint: Optional[str] = None,
        rpc_headers: Optional[Dict[str, str]] = None,
    ) -> None:
        if billing_mode not in ("free", "paid"):
            raise ValueError(
                f"billing_mode must be 'free' or 'paid', got {billing_mode!r}"
            )
        self._subscribe_key = subscribe_key
        self._billing_mode: Literal["free", "paid"] = billing_mode
        self._publish_key = publish_key or ""
        self._pubnub = pubnub
        self._create_pubnub = create_pubnub
        self._create_session_pubnub_factory = create_session_pubnub
        self._default_owner_id = default_owner_id
        self._base_url = base_url
        self._agent_auth = agent_auth
        # We own the PubNub instance only if it will be created via the factory
        self._owns_pubnub: bool = pubnub is None and create_pubnub is not None

        self._auth_provider: Optional[AuthProvider] = auth_provider

        # ConsumerAuth instance (set by create() when using provider modes)
        self._consumer_auth: Any = None

        # Anonymous consumer mode: when set, connect() skips the auth-provider
        # gate and mints read tokens via the fingerprint-gated public endpoint.
        self._anon_fingerprint: Optional[str] = anon_fingerprint

        # Optional caller-supplied headers merged UNDER SDK-owned headers on
        # every RPC. Protected headers (Authorization / Content-Type /
        # protocol version / X-Write-Affinity) are enforced by the rpc-client
        # merge. Default-off.
        self._rpc_headers: Optional[Dict[str, str]] = rpc_headers

    # -- Factory classmethod ---------------------------------------------------

    @classmethod
    def create(
        cls,
        billing_mode: Literal["free", "paid"],
        cdm_url: Optional[str] = None,
        subscribe_key: Optional[str] = None,
        publish_key: Optional[str] = None,
        base_url: Optional[str] = None,
        api_key: Optional[str] = None,
        token_endpoint: Optional[Union[str, "TokenEndpointConfig"]] = None,
        token_provider: Optional[Callable[[], Any]] = None,
        on_auth_error: Optional[Callable[[Exception], None]] = None,
        anon_fingerprint: Optional[str] = None,
        rpc_headers: Optional[Dict[str, str]] = None,
    ) -> "TaskClient":
        """Create a TaskClient from environment variables or CDM config.

        Resolution order for each config value:
        - Explicit options (if provided)
        - Environment variables (BLOCKS_*)
        - CDM config (fetched from cdm_url or BLOCKS_CDM_URL)

        Parameters
        ----------
        billing_mode:
            Required. Billing mode of the target agent. ``'free'`` selects
            the playground keyset; ``'paid'`` selects the network keyset.
        cdm_url:
            CDM config URL. Falls back to BLOCKS_CDM_URL env var.
        subscribe_key:
            Explicit subscribe key (overrides env/CDM).
        publish_key:
            Explicit publish key (overrides env/CDM).
        base_url:
            Explicit backend URL (overrides env/CDM).
        api_key:
            API key for consumer-token endpoint.
        token_endpoint:
            Customer-owned proxy endpoint URL.
        token_provider:
            Custom sync function returning TokenResult. Mutually
            exclusive with other provider modes.
        on_auth_error:
            Called on permanent refresh failure.
        anon_fingerprint:
            Anonymous consumer mode. When set, the TaskClient skips the
            auth-provider path entirely and instead mints read tokens via
            ``POST /api/v1/auth/anon-task-read-token`` with this fingerprint.
            Mutually exclusive with ``api_key``, ``token_endpoint``, and
            ``token_provider``. Only supported when ``billing_mode='free'``.
            Intended for the Playground's anonymous-viewer flow; non-browser
            SDK callers should not use this.
        """
        from .cdm_config import fetch_cdm_config

        if billing_mode not in ("free", "paid"):
            raise ValueError(
                f"billing_mode must be 'free' or 'paid', got {billing_mode!r}"
            )

        # Mutual exclusion validation: at most one of the four modes.
        provider_modes = sum(
            1
            for m in (api_key, token_endpoint, token_provider, anon_fingerprint)
            if m
        )
        if provider_modes > 1:
            raise ValueError(
                "Only one token provider mode may be specified"
            )

        # Anon mode is billing_mode='free' only: reject before any CDM fetch.
        if anon_fingerprint and billing_mode != "free":
            raise ValueError(
                "TaskClient.create() with anon_fingerprint requires "
                "billing_mode='free'"
            )

        # Fetch CDM config
        effective_cdm_url = cdm_url or os.environ.get("BLOCKS_CDM_URL") or None
        cdm = fetch_cdm_config(effective_cdm_url)

        # Select keyset based on billing_mode:
        # 'free' -> playground keyset, 'paid' -> network keyset.
        if billing_mode == "free":
            keyset = cdm.playground
        else:
            keyset = cdm.network

        # Resolve each value: explicit > env > CDM
        resolved_subscribe_key = (
            subscribe_key
            or os.environ.get("BLOCKS_SUBSCRIBE_KEY")
            or keyset.subscribe_key
        )
        resolved_publish_key = (
            publish_key
            or os.environ.get("BLOCKS_PUBLISH_KEY")
            or keyset.publish_key
        )
        resolved_base_url = (
            base_url
            or os.environ.get("BLOCKS_BACKEND_URL")
            or cdm.api.base_url
        )

        if not resolved_subscribe_key:
            raise ValueError(
                "subscribe_key could not be resolved from explicit option, "
                "BLOCKS_SUBSCRIBE_KEY env var, or CDM config"
            )

        if not resolved_base_url:
            raise ValueError(
                "base_url could not be resolved from explicit option, "
                "BLOCKS_BACKEND_URL env var, or CDM config. "
                "base_url is required for RPC calls."
            )

        # Build auth provider (skipped in anon mode -- connect() routes to
        # the fingerprint-gated public endpoint, not task-read-token).
        consumer_auth_instance = None
        auth_provider_instance: Optional[AuthProvider] = None

        authed_provider_modes = sum(
            1 for m in (api_key, token_endpoint, token_provider) if m
        )
        if authed_provider_modes > 0:
            from .consumer_auth import ConsumerAuth

            # Wrap raw token_provider to return TokenResult if needed
            wrapped_provider = None
            if token_provider is not None:
                wrapped_provider = token_provider

            consumer_auth_instance = ConsumerAuth(
                api_key=api_key,
                token_endpoint=token_endpoint,
                token_provider=wrapped_provider,
                base_url=resolved_base_url,
                on_auth_error=on_auth_error,
            )
            consumer_auth_instance.init()
            auth_provider_instance = consumer_auth_instance

        def _default_session_pubnub_factory() -> Any:
            from .pubnub_client import create_pubnub_client
            import uuid

            return create_pubnub_client(
                subscribe_key=resolved_subscribe_key,
                publish_key=resolved_publish_key or None,
                user_id=f"blocks-task-{uuid.uuid4().hex[:12]}",
                subscribe_retry_unbounded=False,
            )

        # Wire default_owner_id from ConsumerAuth identity
        default_owner = None
        if consumer_auth_instance is not None:
            default_owner = consumer_auth_instance.get_user_id()

        client = cls(
            subscribe_key=resolved_subscribe_key,
            billing_mode=billing_mode,
            publish_key=resolved_publish_key,
            base_url=resolved_base_url,
            create_session_pubnub=_default_session_pubnub_factory,
            auth_provider=auth_provider_instance,
            default_owner_id=default_owner,
            anon_fingerprint=anon_fingerprint,
            rpc_headers=rpc_headers,
        )
        client._consumer_auth = consumer_auth_instance
        return client

    def __enter__(self) -> "TaskClient":
        return self

    def __exit__(self, *args: Any) -> None:
        self.destroy()

    def _get_pubnub(self) -> Any:
        """Lazily resolve the PubNub instance for subscribe operations."""
        if self._pubnub is None and self._create_pubnub is not None:
            self._pubnub = self._create_pubnub()
        if self._pubnub is None:
            raise RuntimeError(
                "TaskClient requires a pubnub instance for subscribe. "
                "Pass pubnub or create_pubnub in TaskClientOptions."
            )
        return self._pubnub

    def get_user_id(self) -> Optional[str]:
        """Return the authenticated user ID from ConsumerAuth, or None."""
        if self._consumer_auth is not None:
            return self._consumer_auth.get_user_id()
        return None

    def _bearer_credential(self) -> Optional[str]:
        """The client's bearer credential, or ``None`` when it has none.

        ``get_agent``'s ``api_key`` argument is sent verbatim as
        ``Authorization: Bearer <value>``, so whatever bearer credential the
        provider is holding works. In practice that is the consumer JWT in all
        three ``ConsumerAuth`` modes: API-key mode exchanges the key for a JWT
        during ``init()`` and never sends the raw key here. The provider owns
        refresh, so the header is read per call rather than cached here, keeping a
        rotated token from going stale.
        """
        provider = self._auth_provider or self._consumer_auth
        # No provider does not mean no credential: ``agent_auth`` is the other
        # supported way to construct an authenticated client, and the RPC and
        # file-upload paths both honour it. Skipping it here would make an
        # agent-side client's card lookup anonymous, so on Blocks Enterprise it
        # would read ``None`` while every other call it makes is authenticated.
        #
        # Its access token is forwarded rather than routed through
        # ``authenticated_fetch``: that method owns the whole request, and the
        # registry read is on optional auth so it cannot 401 — there is no
        # rejection for the wrapper's retry to act on. Because that 401 retry is
        # ``AgentAuth``'s only refresh trigger and it has no proactive scheduler,
        # ``get_agent_card`` drives ``refresh()`` itself off an empty result.
        if provider is None:
            if self._agent_auth is not None:
                return self._agent_auth.get_access_token() or None
            return None

        # A configured provider that cannot produce a credential is an auth
        # failure, not a missing agent. Swallowing it would report ``None`` —
        # indistinguishable from "no such agent" — and would skip the refresh
        # ``on_auth_failure`` performs, so an expired token would never recover.
        # This is the same preflight the RPC path runs before an authenticated
        # call.
        #
        # ``ensure_ready`` and ``get_last_auth_error`` are both optional and
        # deliberately undeclared on the ``AuthProvider`` protocol -- see that
        # protocol's docstring for why -- so both are discovered dynamically
        # rather than called outright, as ``rpc_client.py`` and Node's
        # ``ensureReady?.()`` also do. ``ensure_ready`` is idempotent, so the
        # already-initialized case costs nothing.
        preflight_auth_or_raise(provider)
        if hasattr(provider, "ensure_ready"):
            provider.ensure_ready()

        header = provider.get_auth_header()
        if not header:
            return None
        prefix = "Bearer "
        return header[len(prefix):] if header.startswith(prefix) else header

    def get_agent_card(self, agent_name: str) -> Optional[Dict[str, Any]]:
        """Look up an agent's card from the registry.

        Delegates to :func:`~blocks_network.agent_registry.get_agent`
        and returns the ``card`` field from the registry entry, or
        ``None`` if the agent is not found or has no card.

        Forwards the client's credential when it has one. The registry read is
        mounted on optional auth, so this is not required on Blocks Network — but
        a Blocks Enterprise deployment serves agent metadata to authenticated
        callers only, and would answer an unauthenticated lookup with ``None``
        even for a correctly configured client.

        Raises ``AuthRefreshFailedError`` when a configured credential cannot be
        produced. ``None`` means "no such agent, or no card"; it must not also
        mean "your token expired", or an auth problem would read as a missing
        agent.
        """
        provider = self._auth_provider or self._consumer_auth

        def _lookup() -> Any:
            return get_agent(
                agent_name,
                base_url=self._base_url,
                api_key=self._bearer_credential(),
            )

        entry = _lookup()

        if entry is None and provider is None and self._agent_auth is not None:
            # Empty result with an ``agent_auth`` credential and no auth provider.
            # This is the one credential owner whose refresh the read cannot reach
            # at all: ``AgentAuth.refresh()`` is driven solely from
            # ``authenticated_fetch``'s 401 retry, and the registry read is on
            # optional auth so it never 401s. There is no proactive scheduler
            # behind it either, so without this the token is never renewed on this
            # path and a card lookup keeps answering ``None`` for the client's
            # lifetime.
            #
            # Refresh and retry once, letting a failure propagate — ``refresh()``
            # raises ``AgentAuthFatalError`` on an invalid API key and reraises any
            # other failure, which is the same "an auth failure is not a missing
            # agent" guarantee the provider branch below gives.
            self._agent_auth.refresh()
            entry = _lookup()
        elif entry is None and provider is not None:
            # A rejected credential does not reach us as a 401. The registry read
            # is mounted on optional auth, which degrades an expired, revoked or
            # denied-jti bearer to *anonymous* rather than rejecting it — so on
            # Blocks Enterprise the read then 404s and a stale token is
            # indistinguishable from a missing agent. Nothing 401s, so the
            # transport's reactive refresh never fires.
            #
            # ``preflight_auth_or_raise`` in ``_bearer_credential`` does not cover
            # this: it only reacts to a failure the provider has already recorded.
            # So drive the refresh from the one signal we do get — an empty result
            # while holding a provider — and retry once. A genuinely absent agent
            # costs one extra GET.
            if provider.on_auth_failure():
                entry = _lookup()
            else:
                # Refresh was not possible. Surface an auth failure only on
                # evidence the provider actually has one — ``get_last_auth_error``
                # is what ``preflight_auth_or_raise`` reads for the same purpose,
                # and it is optional on the protocol, so it is probed the same way.
                # A provider that merely cannot refresh gives no evidence the
                # credential was the problem, and for it a genuinely absent agent
                # must still be ``None`` rather than a raised auth error.
                if not hasattr(provider, "get_last_auth_error"):
                    return None
                recorded = provider.get_last_auth_error()
                if recorded is not None:
                    raise recorded
                return None

        if entry is not None and getattr(entry, "card", None) is not None:
            return entry.card
        return None

    def update_keys(
        self,
        subscribe_key: str,
        publish_key: Optional[str] = None,
    ) -> None:
        """Update keyset keys after an environment switch."""
        self._subscribe_key = subscribe_key
        self._publish_key = publish_key or ""

    def destroy(self) -> None:
        """Clean up the PubNub instance and stop auth refresh timer.

        Externally-provided PubNub instances are left untouched.
        The last-known token remains readable for active sessions.
        """
        if self._pubnub is not None and self._owns_pubnub:
            try:
                self._pubnub.stop()
            except Exception:
                pass  # cleanup exception, stay silent
            self._pubnub = None
        if self._consumer_auth is not None:
            self._consumer_auth.destroy()

    def _create_session_pubnub(
        self, read_token: Optional[str] = None, subscribe_key: Optional[str] = None,
        publish_key: Optional[str] = None,
    ) -> Any:
        """Create a per-session PubNub subscribe client with the given T4 token.

        Each TaskSession gets its own client to prevent token stomping
        when multiple sessions are active concurrently.

        Uses the dedicated ``create_session_pubnub`` factory when available
        and the subscribe key matches (same keyset). Cross-keyset sessions
        need a fresh client with the target's subscribe key.
        Falls back to the internal ``create_pubnub_client()`` otherwise.
        Never uses the shared ``create_pubnub`` factory -- that is reserved
        for low-level ``subscribe_to_task()`` operations.
        """
        effective_subscribe_key = subscribe_key or self._subscribe_key
        effective_publish_key = publish_key or self._publish_key

        if (
            self._create_session_pubnub_factory is not None
            and effective_subscribe_key == self._subscribe_key
        ):
            pn = self._create_session_pubnub_factory()
        else:
            from .pubnub_client import create_pubnub_client

            import uuid
            session_id = f"blocks-task-{uuid.uuid4().hex[:12]}"
            pn = create_pubnub_client(
                subscribe_key=effective_subscribe_key,
                publish_key=effective_publish_key or None,
                user_id=session_id,
                subscribe_retry_unbounded=False,
            )
        if read_token:
            pn.set_token(read_token)
        return pn

    def _fetch_task_read_token(self, task_id: str, role: str = "consumer") -> Dict[str, Any]:
        """Call POST /api/v1/auth/task-read-token with the given role.

        Returns dict with pamToken, channel, ttlMinutes.
        Uses auth_provider for Authorization header and 401 retry.
        """
        import json
        import ssl
        import urllib.request
        import urllib.error

        import certifi

        from .protocol_version import CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER
        from .write_affinity import capture_affinity, inject_affinity

        if not self._base_url:
            raise ValueError("base_url is required for connect()")

        url = f"{self._base_url.rstrip('/')}/api/v1/auth/task-read-token"
        payload = json.dumps({"taskId": task_id, "role": role}).encode("utf-8")

        def _build_request() -> urllib.request.Request:
            headers: Dict[str, str] = {
                "Content-Type": "application/json",
                PROTOCOL_VERSION_HEADER: CURRENT_PROTOCOL_VERSION,
            }
            if self._auth_provider is not None:
                auth_header = self._auth_provider.get_auth_header()
                if auth_header:
                    headers["Authorization"] = auth_header
            inject_affinity(headers)
            return urllib.request.Request(
                url,
                data=payload,
                headers=headers,
                method="POST",
            )

        def _execute(req: urllib.request.Request) -> Dict[str, Any]:
            ssl_ctx = ssl.create_default_context(cafile=certifi.where())
            try:
                with urllib.request.urlopen(req, context=ssl_ctx, timeout=15) as resp:
                    capture_affinity(resp)
                    return json.loads(resp.read().decode("utf-8"))
            except urllib.error.HTTPError as e:
                body_text = ""
                try:
                    body_text = e.read().decode("utf-8", errors="replace")
                except Exception:
                    pass
                raise RuntimeError(
                    f"task-read-token request failed with status {e.code}: {body_text}"
                ) from e

        try:
            return _execute(_build_request())
        except RuntimeError as err:
            # 401 reactive refresh
            cause = err.__cause__
            if (
                isinstance(cause, urllib.error.HTTPError)
                and cause.code == 401
                and self._auth_provider is not None
                and self._auth_provider.on_auth_failure()
            ):
                return _execute(_build_request())
            raise

    def _fetch_anon_consumer_read_token(self, task_id: str) -> Dict[str, Any]:
        """Call POST /api/v1/auth/anon-task-read-token with {taskId, fingerprint}.

        Returns dict with pamToken, channel, ttlMinutes. Sends no
        Authorization header. Raises :class:`AnonTaskAccessDenied` on
        HTTP 403 so callers can map it to the sanitized-record fallback
        the same way authenticated non-owners do.
        """
        import json
        import ssl
        import urllib.request
        import urllib.error

        import certifi

        from .protocol_version import CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER
        from .write_affinity import capture_affinity, inject_affinity

        if not self._base_url:
            raise ValueError("base_url is required for connect()")
        if not self._anon_fingerprint:
            raise RuntimeError(
                "_fetch_anon_consumer_read_token called on a non-anon TaskClient"
            )

        url = f"{self._base_url.rstrip('/')}/api/v1/auth/anon-task-read-token"
        payload = json.dumps(
            {"taskId": task_id, "fingerprint": self._anon_fingerprint}
        ).encode("utf-8")

        headers: Dict[str, str] = {
            "Content-Type": "application/json",
            PROTOCOL_VERSION_HEADER: CURRENT_PROTOCOL_VERSION,
        }
        inject_affinity(headers)
        req = urllib.request.Request(
            url,
            data=payload,
            headers=headers,
            method="POST",
        )

        ssl_ctx = ssl.create_default_context(cafile=certifi.where())
        try:
            with urllib.request.urlopen(req, context=ssl_ctx, timeout=15) as resp:
                capture_affinity(resp)
                return json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as e:
            body_text = ""
            try:
                body_text = e.read().decode("utf-8", errors="replace")
            except Exception:
                pass
            if e.code == 403:
                raise AnonTaskAccessDenied(
                    "anon-task-read-token returned 403: "
                    "not authorized to view this task"
                ) from e
            raise RuntimeError(
                f"anon-task-read-token request failed with status {e.code}: {body_text}"
            ) from e

    # -- Primary method -----------------------------------------------------

    def send_message(
        self,
        *,
        agent_name: str = "",
        request_parts: Optional[List[Any]] = None,
        idempotency_key: Optional[str] = None,
        owner_id: str = "",
        task_kind: Optional[str] = None,
        duration: Optional[int] = None,
        consumer_public_key: Optional[str] = None,
        stream: Optional[bool] = None,
        push_notification_config: Optional[Dict[str, Any]] = None,
        retry_policy: Optional[Dict[str, Any]] = None,
        auto_drain: Optional[bool] = None,
        drain_window_s: Optional[float] = None,
    ) -> TaskSession:
        """Send a message (task) to an agent via JSON-RPC ``SendMessage``.

        Handles file attachments transparently:
        - Small files (<= 16 KB) are inlined as base64 artifactRef on the part.
        - Large files (> 16 KB) are uploaded via the pre-signed URL flow,
          then the uploadSessionId is included in the RPC call.

        Returns a TaskSession that eagerly subscribes to the task channel.

        Example::

            session = client.send_message(
                agent_name="echo",
                request_parts=[{"partId": "text", "text": "Hello"}],
            )
        """
        if self._anon_fingerprint:
            raise ValueError(
                "anon-mode TaskClient does not support send_message()"
            )

        from .auth_provider import preflight_auth_or_raise
        preflight_auth_or_raise(self._auth_provider)

        from .artifacts import build_artifact_ref, should_inline_artifact
        from .file_upload import presigned_upload_flow

        if self._auth_provider is not None and hasattr(self._auth_provider, "ensure_ready"):
            self._auth_provider.ensure_ready()

        params = SendMessageParams(
            agent_name=agent_name,
            request_parts=request_parts or [],
            idempotency_key=idempotency_key,
            owner_id=owner_id,
            task_kind=task_kind,
            duration=duration,
            consumer_public_key=consumer_public_key,
            stream=stream,
            push_notification_config=push_notification_config,
            retry_policy=retry_policy,
            auto_drain=auto_drain,
            drain_window_s=drain_window_s,
        )

        if task_kind == "pipe":
            if (
                duration is None
                or isinstance(duration, bool)
                or not isinstance(duration, int)
                or duration < 1
                or duration > 43200
            ):
                raise ValueError(
                    "Pipe tasks require an integer duration between 1 and 43200 minutes"
                )
        elif duration is not None:
            raise ValueError(
                "Request tasks must not include a duration. "
                "Duration is only valid for pipe tasks."
            )

        # Process request parts: handle file attachments
        wire_parts: List[Any] = []
        upload_session_id: Optional[str] = None

        for part in params.request_parts:
            file_data: Optional[bytes] = None
            file_name: Optional[str] = None
            part_id: Optional[str] = None
            part_mime: str = "application/octet-stream"

            if isinstance(part, SendMessageRequestPart):
                file_data = part.file
                file_name = part.file_name
                part_id = part.part_id
                wire_part: Dict[str, Any] = {}
                if part.part_id is not None:
                    wire_part["partId"] = part.part_id
                # Text or file, not both: only include text when no file data
                if part.text is not None and part.file is None:
                    wire_part["text"] = part.text
                if part.content_type is not None:
                    wire_part["contentType"] = part.content_type
                    part_mime = part.content_type
            elif isinstance(part, dict):
                file_data = part.get("file")
                file_name = part.get("fileName") or part.get("file_name")
                part_id = part.get("partId") or part.get("part_id")
                part_mime = part.get("contentType") or part.get("content_type") or "application/octet-stream"
                # Text or file, not both: exclude text when file data present
                exclude_keys = {"file", "fileName", "file_name"}
                if file_data is not None:
                    exclude_keys.add("text")
                wire_part = {
                    k: v for k, v in part.items()
                    if k not in exclude_keys
                }
            else:
                wire_parts.append(part)
                continue

            # partId is required for file-bearing parts
            if file_data is not None and not part_id:
                raise ValueError("partId is required for file-bearing request parts")

            if file_data is not None and isinstance(file_data, (bytes, bytearray)):
                file_data = bytes(file_data)
                effective_name = file_name or "attachment"

                if should_inline_artifact(len(file_data)):
                    # Small file: inline as base64
                    artifact_ref = build_artifact_ref(
                        data=file_data,
                        mime_type=part_mime,
                        file_name=effective_name,
                    )
                    wire_part["artifactRef"] = artifact_ref
                elif self._base_url:
                    # Large file: pre-signed URL flow
                    upload_result = presigned_upload_flow(
                        self._base_url,
                        file_data,
                        role="consumer-input",
                        file_name=effective_name,
                        mime_type=part_mime,
                        agent_name=params.agent_name,
                        part_id=part_id,
                        upload_session_id=upload_session_id,
                        agent_auth=self._agent_auth,
                        auth_provider=self._auth_provider,
                    )
                    if upload_session_id is None:
                        upload_session_id = upload_result.get("uploadSessionId")
                    # Do NOT attach artifactRef -- backend reconstructs it
                    # from the task_file row. wire_part already carries
                    # partId + contentType (set above). contentType is kept
                    # on the wire so agent handlers that branch on
                    # `part.contentType` still see the declared MIME for
                    # uploaded files (cross-SDK parity with Node).
                else:
                    raise ValueError(
                        "base_url is required for artifacts larger than 16 KB"
                    )

            wire_parts.append(wire_part)

        rpc_params: Dict[str, Any] = {
            "agentName": params.agent_name,
            "billingMode": self._billing_mode,
            "requestParts": wire_parts,
        }
        if params.idempotency_key:
            rpc_params["idempotencyKey"] = params.idempotency_key
        effective_owner_id = owner_id or self._default_owner_id or ""
        rpc_params["ownerId"] = effective_owner_id

        if upload_session_id:
            rpc_params["uploadSessionId"] = upload_session_id

        # Build extensions.blocks with stream, task_kind, duration, and consumer_public_key
        blocks_ext: Dict[str, Any] = {}
        if params.stream is not None:
            blocks_ext["stream"] = params.stream
        if task_kind:
            blocks_ext["taskKind"] = task_kind
        if duration is not None:
            blocks_ext["duration"] = duration
        if params.consumer_public_key:
            blocks_ext["consumerPublicKey"] = params.consumer_public_key
        if blocks_ext:
            rpc_params["extensions"] = {"blocks": blocks_ext}

        if params.push_notification_config:
            rpc_params["pushNotificationConfig"] = params.push_notification_config
        if params.retry_policy:
            rpc_params["retryPolicy"] = params.retry_policy

        result = call_rpc(
            self._subscribe_key, "SendMessage", rpc_params,
            base_url=self._base_url, agent_auth=self._agent_auth,
            auth_provider=self._auth_provider,
            rpc_headers=self._rpc_headers,
        )

        is_idempotent = result.get("idempotent", False)
        response_state = result.get("state")

        blocks_ext = (result.get("extensions") or {}).get("blocks") or {}
        read_token = blocks_ext.get("readToken")
        status_channel = (blocks_ext.get("streamChannels") or {}).get("status")
        task_subscribe_key = blocks_ext.get("subscribeKey") or self._subscribe_key
        task_publish_key = blocks_ext.get("publishKey") or self._publish_key

        auto_drain_kwarg: Dict[str, Any] = {}
        if params.auto_drain is not None:
            auto_drain_kwarg["auto_drain"] = params.auto_drain
        if params.drain_window_s is not None:
            auto_drain_kwarg["drain_window_s"] = params.drain_window_s

        # Terminal idempotent hit: task already completed/failed/canceled.
        # Create a pre-closed session with no PubNub subscription.
        if is_idempotent and response_state in TERMINAL_STATES:
            return TaskSession(
                task_id=result["taskId"],
                owner_id=effective_owner_id,
                read_token=read_token,
                status_channel=status_channel,
                agent_name=params.agent_name,
                pubnub=None,
                owns_subscribe_client=False,
                sdk_options={
                    "subscribe_key": task_subscribe_key,
                    "publish_key": task_publish_key,
                },
                rpc_config={
                    "subscribe_key": self._subscribe_key,
                    "auth_provider": self._auth_provider,
                    "base_url": self._base_url,
                    "agent_auth": self._agent_auth,
                    "rpc_headers": self._rpc_headers,
                },
                idempotent=True,
                queued=result.get("queued"),
                push_config_id=result.get("pushConfigId"),
                pre_closed_state=response_state,
                **auto_drain_kwarg,
            )

        # Normal path (new task or non-terminal idempotent hit):
        # create a per-session PubNub subscribe client so T4 tokens don't
        # stomp each other when multiple sessions are active concurrently.
        # Use the target agent's keys for cross-billing-mode A2A.
        session_pubnub = self._create_session_pubnub(read_token, task_subscribe_key, task_publish_key)

        # sdk_options carries the target keyset for the streaming session (StreamRef/StreamClient);
        # rpc_config keeps the caller's keyset for HTTP RPC and read-token minting.
        sdk_options = {
            "subscribe_key": task_subscribe_key,
            "publish_key": task_publish_key,
        }
        rpc_config = {
            "subscribe_key": self._subscribe_key,
            "auth_provider": self._auth_provider,
            "base_url": self._base_url,
            "agent_auth": self._agent_auth,
            "rpc_headers": self._rpc_headers,
        }

        # Resolve channel: prefer server-provided, fall back to derived.
        resolved_channel = status_channel or task_channel(
            result["taskId"], effective_owner_id,
        )

        # History-based catch-up: fetch events that may have been published
        # between the RPC dispatch and now (closes the subscribe race for
        # fast handlers — see BN-455). Same pattern as connect().
        try:
            time_result = session_pubnub.time().sync()
            server_timetoken = str(getattr(
                getattr(time_result, "result", time_result),
                "timetoken", "0",
            ))

            preloaded_streams, preloaded_artifacts, _preloaded_events, high_water_mark, history_terminal_state = (
                self._fetch_and_parse_history(
                    session_pubnub, resolved_channel, params.agent_name,
                    result["taskId"], sdk_options,
                )
            )

            # Fast handler already finished: return a pre-populated terminal session.
            if history_terminal_state in TERMINAL_STATES:
                return TaskSession(
                    task_id=result["taskId"],
                    owner_id=effective_owner_id,
                    read_token=read_token,
                    status_channel=resolved_channel,
                    agent_name=params.agent_name,
                    pubnub=session_pubnub,
                    owns_subscribe_client=True,
                    sdk_options=sdk_options,
                    rpc_config=rpc_config,
                    idempotent=result.get("idempotent"),
                    queued=result.get("queued"),
                    push_config_id=result.get("pushConfigId"),
                    state=history_terminal_state,
                    skip_subscription=True,
                    preloaded_streams=preloaded_streams,
                    preloaded_artifacts=preloaded_artifacts,
                    **auto_drain_kwarg,
                )

            # Subscribe from cursor so there is no gap between history and live events.
            subscribe_cursor = high_water_mark if high_water_mark != "0" else server_timetoken

            buffer: List[Dict[str, Any]] = []
            buf_lock = threading.Lock()
            buffering = [True]

            from pubnub.callbacks import SubscribeCallback

            class _BufferListener(SubscribeCallback):
                def status(self, pubnub_inst: Any, status: Any) -> None:
                    pass

                def presence(self, pubnub_inst: Any, presence: Any) -> None:
                    pass

                def message(self, pubnub_inst: Any, event: Any) -> None:
                    evt_channel = getattr(event, "channel", None)
                    if evt_channel != resolved_channel:
                        return
                    msg = getattr(event, "message", None)
                    if not isinstance(msg, dict) or "type" not in msg:
                        return
                    timetoken = getattr(event, "timetoken", None)
                    with buf_lock:
                        if buffering[0]:
                            buffer.append({"message": msg, "timetoken": timetoken})
                        else:
                            dispatch = getattr(self, "_dispatch_fn", None)
                            if dispatch is not None:
                                dispatch(msg, str(timetoken) if timetoken is not None else None)

            buf_listener = _BufferListener()
            session_pubnub.add_listener(buf_listener)
            session_pubnub.subscribe().channels([resolved_channel]).with_timetoken(
                int(subscribe_cursor)
            ).execute()

            dispatch_ref: List[Optional[Callable]] = [None]

            def _on_ready(dispatch_fn: Callable[[Dict[str, Any]], None]) -> None:
                dispatch_ref[0] = dispatch_fn

            session = TaskSession(
                task_id=result["taskId"],
                owner_id=effective_owner_id,
                read_token=read_token,
                status_channel=resolved_channel,
                agent_name=params.agent_name,
                pubnub=session_pubnub,
                owns_subscribe_client=True,
                sdk_options=sdk_options,
                rpc_config=rpc_config,
                idempotent=result.get("idempotent"),
                queued=result.get("queued"),
                push_config_id=result.get("pushConfigId"),
                preloaded_streams=preloaded_streams,
                preloaded_artifacts=preloaded_artifacts,
                external_subscription={
                    "listener": buf_listener,
                    "channel": resolved_channel,
                    "on_ready": _on_ready,
                },
                **auto_drain_kwarg,
            )

            dispatch_fn = dispatch_ref[0]

            # Drain buffer and switch to live mode under the lock so no
            # callback can slip between the drain loop and the flag flip.
            with buf_lock:
                buf_listener._dispatch_fn = dispatch_fn  # type: ignore[attr-defined]
                if dispatch_fn is not None:
                    for entry in buffer:
                        entry_tt = entry.get("timetoken")
                        if entry_tt is not None and subscribe_cursor and str(entry_tt) <= str(subscribe_cursor):
                            continue
                        dispatch_fn(
                            entry["message"],
                            str(entry_tt) if entry_tt is not None else None,
                        )
                buffering[0] = False

            return session
        except Exception:
            # History/subscribe catch-up failed, but the task was already created.
            # Fall back to a basic session so the caller always gets a task_id.
            return TaskSession(
                task_id=result["taskId"],
                owner_id=effective_owner_id,
                read_token=read_token,
                status_channel=resolved_channel,
                agent_name=params.agent_name,
                pubnub=session_pubnub,
                owns_subscribe_client=True,
                sdk_options=sdk_options,
                rpc_config=rpc_config,
                idempotent=result.get("idempotent"),
                queued=result.get("queued"),
                push_config_id=result.get("pushConfigId"),
                **auto_drain_kwarg,
            )

    # -- Connect to existing task -------------------------------------------

    def connect(
        self,
        task_id: str,
        auto_drain: bool = True,
        drain_window_s: Optional[float] = None,
        role: str = "consumer",
    ) -> TaskSession:
        """Connect to an existing task.

        Returns a TaskSession pre-populated with stream refs, artifact
        refs, and task state from history.

        For active tasks: the session subscribes to the task channel and
        live events flow through callbacks from that point forward.

        For terminal tasks: the session is pre-populated but does not
        subscribe. The consumer reads list_artifacts(), list_streams(),
        and session.state, then calls close().

        Uses the task-read-token endpoint to acquire a fresh T4 read
        token. The caller does not need to persist or supply read_token,
        org_id, owner_id, or agent_name.

        Parameters
        ----------
        task_id : str
            Identifier of the task to connect to.
        auto_drain : bool, default True
            Enable auto-drain on terminal. When True, the returned
            TaskSession waits for open streams to drain via stream_end
            before closing.
        drain_window_s : float, optional
            Duration in seconds the session waits for already-open
            streams to finish draining naturally after a terminal event.
            Defaults to 30.0 seconds. Ignored when ``auto_drain`` is
            False. Only applies to streams that were opened while the
            task was still active; unopened streams on a terminal
            session raise ``StreamUnavailableError`` per the merged t7c
            baseline.
        role : str, default "consumer"
            Role to request when minting the read token. Set to
            'provider' when the caller is the agent owner viewing a
            received task.
        """
        # Step 1: Validate auth. Anonymous mode short-circuits the auth-provider
        # gate and routes to the fingerprint-gated public endpoint instead.
        if self._anon_fingerprint:
            # Step 2a: Fetch sanitized task metadata (public for free+public tasks).
            task_info = self.get_task(task_id)
            task_state = task_info.state
            agent_name = task_info.agent_name or ""
            owner_id = task_info.owner or ""

            # Step 3a: Acquire anon read token. Raises AnonTaskAccessDenied on 403.
            token_resp = self._fetch_anon_consumer_read_token(task_id)
            pam_token = token_resp.get("pamToken", "")
            status_channel = token_resp.get("channel", "")
        else:
            # Fail fast when ConsumerAuth has recorded a permanent refresh
            # failure — placed AFTER the anon-fingerprint short-circuit so
            # anon mode never observes the guard (matches Node task-client.ts
            # connect() placement). Uses the shared preflight helper so a
            # transient outage that resolves before connect() can recover.
            from .auth_provider import preflight_auth_or_raise
            preflight_auth_or_raise(self._auth_provider)

            has_auth = (
                self._auth_provider is not None
                and self._auth_provider.get_auth_header()
            )
            if not has_auth:
                raise RuntimeError(
                    "connect() requires an authenticated TaskClient. Use api_key, token_endpoint, or token_provider. "
                    "AgentAuth is not supported for consumer task connections."
                )

            # Step 2: Fetch task state
            task_info = self.get_task(task_id)
            task_state = task_info.state
            agent_name = task_info.agent_name or ""
            owner_id = task_info.owner or ""

            # Step 3: Acquire fresh token
            token_resp = self._fetch_task_read_token(task_id, role)
            pam_token = token_resp.get("pamToken", "")
            status_channel = token_resp.get("channel", "")

        sdk_options = {
            "subscribe_key": self._subscribe_key,
            "publish_key": self._publish_key,
        }
        rpc_config = {
            "subscribe_key": self._subscribe_key,
            "base_url": self._base_url,
            "agent_auth": self._agent_auth,
            "auth_provider": self._auth_provider,
            "rpc_headers": self._rpc_headers,
        }

        # Fetch history up front. History is authoritative for session
        # state: if a terminal event is visible on the status channel,
        # the session IS terminal, even if the backend's task-state RPC
        # hasn't yet reflected the write (backend propagation lag between
        # the PubNub terminal event and the taskFanout DB write). Without
        # this, a consumer reconnecting within a few seconds of task
        # completion would hit the stale RPC state, fall into the active
        # path, and get a live StreamClient against an about-to-be-revoked
        # T7c token — the exact silent-hang that the terminal short-circuit
        # is supposed to prevent.
        session_pn = self._create_session_pubnub(pam_token)
        try:
            time_result = session_pn.time().sync()
            server_timetoken = str(getattr(
                getattr(time_result, "result", time_result),
                "timetoken", "0",
            ))

            preloaded_streams, preloaded_artifacts, preloaded_events, high_water_mark, history_terminal_state = (
                self._fetch_and_parse_history(
                    session_pn, status_channel, agent_name, task_id, sdk_options,
                )
            )

            # Prefer the history-derived terminal state when the RPC hasn't
            # caught up. If either source reports terminal, the session is
            # terminal.
            effective_state = (
                history_terminal_state
                if history_terminal_state in TERMINAL_STATES
                else task_state
            )
            is_terminal = effective_state in TERMINAL_STATES

            if is_terminal:
                return TaskSession(
                    task_id=task_id,
                    owner_id=owner_id,
                    read_token=pam_token,
                    status_channel=status_channel,
                    agent_name=agent_name,
                    pubnub=session_pn,
                    owns_subscribe_client=True,
                    sdk_options=sdk_options,
                    rpc_config=rpc_config,
                    auto_drain=auto_drain,
                    drain_window_s=drain_window_s,
                    state=effective_state,
                    skip_subscription=True,
                    preloaded_streams=preloaded_streams,
                    preloaded_artifacts=preloaded_artifacts,
                    preloaded_events=preloaded_events,
                )

            # Use the server timetoken as fallback when history is empty,
            # so we always subscribe from a known point in time.
            subscribe_cursor = high_water_mark if high_water_mark != "0" else server_timetoken

            # Step 4f: Subscribe from the cursor timetoken so there is no
            # gap between history and live events. Buffer incoming messages
            # until the session is constructed.
            buffer: List[Dict[str, Any]] = []
            buf_lock = threading.Lock()
            buffering = [True]  # mutable flag for closure

            from pubnub.callbacks import SubscribeCallback

            class _BufferListener(SubscribeCallback):
                def status(self, pubnub: Any, status: Any) -> None:
                    pass

                def presence(self, pubnub: Any, presence: Any) -> None:
                    pass

                def message(self, pubnub: Any, event: Any) -> None:
                    evt_channel = getattr(event, "channel", None)
                    if evt_channel != status_channel:
                        return
                    msg = getattr(event, "message", None)
                    if not isinstance(msg, dict) or "type" not in msg:
                        return
                    timetoken = getattr(event, "timetoken", None)
                    with buf_lock:
                        if buffering[0]:
                            buffer.append({"message": msg, "timetoken": timetoken})
                        else:
                            dispatch = getattr(self, "_dispatch_fn", None)
                            if dispatch is not None:
                                dispatch(msg, str(timetoken) if timetoken is not None else None)

            buf_listener = _BufferListener()
            session_pn.add_listener(buf_listener)
            # PubNub Python SDK expects int timetokens internally
            # (SubscriptionManager._timetoken is int, max() would TypeError on str).
            session_pn.subscribe().channels([status_channel]).with_timetoken(
                int(subscribe_cursor)
            ).execute()

            # Step 4g: Construct TaskSession with external_subscription
            dispatch_ref: List[Optional[Callable]] = [None]

            def _on_ready(dispatch_fn: Callable[[Dict[str, Any]], None]) -> None:
                dispatch_ref[0] = dispatch_fn

            session = TaskSession(
                task_id=task_id,
                owner_id=owner_id,
                read_token=pam_token,
                status_channel=status_channel,
                agent_name=agent_name,
                pubnub=session_pn,
                owns_subscribe_client=True,
                sdk_options=sdk_options,
                rpc_config=rpc_config,
                auto_drain=auto_drain,
                drain_window_s=drain_window_s,
                state=task_state,
                preloaded_streams=preloaded_streams,
                preloaded_artifacts=preloaded_artifacts,
                preloaded_events=preloaded_events,
                external_subscription={
                    "listener": buf_listener,
                    "channel": status_channel,
                    "on_ready": _on_ready,
                },
            )

            dispatch_fn = dispatch_ref[0]

            # Step 4h: Drain buffer and switch to live mode under the lock
            # so no callback can slip between the drain loop and the flag flip.
            # Also append each drained message to the history snapshot so
            # list_events() covers the full pre-caller window (history + gap).
            with buf_lock:
                buf_listener._dispatch_fn = dispatch_fn  # type: ignore[attr-defined]
                if dispatch_fn is not None:
                    for entry in buffer:
                        entry_tt = entry.get("timetoken")
                        if entry_tt is not None and subscribe_cursor and str(entry_tt) <= str(subscribe_cursor):
                            continue  # already covered by history
                        msg = entry["message"]
                        tt_str = str(entry_tt) if entry_tt is not None else None
                        if isinstance(msg, dict) and msg.get("type"):
                            session._append_history_event(msg, tt_str)
                        dispatch_fn(msg, tt_str)
                buffering[0] = False

            return session
        except Exception:
            try:
                session_pn.stop()
            except Exception:
                pass
            raise

    def _fetch_and_parse_history(
        self,
        pubnub: Any,
        channel: str,
        agent_name: str,
        task_id: str,
        sdk_options: Dict[str, Any],
    ) -> tuple:
        """Fetch task channel history and parse stream_started, artifact, and task events.

        Returns (preloaded_streams, preloaded_artifacts, preloaded_events,
        high_water_mark, terminal_state). ``terminal_state`` is the most
        recent terminal event's state if one is present in history,
        otherwise ``None``.
        History is authoritative for session state during ``connect()``:
        if a terminal event is visible on the status channel, the session
        IS terminal, even if the backend's task-state RPC hasn't yet
        reflected the write (there is a real propagation lag between the
        PubNub terminal event and the backend's taskFanout DB write).
        """
        from .stream import StreamDescriptor, invert_direction
        from .stream_ref import StreamRef

        preloaded_streams: Dict[str, StreamRef] = {}
        preloaded_artifacts: List[ArtifactRef] = []
        preloaded_events: List[Dict[str, Any]] = []
        high_water_mark = "0"
        terminal_state: Optional[str] = None
        terminal_tt = "0"

        # Paginated history fetch using backward pagination.
        # PubNub fetchMessages with no start returns the most recent
        # messages. The start param is exclusive and returns messages
        # older than it. We page backward using the oldest timetoken
        # from each batch, then sort ascending for chronological order.
        page_size = 100
        cursor_start: Optional[int] = None
        all_messages: list = []

        while True:
            builder = pubnub.fetch_messages().channels([channel]).maximum_per_channel(page_size)
            if cursor_start is not None:
                builder = builder.start(cursor_start)
            result = builder.sync()

            channels_data = getattr(result, "result", result)
            if hasattr(channels_data, "channels"):
                messages_map = channels_data.channels
            elif isinstance(channels_data, dict):
                messages_map = channels_data.get("channels", {})
            else:
                messages_map = {}

            page_messages = messages_map.get(channel, [])
            if not page_messages:
                break

            all_messages.extend(page_messages)

            if len(page_messages) < page_size:
                break

            # Page backward: oldest message in this batch is the cursor
            # for the next older page (start is exclusive).
            oldest_msg = page_messages[0]
            oldest_tt = getattr(oldest_msg, "timetoken", None)
            if oldest_tt is not None:
                cursor_start = int(oldest_tt)
            else:
                break

        # Sort ascending by timetoken for chronological replay order.
        all_messages.sort(key=lambda m: int(getattr(m, "timetoken", 0)))
        messages = all_messages

        for hist_msg in messages:
            # Extract timetoken
            tt = getattr(hist_msg, "timetoken", None)
            if tt is not None and str(tt) > high_water_mark:
                high_water_mark = str(tt)

            # Extract message payload
            msg = getattr(hist_msg, "message", None)
            if isinstance(hist_msg, dict):
                msg = hist_msg.get("message", hist_msg)
            if not isinstance(msg, dict):
                continue

            msg_type = msg.get("type")
            if msg_type:
                preloaded_events.append(msg)

            # Parse terminal events: take the latest one by timetoken
            # in case history contains retries or duplicates.
            if msg_type == "terminal":
                state_val = msg.get("state")
                if state_val in ("completed", "failed", "canceled"):
                    current_tt = str(tt) if tt is not None else "0"
                    if current_tt >= terminal_tt:
                        terminal_state = state_val
                        terminal_tt = current_tt

            # Parse stream_started events
            if (
                msg_type == "progress"
                and msg.get("streamEvent") == "stream_started"
                and isinstance(msg.get("streams"), dict)
            ):
                declared_stream_key = msg.get("declaredStream")
                if not isinstance(declared_stream_key, str):
                    declared_stream_key = None
                for stream_id, entry in msg["streams"].items():
                    if not isinstance(entry, dict):
                        continue
                    if stream_id in preloaded_streams:
                        continue

                    agent_direction = entry.get("direction", "outbound")
                    local_direction = invert_direction(agent_direction)
                    fmt = entry.get("format")
                    if fmt not in ("bytes", "events"):
                        continue

                    affinity = entry.get("affinity")
                    if affinity not in ("dedicated", "shared"):
                        # affinity became schema-required in 4.7.0. Silent
                        # drop would leave a consumer missing a stream
                        # with no log. Warn loudly so a malformed history
                        # entry is diagnosable.
                        logger.warning(
                            'history-preload: dropping stream "%s" for task "%s" -- '
                            "invalid or missing affinity (got %r)",
                            stream_id, task_id, affinity,
                        )
                        continue

                    descriptor = StreamDescriptor(
                        task_id=task_id,
                        stream_id=stream_id,
                        agent_name=agent_name,
                        channel=entry.get("channel", ""),
                        token=entry.get("token", ""),
                        agent_direction=agent_direction,
                        local_direction=local_direction,
                        format=fmt,
                        affinity=affinity,
                        metadata=entry.get("metadata"),
                        declared_stream=declared_stream_key,
                    )
                    ref = StreamRef(descriptor, sdk_options)
                    preloaded_streams[stream_id] = ref

            # Parse artifact events
            elif msg_type == "artifact" and isinstance(msg.get("artifactRef"), dict):
                preloaded_artifacts.append(ArtifactRef.from_dict(msg["artifactRef"]))

        return preloaded_streams, preloaded_artifacts, preloaded_events, high_water_mark, terminal_state

    # -- Task lifecycle -----------------------------------------------------

    def get_task(self, task_id: str) -> TaskInfo:
        """Get task info via JSON-RPC ``GetTask``."""
        result = call_rpc(
            self._subscribe_key, "GetTask", {"taskId": task_id},
            base_url=self._base_url, agent_auth=self._agent_auth,
            auth_provider=self._auth_provider,
            rpc_headers=self._rpc_headers,
        )
        task_data = result.get("task", result) if isinstance(result, dict) else result
        return TaskInfo.from_dict(task_data)

    def list_tasks(self, params: Optional[ListTasksParams] = None) -> ListTasksResult:
        """List tasks via JSON-RPC ``ListTasks``."""
        rpc_params: Dict[str, Any] = {}
        if params:
            if params.owner_id:
                rpc_params["ownerId"] = params.owner_id
            if params.agent_name:
                rpc_params["agentName"] = params.agent_name
            if params.state:
                rpc_params["state"] = params.state
            if params.limit is not None:
                rpc_params["limit"] = params.limit
            if params.cursor:
                rpc_params["cursor"] = params.cursor

        result = call_rpc(
            self._subscribe_key, "ListTasks", rpc_params,
            base_url=self._base_url, agent_auth=self._agent_auth,
            auth_provider=self._auth_provider,
            rpc_headers=self._rpc_headers,
        )
        tasks = [TaskInfo.from_dict(t) for t in result.get("tasks", [])]
        return ListTasksResult(
            tasks=tasks,
            next=result.get("next"),
            total_count=result.get("totalCount"),
        )

    def cancel_task(self, task_id: str) -> None:
        """Cancel a task via JSON-RPC ``CancelTask``."""
        call_rpc(
            self._subscribe_key, "CancelTask", {"taskId": task_id},
            base_url=self._base_url, agent_auth=self._agent_auth,
            auth_provider=self._auth_provider,
            rpc_headers=self._rpc_headers,
        )

    def pause_task(self, task_id: str) -> None:
        """Pause a task via JSON-RPC ``PauseTask``."""
        call_rpc(
            self._subscribe_key, "PauseTask", {"taskId": task_id},
            base_url=self._base_url, agent_auth=self._agent_auth,
            auth_provider=self._auth_provider,
            rpc_headers=self._rpc_headers,
        )

    def resume_task(self, task_id: str) -> None:
        """Resume a task via JSON-RPC ``ResumeTask``."""
        call_rpc(
            self._subscribe_key, "ResumeTask", {"taskId": task_id},
            base_url=self._base_url, agent_auth=self._agent_auth,
            auth_provider=self._auth_provider,
            rpc_headers=self._rpc_headers,
        )

    def retry_task(self, task_id: str) -> None:
        """Retry a task via JSON-RPC ``RetryTask``."""
        call_rpc(
            self._subscribe_key, "RetryTask", {"taskId": task_id},
            base_url=self._base_url, agent_auth=self._agent_auth,
            auth_provider=self._auth_provider,
            rpc_headers=self._rpc_headers,
        )

    def terminate_task(self, task_id: str) -> None:
        """Terminate a task via JSON-RPC ``TerminateTask``."""
        call_rpc(
            self._subscribe_key, "TerminateTask", {"taskId": task_id},
            base_url=self._base_url, agent_auth=self._agent_auth,
            auth_provider=self._auth_provider,
            rpc_headers=self._rpc_headers,
        )

    # -- Subscribe helper ---------------------------------------------------

    def subscribe_to_task(
        self,
        task_id: str,
        owner_id: str,
        callbacks: TaskEventCallbacks,
    ) -> TaskSubscription:
        """Subscribe to real-time task events via PubNub."""
        return subscribe_to_task(
            self._get_pubnub(), task_id, owner_id, callbacks
        )


def create_task_client(
    billing_mode: Literal["free", "paid"] = "free",
    *,
    api_key: Optional[str] = None,
    token_endpoint: Optional[Union[str, "TokenEndpointConfig"]] = None,
    token_provider: Optional[Callable[[], Any]] = None,
    **kwargs: Any,
) -> TaskClient:
    """Create a ready-to-use :class:`TaskClient` with minimal boilerplate.

    Loads environment variables from ``.env`` via ``python-dotenv``
    and delegates to :meth:`TaskClient.create`.

    When no explicit auth mode is provided (*api_key*, *token_endpoint*,
    or *token_provider*), the ``BLOCKS_API_KEY`` environment variable is
    used automatically.

    Parameters
    ----------
    billing_mode:
        Billing mode for the target agent. ``"free"`` -> playground keyset;
        ``"paid"`` -> network keyset. Defaults to ``"free"``.
    api_key:
        Explicit API key.  When *None* and no other auth mode is given,
        the value of ``BLOCKS_API_KEY`` from the environment is used.
    token_endpoint:
        Customer-owned proxy endpoint URL.
    token_provider:
        Custom sync function returning a token result.
    **kwargs:
        Additional keyword arguments forwarded to
        :meth:`TaskClient.create` (e.g. ``cdm_url``, ``subscribe_key``,
        ``on_auth_error``).

    Returns
    -------
    TaskClient
        A configured client ready for :meth:`~TaskClient.send_message` calls.

    Raises
    ------
    KeyError
        If no auth mode is provided and ``BLOCKS_API_KEY`` is not set in
        the environment.
    """
    from dotenv import load_dotenv

    load_dotenv()

    # Only fall back to BLOCKS_API_KEY when no auth mode was provided.
    if api_key is None and token_endpoint is None and token_provider is None:
        api_key = os.environ["BLOCKS_API_KEY"]

    return TaskClient.create(
        billing_mode=billing_mode,
        api_key=api_key,
        token_endpoint=token_endpoint,
        token_provider=token_provider,
        **kwargs,
    )
