export { startAgentInstance } from './runtime/agent-instance.js';

export type {
  ArtifactEntry,
  HandlerFn,
  HandlerResult,
  RequestPart,
  StartTaskMessage,
  ExpireTaskMessage,
  TaskContext,
  CreateStreamOptions,
  AgentInstanceOptions,
  AgentInstancePresenceState,
  AgentInstanceHandle,
} from './runtime/agent-instance.js';

export type { StreamObject, OnActivateCallback } from './runtime/stream-context.js';
export type { InboundMessage, StreamError } from './stream/index.js';

export type { AgentCard, OutputAgentCard, AgentTag } from './runtime/agent-registry.js';

// Runtime utilities (for advanced usage)
export { createPubNubClient, type PubNubClientConfig } from './runtime/pubnub-client.js';
export { createChannelManager, ChannelManager, streamChannel } from './runtime/channel-manager.js';
export {
  buildArtifactRef,
  shouldInlineArtifact,
  decodeInlineArtifact,
  downloadArtifact,
  type ArtifactRef,
  type DownloadedArtifact,
} from './runtime/artifacts.js';
export {
  connectAgent,
  fetchAgentRegistry,
  getAgent,
  removeAgent,
  fetchAgentsByTag,
  fetchAgentsByListing,
  type AgentEntry,
  type AgentRegistryResult,
} from './runtime/agent-registry.js';
export { DEFAULTS } from './defaults.js';
import { DEFAULTS } from './defaults.js';
/** Platform-wide upload ceiling (bytes). Mirrors the service's MAX_FILE_SIZE_BYTES. */
export const BLOCKS_MAX_UPLOAD_BYTES = DEFAULTS.maxUploadBytes;
export { loadBlocksConfig, type BlocksConfig } from './config-loader.js';

// Task client (agent-to-agent communication)
export { TaskClient, AnonTaskAccessDeniedError } from './runtime/task-client.js';
export type {
  TaskClientOptions,
  SendMessageParams,
  SendMessageRequestPart,
  TaskInfo,
  ListTasksParams,
  ListTasksResult,
  TaskEvent as TaskClientEvent,
  TaskEventCallbacks,
  TaskSubscription,
} from './runtime/task-client.js';

// RPC error types (typed errors surfaced from JSON-RPC error envelopes)
export { RpcError, BillingModeMismatchError } from './runtime/rpc-client.js';

// Consumer session API
export { TaskSession } from './runtime/task-session.js';
export type {
  TaskEvent,
  ProgressEvent,
  ArtifactEvent,
  TerminalEvent,
  CancelRequestedEvent,
  Unsubscribe,
  CallbackErrorContext,
} from './runtime/task-session.js';

// Part builder helpers
export { textPart, filePart, filePartFromPath } from './runtime/part-helpers.js';
export { StreamRef, StreamUnavailableError } from './runtime/stream-ref.js';
export type { StreamDescriptor } from './runtime/stream-ref.js';

// Agent auth (API key-based authentication)
export { AgentAuth, AgentAuthFatalError } from './runtime/agent-auth.js';
export type { RegistrationPayload, RegistrationResult } from './runtime/agent-auth.js';

// Auth provider (consumer/transport auth abstraction)
export type { AuthProvider } from './runtime/auth-provider.js';
export { ConsumerAuth, AuthRefreshFailedError } from './runtime/consumer-auth.js';
export type {
  ConsumerAuthOptions,
  TokenEndpointConfig,
  TokenEndpointCredentials,
  TokenResult,
} from './runtime/consumer-auth.js';

// Stream registry and credential cache (advanced runtime usage)
export { StreamRegistry } from './runtime/stream-registry.js';
export { CredentialCache } from './runtime/credential-cache.js';

// CDM config (dynamic key management)
export { fetchCdmConfig, DEFAULT_CDM_URL } from './runtime/cdm-config.js';
export type { CdmConfig, CdmKeyset, CdmApiConfig } from './runtime/cdm-config.js';
