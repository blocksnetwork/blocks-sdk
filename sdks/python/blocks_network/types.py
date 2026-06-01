"""
Message dataclasses for Blocks Network control and task events.

Wire format uses camelCase (matching PubNub JSON conventions).
Python API uses snake_case.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any, Callable, Dict, List, Optional, Set, Tuple, Union

if TYPE_CHECKING:
    from .stream_context import StreamObject
    from .task_client import TaskClient


# ============================================================================
# Request part
# ============================================================================


@dataclass
class RequestPart:
    """A single part of a task request.

    Wire format fields (camelCase) are mapped to snake_case.  The ``extra``
    dict captures any additional properties the wire may carry, allowing
    forward-compatible evolution without breaking existing handlers.
    """

    part_id: Optional[str] = None
    text: Optional[str] = None
    content_type: Optional[str] = None
    artifact_ref: Optional["ArtifactRef"] = None
    extra: Dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> Dict[str, Any]:
        """Serialize to camelCase dict for PubNub wire format."""
        d: Dict[str, Any] = {}
        if self.part_id is not None:
            d["partId"] = self.part_id
        if self.text is not None:
            d["text"] = self.text
        if self.content_type is not None:
            d["contentType"] = self.content_type
        if self.artifact_ref is not None:
            d["artifactRef"] = self.artifact_ref.to_dict()
        d.update(self.extra)
        return d

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "RequestPart":
        """Deserialize from camelCase dict.

        Known fields are extracted into typed attributes; everything else
        is collected in ``extra``.
        """
        known_keys = {"partId", "text", "contentType", "artifactRef"}
        extra = {k: v for k, v in d.items() if k not in known_keys}
        artifact_ref_raw = d.get("artifactRef")
        artifact_ref = (
            ArtifactRef.from_dict(artifact_ref_raw)
            if isinstance(artifact_ref_raw, dict) else None
        )
        return cls(
            part_id=d.get("partId"),
            text=d.get("text"),
            content_type=d.get("contentType"),
            artifact_ref=artifact_ref,
            extra=extra,
        )


# ============================================================================
# Control messages (received on agent.{agentId}.control)
# ============================================================================


@dataclass
class StartTaskMessage:
    """Initiates a new task for processing."""

    type: str = "StartTask"
    task_id: str = ""
    agent_name: str = ""
    owner_id: str = ""
    request_parts: List[RequestPart] = field(default_factory=list)
    caller_claims: Dict[str, Any] = field(default_factory=dict)
    request_summary: Dict[str, Any] = field(default_factory=dict)
    task_kind: Optional[str] = None  # "request" | "pipe"; default "request"
    duration: Optional[float] = None  # Task duration in minutes (pipe tasks only)
    duration_expires_at_ms: Optional[int] = None  # epoch ms — server-computed pipe-task deadline
    consumer_public_key: Optional[str] = None  # Consumer's public key for E2E encryption
    has_stream: bool = False
    org_id: Optional[str] = None  # Org ID for org-scoped task channels
    protocol_version: Optional[str] = None  # Wire protocol version pinned at task creation

    def to_dict(self) -> Dict[str, Any]:
        """Serialize to camelCase dict for PubNub wire format."""
        d: Dict[str, Any] = {
            "type": self.type,
            "taskId": self.task_id,
        }
        if self.protocol_version is not None:
            d["protocolVersion"] = self.protocol_version
        if self.agent_name:
            d["agentName"] = self.agent_name
        if self.owner_id:
            d["ownerId"] = self.owner_id
        if self.request_parts:
            d["requestParts"] = [p.to_dict() for p in self.request_parts]
        if self.caller_claims:
            d["callerClaims"] = self.caller_claims
        if self.request_summary:
            d["requestSummary"] = self.request_summary
        if self.task_kind is not None:
            d["taskKind"] = self.task_kind
        if self.duration is not None:
            d["duration"] = self.duration
        if self.duration_expires_at_ms is not None:
            d["durationExpiresAtMs"] = self.duration_expires_at_ms
        if self.consumer_public_key is not None:
            d["consumerPublicKey"] = self.consumer_public_key
        if self.has_stream:
            d["hasStream"] = self.has_stream
        if self.org_id is not None:
            d["orgId"] = self.org_id
        return d

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "StartTaskMessage":
        """Deserialize from camelCase dict."""
        return cls(
            type=d.get("type", "StartTask"),
            task_id=d.get("taskId", ""),
            agent_name=d.get("agentName", ""),
            owner_id=d.get("ownerId", ""),
            request_parts=[
                RequestPart.from_dict(p) if isinstance(p, dict) else p
                for p in d.get("requestParts", [])
            ],
            caller_claims=d.get("callerClaims", {}),
            request_summary=d.get("requestSummary", {}),
            task_kind=d.get("taskKind"),
            duration=d.get("duration"),
            duration_expires_at_ms=d.get("durationExpiresAtMs"),
            consumer_public_key=d.get("consumerPublicKey"),
            has_stream=bool(d.get("hasStream", False)),
            org_id=d.get("orgId"),
            protocol_version=d.get("protocolVersion"),
        )


@dataclass
class CancelTaskMessage:
    """Request cancellation of a running task."""

    type: str = "CancelTask"
    task_id: str = ""
    reason: str = ""
    caller: str = ""
    protocol_version: Optional[str] = None

    def to_dict(self) -> Dict[str, Any]:
        d: Dict[str, Any] = {
            "type": self.type,
            "taskId": self.task_id,
        }
        if self.protocol_version is not None:
            d["protocolVersion"] = self.protocol_version
        if self.reason:
            d["reason"] = self.reason
        if self.caller:
            d["caller"] = self.caller
        return d

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "CancelTaskMessage":
        return cls(
            type=d.get("type", "CancelTask"),
            task_id=d.get("taskId", ""),
            reason=d.get("reason", ""),
            caller=d.get("caller", ""),
            protocol_version=d.get("protocolVersion"),
        )


@dataclass
class ExpireTaskMessage:
    """Duration-expired signal for a running task.

    Sent by the taskRetry scanner when a pipe task exceeds its
    ``maxRunningTimeSec``.  The SDK signals cooperative cancellation
    (same mechanism as CancelTask) and then publishes
    ``terminal: completed`` with ``completionReason: "duration_expired"``.
    """

    type: str = "ExpireTask"
    task_id: str = ""
    agent_name: str = ""
    reason: str = ""
    protocol_version: Optional[str] = None

    def to_dict(self) -> Dict[str, Any]:
        d: Dict[str, Any] = {
            "type": self.type,
            "taskId": self.task_id,
        }
        if self.protocol_version is not None:
            d["protocolVersion"] = self.protocol_version
        if self.agent_name:
            d["agentName"] = self.agent_name
        if self.reason:
            d["reason"] = self.reason
        return d

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "ExpireTaskMessage":
        return cls(
            type=d.get("type", "ExpireTask"),
            task_id=d.get("taskId", ""),
            agent_name=d.get("agentName", ""),
            reason=d.get("reason", ""),
            protocol_version=d.get("protocolVersion"),
        )


@dataclass
class ControlMessage:
    """Generic control message (Pause, Resume, Retry, Terminate)."""

    type: str = ""  # PauseTask | ResumeTask | RetryTask | TerminateTask
    task_id: str = ""
    caller: str = ""
    protocol_version: Optional[str] = None

    def to_dict(self) -> Dict[str, Any]:
        d: Dict[str, Any] = {
            "type": self.type,
            "taskId": self.task_id,
        }
        if self.protocol_version is not None:
            d["protocolVersion"] = self.protocol_version
        if self.caller:
            d["caller"] = self.caller
        return d

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "ControlMessage":
        return cls(
            type=d.get("type", ""),
            task_id=d.get("taskId", ""),
            caller=d.get("caller", ""),
            protocol_version=d.get("protocolVersion"),
        )


# Union of all control message types
AnyControlMessage = Union[StartTaskMessage, CancelTaskMessage, ExpireTaskMessage, ControlMessage]


def extract_start_task_tokens(msg: Dict[str, Any]) -> Tuple[Optional[str], Optional[str]]:
    """Extract PAM tokens from a raw StartTask dict before parsing.

    Returns (write_token, control_token). The dict is not mutated.
    """
    return msg.get("writeToken"), msg.get("controlToken")


def parse_control_message(msg: Dict[str, Any]) -> Optional[AnyControlMessage]:
    """Parse a raw dict into the appropriate control message dataclass.

    Returns ``None`` if the message has no recognizable ``type`` field.
    """
    msg_type = msg.get("type")
    if msg_type == "StartTask":
        return StartTaskMessage.from_dict(msg)
    if msg_type == "CancelTask":
        return CancelTaskMessage.from_dict(msg)
    if msg_type == "ExpireTask":
        return ExpireTaskMessage.from_dict(msg)
    if msg_type in ("PauseTask", "ResumeTask", "RetryTask", "TerminateTask"):
        return ControlMessage.from_dict(msg)
    return None


# ============================================================================
# Artifact reference
# ============================================================================


@dataclass
class ArtifactRef:
    """Reference to a task output artifact (inline data or file pointer).

    **v5.0.0 changes:** ``file_url`` removed (static URLs fail under PAM).
    ``channel`` added to the file variant (required for ``pubnub.downloadFile()``).
    ``file_name`` now valid on both inline and file variants.
    """

    kind: str = "inline"  # 'inline' | 'file'
    mime_type: str = "application/octet-stream"
    size: int = 0
    hash: Optional[str] = None
    # Inline fields
    data: Optional[str] = None  # base64-encoded
    # Shared (both inline and file)
    file_name: Optional[str] = None
    # File fields
    channel: Optional[str] = None  # PubNub channel where the file is stored
    file_id: Optional[str] = None
    expires_at: Optional[str] = None

    def to_dict(self) -> Dict[str, Any]:
        """Serialize to camelCase dict for PubNub wire format."""
        d: Dict[str, Any] = {
            "kind": self.kind,
            "mimeType": self.mime_type,
            "size": self.size,
        }
        if self.hash is not None:
            d["hash"] = self.hash
        if self.data is not None:
            d["data"] = self.data
        if self.file_name is not None:
            d["fileName"] = self.file_name
        if self.channel is not None:
            d["channel"] = self.channel
        if self.file_id is not None:
            d["fileId"] = self.file_id
        if self.expires_at is not None:
            d["expiresAt"] = self.expires_at
        return d

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "ArtifactRef":
        return cls(
            kind=d.get("kind", "inline"),
            mime_type=d.get("mimeType", "application/octet-stream"),
            size=d.get("size", 0),
            hash=d.get("hash"),
            data=d.get("data"),
            file_name=d.get("fileName"),
            channel=d.get("channel"),
            file_id=d.get("fileId"),
            expires_at=d.get("expiresAt"),
        )


# ============================================================================
# Presence state
# ============================================================================


@dataclass
class AgentInstancePresenceState:
    """Presence state for agent instances (load tracking)."""

    instance_id: str = ""
    active_tasks: int = 0
    concurrency: int = 1
    started_at: int = 0  # Unix timestamp ms
    active_streams: int = 0  # Number of active outbound streams
    preferred_protocol_version: str = ""
    protocol_versions: List[str] = field(default_factory=list)

    def to_dict(self) -> Dict[str, Any]:
        return {
            "instanceId": self.instance_id,
            "activeTasks": self.active_tasks,
            "concurrency": self.concurrency,
            "startedAt": self.started_at,
            "activeStreams": self.active_streams,
            "preferredProtocolVersion": self.preferred_protocol_version,
            "protocolVersions": self.protocol_versions,
        }


# ============================================================================
# Task context (passed to handler)
# ============================================================================


def _no_stream(*args: Any, **kwargs: Any) -> None:
    """Default ``create_stream`` stub that raises when streaming is unavailable."""
    raise RuntimeError(
        "create_stream() is not available in this context. "
        "Ensure the handler is dispatched through the agent instance runtime."
    )


def _no_download(*args: Any, **kwargs: Any) -> bytes:
    """Default ``download_input_artifact`` stub that raises."""
    raise RuntimeError(
        "download_input_artifact() is not available in this context. "
        "Ensure the handler is dispatched through the agent instance runtime."
    )


def _no_publish_artifact(*args: Any, **kwargs: Any) -> None:
    """Default ``publish_artifact`` stub that raises."""
    raise RuntimeError(
        "publish_artifact() is not available in this context. "
        "Ensure the handler is dispatched through the agent instance runtime."
    )


@dataclass
class TaskContext:
    """Context object passed to handlers for reporting status during execution.

    The ``cancel_event`` is set by the SDK when a ``CancelTask`` or
    ``ExpireTask`` message arrives.  Handlers should check
    :meth:`is_cancelled` at natural checkpoints (e.g. between iterations
    of a processing loop) and exit cooperatively when it returns ``True``.

    Use :attr:`is_expired` to distinguish ``ExpireTask`` from ``CancelTask``
    when the handler detects cancellation.

    ``create_stream()`` is the unified stream creation API. It accepts
    keyword options only: ``direction``, ``on_activate``, ``metadata``,
    ``external``, ``format``, ``bundle_size_bytes``, ``max_latency_ms``,
    ``declared_stream``, ``subscribe_grace_ms``. The SDK derives the
    channel from ``declared_stream`` plus the card's affinity; handler
    code cannot specify the channel suffix directly.

    ``download_input_artifact(part)`` downloads the artifact from a request
    part's ``artifactRef``. Works for both inline (base64 decode) and file
    (PubNub Files download via PAM token) variants.

    ``publish_artifact(data, ...)`` publishes a mid-execution artifact.
    Small artifacts are inlined; large artifacts use the pre-signed URL flow.
    """

    task_id: str = ""
    has_stream: bool = False
    consumer_public_key: Optional[str] = None
    report_status: Callable[[str], None] = field(default_factory=lambda: (lambda _msg: None))
    create_stream: Callable[..., "StreamObject"] = field(default_factory=lambda: _no_stream)
    download_input_artifact: Callable[..., bytes] = field(default_factory=lambda: _no_download)
    publish_artifact: Callable[..., None] = field(default_factory=lambda: _no_publish_artifact)
    cancel_event: Any = field(default_factory=lambda: __import__("threading").Event())
    task_client: Optional["TaskClient"] = None
    _expired_tasks: Optional[Set[str]] = field(default=None, repr=False)

    @property
    def is_cancelled(self) -> bool:
        """Return ``True`` if cancellation has been requested for this task."""
        return self.cancel_event.is_set()

    @property
    def is_expired(self) -> bool:
        """Return ``True`` if the task's duration has expired (ExpireTask received)."""
        if self._expired_tasks is not None:
            return self.task_id in self._expired_tasks
        return False


