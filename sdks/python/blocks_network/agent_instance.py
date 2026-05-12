"""
Core agent instance runtime.

Phase 3 rewrite: three-tier connection model, unified create_stream(),
stream registry with ref-counting, credential cache, on_activate
processing model, and instance-level publish_terminal/fail_stream APIs.

Agent instances:
- Subscribe to ``agent.{agentId}.control`` via controlClient.
- Handle StartTask, CancelTask, ExpireTask, PauseTask, ResumeTask,
  TerminateTask, RetryTask.
- Use per-task PubNub clients (taskClient tier) for all task-specific
  publishes.
- Use per-stream StreamClient instances (streamClient tier) for all
  stream data I/O.
- Use ThreadPoolExecutor for concurrent handler execution.
"""

from __future__ import annotations

import logging
import math
import os
import re
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Callable, Dict, List, Literal, Optional, Set

import os as _os_top

logger = logging.getLogger(__name__)

from .agent_auth import AgentAuth
from .auth_provider import StaticAuthProvider
from .artifacts import build_artifact_ref, should_inline_artifact
from .protocol_version import (
    CURRENT_PROTOCOL_VERSION,
    SUPPORTED_PROTOCOL_VERSIONS,
    is_supported,
)
from .cdm_config import fetch_cdm_config, CdmConfig, CdmKeyset
from .agent_registry import get_agent
from .channel_manager import ChannelManager, create_channel_manager
from .credential_cache import CredentialCache
from .logging_utils import log_agent_instance_event
from .pubnub_client import create_pubnub_client
from .stream_context import (
    ExternalStreamObject,
    StreamObject,
    run_on_activate,
)
from .stream_registry import StreamRegistry
from .task_client import TaskClient
from .consumer_auth import ConsumerAuth
from .types import (
    AgentInstanceOptions,
    CancelTaskMessage,
    ControlMessage,
    ExpireTaskMessage,
    StartTaskMessage,
    TaskContext,
    extract_start_task_tokens,
    parse_control_message,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _extract_owner_id(
    owner_id: Optional[str] = None,
    caller_claims: Optional[Dict[str, Any]] = None,
) -> str:
    """Extract owner ID with priority: ownerId > callerClaims.sub > 'anonymous'."""
    if isinstance(owner_id, str) and len(owner_id) > 0:
        return owner_id
    if caller_claims:
        sub = caller_claims.get("sub")
        if isinstance(sub, str) and len(sub) > 0:
            return sub
    return "anonymous"


def _resolve_max_running_time_sec(
    opts_value: Optional[int],
    card_value: Optional[int],
) -> Optional[int]:
    """Reconcile ``opts.max_running_time_sec`` with ``card.runtime.maxRunningTimeSec``.

    Precedence is opts-first, card-fallback. When both are set and disagree,
    opts wins and the SDK emits a one-time info-level log so the divergence
    is visible rather than silent.

    Mirrors Node's ``resolveMaxRunningTimeSec`` in
    ``blocks-sdk/sdks/node/src/runtime/agent-instance.ts``.
    """
    if (
        opts_value is not None
        and card_value is not None
        and opts_value != card_value
    ):
        log_agent_instance_event(
            "info",
            (
                f"opts.max_running_time_sec ({opts_value}) overrides "
                f"card.runtime.maxRunningTimeSec ({card_value})."
            ),
        )
    return opts_value if opts_value is not None else card_value


def _extract_card_max_running_time_sec(card: Any) -> Optional[int]:
    """Duck-typed read of ``card.runtime.maxRunningTimeSec``.

    The ``card`` field on ``AgentInstanceOptions`` is typed as a dict but
    programmatic callers may pass a dataclass or other typed object. Return
    ``None`` when the field is missing or not a positive integer.
    """
    runtime: Any = None
    if isinstance(card, dict):
        runtime = card.get("runtime")
    elif card is not None:
        runtime = getattr(card, "runtime", None)

    value: Any = None
    if isinstance(runtime, dict):
        value = runtime.get("maxRunningTimeSec")
    elif runtime is not None:
        # Support camelCase (attribute mirrored from JSON) and snake_case.
        value = (
            getattr(runtime, "maxRunningTimeSec", None)
            or getattr(runtime, "max_running_time_sec", None)
        )

    if value is None:
        return None
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def _compute_stream_duration_minutes(
    task_duration: Optional[int],
    is_pipe_task: bool,
    effective_max_running_time_sec: Optional[int],
) -> int:
    """Derive the ``durationMinutes`` sent to the ``streamSetup`` Function.

    Mirrors Node's ``computeStreamDurationMinutes``:

    - If the StartTask message carries an explicit ``duration``, it wins.
    - Pipe tasks with no ``task.duration`` fall back to 60 minutes.
    - Request tasks with no ``task.duration`` derive from
      ``effective_max_running_time_sec`` via ``math.ceil(... / 60)``; when
      that value is ``None`` the final fallback is 60 minutes (3600 seconds).
    """
    if task_duration is not None:
        return int(task_duration)
    if is_pipe_task:
        return 60
    source = (
        effective_max_running_time_sec
        if effective_max_running_time_sec is not None
        else 3600
    )
    return math.ceil(int(source) / 60)


def _publish_task_event(
    pubnub: Any,
    task_id: str,
    owner_id: str,
    agent_name: str,
    message: Dict[str, Any],
    protocol_version: Optional[str] = None,
) -> None:
    """Publish a task event to ``u.{ownerId}.{taskId}``."""
    cm = create_channel_manager(agent_name)
    channel = cm.task_channel(task_id, owner_id)
    pv = protocol_version or CURRENT_PROTOCOL_VERSION
    message.setdefault("protocolVersion", pv)
    try:
        pubnub.publish().channel(channel).message(message).meta(
            {
                "agentName": agent_name,
                "taskId": task_id,
                "protocolVersion": pv,
            }
        ).should_store(True).use_post(True).sync()
    except Exception:
        pass


def _upload_artifact_presigned(
    data: bytes,
    task_id: str,
    mime_type: str,
    base_url: str,
    agent_auth: Any = None,
    auth_provider: Any = None,
    custom_file_name: Optional[str] = None,
    output_id: Optional[str] = None,
) -> Dict[str, Any]:
    """Upload an artifact via the pre-signed URL flow.

    The backend publishes the typed artifact event on confirm-upload.
    Returns the confirm-upload response containing ``artifactRef``.
    """
    from .file_upload import presigned_upload_flow

    file_name = custom_file_name or f"{task_id}-artifact"

    return presigned_upload_flow(
        base_url,
        data,
        role="provider-output",
        file_name=file_name,
        mime_type=mime_type,
        task_id=task_id,
        output_id=output_id,
        agent_auth=agent_auth,
        auth_provider=auth_provider,
    )


def _download_input_artifact(
    pubnub: Any,
    artifact_ref: Any,
) -> bytes:
    """Download an input artifact from a request part's artifactRef.

    For inline artifacts, decodes the base64 data.
    For file artifacts, uses ``pubnub.downloadFile()`` with PAM token.
    """
    import base64

    if artifact_ref is None:
        raise ValueError("No artifactRef on this request part")

    # Support both ArtifactRef dataclass and raw dict
    kind = getattr(artifact_ref, "kind", None) or (
        artifact_ref.get("kind") if isinstance(artifact_ref, dict) else None
    )

    if kind == "inline":
        data_b64 = getattr(artifact_ref, "data", None) or (
            artifact_ref.get("data") if isinstance(artifact_ref, dict) else None
        )
        if not data_b64:
            raise ValueError("Inline artifactRef has no data field")
        return base64.b64decode(data_b64)

    if kind == "file":
        channel = getattr(artifact_ref, "channel", None) or (
            artifact_ref.get("channel") if isinstance(artifact_ref, dict) else None
        )
        file_id = getattr(artifact_ref, "file_id", None) or getattr(artifact_ref, "fileId", None) or (
            artifact_ref.get("fileId") if isinstance(artifact_ref, dict) else None
        )
        file_name = getattr(artifact_ref, "file_name", None) or getattr(artifact_ref, "fileName", None) or (
            artifact_ref.get("fileName") if isinstance(artifact_ref, dict) else None
        )

        if not channel or not file_id or not file_name:
            raise ValueError(
                "File artifactRef missing required fields (channel, fileId, fileName)"
            )

        result = pubnub.download_file().channel(channel).file_id(
            file_id
        ).file_name(file_name).sync()

        if hasattr(result, "result") and result.result:
            content = getattr(result.result, "data", None)
            if content is not None:
                return bytes(content) if not isinstance(content, bytes) else content
        raise ValueError("downloadFile returned no data")

    raise ValueError(f"Unknown artifactRef kind: {kind}")


def _make_pubnub_retry_logger(instance_id: str):
    """Build the on_retry callback used at both control-client construction
    sites. Dispatches on the category emitted by _RetryLogForwarder so the
    agent log carries a distinct event for each reconnection state:

    - retry     → warn,  event=pubnub_transport_retry
    - recovered → info,  event=pubnub_transport_recovered
    - failed    → error, event=pubnub_transport_failed

    The retry-budget bump (subscribe_retry_unbounded=True, 43_200 attempts)
    means "failed" should not fire in normal operation; surfacing it at
    error level guarantees a regression that shrinks the budget is loud.

    What "recovered" actually means. The signal fires when PubNub's
    NativeReconnectionManager observes a successful time() round-trip
    after a streak of failures — i.e. transport-level connectivity is
    confirmed back. It does NOT mean the agent is fully presence-active
    yet: the subscribe long-poll thread may still be hung on the
    pre-cut socket waiting for `subscribe_request_timeout` (PubNub
    default 310s) to expire before it issues a fresh subscribe, and
    the broker may need 1-2 heartbeat cycles after that to re-emit a
    presence join for the UUID. Verified live on 2026-05-07: recovered
    fired at +11m9s after blip start; broker-side `Action: join` for
    the same UUID appeared at +15m49s — a ~4-5 min lag dominated by
    `subscribe_request_timeout`. Useful follow-up if the lag is a UX
    problem: lower `subscribe_request_timeout` to ~60s on the control
    client.
    """

    _CATEGORY_DISPATCH = {
        "retry": ("warn", "pubnub transport retrying", "pubnub_transport_retry"),
        "recovered": ("info", "pubnub transport recovered", "pubnub_transport_recovered"),
        "failed": ("error", "pubnub transport failed", "pubnub_transport_failed"),
    }

    def on_retry(category: str, message: str) -> None:
        dispatched = _CATEGORY_DISPATCH.get(category)
        if dispatched is None:
            return
        level, log_message, event = dispatched
        log_agent_instance_event(
            level,
            log_message,
            event=event,
            retry_message=message,
            instance_id=instance_id,
        )

    return on_retry


# ---------------------------------------------------------------------------
# Main entry point
# ---------------------------------------------------------------------------


def start_agent_instance(
    options: Optional[AgentInstanceOptions] = None,
) -> Dict[str, Any]:
    """Start an agent instance runtime.

    Returns a dict with keys: stop, agent_name, instance_id,
    task_client, publish_terminal, fail_stream.
    """
    if options is None:
        raise ValueError(
            "options is required: provide an AgentInstanceOptions "
            "with at minimum card=... and agent_name=..."
        )

    # -- Require card (parity with Node's required `card: AgentCard`) -------
    # Node enforces this at the type level in
    # blocks-sdk/sdks/node/src/runtime/agent-instance.ts:108
    # (``card: AgentCard``). Python has no compile-time type check, so we
    # validate at runtime with the same intent: reject any start where the
    # card is missing or empty. The card drives stream affinity, declared-
    # stream lookup, and runtime.maxRunningTimeSec; starting without one
    # silently breaks those contracts.
    card = options.card
    if not isinstance(card, dict) or not card:
        raise ValueError(
            "card is required: provide options.card (a registered agent card dict). "
            "The card is the source of truth for declared streams, affinity, and "
            "runtime.maxRunningTimeSec."
        )

    # -- Resolve agent name -------------------------------------------------
    agent_name: str = options.agent_name or ""
    if not agent_name:
        raise ValueError(
            "agent_name is required: provide options.agent_name"
        )

    if not re.fullmatch(r'[a-zA-Z0-9_]+', agent_name):
        raise ValueError(
            "agent_name must contain only alphanumeric characters "
            "and underscores (no hyphens)"
        )

    cm = create_channel_manager(agent_name)

    # -- Generate instance ID -----------------------------------------------
    instance_id: str = options.instance_id or f"AG-{agent_name}-{uuid.uuid4()}"

    # -- Fetch CDM config -------------------------------------------------------
    cdm_config: Optional[CdmConfig] = None
    env_keysets: Dict[str, CdmKeyset] = {}
    primary_env: str = "playground"

    if options.pubnub is not None:
        # External PubNub client — single-instance mode, skip CDM
        keyset = CdmKeyset(publish_key="", subscribe_key="")
        env_keysets = {"playground": keyset, "network": keyset}
    else:
        cdm_config = fetch_cdm_config(options.cdm_url)
        env_keysets = {
            "playground": cdm_config.playground,
            "network": cdm_config.network,
        }
        log_agent_instance_event(
            "info",
            "CDM config loaded — environment switching enabled (playground + network)",
        )

    # -- Require BLOCKS_API_KEY -------------------------------------------------
    api_key = _os_top.environ.get("BLOCKS_API_KEY", "")
    if not api_key:
        raise RuntimeError(
            "BLOCKS_API_KEY is required. Run 'blocks login --write-env' to set up credentials."
        )

    # -- Initialize AgentAuth (API key-based auth) -----------------------------
    # AgentAuth is created here but init() is deferred to connect.
    # connect is the auth entry point: connect_agent() calls
    # agent_auth.init(payload), which POSTs to /api/v1/auth/agent/connect
    # with the API key and stores the returned JWT + refresh token.
    agent_auth: Optional[AgentAuth] = None
    base_url_for_auth = options.base_url or (cdm_config.api.base_url if cdm_config else "")
    if base_url_for_auth:
        agent_auth = AgentAuth(api_key=api_key, base_url=base_url_for_auth)
        log_agent_instance_event(
            "info",
            "AgentAuth created — tokens will be obtained at registration",
        )

    # Resolve environment from registry billing_mode (only when CDM config
    # available). Per Billing Mode Contract IMPL §3 the boot-time registry
    # GET is the AUTHORITATIVE source for the agent's own billing mode:
    # there is no provider-supplied override path. To change billing mode,
    # the provider updates the registry, restarts, and lets the new value
    # flow registry GET → connect payload. Listing retained for the
    # FORBIDDEN_TRANSITIONS guard in _switch_environment.
    registry_billing_mode: Optional[Literal["free", "paid"]] = None
    registry_listing: Optional[str] = None
    if cdm_config is not None:
        registry_base_url = options.base_url or cdm_config.api.base_url
        agent_entry = get_agent(
            agent_name, base_url=registry_base_url, api_key=api_key
        )
        if agent_entry is None:
            raise RuntimeError(
                f'Agent "{agent_name}" not found in registry. '
                f"start_agent_instance requires a registered agent so the "
                f"SDK can resolve billing_mode authoritatively from the "
                f"registry GET. Run 'blocks publish' first."
            )
        if agent_entry.billing_mode not in ("free", "paid"):
            # Fail fast rather than guessing from price fields or keyset
            # names. Backend's connect schema requires billingMode on every
            # connect payload (Billing Mode Contract Phase 2); the SDK has
            # nothing valid to forward without it.
            raise RuntimeError(
                f'Registry response for "{agent_name}" is missing a valid '
                f"billing_mode (got {agent_entry.billing_mode!r}). The "
                f"backend connect schema requires billingMode; the SDK does "
                f"not infer it from prices or keyset names. Re-publish the "
                f"agent with an explicit billing mode."
            )
        registry_billing_mode = agent_entry.billing_mode
        registry_listing = agent_entry.listing
        primary_env = "network" if registry_billing_mode == "paid" else "playground"
        log_agent_instance_event(
            "info",
            f"Registry billing_mode: {registry_billing_mode} — using {primary_env} environment",
        )

    active_env: str = primary_env

    # -- Tier 1: Single active control client --------------------------------
    if options.pubnub is not None:
        control_client = options.pubnub
    else:
        ks = env_keysets[active_env]
        control_client = create_pubnub_client(
            user_id=instance_id,
            publish_key=ks.publish_key,
            subscribe_key=ks.subscribe_key,
            presence_timeout=20,
            subscribe_retry_unbounded=True,
            on_retry=_make_pubnub_retry_logger(instance_id),
        )

    if options.token:
        control_client.set_token(options.token)

    # -- TaskClient for inter-agent messaging --------------------------------
    # Billing mode for the provider's own outgoing inter-agent messages
    # defaults to the provider agent's resolved registry billing mode, so
    # the consumer-side TaskClient.send_message() carries the correct
    # caller-owned billingMode on every RPC. When CDM config is absent
    # (external pubnub mode), default to 'free' since no real RPC is sent.
    task_client_billing_mode: Literal["free", "paid"] = (
        registry_billing_mode if registry_billing_mode in ("free", "paid") else "free"
    )
    primary_keys = env_keysets[primary_env]

    # ConsumerAuth for A2A calls — lazy init via ensure_ready() on first RPC call.
    # Only created when base_url is available (production path via CDM config).
    # Tests that inject a mock PubNub skip CDM, so base_url_for_auth is empty.
    consumer_auth = (
        ConsumerAuth(api_key=api_key, base_url=base_url_for_auth)
        if base_url_for_auth
        else None
    )

    task_client = TaskClient(
        subscribe_key=primary_keys.subscribe_key,
        billing_mode=task_client_billing_mode,
        publish_key=primary_keys.publish_key,
        auth_provider=consumer_auth,
        create_pubnub=lambda: create_pubnub_client(
            user_id=f"{instance_id}-taskclient",
            publish_key=env_keysets[active_env].publish_key,
            subscribe_key=env_keysets[active_env].subscribe_key,
            subscribe_retry_unbounded=False,
        ),
        base_url=options.base_url or (cdm_config.api.base_url if cdm_config else None),
    )

    # -- Multi-instance configuration ---------------------------------------
    concurrency: int = options.concurrency if options.concurrency is not None else 1
    expected_instances: int = (
        options.expected_instances
        if options.expected_instances is not None
        else 1
    )

    # -- Scaling options ----------------------------------------------------
    max_pending_backlog: Optional[int] = options.max_pending_backlog
    # Single source of truth for max-running-time: reconcile opts with the
    # card's declared value at startup so the connect scaling payload and
    # the per-task stream TTL derivation can't drift. See Fix B in
    # dev_docs/initiative/t7c_token_lifecycle/T7C_TOKEN_LIFECYCLE_IMPL.md.
    max_running_time_sec: Optional[int] = _resolve_max_running_time_sec(
        options.max_running_time_sec,
        _extract_card_max_running_time_sec(options.card),
    )

    started_at: int = int(time.time() * 1000)

    # -- Instance-level state -----------------------------------------------
    lock = threading.Lock()
    active_thread_count: int = 0
    inflight: Set[str] = set()
    task_owner_map: Dict[str, str] = {}
    task_org_map: Dict[str, str] = {}
    task_cancel_events: Dict[str, threading.Event] = {}
    task_last_status_time: Dict[str, float] = {}  # per-task throttle for report_status
    task_status_buffer: Dict[str, str] = {}  # buffered latest message per task
    task_status_timers: Dict[str, threading.Timer] = {}  # flush timers per task
    expired_tasks: Set[str] = set()
    terminated_tasks: Set[str] = set()
    per_task_pubnub_clients: Dict[str, Any] = {}
    latest_control_token: Optional[str] = None

    # Phase 3: shared stream registry and credential cache
    stream_registry = StreamRegistry()
    credential_cache = CredentialCache()

    # Per-task unnamed stream counter
    task_stream_counters: Dict[str, int] = {}

    # Shared-stream per-task handle cache: stream_id -> {task_id -> StreamObject}.
    # Populated on the first successful `create_stream` for a shared stream
    # from a given task; consulted on repeat calls so the second acquire
    # returns the same StreamObject without re-publishing stream_setup
    # (see IMPL Fix e). Evicted on every cleanup boundary (task terminal,
    # fail_stream, release_all_for_task, explicit StreamObject.end()).
    shared_stream_handles: Dict[str, Dict[str, Any]] = {}
    shared_stream_handles_lock = threading.Lock()

    def _evict_shared_handle(stream_id: str, task_id: str) -> None:
        """Remove a single (stream_id, task_id) entry from the handle cache."""
        with shared_stream_handles_lock:
            per_stream = shared_stream_handles.get(stream_id)
            if per_stream is not None:
                per_stream.pop(task_id, None)
                if not per_stream:
                    shared_stream_handles.pop(stream_id, None)

    def _evict_shared_handles_for_stream(stream_id: str) -> None:
        """Remove all cached handles for a stream (fail_stream / force_remove)."""
        with shared_stream_handles_lock:
            shared_stream_handles.pop(stream_id, None)

    def _evict_shared_handles_for_task(task_id: str) -> None:
        """Remove every cached handle owned by ``task_id`` (release_all_for_task)."""
        with shared_stream_handles_lock:
            empty_streams: List[str] = []
            for sid, per_stream in shared_stream_handles.items():
                per_stream.pop(task_id, None)
                if not per_stream:
                    empty_streams.append(sid)
            for sid in empty_streams:
                shared_stream_handles.pop(sid, None)

    def _release_all_streams_for_task(task_id: str) -> None:
        """Release every stream this task holds, atomically.

        Bundles the three operations that every cleanup boundary must
        pair -- handle-cache eviction, registry release, and last-ref
        ``stream_client.end()`` -- into a single call so a new cleanup
        site cannot forget one leg. See QUESTIONS.md D4
        (shared_stream_lifecycle).
        """
        _evict_shared_handles_for_task(task_id)
        for entry in stream_registry.release_all_for_task(task_id):
            if entry.stream_client is not None:
                try:
                    entry.stream_client.end()
                except Exception:
                    pass

    control_channel: Optional[str] = None

    executor = ThreadPoolExecutor(max_workers=concurrency if concurrency > 0 else None)

    # -- Presence state helper ----------------------------------------------

    def update_presence_state() -> None:
        if not control_channel:
            return
        try:
            control_client.set_state().channels([control_channel]).state(
                {
                    "instanceId": instance_id,
                    "activeTasks": active_thread_count,
                    "concurrency": concurrency,
                    "startedAt": started_at,
                    "activeStreams": stream_registry.active_stream_count,
                    "preferredProtocolVersion": CURRENT_PROTOCOL_VERSION,
                    "protocolVersions": list(SUPPORTED_PROTOCOL_VERSIONS),
                }
            ).sync()
        except Exception:
            pass

    # -- Instance-level APIs ------------------------------------------------

    def publish_terminal(task_id: str, event: Dict[str, Any]) -> None:
        """Publish a terminal event using cached credentials.

        Works after pipe-task handler exit. Creates an ephemeral PubNub
        client with the cached writeToken, publishes terminal, then
        cleans up.
        """
        creds = credential_cache.get(task_id)
        if creds is None:
            raise RuntimeError(
                f"No cached credentials for task {task_id}. "
                f"Cannot publish terminal after credentials are removed."
            )

        # Create ephemeral PubNub client using cached environment keys
        env_keys = env_keysets.get(creds.environment, env_keysets[primary_env])
        ephemeral_pn = create_pubnub_client(
            user_id=instance_id,
            publish_key=env_keys.publish_key,
            subscribe_key=env_keys.subscribe_key,
            subscribe_retry_unbounded=False,
        )
        try:
            ephemeral_pn.set_token(creds.write_token)
            terminal_event = {**event, "type": "terminal", "taskId": task_id}
            _publish_task_event(
                ephemeral_pn, task_id, creds.org_id, creds.agent_name, terminal_event
            )
        finally:
            try:
                ephemeral_pn.stop()
            except Exception:
                pass

        _release_all_streams_for_task(task_id)

        # Remove credential cache entry
        credential_cache.remove(task_id)
        update_presence_state()

    def release_stream(stream_id: str, task_id: str) -> None:
        """Task-scoped release hook invoked by ``StreamObject.end()``.

        Implements IMPL fix (d):
        - Evicts the per-task shared-stream handle cache entry.
        - Releases the task's registry reference; when that drops the
          entry's ``task_ids`` set to empty the registry removes the
          entry and hands it back to the caller to tear down.
        - Shared streams: teardown calls ``StreamClient.end()`` which
          is gated on affinity and therefore never publishes a
          ``stream_end`` marker for shared broadcasts.
        - Dedicated streams: teardown calls ``StreamClient.end()`` as
          before (publishing the end marker).
        - No per-task KV mutation; shared-stream T7c revocation
          remains coupled to TerminateTask / task terminal / TTL.
        """
        _evict_shared_handle(stream_id, task_id)
        # NOTE: get + release are NOT atomic under a single lock here.
        # A concurrent fail_stream can race this path and cause a
        # benign double-end() on the underlying StreamClient.
        # Accepted as informational per QUESTIONS.md I3 — both SDKs'
        # StreamClient.end() early-return on already-ended clients, so
        # the practical outcome is a no-op. Do NOT "fix" this by
        # widening the registry lock: that would serialize all release
        # calls against every other registry op and regress parity
        # with Node's lock-free model.
        entry = stream_registry.get(stream_id)
        remaining = stream_registry.release(stream_id, task_id)
        if remaining == 0 and entry is not None and entry.stream_client is not None:
            # Distinguish last-ref teardown from non-last release for ops.
            # On shared-affinity streams StreamClient.end() suppresses the
            # marker publish; the teardown still closes the local writer.
            # See QUESTIONS.md I1 (shared_stream_lifecycle).
            logger.info(
                "stream_registry_last_ref_teardown",
                extra={
                    "event": "stream_registry_last_ref_teardown",
                    "stream_id": stream_id,
                    "affinity": entry.affinity,
                    "releasing_task_id": task_id,
                },
            )
            try:
                entry.stream_client.end()
            except Exception:
                pass
        update_presence_state()

    def fail_stream(stream_id: str, reason: str) -> None:
        """Force-fail all tasks mapped to a stream.

        Looks up the stream in the registry, publishes failed terminals
        to all affected tasks, destroys the stream.
        """
        entry = stream_registry.force_remove(stream_id)
        _evict_shared_handles_for_stream(stream_id)
        if entry is None:
            return

        # End the stream client
        if entry.stream_client is not None:
            try:
                entry.stream_client.end()
            except Exception:
                pass

        # Publish failed terminals to all mapped tasks
        for tid in list(entry.task_ids):
            creds = credential_cache.get(tid)
            if creds is not None:
                try:
                    publish_terminal(tid, {
                        "type": "terminal",
                        "taskId": tid,
                        "state": "failed",
                        "error": reason,
                    })
                except Exception:
                    pass

        update_presence_state()

    # -- Setup handshake helper ---------------------------------------------

    def _perform_setup_handshake(
        task_pn: Any,
        task_id: str,
        org_id: str,
        agent_name_arg: str,
        stream_id: str,
        stream_channel: str,
        direction: str,
        task_kind: str,
        duration_minutes: int,
        format: str,
        affinity: str,
        metadata: Optional[Dict[str, Any]] = None,
        phase: Optional[str] = None,
        declared_stream: Optional[str] = None,
    ) -> Dict[str, Any]:
        """Publish stream_setup to setup.{orgId}.{taskId} and extract T7a
        from the 403 response (request.abort(payload)).

        ``affinity`` is a required wire-level field after the ssl-wire
        phase (see ``schemas/internal/stream-setup.schema.json`` v5.5.0).
        The Function rejects any payload missing it with
        ``InvalidArgument``.

        Returns the response payload dict containing the T7a token.
        """
        setup_channel = f"setup.{org_id}.{task_id}"
        setup_payload: Dict[str, Any] = {
            "type": "stream_setup",
            "protocolVersion": CURRENT_PROTOCOL_VERSION,
            "taskId": task_id,
            "orgId": org_id,
            "agentName": agent_name_arg,
            "streamId": stream_id,
            "channel": stream_channel,
            "direction": direction,
            "taskKind": task_kind,
            "durationMinutes": duration_minutes,
            "format": format,
            "affinity": affinity,
        }
        if declared_stream is not None:
            setup_payload["declaredStream"] = declared_stream
        if metadata is not None:
            setup_payload["metadata"] = metadata
        if phase is not None:
            setup_payload["phase"] = phase

        # The streamSetup Function intercepts this publish via
        # onBeforePublish and returns T7a via request.abort(payload).
        # The PubNub SDK raises an exception on 403, and the response
        # payload is in the exception details.
        try:
            task_pn.publish().channel(setup_channel).message(
                setup_payload
            ).should_store(False).use_post(True).sync()
        except Exception as exc:
            # Extract the abort payload from the exception.
            # The shape depends on the PubNub SDK version but typically
            # the custom abort payload is in the error response body.
            response = _extract_abort_response(exc)
            if response:
                # The payload may be at top level or nested under "message"
                # depending on how the PubNub SDK wraps the 403 response.
                payload = response
                if isinstance(response.get("message"), dict):
                    payload = response["message"]
                if payload.get("streamSetupResponse"):
                    return payload
                # Check for structured error from the Function
                if payload.get("ok") is False and isinstance(payload.get("error"), dict):
                    err = payload["error"]
                    raise RuntimeError(
                        f"Setup handshake failed for stream {stream_id}: "
                        f"[{err.get('code', 'unknown')}] {err.get('message', str(exc))}"
                    )
            raise RuntimeError(
                f"Setup handshake failed for stream {stream_id}: {exc}"
            ) from exc

        raise RuntimeError(
            f"Setup handshake for stream {stream_id} did not receive "
            f"expected 403 response with T7a token"
        )

    def _extract_abort_response(exc: Exception) -> Optional[Dict[str, Any]]:
        """Extract the custom abort payload from a PubNub publish exception.

        The streamSetup Function uses request.abort(customPayload) which
        returns a 403 with the payload embedded in the error response.
        """
        # Try multiple extraction paths depending on PubNub SDK version
        if hasattr(exc, "result") and exc.result is not None:
            result = exc.result
            if hasattr(result, "status") and hasattr(result.status, "error_data"):
                error_data = result.status.error_data
                if isinstance(error_data, dict):
                    return error_data
        if hasattr(exc, "status") and hasattr(exc.status, "error_data"):
            if isinstance(exc.status.error_data, dict):
                return exc.status.error_data
        # Fallback: parse from string if embedded
        import json
        try:
            s = str(exc)
            # Look for JSON embedded in the error message
            start = s.find("{")
            end = s.rfind("}") + 1
            if start >= 0 and end > start:
                parsed = json.loads(s[start:end])
                if isinstance(parsed, dict):
                    return parsed
        except Exception:
            pass
        return None

    # -- Default handlers ---------------------------------------------------

    def default_on_start(
        task: StartTaskMessage,
        pn: Any,
        task_pn: Any = None,
        cancel_evt: threading.Event | None = None,
        task_env: str = "playground",
    ) -> None:
        """Default StartTask handler: run user handler, publish events."""

        event_pn = task_pn if task_pn is not None else pn
        owner_id = _extract_owner_id(task.owner_id, task.caller_claims)
        org_id = task.org_id or owner_id
        effective_agent_name = agent_name or task.agent_name
        if not effective_agent_name:
            raise RuntimeError("agentName is required for task processing")

        log_agent_instance_event(
            "info",
            f"Task {task.task_id} started",
            taskId=task.task_id,
            agentName=effective_agent_name,
            owner=owner_id,
        )

        # Publish running state
        _publish_task_event(event_pn, task.task_id, org_id, effective_agent_name, {
            "type": "progress",
            "taskId": task.task_id,
            "progress": 0,
            "state": "running",
        })


        # Stream tracking for this task
        task_streams: List[str] = []

        def _report_status(message: str) -> None:
            now = time.time()
            last = task_last_status_time.get(task.task_id, 0.0)
            if now - last < 1.0:
                # Within throttle window: buffer the latest message
                with lock:
                    task_status_buffer[task.task_id] = message
                    if task.task_id not in task_status_timers:
                        delay = 1.0 - (now - last)

                        def _flush_buffered(tid: str = task.task_id) -> None:
                            with lock:
                                buffered = task_status_buffer.pop(tid, None)
                                task_status_timers.pop(tid, None)
                            if buffered is not None:
                                task_last_status_time[tid] = time.time()
                                _publish_task_event(event_pn, tid, org_id, effective_agent_name, {
                                    "type": "progress",
                                    "taskId": tid,
                                    "message": buffered,
                                })

                        timer = threading.Timer(delay, _flush_buffered)
                        timer.daemon = True
                        task_status_timers[task.task_id] = timer
                        timer.start()
                return
            task_last_status_time[task.task_id] = now
            _publish_task_event(event_pn, task.task_id, org_id, effective_agent_name, {
                "type": "progress",
                "taskId": task.task_id,
                "message": message,
            })

        def _create_stream(
            *,
            direction: str = "outbound",
            on_activate: Optional[Callable] = None,
            metadata: Optional[Dict[str, Any]] = None,
            external: bool = False,
            format: str = "bytes",
            bundle_size_bytes: Optional[int] = None,
            max_latency_ms: Optional[int] = None,
            declared_stream: str = "_default",
            subscribe_grace_ms: Optional[int] = None,
        ) -> Any:
            """Unified create_stream() API."""
            if not task.has_stream:
                raise RuntimeError(
                    "Streaming was not negotiated for this task. "
                    "Ensure the agent card is registered with streaming capability."
                )

            task_kind = task.task_kind or "request"

            # Request-task constraints
            if task_kind == "request":
                if direction != "outbound":
                    raise RuntimeError(
                        "Request tasks only support outbound streams"
                    )
                if external:
                    raise RuntimeError(
                        "Request tasks cannot use external streams"
                    )

            # -- Card stream affinity enforcement ---------------------------------
            card = options.card or {}
            card_streams = card.get("streams") if isinstance(card, dict) else None

            if card_streams and isinstance(card_streams, dict):
                # Resolve which declared stream to use
                effective_declared = declared_stream
                if effective_declared == "_default":
                    keys = list(card_streams.keys())
                    if len(keys) == 1:
                        effective_declared = keys[0]
                    else:
                        raise RuntimeError(
                            f"Card declares multiple streams ({', '.join(keys)}). "
                            f"Specify declared_stream to select one."
                        )

                # Validate the declared stream exists
                decl = card_streams.get(effective_declared)
                if decl is None:
                    raise RuntimeError(
                        f"Undeclared stream: '{effective_declared}'. "
                        f"Available streams: {', '.join(card_streams.keys())}"
                    )

                # Use card values as defaults for direction and format
                if isinstance(decl, dict):
                    card_direction = decl.get("direction")
                    card_format = decl.get("format")
                    card_affinity = decl.get("affinity", "dedicated")

                    # Validate no conflict with explicit parameters
                    if card_direction and direction != card_direction:
                        if direction != "outbound":
                            # Only raise if the caller explicitly passed a conflicting direction
                            raise RuntimeError(
                                f"Direction conflict: card declares '{card_direction}' "
                                f"but create_stream called with '{direction}'"
                            )
                        direction = card_direction
                    if card_format and format != card_format:
                        if format != "bytes":
                            raise RuntimeError(
                                f"Format conflict: card declares '{card_format}' "
                                f"but create_stream called with '{format}'"
                            )
                        format = card_format
                else:
                    card_affinity = "dedicated"

                # Update declared_stream to the resolved key
                declared_stream = effective_declared
            else:
                card_affinity = "dedicated"

            if card_streams is not None and not isinstance(card_streams, dict):
                # card.streams exists but is not a dict -- ignore
                pass

            # Fix (g): shared-affinity streams are inherently cross-task
            # broadcast; request tasks are single-shot so the model is
            # moot. Fail fast with a clear message so authors either
            # switch to dedicated affinity or drop 'request' from the
            # card's taskKinds.
            if card_affinity == "shared" and task_kind == "request":
                raise RuntimeError(
                    "Shared-affinity streams are not supported on request tasks. "
                    f"Declared stream '{declared_stream}' has affinity: 'shared'. "
                    "Request tasks are single-shot; cross-task broadcast is "
                    "inherently a pipe-task concept. Use affinity: 'dedicated' "
                    "or remove 'request' from the agent card's taskKinds."
                )

            # Fix (h): shared-affinity + external is a design
            # contradiction. Shared affinity is "one SDK-managed
            # broadcast writer, many ref-holding tasks"; external
            # streams delegate the writer to an external process
            # entirely. In the shared+external combination there is no
            # single writer in the SDK's registry model -- each task
            # would hand its own T7a to a different external process,
            # all writing the same broadcast channel. Fail fast before
            # the registry / handshake state gets touched. A future
            # initiative can model external broadcast explicitly (see
            # GitHub #516).
            if card_affinity == "shared" and external:
                raise RuntimeError(
                    "Shared-affinity external streams are not supported. "
                    f"Declared stream '{declared_stream}' has affinity: 'shared' "
                    "and create_stream was called with external=True. Shared "
                    "affinity requires a single SDK-managed writer with "
                    "per-task ref-counting; external streams delegate the "
                    "writer entirely. Use affinity: 'dedicated' with "
                    "external=True, or affinity: 'shared' without external."
                )

            # Stream channel format: stream.{agent_name}.{stream_id}. The
            # stream_id is SDK-derived: dedicated-affinity uses
            # `{task_id}-{counter}`, shared-affinity uses the card-declared
            # key. Handler code cannot override the channel suffix.
            if card_affinity == "shared":
                # Shared: use the declared key as the stream ID
                effective_stream_id = declared_stream
            else:
                # Dedicated: auto-generate per-task channel suffix.
                with lock:
                    counter = task_stream_counters.get(task.task_id, 0) + 1
                    task_stream_counters[task.task_id] = counter
                effective_stream_id = f"{task.task_id}-{counter}"

            # Fix (e): same-task idempotent handle cache. A second
            # create_stream call from the same task for the same shared
            # stream returns the existing StreamObject without any
            # registry mutation or setup handshake.
            if card_affinity == "shared":
                with shared_stream_handles_lock:
                    cached = shared_stream_handles.get(
                        effective_stream_id, {}
                    ).get(task.task_id)
                if cached is not None:
                    return cached

            # Acquire from registry (compatibility check + task-scoped tracking)
            entry, is_new, is_new_for_task = stream_registry.acquire(
                effective_stream_id,
                task.task_id,
                direction=direction,
                format=format,
                external=external,
                affinity=card_affinity,
            )

            task_streams.append(effective_stream_id)
            credential_cache.add_stream(task.task_id, effective_stream_id)

            # Same-task reacquire. Fix (e): return the EXACT same
            # StreamObject the first call returned, with no registry
            # mutation and no extra setup publish.
            #
            # The top-of-function cache lookup handles sequential
            # same-task reacquires because the first call populates
            # the cache before returning. CONCURRENT same-task calls
            # (thread-pool handler spawning sub-threads, or an async
            # gather) land here because the second call enters
            # create_stream before the first's setup has cached its
            # handle -- cache miss, then registry.acquire correctly
            # reports is_new_for_task=False.
            #
            # We MUST NOT fall through to the `!is_new`/activate path
            # below -- that would publish `phase: 'activate'` for this
            # task, duplicating the first call's setup and minting a
            # second T7c for the SAME task. Wait for the first
            # acquirer's setup instead, then return the cached handle.
            if not is_new_for_task:
                if (
                    entry.stream_client is None
                    and entry.setup_complete is not None
                ):
                    entry.setup_complete.wait()
                    if entry.setup_error is not None:
                        raise entry.setup_error
                with shared_stream_handles_lock:
                    cached = shared_stream_handles.get(
                        effective_stream_id, {}
                    ).get(task.task_id)
                if cached is not None:
                    return cached
                # Post-wait fallback: client installed but not cached.
                # Should be unreachable because the first acquirer
                # caches BEFORE setup_event.set() fires; guard with a
                # defensive wrap rather than falling through to a
                # duplicate activate publish.
                if entry.stream_client is not None:
                    stream_obj = StreamObject(
                        effective_stream_id,
                        entry.stream_client,
                        task_id=task.task_id,
                        release_stream=release_stream,
                    )
                    with shared_stream_handles_lock:
                        shared_stream_handles.setdefault(
                            effective_stream_id, {}
                        )[task.task_id] = stream_obj
                    return stream_obj
                # External + shared is blocked by fix (h), so
                # stream_client must exist here on a fresh shared
                # entry. Raise rather than return a broken handle.
                raise RuntimeError(
                    f"Stream '{effective_stream_id}' same-task reacquire "
                    "reached an impossible state: setup completed but no "
                    "client installed. This indicates a logic error in "
                    "the shared-stream handle cache."
                )

            # Use duration from StartTask message (set by messageSend for pipe tasks),
            # fall back to defaults when not provided. Request-task default
            # derives from the card's runtime.maxRunningTimeSec (via the
            # resolver above); 60 minutes is the final fallback when neither
            # opts nor card set it.
            is_pipe = task_kind == "pipe"
            duration_minutes = _compute_stream_duration_minutes(
                task.duration,
                is_pipe,
                max_running_time_sec,
            )

            stream_channel = cm.stream_channel(effective_stream_id)

            # Cross-task second-acquirer path (is_new=False,
            # is_new_for_task=True). `registry.acquire` added this
            # task to `entry.task_ids`; any throw from here onward
            # leaves a zombie ref on the shared entry. Wrap both the
            # setup-barrier wait and the activate publish in a
            # try/except with rollback so a first-acquirer setup
            # failure or a rejected activate doesn't brick the
            # channel for subsequent tasks (PR#515 review finding).
            try:
                # CRITICAL: if THIS task is a second-or-later acquirer
                # on a shared entry, wait for the first-acquirer's
                # setup to finish before touching entry.stream_client.
                # The handshake is synchronous-but-slow (PubNub
                # round-trip); without the barrier a concurrent Task
                # B would observe entry.stream_client == None, skip
                # the activate branch below, and fall through to the
                # embedded handshake at the bottom — creating a
                # DUPLICATE StreamClient on the same shared channel.
                # See PR#515 reviewer finding.
                if not is_new and entry.setup_complete is not None:
                    entry.setup_complete.wait()
                    if entry.setup_error is not None:
                        # First acquirer's setup raised; propagate so
                        # this task fails fast rather than silently
                        # limping on a half-initialized registry
                        # entry.
                        raise entry.setup_error

                # Fix (b): shared stream with an existing writer + a
                # fresh task attacher. Publish `phase: 'activate'` so
                # the Function mints a per-task T7c keyed by this
                # task, writes streamtoken:{taskId}:{streamId}, and
                # publishes stream_started to this task's status
                # channel. No StreamClient is created — this task
                # rides the shared writer via the cached
                # entry.stream_client.
                if (
                    card_affinity == "shared"
                    and not is_new
                    and is_new_for_task
                    and entry.stream_client is not None
                ):
                    _perform_setup_handshake(
                        event_pn,
                        task.task_id,
                        org_id,
                        effective_agent_name,
                        effective_stream_id,
                        stream_channel,
                        direction,
                        task_kind,
                        duration_minutes,
                        format,
                        affinity=card_affinity,
                        metadata=metadata,
                        phase="activate",
                        declared_stream=declared_stream,
                    )
                    stream_obj = StreamObject(
                        effective_stream_id,
                        entry.stream_client,
                        task_id=task.task_id,
                        release_stream=release_stream,
                    )
                    with shared_stream_handles_lock:
                        shared_stream_handles.setdefault(
                            effective_stream_id, {}
                        )[task.task_id] = stream_obj
                    update_presence_state()
                    return stream_obj
            except BaseException:
                # BaseException is deliberate: rollback must run even
                # on KeyboardInterrupt / SystemExit so a zombie
                # shared-stream entry doesn't persist in the registry
                # if the agent is being torn down mid-setup. The bare
                # `raise` at the end propagates the interrupt
                # unchanged.
                #
                # Only roll back if this call actually added the task
                # to entry.task_ids (is_new_for_task=True). Idempotent
                # reacquires (is_new_for_task=False) are handled by
                # the `not is_new_for_task` branch above and never
                # reach here anyway; the guard is defensive.
                if is_new_for_task:
                    _evict_shared_handle(
                        effective_stream_id, task.task_id
                    )
                    try:
                        stream_registry.release(
                            effective_stream_id, task.task_id
                        )
                    except Exception:
                        pass  # best-effort rollback
                raise

            # First-acquirer path. Install the setup barrier on the
            # entry BEFORE starting the handshake so concurrent second
            # acquirers block above rather than racing to observe a
            # null stream_client. Barrier fires on success OR failure
            # (via the try/except/finally below) so second acquirers
            # don't hang on a crashed first acquirer.
            setup_event: Optional[threading.Event] = None
            if is_new:
                setup_event = threading.Event()
                entry.setup_complete = setup_event

            try:
                if external:
                    # Two-phase handshake: token_request first
                    response = _perform_setup_handshake(
                        event_pn,
                        task.task_id,
                        org_id,
                        effective_agent_name,
                        effective_stream_id,
                        stream_channel,
                        direction,
                        task_kind,
                        duration_minutes,
                        format,
                        affinity=card_affinity,
                        metadata=metadata,
                        phase="token_request",
                        declared_stream=declared_stream,
                    )
                    # Token is nested inside streamSetupResponse (matches Node SDK's extractFromPayload)
                    setup_resp = response.get("streamSetupResponse", {})
                    t7a = setup_resp.get("token", "") if isinstance(setup_resp, dict) else response.get("token", "")

                    def _activate_fn(**kwargs: Any) -> None:
                        act_metadata = kwargs.get("metadata", metadata)
                        _perform_setup_handshake(
                            event_pn,
                            task.task_id,
                            org_id,
                            effective_agent_name,
                            effective_stream_id,
                            stream_channel,
                            direction,
                            task_kind,
                            duration_minutes,
                            format,
                            affinity=card_affinity,
                            metadata=act_metadata,
                            phase="activate",
                            declared_stream=declared_stream,
                        )

                    ext_obj = ExternalStreamObject(
                        effective_stream_id,
                        stream_channel,
                        t7a,
                        _activate_fn,
                    )
                    # Release concurrent second acquirers. External
                    # streams leave stream_client == None by design;
                    # the !is_new branch's external fallback handles
                    # that case after the wait returns.
                    if setup_event is not None:
                        setup_event.set()
                        # Null the barrier now that setup has settled;
                        # no future caller needs to wait on it.
                        entry.setup_complete = None
                    return ext_obj

                # Embedded stream -- single-phase handshake. Publish
                # ``phase: 'embedded'`` explicitly so cross-SDK parity
                # holds on the wire (Node publishes the same). See
                # QUESTIONS.md D5 (shared_stream_lifecycle).
                response = _perform_setup_handshake(
                    event_pn,
                    task.task_id,
                    org_id,
                    effective_agent_name,
                    effective_stream_id,
                    stream_channel,
                    direction,
                    task_kind,
                    duration_minutes,
                    format,
                    affinity=card_affinity,
                    metadata=metadata,
                    phase="embedded",
                    declared_stream=declared_stream,
                )
                # Token is nested inside streamSetupResponse (matches Node SDK's extractFromPayload)
                setup_resp = response.get("streamSetupResponse", {})
                t7a = setup_resp.get("token", "") if isinstance(setup_resp, dict) else response.get("token", "")

                # Create StreamClient from Phase 2 Stream SDK
                from .stream import StreamClient as PnafStreamClient

                stream_keys = env_keysets.get(task_env, env_keysets[primary_env])
                stream_client = PnafStreamClient(
                    subscribe_key=stream_keys.subscribe_key,
                    publish_key=stream_keys.publish_key,
                    token=t7a,
                    agent_name=effective_agent_name,
                    stream_id=effective_stream_id,
                    format=format,
                    direction=direction,
                    bundle_size_bytes=bundle_size_bytes,
                    max_latency_ms=max_latency_ms,
                    affinity=card_affinity,
                )

                entry.stream_client = stream_client

                # Build + cache the per-task handle synchronously
                # BEFORE signalling setup_event. Order matters:
                # concurrent second acquirers (same task, or
                # cross-task for shared) wake on setup_complete and
                # immediately read shared_stream_handles -- they
                # must see a populated cache, otherwise the
                # same-task fix (e) idempotent path degrades to a
                # duplicate activate publish.
                stream_obj = StreamObject(
                    effective_stream_id,
                    stream_client,
                    task_id=task.task_id,
                    release_stream=release_stream,
                )
                if card_affinity == "shared":
                    with shared_stream_handles_lock:
                        shared_stream_handles.setdefault(
                            effective_stream_id, {}
                        )[task.task_id] = stream_obj

                # Release concurrent second acquirers now that
                # stream_client is installed AND the handle cache
                # is populated. Fires BEFORE the subscribe-grace
                # sleep so cross-task second acquirers can attach
                # during the grace window.
                if setup_event is not None:
                    setup_event.set()
                    # Null the barrier now that setup has settled; no
                    # future caller needs to wait on it.
                    entry.setup_complete = None
            except BaseException as err:
                # BaseException is deliberate: rollback must run even
                # on KeyboardInterrupt / SystemExit so a zombie
                # shared-stream entry doesn't brick the channel if the
                # agent is being torn down mid-setup. The `raise` at
                # the end propagates the interrupt unchanged.
                if setup_event is not None:
                    entry.setup_error = err
                    setup_event.set()
                # Roll back the registry ref this task just acquired.
                # Shared streams reuse the same stream_id across tasks,
                # so leaving a failed first-acquirer entry in the
                # registry would brick the channel for every subsequent
                # task on this agent instance — each new acquire would
                # find the zombie entry, wait on the signalled
                # setup_complete event, observe setup_error, and
                # re-raise the original error until the agent restarts.
                # Release the ref locally (and evict any cached handle)
                # before propagating. Dedicated streams are also
                # covered: stream_id is task-scoped so the release
                # simply removes the lone ref.
                _evict_shared_handle(effective_stream_id, task.task_id)
                try:
                    stream_registry.release(
                        effective_stream_id, task.task_id
                    )
                except Exception:
                    pass  # best-effort rollback
                raise

            # Register on_end callback for cleanup
            def _on_stream_end() -> None:
                update_presence_state()

            stream_client.on_end(_on_stream_end)
            update_presence_state()

            # Run on_activate on a dedicated daemon thread
            if on_activate is not None:
                entry.activated = True
                thread = run_on_activate(
                    effective_stream_id,
                    stream_obj,
                    on_activate,
                    fail_stream,
                )
                entry.activate_thread = thread

            # Subscribe grace period: delay outbound/bidirectional streams
            # so consumer has time to subscribe before first write.
            if direction in ("outbound", "bidirectional"):
                grace_ms = subscribe_grace_ms if subscribe_grace_ms is not None else 1000
                if grace_ms > 0:
                    time.sleep(grace_ms / 1000)

            return stream_obj

        # Use the pre-created cancel event
        if cancel_evt is None:
            cancel_evt = threading.Event()
            with lock:
                task_cancel_events[task.task_id] = cancel_evt

        # Resolve base_url for pre-signed upload flow
        _task_base_url = options.base_url or (
            cdm_config.api.base_url if cdm_config else ""
        )
        _task_auth_provider = (
            StaticAuthProvider(options.token) if options.token else None
        )
        _task_agent_auth = agent_auth

        def _download_input(part: Any) -> bytes:
            """Download the artifact from a request part."""
            ref = getattr(part, "artifact_ref", None) or (
                part.get("artifactRef") if isinstance(part, dict) else None
            )
            return _download_input_artifact(event_pn, ref)

        def _publish_artifact_mid(
            data: Any,
            *,
            mime_type: Optional[str] = None,
            file_name: Optional[str] = None,
            output_id: Optional[str] = None,
        ) -> None:
            """Publish an artifact mid-execution."""
            effective_mime = mime_type or "application/octet-stream"
            if isinstance(data, str):
                buf = data.encode("utf-8")
            elif isinstance(data, (bytes, bytearray)):
                buf = bytes(data)
            else:
                raise TypeError("publish_artifact data must be str or bytes")

            if should_inline_artifact(len(buf)):
                ref = build_artifact_ref(
                    data=buf, mime_type=effective_mime, file_name=file_name,
                )
                evt: Dict[str, Any] = {
                    "type": "artifact",
                    "taskId": task.task_id,
                    "artifactRef": ref,
                }
                if output_id is not None:
                    evt["outputId"] = output_id
                _publish_task_event(event_pn, task.task_id, org_id, effective_agent_name, evt)
            elif _task_base_url:
                # Large artifact: pre-signed URL flow. Backend publishes the artifact event.
                _upload_artifact_presigned(
                    buf,
                    task.task_id,
                    effective_mime,
                    _task_base_url,
                    agent_auth=_task_agent_auth,
                    auth_provider=_task_auth_provider,
                    custom_file_name=file_name,
                    output_id=output_id,
                )
            else:
                raise ValueError(
                    "base_url is required for artifacts larger than 16 KB"
                )

        task_context = TaskContext(
            task_id=task.task_id,
            has_stream=bool(task.has_stream),
            consumer_public_key=task.consumer_public_key,
            report_status=_report_status,
            create_stream=_create_stream,
            download_input_artifact=_download_input,
            publish_artifact=_publish_artifact_mid,
            cancel_event=cancel_evt,
            task_client=task_client,
            _expired_tasks=expired_tasks,
        )

        # Execute the user-supplied handler
        result = None
        try:
            if options.handler:
                result = options.handler(task, task_context)
        finally:
            # For request tasks: auto-end streams on handler return.
            # Request tasks are rejected on shared affinity upstream
            # (fix g), so `release` here always destroys the entry
            # (single ref-holder) and publishes the dedicated-stream
            # end marker via stream_client.end().
            task_kind = task.task_kind or "request"
            if task_kind == "request":
                for sid in task_streams:
                    entry = stream_registry.get(sid)
                    if entry and entry.stream_client and entry.stream_client.is_active:
                        try:
                            entry.stream_client.end()
                        except Exception:
                            pass
                    # Release registry entry + drop any cached handle.
                    stream_registry.release(sid, task.task_id)
                    _evict_shared_handle(sid, task.task_id)

        # Handle artifacts from result (plural: artifacts array)
        if result is not None and isinstance(result, dict):
            artifacts_list = result.get("artifacts")
            if isinstance(artifacts_list, list):
                for artifact_entry in artifacts_list:
                    if not isinstance(artifact_entry, dict):
                        continue
                    a_data = artifact_entry.get("data")
                    a_mime = artifact_entry.get("mimeType") or "application/octet-stream"
                    a_file_name = artifact_entry.get("fileName")
                    a_output_id = artifact_entry.get("outputId")

                    if a_data is None:
                        continue
                    if isinstance(a_data, str):
                        a_buf = a_data.encode("utf-8")
                    elif isinstance(a_data, (bytes, bytearray)):
                        a_buf = bytes(a_data)
                    else:
                        continue

                    _publish_artifact_mid(
                        a_buf,
                        mime_type=a_mime,
                        file_name=a_file_name,
                        output_id=a_output_id,
                    )

        # Determine terminal state
        was_cancelled = cancel_evt.is_set()
        task_kind = task.task_kind or "request"

        if task_kind == "pipe":
            was_expired = task.task_id in expired_tasks
            was_terminated = task.task_id in terminated_tasks

            if was_cancelled or was_expired or was_terminated:
                # Clean up streams and publish terminal AFTER the artifact
                # (published above) so consumers see correct event ordering.
                _release_all_streams_for_task(task.task_id)

                terminal_state = "canceled" if (was_cancelled and not was_expired) else "completed"
                terminal_payload: Dict[str, Any] = {
                    "type": "terminal",
                    "taskId": task.task_id,
                    "state": terminal_state,
                }
                if was_expired:
                    terminal_payload["completionReason"] = "duration_expired"
                if was_terminated:
                    terminal_payload["reason"] = "terminated"

                _publish_task_event(event_pn, task.task_id, org_id, effective_agent_name, terminal_payload)
                credential_cache.remove(task.task_id)
            else:
                # Voluntary return -- no terminal, credentials cached
                log_agent_instance_event(
                    "info",
                    f"Task {task.task_id} handler returned (pipe, no auto-terminal)",
                    taskId=task.task_id,
                    agentName=effective_agent_name,
                    owner=owner_id,
                )
        else:
            # Request tasks: existing auto-complete behavior
            terminal_state = "canceled" if was_cancelled else "completed"
            was_terminated_req = task.task_id in terminated_tasks
            terminal_payload: Dict[str, Any] = {
                "type": "terminal",
                "taskId": task.task_id,
                "state": terminal_state,
            }
            if was_terminated_req:
                terminal_payload["reason"] = "terminated"

            _publish_task_event(event_pn, task.task_id, org_id, effective_agent_name, terminal_payload)

            # Clean up streams for request tasks
            _release_all_streams_for_task(task.task_id)
            credential_cache.remove(task.task_id)

            log_agent_instance_event(
                "info",
                f"Task {task.task_id} {terminal_state}",
                taskId=task.task_id,
                agentName=effective_agent_name,
                owner=owner_id,
            )

    def default_on_cancel(task_id: str, pn: Any) -> None:
        """Default CancelTask handler: signal cooperative cancellation."""
        with lock:
            evt = task_cancel_events.get(task_id)
        if evt:
            evt.set()
            log_agent_instance_event(
                "info",
                f"Task {task_id} cancel requested (cooperative)",
                taskId=task_id,
                instanceId=instance_id,
            )
        else:
            # Task not in flight -- for pipe tasks server owns terminal
            pass

    on_start: Callable = options.on_start_task or default_on_start
    on_cancel: Callable = options.on_cancel_task or default_on_cancel

    # -- Environment switching helper ---------------------------------------

    def _switch_environment(new_env: str, pam_token: Optional[str] = None) -> None:
        """Switch the active PubNub environment.

        pamToken is required. SwitchEnvironment messages without a pamToken
        are rejected to prevent a race where subscribe fires before the token
        is applied.
        """
        nonlocal active_env, control_client, latest_control_token, control_channel

        if not control_channel:
            log_agent_instance_event(
                "error",
                "SwitchEnvironment rejected: controlChannel not yet set (connect pending)",
            )
            return

        # Require pamToken — no fallback re-registration
        if not pam_token:
            log_agent_instance_event(
                "error",
                "SwitchEnvironment rejected: pamToken is required. "
                "The server must provide a pamToken for the target environment.",
            )
            return

        log_agent_instance_event(
            "info",
            f"Switching environment from {active_env} to {new_env}",
        )

        # Save reference to old client so we can stop it after the new one subscribes
        previous_control_client = control_client

        # Unsubscribe and remove listener from current client
        try:
            previous_control_client.remove_listener(listener)
            previous_control_client.unsubscribe().channels([control_channel]).execute()
        except Exception:
            pass

        # Create new client with new environment's keys
        ks = env_keysets[new_env]
        control_client = create_pubnub_client(
            user_id=instance_id,
            publish_key=ks.publish_key,
            subscribe_key=ks.subscribe_key,
            presence_timeout=20,
            subscribe_retry_unbounded=True,
            on_retry=_make_pubnub_retry_logger(instance_id),
        )

        # Clear stale token from previous environment
        latest_control_token = None

        # Apply PAM token for the new environment
        control_client.set_token(pam_token)

        # Set filter expression
        filter_expression = (
            f"meta.instance == '{instance_id}' || meta.broadcast == \"true\""
        )
        try:
            if hasattr(control_client, "set_filter_expression"):
                control_client.set_filter_expression(filter_expression)
            elif hasattr(control_client, "_config") and control_client._config is not None:
                control_client._config.filter_expression = filter_expression
            elif hasattr(control_client, "config") and control_client.config is not None:
                control_client.config.filter_expression = filter_expression
        except Exception:
            log_agent_instance_event(
                "warn",
                "Could not set filter expression on new PubNub client",
            )

        # Add listener and subscribe
        control_client.add_listener(listener)
        control_client.subscribe().channels([control_channel]).execute()

        # Stop old client AFTER new one is subscribed (matches Node SDK ordering)
        try:
            previous_control_client.stop()
        except Exception:
            pass

        active_env = new_env

        # Update TaskClient RPC keys so that post-switch RPC calls
        # (send_message, get_task, cancel_task, etc.) target the new keyset.
        task_client.update_keys(
            subscribe_key=ks.subscribe_key,
            publish_key=ks.publish_key,
        )

        log_agent_instance_event(
            "info",
            f"Switched to {new_env} environment",
        )
        update_presence_state()

    # -- Control message dispatcher -----------------------------------------

    def handle_control_message(
        msg_dict: Dict[str, Any],
        meta: Optional[Dict[str, Any]] = None,
    ) -> None:
        nonlocal active_thread_count

        # Handle environment switch (not a standard control message)
        if msg_dict.get("type") == "SwitchEnvironment":
            switch_version = msg_dict.get("protocolVersion")
            if switch_version and not is_supported(switch_version):
                log_agent_instance_event(
                    "warn",
                    f"SwitchEnvironment rejected: unsupported protocolVersion "
                    f"{switch_version}",
                )
                return
            new_env = msg_dict.get("environment", "")
            if new_env and new_env in env_keysets and new_env != active_env:
                _switch_environment(new_env, msg_dict.get("pamToken"))
            return

        # Extract PAM tokens before parsing (BLOCKS-232: tokens never on handler-visible object)
        _write_token, _control_token = extract_start_task_tokens(msg_dict)

        msg = parse_control_message(msg_dict)
        if msg is None:
            return

        if isinstance(msg, StartTaskMessage):
            task_env = active_env  # capture environment at task receipt
            owner_id = _extract_owner_id(msg.owner_id, msg.caller_claims)
            org_id = msg.org_id or owner_id
            is_broadcast = bool(meta and meta.get("broadcast"))

            # Protocol version compatibility check
            msg_version = msg.protocol_version
            if msg_version and not is_supported(msg_version):
                if is_broadcast:
                    # Broadcast with unsupported version: silently ignore
                    log_agent_instance_event(
                        "debug",
                        f"Ignoring broadcast StartTask {msg.task_id} with "
                        f"unsupported protocolVersion {msg_version}",
                        taskId=msg.task_id,
                    )
                    return
                # Targeted with unsupported version: terminal failed
                _publish_task_event(control_client, msg.task_id, org_id, agent_name, {
                    "type": "terminal",
                    "taskId": msg.task_id,
                    "state": "failed",
                    "error": "unsupported_protocol_version",
                }, protocol_version=msg_version)
                log_agent_instance_event(
                    "warn",
                    f"Task {msg.task_id} rejected: unsupported protocolVersion "
                    f"{msg_version}",
                    taskId=msg.task_id,
                    instanceId=instance_id,
                )
                return

            # Defensive guard: reject pipe StartTask with missing/invalid
            # duration. The backend and scanner should prevent this, but if
            # a malformed StartTask arrives, do not start the handler
            # without a duration timer.
            msg_task_kind = msg.task_kind or "request"
            if msg_task_kind == "pipe":
                dur = msg.duration
                expires_at = msg.duration_expires_at_ms
                if (
                    dur is None
                    or isinstance(dur, bool)
                    or not isinstance(dur, (int, float))
                    or dur != int(dur)
                    or int(dur) < 1
                    or int(dur) > 43200
                    or not isinstance(expires_at, (int, float))
                    or expires_at <= 0
                ):
                    log_agent_instance_event(
                        "error",
                        f"Task {msg.task_id} rejected: pipe StartTask with "
                        f"invalid duration (duration={dur}, "
                        f"duration_expires_at_ms={expires_at})",
                        taskId=msg.task_id,
                        instanceId=instance_id,
                    )
                    _publish_task_event(
                        control_client, msg.task_id, org_id, agent_name, {
                            "type": "terminal",
                            "taskId": msg.task_id,
                            "state": "failed",
                            "error": "invalid_start_task",
                        }, protocol_version=msg_version,
                    )
                    return

            with lock:
                if msg.task_id in inflight:
                    log_agent_instance_event(
                        "warn",
                        f"Ignoring duplicate StartTask for in-flight task {msg.task_id}",
                        taskId=msg.task_id,
                    )
                    return

                if concurrency > 0 and active_thread_count >= concurrency:
                    if is_broadcast:
                        log_agent_instance_event(
                            "debug",
                            f"Task {msg.task_id} skipped (broadcast, at capacity "
                            f"{active_thread_count}/{concurrency})",
                            taskId=msg.task_id,
                            instanceId=instance_id,
                        )
                        return

                    _publish_task_event(control_client, msg.task_id, org_id, agent_name, {
                        "type": "terminal",
                        "taskId": msg.task_id,
                        "state": "failed",
                        "error": "agent_at_capacity",
                    })
                    log_agent_instance_event(
                        "warn",
                        f"Task {msg.task_id} rejected: agent at capacity "
                        f"({active_thread_count}/{concurrency})",
                        taskId=msg.task_id,
                        instanceId=instance_id,
                        activeThreads=active_thread_count,
                        concurrency=concurrency,
                    )
                    return

                task_owner_map[msg.task_id] = owner_id
                task_org_map[msg.task_id] = org_id
                inflight.add(msg.task_id)
                active_thread_count += 1

            # Refresh control token
            if _control_token:
                nonlocal latest_control_token
                latest_control_token = _control_token

            try:
                # Tier 2: per-task PubNub client for ALL tasks
                task_keys = env_keysets.get(task_env, env_keysets[primary_env])
                per_task_pn = create_pubnub_client(
                    user_id=instance_id,
                    publish_key=task_keys.publish_key,
                    subscribe_key=task_keys.subscribe_key,
                    subscribe_retry_unbounded=False,
                )
                per_task_pubnub_clients[msg.task_id] = per_task_pn
                if _write_token:
                    per_task_pn.set_token(_write_token)

                # Cache credentials for post-handler operations (BLOCKS-232: moved here
                # so tokens never need to be on the StartTaskMessage passed to handlers)
                credential_cache.set(
                    msg.task_id,
                    owner_id=owner_id,
                    write_token=_write_token or "",
                    agent_name=agent_name or msg.agent_name,
                    environment=task_env,
                    org_id=org_id,
                )

                cancel_evt = threading.Event()
                with lock:
                    task_cancel_events[msg.task_id] = cancel_evt

                update_presence_state()

                def _run_task(
                    task_msg: StartTaskMessage = msg,
                    task_pn: Any = per_task_pn,
                    _cancel_evt: threading.Event = cancel_evt,
                    _task_env: str = task_env,
                ) -> None:
                    nonlocal active_thread_count

                    # Start local duration timer for pipe tasks.
                    # Uses server-computed durationExpiresAtMs for clock alignment.
                    _duration_timer: Optional[threading.Timer] = None
                    _task_kind = task_msg.task_kind or "request"
                    if _task_kind == "pipe" and task_msg.duration_expires_at_ms is not None:
                        _delay_s = max(0.0, (task_msg.duration_expires_at_ms - time.time() * 1000)) / 1000
                        if _delay_s > 0 and _cancel_evt is not None:
                            def _local_expire() -> None:
                                expired_tasks.add(task_msg.task_id)
                                _cancel_evt.set()
                            _duration_timer = threading.Timer(_delay_s, _local_expire)
                            _duration_timer.daemon = True
                            _duration_timer.start()

                    try:
                        if on_start is default_on_start:
                            on_start(task_msg, control_client, task_pn, _cancel_evt, _task_env)
                        else:
                            on_start(task_msg, control_client)
                    except Exception as exc:
                        import traceback
                        traceback.print_exc()
                        err_msg = str(exc) or f"Agent instance error ({type(exc).__name__})"
                        org_id_err = task_org_map.get(task_msg.task_id, "anonymous")
                        err_pn = per_task_pubnub_clients.get(task_msg.task_id) or control_client
                        # Release any streams this task acquired before
                        # we publish the failed terminal. Belt-and-
                        # suspenders with the rollback inside
                        # create_stream itself: if a create_stream call
                        # succeeded and was later followed by an
                        # unrelated handler failure, the stream's
                        # registry ref would otherwise leak until the
                        # next agent restart (or brick a shared channel
                        # per PR#515 review finding).
                        try:
                            _release_all_streams_for_task(task_msg.task_id)
                        except Exception:
                            pass  # best-effort cleanup
                        _publish_task_event(err_pn, task_msg.task_id, org_id_err, agent_name, {
                            "type": "terminal",
                            "taskId": task_msg.task_id,
                            "state": "failed",
                            "error": err_msg,
                        })
                        if options.on_error:
                            try:
                                options.on_error(task_msg.task_id, exc)
                            except Exception:
                                pass
                        log_agent_instance_event(
                            "error", err_msg, taskId=task_msg.task_id,
                        )
                    finally:
                        if _duration_timer is not None:
                            _duration_timer.cancel()
                            _duration_timer = None
                        with lock:
                            inflight.discard(task_msg.task_id)
                            task_owner_map.pop(task_msg.task_id, None)
                            task_org_map.pop(task_msg.task_id, None)
                            task_last_status_time.pop(task_msg.task_id, None)
                            _flush_timer = task_status_timers.pop(task_msg.task_id, None)
                            if _flush_timer is not None:
                                _flush_timer.cancel()
                            task_status_buffer.pop(task_msg.task_id, None)
                            task_cancel_events.pop(task_msg.task_id, None)
                            expired_tasks.discard(task_msg.task_id)
                            terminated_tasks.discard(task_msg.task_id)
                            task_stream_counters.pop(task_msg.task_id, None)
                            active_thread_count -= 1

                            _per_task_pn = per_task_pubnub_clients.pop(task_msg.task_id, None)
                            if _per_task_pn is not None:
                                try:
                                    _per_task_pn.stop()
                                except Exception:
                                    pass

                            if active_thread_count == 0 and latest_control_token:
                                control_client.set_token(latest_control_token)

                        update_presence_state()

                executor.submit(_run_task)

            except Exception as exc:
                credential_cache.remove(msg.task_id)
                with lock:
                    active_thread_count -= 1
                    inflight.discard(msg.task_id)
                    task_owner_map.pop(msg.task_id, None)
                    task_org_map.pop(msg.task_id, None)
                    task_cancel_events.pop(msg.task_id, None)
                    expired_tasks.discard(msg.task_id)
                    terminated_tasks.discard(msg.task_id)
                log_agent_instance_event(
                    "error",
                    f"Failed to start task {msg.task_id}: {exc}",
                )
                _rollback_pn = per_task_pubnub_clients.get(msg.task_id) or control_client
                _publish_task_event(_rollback_pn, msg.task_id, org_id, agent_name, {
                    "type": "terminal",
                    "taskId": msg.task_id,
                    "state": "failed",
                    "error": str(exc),
                })
                _pn = per_task_pubnub_clients.pop(msg.task_id, None)
                if _pn:
                    try:
                        _pn.stop()
                    except Exception:
                        pass
                update_presence_state()

        elif isinstance(msg, CancelTaskMessage):
            not_in_flight = False
            with lock:
                not_in_flight = msg.task_id not in inflight
            if not_in_flight:
                # Not in flight (external stream outlived handler).
                # Use publish_terminal — it reads credential_cache for
                # ownerId/writeToken, creates an ephemeral PubNub, publishes.
                if credential_cache.get(msg.task_id) is not None:
                    publish_terminal(msg.task_id, {
                        "state": "canceled",
                    })
                # No creds: server safety net handles it
                return
            on_cancel(msg.task_id, control_client)

        elif isinstance(msg, ExpireTaskMessage):
            with lock:
                evt = task_cancel_events.get(msg.task_id)
                if evt is None:
                    pass  # not in flight
                else:
                    expired_tasks.add(msg.task_id)
            if evt:
                evt.set()
                log_agent_instance_event(
                    "info",
                    f"Task {msg.task_id} expired (duration_expired)",
                    taskId=msg.task_id,
                    instanceId=instance_id,
                )
            else:
                # Not in flight (external stream outlived handler).
                if credential_cache.get(msg.task_id) is not None:
                    publish_terminal(msg.task_id, {
                        "state": "completed",
                        "completionReason": "duration_expired",
                    })
                # No creds: server safety net (Phase 4) handles it

        elif isinstance(msg, ControlMessage):
            if msg.type == "PauseTask":
                with lock:
                    if msg.task_id not in inflight:
                        return
                org_id = task_org_map.get(msg.task_id, "anonymous")
                event_pn = per_task_pubnub_clients.get(msg.task_id) or control_client
                _publish_task_event(event_pn, msg.task_id, org_id, agent_name, {
                    "type": "system",
                    "taskId": msg.task_id,
                    "status": "paused",
                })
                log_agent_instance_event(
                    "info", f"Paused task {msg.task_id}", taskId=msg.task_id,
                )

            elif msg.type == "ResumeTask":
                with lock:
                    if msg.task_id not in inflight:
                        return
                org_id = task_org_map.get(msg.task_id, "anonymous")
                event_pn = per_task_pubnub_clients.get(msg.task_id) or control_client
                _publish_task_event(event_pn, msg.task_id, org_id, agent_name, {
                    "type": "system",
                    "taskId": msg.task_id,
                    "status": "resumed",
                })
                log_agent_instance_event(
                    "info", f"Resumed task {msg.task_id}", taskId=msg.task_id,
                )

            elif msg.type == "TerminateTask":
                with lock:
                    evt = task_cancel_events.get(msg.task_id)
                    if evt is None:
                        # Not in flight (external stream outlived handler).
                        if credential_cache.get(msg.task_id) is not None:
                            publish_terminal(msg.task_id, {
                                "state": "canceled",
                                "reason": "terminated",
                            })
                        # No creds: server safety net handles it
                        return
                    terminated_tasks.add(msg.task_id)
                evt.set()

            elif msg.type == "RetryTask":
                log_agent_instance_event(
                    "info",
                    "RetryTask received (no-op in demo)",
                    taskId=msg.task_id,
                )

    # -- PubNub listener ----------------------------------------------------

    listener: Any = None
    try:
        from pubnub.callbacks import SubscribeCallback

        _access_denied_handled = False

        class _AgentListener(SubscribeCallback):
            def message(self, pubnub_instance: Any, event: Any) -> None:
                msg = event.message
                if not isinstance(msg, dict):
                    return
                meta = getattr(event, "user_metadata", None)
                if not isinstance(meta, dict):
                    meta = None
                try:
                    handle_control_message(msg, meta)
                except Exception as exc:
                    log_agent_instance_event(
                        "error",
                        f"Unhandled error in message handler: {exc}",
                    )

            def presence(self, pubnub_instance: Any, event: Any) -> None:
                pass

            def status(self, pubnub_instance: Any, event: Any) -> None:
                nonlocal _access_denied_handled
                try:
                    from pubnub.enums import PNStatusCategory
                    if (
                        getattr(event, "category", None) == PNStatusCategory.PNAccessDeniedCategory
                        and not _access_denied_handled
                    ):
                        _access_denied_handled = True
                        log_agent_instance_event(
                            "error",
                            f"PAM token expired or revoked — agent {instance_id} is no longer receiving tasks. "
                            "Re-register the agent to resume.",
                        )
                        try:
                            control_client.stop()
                        except Exception:
                            pass
                except ImportError:
                    pass
                except Exception as exc:
                    log_agent_instance_event(
                        "warn",
                        f"Unexpected error in status handler: {exc}",
                    )

        listener = _AgentListener()
    except ImportError:
        def _dict_message_handler(event: Any) -> None:
            msg = event.message if hasattr(event, "message") else event.get("message", {})
            if not isinstance(msg, dict):
                return
            meta = getattr(event, "user_metadata", None)
            if not isinstance(meta, dict):
                meta = None
            handle_control_message(msg, meta)

        listener = {"message": _dict_message_handler}

    control_client.add_listener(listener)

    # -- Set subscribe filter -----------------------------------------------
    filter_expression = (
        f"meta.instance == '{instance_id}' || meta.broadcast == \"true\""
    )
    try:
        if hasattr(control_client, "set_filter_expression"):
            control_client.set_filter_expression(filter_expression)
        elif hasattr(control_client, "_config") and control_client._config is not None:
            control_client._config.filter_expression = filter_expression
        elif hasattr(control_client, "config") and control_client.config is not None:
            control_client.config.filter_expression = filter_expression
    except Exception:
        log_agent_instance_event(
            "warn",
            "Could not set filter expression on PubNub config",
        )

    # -- Register then subscribe --------------------------------------------
    def _connect_then_subscribe() -> None:
        nonlocal control_channel
        try:
            from .agent_registry import connect_agent as _connect_fn
            from .agent_registry import ConnectAgentOptions, AgentScaling

            scaling = AgentScaling(
                expected_instances=expected_instances,
                concurrency=concurrency,
                max_pending_backlog=max_pending_backlog,
                max_running_time_sec=max_running_time_sec,
            )

            connect_options = ConnectAgentOptions(
                instance_id=instance_id,
                description=options.description,
                skills=options.skills,
                scaling=scaling,
                card=options.card,
                card_ref=options.card_ref,
                card_summary=options.card_summary,
                listing=registry_listing or options.listing,
                # Authoritatively sourced from the boot-time registry GET
                # above (registry_billing_mode); no provider override.
                # When CDM config is absent (external pubnub mode in tests)
                # there's no registry GET, so fall through with no value
                # and the connect call is also stubbed.
                billing_mode=registry_billing_mode,
                actor=f"agent-instance:{instance_id}",
                base_url=options.base_url or (cdm_config.api.base_url if cdm_config else None),
                agent_auth=agent_auth,
            )
            result = _connect_fn(agent_name, connect_options)
            if not result.control_channel:
                raise RuntimeError("Connect response missing controlChannel — server may be outdated")
            control_channel = result.control_channel
            if agent_auth is not None and agent_auth.get_access_token():
                log_agent_instance_event(
                    "info",
                    "AgentAuth initialized via registration — API key authentication active",
                )
            log_agent_instance_event(
                "info",
                f"Registered agent: {agent_name} (instance: {instance_id})",
            )
            if result.pam_token:
                control_client.set_token(result.pam_token)
                log_agent_instance_event(
                    "info",
                    "PAM token applied for control channel",
                )
        except ImportError:
            log_agent_instance_event(
                "warn",
                "agent_registry module not available; skipping registration",
            )
        except Exception as exc:
            log_agent_instance_event(
                "warn",
                f"Failed to register agent: {agent_name}: {exc}",
            )
        finally:
            if control_channel:
                control_client.subscribe().channels([control_channel]).execute()
            update_presence_state()

    reg_thread = threading.Thread(target=_connect_then_subscribe, daemon=True)
    reg_thread.start()

    # -- Log startup --------------------------------------------------------
    log_agent_instance_event(
        "info",
        f"Agent instance {instance_id} started (agent name: {agent_name})",
        agentName=agent_name,
        event="agent_instance_started",
        instanceId=instance_id,
    )

    # -- Stop function ------------------------------------------------------

    def stop() -> None:
        """Shut down the agent instance."""
        try:
            if consumer_auth:
                consumer_auth.destroy()
            task_client.destroy()
            for _pn in list(per_task_pubnub_clients.values()):
                try:
                    _pn.stop()
                except Exception:
                    pass
            per_task_pubnub_clients.clear()
            for sid in stream_registry.stream_ids():
                entry = stream_registry.get(sid)
                if entry and entry.stream_client:
                    try:
                        entry.stream_client.end()
                    except Exception:
                        pass
            stream_registry.clear()
            with shared_stream_handles_lock:
                shared_stream_handles.clear()
            credential_cache.clear()
            for _timer in list(task_status_timers.values()):
                try:
                    _timer.cancel()
                except Exception:
                    pass
            task_status_timers.clear()
            task_status_buffer.clear()
            control_client.remove_listener(listener)
            if control_channel:
                control_client.unsubscribe().channels([control_channel]).execute()
        except Exception:
            pass
        try:
            executor.shutdown(wait=False)
        except Exception:
            pass
        try:
            control_client.stop()
        except Exception:
            pass

    class _Handle:
        """Thin wrapper providing live references that track SwitchEnvironment."""

        def __init__(self):
            self.stop = stop
            self.agent_name = agent_name
            self.instance_id = instance_id
            self.publish_terminal = publish_terminal
            self.fail_stream = fail_stream
            self.cdm_config = cdm_config

        @property
        def pubnub(self):
            return control_client

        @property
        def task_client(self):
            return task_client

        def __getitem__(self, key):
            return getattr(self, key)

        def __contains__(self, key):
            return hasattr(self, key)

    return _Handle()
