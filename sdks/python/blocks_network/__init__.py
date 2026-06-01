"""
Blocks Network Python SDK -- Agent Instance Runtime.

Public API exports:
    start_agent_instance    - Start an agent instance runtime.
    StartTaskMessage        - Dataclass for StartTask control messages.
    TaskContext             - Context passed to task handlers.
    build_artifact_ref      - Build an ArtifactRef dict.
    should_inline_artifact  - Check whether an artifact should be inlined.
    download_artifact       - Download an artifact from an ArtifactRef.
    decode_inline_artifact  - Decode an inline artifact's base64 data.
    DownloadedArtifact      - Result of downloading an artifact.
    create_pubnub_client    - Factory for configured PubNub instances.
    ChannelManager          - Per-agent-type channel name builder.
    create_channel_manager  - Factory for ChannelManager instances.
    TaskClient              - Client for sending tasks to other agents.
    create_task_client      - Convenience factory for TaskClient with dotenv support.
    TaskSubscription        - Subscription handle for task events.
    SendMessageParams       - Parameters for TaskClient.send_message().
    TaskSession             - Consumer task session (returned by send_message).
    TaskEventCallbacks      - Callbacks for task event dispatch.
    TaskInfo                - Information about a task.
    ListTasksParams         - Parameters for TaskClient.list_tasks().
    ListTasksResult         - Result from TaskClient.list_tasks().
    subscribe_to_task       - Standalone subscribe to real-time task events.
    task_channel            - Standalone task channel name builder.
    StreamRegistry          - Shared stream registry with ref-counting.
    CredentialCache         - Task credential cache.
    StreamObject            - Stream object returned by create_stream().
    ExternalStreamObject    - External stream object.
    OnActivateCallback      - on_activate callback type.
    InboundMessage          - Inbound message yielded by stream.inbound.
    StreamError             - Error payload fired to stream.on_error callbacks.
    StreamRef               - Consumer-side stream reference.
    StreamUnavailableError  - Raised when StreamRef.open() is called on a terminal session.
    TaskSession             - Consumer-side task session.
    TaskEvent               - Parsed task event.
    CallbackErrorContext    - Context for callback error routing.
    ArtifactRef             - Reference to a task output artifact.
    TokenEndpointConfig     - TypedDict for Mode 2 token-endpoint config object form.
    BLOCKS_MAX_UPLOAD_BYTES - Platform upload ceiling in bytes (mirrors backend MAX_FILE_SIZE_BYTES).
    BillingModeMismatchError - Raised when the backend rejects SendMessage with a BillingModeMismatch.
    RpcError                - Base structured error for JSON-RPC error responses.
    AnonTaskAccessDenied    - Raised when the anon-task-read-token endpoint returns 403.
    get_agent               - Look up an agent registry entry by name.
    AgentEntry              - Agent registry entry (carries billing_mode, listing, card, etc.).
"""

from .agent_auth import AgentAuth, AgentAuthFatalError
from .agent_instance import start_agent_instance
from .agent_registry import AgentEntry, get_agent
from .auth_provider import AuthProvider
from .config import BLOCKS_MAX_UPLOAD_BYTES
from .consumer_auth import (
    AuthRefreshFailedError,
    ConsumerAuth,
    TokenEndpointConfig,
    TokenResult,
)
from .artifacts import (
    build_artifact_ref,
    decode_inline_artifact,
    download_artifact,
    DownloadedArtifact,
    should_inline_artifact,
)
from .cdm_config import fetch_cdm_config, CdmConfig, CdmKeyset, CdmApiConfig, DEFAULT_CDM_URL
from .channel_manager import ChannelManager, create_channel_manager, task_channel
from .credential_cache import CredentialCache
from .pubnub_client import create_pubnub_client
from .rpc_client import BillingModeMismatchError, RpcError
from .stream.stream_client import StreamError
from .stream.types import InboundMessage
from .stream_context import ExternalStreamObject, OnActivateCallback, StreamObject
from .stream_ref import StreamRef, StreamUnavailableError
from .stream_registry import StreamRegistry
from .file_upload import FileUploadError, presigned_upload_flow
from .part_helpers import file_part, text_part
from .task_client import (
    AnonTaskAccessDenied,
    ListTasksParams,
    ListTasksResult,
    SendMessageParams,
    SendMessageRequestPart,
    TaskClient,
    TaskEventCallbacks,
    TaskInfo,
    TaskSubscription,
    create_task_client,
    subscribe_to_task,
)
from .task_session import CallbackErrorContext, TaskEvent, TaskSession
from .types import ArtifactRef, ExpireTaskMessage, RequestPart, StartTaskMessage, TaskContext

__all__ = [
    "start_agent_instance",
    "ExpireTaskMessage",
    "RequestPart",
    "StartTaskMessage",
    "TaskContext",
    "build_artifact_ref",
    "decode_inline_artifact",
    "download_artifact",
    "DownloadedArtifact",
    "should_inline_artifact",
    "create_pubnub_client",
    "ChannelManager",
    "create_channel_manager",
    "TaskClient",
    "create_task_client",
    "TaskSubscription",
    "SendMessageParams",
    "SendMessageRequestPart",
    "FileUploadError",
    "presigned_upload_flow",
    "TaskEventCallbacks",
    "TaskInfo",
    "ListTasksParams",
    "ListTasksResult",
    "subscribe_to_task",
    "task_channel",
    "StreamRegistry",
    "CredentialCache",
    "StreamObject",
    "ExternalStreamObject",
    "OnActivateCallback",
    "InboundMessage",
    "StreamError",
    "StreamRef",
    "StreamUnavailableError",
    "TaskSession",
    "TaskEvent",
    "CallbackErrorContext",
    "ArtifactRef",
    "AgentAuth",
    "AgentAuthFatalError",
    "AuthRefreshFailedError",
    "AuthProvider",
    "ConsumerAuth",
    "TokenEndpointConfig",
    "TokenResult",
    "fetch_cdm_config",
    "CdmConfig",
    "CdmKeyset",
    "CdmApiConfig",
    "DEFAULT_CDM_URL",
    "text_part",
    "file_part",
    "BLOCKS_MAX_UPLOAD_BYTES",
    "BillingModeMismatchError",
    "RpcError",
    "AnonTaskAccessDenied",
    "get_agent",
    "AgentEntry",
]