# ============================================================================
# Agent instance options
# ============================================================================


@dataclass
class AgentInstanceOptions:
    """Options for :func:`start_agent_instance`.

    Mirrors the Node ``AgentInstanceOptions`` interface. ``card`` is
    required -- an agent cannot start without a registered agent card
    because the card drives stream affinity, declared-stream lookup,
    and ``runtime.maxRunningTimeSec``. Node enforces this at the type
    level (``card: AgentCard`` in ``agent-instance.ts:108``); Python
    mirrors it here and validates at ``start_agent_instance`` entry.
    """

    # Agent card (App Context model) -- required for all provider agents.
    # Placed first so it has no default; Python 3.9 dataclasses forbid
    # required fields after fields with defaults.
    card: Dict[str, Any]

    pubnub: Any = None
    token: Optional[str] = None
    user_id: Optional[str] = None
    agent_name: Optional[str] = None
    description: Optional[str] = None
    # AgentTag objects ({id, name, description?, examples?}), matching Node
    # AgentInstanceOptions.tags: AgentTag[]. (The pre-rename `skills` field was
    # List[str] here, which diverged from Node; the rename aligns the shape.)
    tags: Optional[List[Dict[str, Any]]] = None

    # Callbacks
    on_start_task: Optional[Callable] = None
    on_cancel_task: Optional[Callable] = None
    handler: Optional[Callable] = None
    on_error: Optional[Callable] = None

    artifact_base_path: Optional[str] = None
    log_channel: Optional[str] = None

    # Internal only — used by tests. Always auto-generated at runtime.
    instance_id: Optional[str] = None
    concurrency: Optional[int] = None
    expected_instances: Optional[int] = None

    # Scaling options for task lifecycle management
    max_pending_backlog: Optional[int] = None
    max_running_time_sec: Optional[int] = None

    card_ref: Optional[str] = None
    card_summary: Optional[str] = None
    listing: Optional[str] = None

    # Base URL override for local development
    base_url: Optional[str] = None
    # CDM URL override
    cdm_url: Optional[str] = None
