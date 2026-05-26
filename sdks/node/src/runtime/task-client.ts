/**
 * TaskClient -- send tasks to other agents via the PubNub Functions RPC gateway.
 *
 * Provides:
 * - `sendMessage()` -- calls JSON-RPC "SendMessage" and returns a TaskSession with eager subscription
 * - `connect()` -- connect to an existing task and return a pre-populated TaskSession
 * - `create()` -- static factory to build a TaskClient from env vars or CDM config
 * - Task lifecycle methods: getTask, listTasks, cancelTask, pauseTask, resumeTask, retryTask, terminateTask
 * - `subscribeToTask()` -- low-level subscribe to real-time task events via PubNub channels
 */

import PubNub from 'pubnub';
import { buildPubNubLogConfig } from './pubnub-client.js';
import { callRpc, type RpcClientConfig } from './rpc-client.js';
import { taskChannel } from './channel-manager.js';
import { TaskSession, type CallbackErrorContext } from './task-session.js';
import type { TaskEvent as SessionTaskEvent } from './task-session.js';
import type { AgentAuth } from './agent-auth.js';
import type { AuthProvider } from './auth-provider.js';
import { ConsumerAuth, type TokenResult, type TokenEndpointConfig } from './consumer-auth.js';
import { shouldInlineArtifact, buildArtifactRef, type ArtifactRef } from './artifacts.js';
import { uploadFile, type FileUploadAuth, type ConsumerUploadParams } from './file-upload.js';
import { normalizeFileInput } from './file-input.js';
import { fetchCdmConfig } from './cdm-config.js';
import { captureAffinity, injectAffinity } from './write-affinity.js';
import { getEnv } from '../env.js';
import { log as baseLog } from './logger.js';
import { StreamRef } from './stream-ref.js';
import {
  invertDirection,
  type StreamDescriptor,
  type StreamClientFromDescriptorOptions,
} from '../stream/index.js';
import { asPubNubFetcher, type FetchedMessage } from './pubnub-types.js';
import { CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER } from './protocol-version.js';
import { getAgent, type AgentCard } from './agent-registry.js';

const log = (
  level: 'debug' | 'info' | 'warn' | 'error',
  message: string,
  meta?: Record<string, unknown>,
): void => baseLog('[TaskClient]', level, message, meta);

// ============================================================================
// Types
// ============================================================================

export interface TaskClientOptions {
  /**
   * Caller's billing mode. Required.
   *
   * Threaded into every SendMessage RPC params object so the backend can
   * compare against the target agent's persisted `billingMode` and reject
   * mismatches with `BillingModeMismatchError` carrying `expected`/`got`.
   *
   * `TaskClient.create({ billingMode })` populates this from its own
   * required option. Direct constructor callers must supply it explicitly.
   */
  billingMode: 'free' | 'paid';
  subscribeKey: string;
  publishKey?: string; // Required for stream I/O via StreamRef.open()
  /** Internal/advanced hook for supplying an auth provider directly. */
  authProvider?: AuthProvider;

  /** Shared PubNub instance for low-level subscribeToTask() operations. */
  pubnub?: PubNub;
  /** Shared factory for low-level subscribeToTask(). Creates once, caches. */
  createPubNub?: () => PubNub;

  /**
   * Per-session factory for sendMessage() -> TaskSession eager subscriptions.
   * Must return a fresh PubNub client per call so each session gets its own
   * token-isolated instance. Not used by subscribeToTask().
   */
  createSessionPubNub?: () => PubNub;

  defaultOwnerId?: string;
  baseUrl?: string;
  /** AgentAuth instance for API key-based authentication */
  agentAuth?: AgentAuth;

  /**
   * Anonymous Playground consumer fingerprint.
   *
   * When set, the TaskClient operates in anon-consumer mode: `connect()`
   * skips the authProvider JWT gate and mints T4 read tokens via
   * `POST /api/v1/auth/anon-task-read-token` with `{ taskId, fingerprint }`.
   * `sendMessage()` is rejected on anon clients. Only valid with
   * `billingMode === 'free'`. Intended for the Playground's anonymous
   * viewer flow; non-browser SDK callers should not use this.
   */
  anonFingerprint?: string;
}

/**
 * A request part item. Each part may include a `partId` referencing a
 * declared input in the agent's io.inputs[].id.
 * Explicit optional fields match the agent-instance RequestPart for consistency.
 *
 * For file inputs: set `file` (raw data) and `fileName`. The SDK
 * automatically inlines small files (<= 16 KB) or runs the pre-signed
 * URL upload flow for large files.
 */
export interface SendMessageRequestPart {
  partId?: string;
  text?: string;
  contentType?: string;
  /** Raw file data. Small files (<= 16 KB) are inlined as base64;
   *  large files use the pre-signed URL upload flow. Accepts
   *  `Uint8Array`, `ArrayBuffer`, `Blob`, or `File` -- browser
   *  consumers can pass a `File` from `<input type="file">` directly. */
  file?: Uint8Array | ArrayBuffer | Blob | File;
  /** Original file name. Required when `file` is provided. */
  fileName?: string;
  [key: string]: unknown;
}

export interface SendMessageParams {
  agentName: string;
  requestParts: SendMessageRequestPart[];
  /** Optional idempotency key for duplicate detection. Scoped to the caller's identity. */
  idempotencyKey?: string;
  ownerId?: string;
  /** Task kind. Defaults to request when omitted. */
  taskKind?: 'request' | 'pipe';
  /** Duration in minutes. Required for pipe tasks. */
  duration?: number;
  /** Consumer's public key for E2E encryption. Included in extensions.blocks. */
  consumerPublicKey?: string;
  pushNotificationConfig?: {
    url: string;
    filter?: string;
    authStrategy?: string;
  };
  retryPolicy?: {
    maxRetries?: number;
    expiresAfterSec?: number;
  };
  /** Enable auto-drain on terminal (default: true). When true, TaskSession
   *  waits for open streams to drain via stream_end before closing. When
   *  false, terminal causes immediate close with no stream force-end. */
  autoDrain?: boolean;
  /**
   * Duration in milliseconds the session waits for already-open streams
   * to finish draining naturally after a terminal event. Defaults to
   * 30000 ms (30 seconds). Ignored when `autoDrain` is false.
   *
   * Only applies to streams that were opened while the task was still
   * active. Unopened streams on a terminal session throw
   * `StreamUnavailableError` per the merged t7c baseline.
   */
  drainWindowMs?: number;
}

export interface TaskInfo {
  taskId: string;
  agentName?: string;
  owner?: string;
  state?: string;
  createdTime?: string;
  updatedTime?: string;
  [key: string]: unknown;
}

export interface ListTasksParams {
  ownerId?: string;
  agentName?: string;
  state?: string;
  limit?: number;
  cursor?: string;
}

export interface ListTasksResult {
  tasks: TaskInfo[];
  next?: string;
  totalCount?: number;
}

// ============================================================================
// Subscribe Types
// ============================================================================

export interface TaskEvent {
  type: string;
  taskId: string;
  [key: string]: unknown;
}

export interface TaskEventCallbacks {
  /** Progress events (type: "progress") */
  onProgress?: (event: TaskEvent) => void;
  /** Artifact events (type: "artifact") */
  onArtifact?: (event: TaskEvent) => void;
  /** Terminal events (type: "terminal") -- task completed, failed, or canceled */
  onTerminal?: (event: TaskEvent) => void;
  /** System events (type: "system") -- paused, resumed, etc. */
  onSystem?: (event: TaskEvent) => void;
  /** Catch-all for any event */
  onEvent?: (event: TaskEvent) => void;
  /** Error handler for callback exceptions (P1-3) */
  onError?: (error: Error, context: CallbackErrorContext) => void;
}

export interface TaskSubscription {
  unsubscribe(): void;
}

// ============================================================================
// Terminal state set
// ============================================================================

const TERMINAL_STATES = new Set(['completed', 'failed', 'canceled']);

// ============================================================================
// Errors
// ============================================================================

/**
 * Thrown when an anonymous `connect()` is denied by the backend's
 * `/api/v1/auth/anon-task-read-token` endpoint (HTTP 403).
 *
 * Typical cause: the caller's fingerprint does not match the
 * submitter's fingerprint persisted on the task row. The Playground
 * frontend catches this via a `/\b403\b/` regex on `.message` and
 * falls back to the sanitized-record view -- the class name alone is
 * not load-bearing, but the "403" substring in the message IS.
 */
export class AnonTaskAccessDeniedError extends Error {
  constructor(message: string = 'anon-task-read-token denied: HTTP 403 Forbidden') {
    super(message);
    this.name = 'AnonTaskAccessDeniedError';
  }
}

// ============================================================================
// History parsing helpers
// ============================================================================

/** Parsed stream entry from a stream_started event in history. */
interface HistoryStreamEntry {
  channel: string;
  direction: string;
  format: string;
  affinity: string;
  token: string;
  tokenTtlMinutes: number;
  metadata?: Record<string, unknown>;
}

/** Parse stream refs and artifact refs from task channel history messages. */
function parseHistoryMessages(
  messages: FetchedMessage[],
  taskId: string,
  agentName: string,
  sdkOptions: StreamClientFromDescriptorOptions,
): {
  streams: Map<string, StreamRef>;
  artifacts: ArtifactRef[];
  events: SessionTaskEvent[];
  highWaterMark: string;
  /**
   * Terminal state extracted from the most recent `terminal` event in
   * history, if any. History is authoritative for session state during
   * `connect()`: if a terminal event is visible on the status channel,
   * the session IS terminal, even if the backend's task-state RPC
   * hasn't yet reflected the write (there is a real propagation lag
   * between the PubNub pubsub terminal event and the backend's
   * `taskFanout` DB write).
   */
  terminalState?: 'completed' | 'failed' | 'canceled';
} {
  const streams = new Map<string, StreamRef>();
  const artifacts: ArtifactRef[] = [];
  const events: SessionTaskEvent[] = [];
  let highWaterMark = '0';
  let terminalState: 'completed' | 'failed' | 'canceled' | undefined;
  let terminalTimetoken = '0';

  for (const msg of messages) {
    if (msg.timetoken && msg.timetoken > highWaterMark) {
      highWaterMark = msg.timetoken;
    }

    const event = msg.message as Record<string, unknown> | undefined;
    if (!event || typeof event !== 'object' || !event.type) continue;

    events.push(event as SessionTaskEvent);

    // Extract terminal state from terminal events. Take the latest one by
    // timetoken in case history contains retries or duplicates.
    if (event.type === 'terminal') {
      const tt = String(msg.timetoken ?? '0');
      if (tt >= terminalTimetoken) {
        const state = event.state as string | undefined;
        if (state === 'completed' || state === 'failed' || state === 'canceled') {
          terminalState = state;
          terminalTimetoken = tt;
        }
      }
    }

    // Extract stream refs from stream_started events
    if (
      event.type === 'progress' &&
      event.streamEvent === 'stream_started' &&
      event.streams
    ) {
      const declaredStreamKey = event.declaredStream as string | undefined;
      const streamsMap = event.streams as Record<string, HistoryStreamEntry>;
      for (const [streamId, entry] of Object.entries(streamsMap)) {
        if (!entry || typeof entry !== 'object' || streams.has(streamId)) continue;
        const agentDirection = entry.direction as 'outbound' | 'inbound' | 'bidirectional';
        const localDirection = invertDirection(agentDirection);
        const format = entry.format as 'bytes' | 'events';
        if (!format || (format !== 'bytes' && format !== 'events')) continue;

        const affinity = entry.affinity as 'dedicated' | 'shared';
        if (affinity !== 'dedicated' && affinity !== 'shared') {
          // affinity became schema-required in 4.7.0. Silent drop would
          // leave a consumer missing a stream with no log. Warn loudly
          // so a malformed history entry is diagnosable.
          log('warn', `history-preload: dropping stream "${streamId}" for task "${taskId}" — invalid or missing affinity`, {
            event: 'history_preload_invalid_affinity',
            streamId,
            taskId,
            receivedAffinity: entry.affinity,
          });
          continue;
        }

        const descriptor: StreamDescriptor = {
          taskId,
          streamId,
          agentName,
          channel: entry.channel,
          token: entry.token,
          agentDirection,
          localDirection,
          format,
          affinity,
          metadata: entry.metadata,
          declaredStream: declaredStreamKey,
        };
        streams.set(streamId, new StreamRef(descriptor, sdkOptions));
      }
    }

    // Extract artifact refs from artifact events
    if (event.type === 'artifact' && event.artifactRef) {
      const artifactRef = event.artifactRef as ArtifactRef;
      if (artifactRef && typeof artifactRef === 'object' && artifactRef.kind) {
        artifacts.push(artifactRef);
      }
    }
  }

  return { streams, artifacts, events, highWaterMark, terminalState };
}

// ============================================================================
// Paginated history fetch
// ============================================================================

const HISTORY_PAGE_SIZE = 100;

/**
 * Fetch all history messages from a channel using backward pagination.
 *
 * PubNub fetchMessages with no start returns the most recent messages.
 * The `start` param is exclusive and returns messages older than it.
 * We page backward using the oldest timetoken from each batch, then
 * sort by timetoken ascending for chronological replay order.
 */
async function fetchAllHistory(
  fetcher: { fetchMessages?(params: { channels: string[]; count?: number; start?: string; end?: string }): Promise<{ channels?: Record<string, FetchedMessage[]> }> },
  channel: string,
): Promise<FetchedMessage[]> {
  const allMessages: FetchedMessage[] = [];
  let start: string | undefined;

  for (;;) {
    const params: { channels: string[]; count: number; start?: string } = {
      channels: [channel],
      count: HISTORY_PAGE_SIZE,
    };
    if (start) {
      params.start = start;
    }

    const result = await fetcher.fetchMessages!(params);
    const batch = result.channels?.[channel] ?? [];
    if (batch.length === 0) break;

    allMessages.push(...batch);

    if (batch.length < HISTORY_PAGE_SIZE) break;

    // Page backward: oldest message in this batch is the cursor for
    // the next older page (start is exclusive).
    start = batch[0].timetoken;
  }

  allMessages.sort((a, b) => {
    if (a.timetoken < b.timetoken) return -1;
    if (a.timetoken > b.timetoken) return 1;
    return 0;
  });

  return allMessages;
}

// ============================================================================
// TaskClient
// ============================================================================

export class TaskClient {
  private readonly config: RpcClientConfig;
  private _pubnub?: PubNub;
  private readonly _createPubNub?: () => PubNub;
  private readonly _createSessionPubNub?: () => PubNub;
  private readonly defaultOwnerId?: string;
  private readonly _ownsPubNub: boolean = false;
  private _subscribeKey: string;
  private _publishKey: string;
  private _consumerAuth?: ConsumerAuth;
  private readonly _billingMode: 'free' | 'paid';
  /**
   * Anonymous Playground consumer fingerprint, or null.
   *
   * When set, `connect()` skips the authProvider JWT gate and mints T4
   * read tokens via `POST /api/v1/auth/anon-task-read-token`. Only
   * valid with `billingMode === 'free'`. `sendMessage()` is rejected.
   */
  private readonly _anonFingerprint: string | null;

  constructor(options: TaskClientOptions) {
    if (options.billingMode !== 'free' && options.billingMode !== 'paid') {
      throw new Error(
        "TaskClient requires a billingMode option ('free' or 'paid')",
      );
    }
    this._billingMode = options.billingMode;
    this._anonFingerprint = options.anonFingerprint ?? null;
    this.config = {
      subscribeKey: options.subscribeKey,
      authProvider: options.authProvider,
      baseUrl: options.baseUrl,
      agentAuth: options.agentAuth,
    };
    this._pubnub = options.pubnub;
    this._createPubNub = options.createPubNub;
    this._createSessionPubNub = options.createSessionPubNub;
    this.defaultOwnerId = options.defaultOwnerId;
    this._subscribeKey = options.subscribeKey;
    this._publishKey = options.publishKey ?? '';
    // We own the PubNub instance only if it will be created via the factory
    this._ownsPubNub = !options.pubnub && !!options.createPubNub;
  }

  // --------------------------------------------------------------------------
  // Static factory (P1-5)
  // --------------------------------------------------------------------------

  /**
   * Create a TaskClient from environment variables or CDM config.
   *
   * Resolution order for each config value:
   * - Explicit options (if provided)
   * - Environment variables (BLOCKS_*)
   * - CDM config (fetched from cdmUrl or BLOCKS_CDM_URL)
   *
   * `billingMode` is required -- it determines which CDM keyset to use:
   * - 'free' selects the CDM playground keyset
   * - 'paid' selects the CDM network keyset
   *
   * Keyset names (`playground`, `network`) are unchanged; only the input
   * surface moves from `listing` to `billingMode`.
   */
  static async create(options?: {
    billingMode?: 'free' | 'paid';
    cdmUrl?: string;
    subscribeKey?: string;
    publishKey?: string;
    baseUrl?: string;
    apiKey?: string;
    tokenEndpoint?: TokenEndpointConfig;
    tokenProvider?: () => Promise<TokenResult>;
    onAuthError?: (error: Error) => void;
    /**
     * Anonymous Playground consumer mode. When set, the TaskClient will
     * skip the authProvider path entirely and instead mint T4 read tokens
     * via `POST /api/v1/auth/anon-task-read-token` with this fingerprint.
     *
     * Mutually exclusive with apiKey, tokenEndpoint, and tokenProvider.
     * Only supported for `billingMode: 'free'`. Intended for the
     * Playground's anonymous-viewer flow; non-browser SDK callers should
     * not use this.
     */
    anonFingerprint?: string;
  }): Promise<TaskClient> {
    const opts = options ?? {};
    const billingMode = opts.billingMode;
    // Strict equality, not truthiness. The keyset selection at line 454 uses
    // `billingMode === 'paid' ? cdm.network : cdm.playground`, so any
    // non-'paid' truthy value (typo, wrong case, whitespace) silently routes
    // to the playground keyset. JS callers and stale TS callers can pass
    // anything truthy; reject early to match the constructor's check at
    // `:382` and the Python SDK's validator.
    if (billingMode !== 'free' && billingMode !== 'paid') {
      throw new Error(
        "TaskClient.create() requires a billingMode option ('free' or 'paid')",
      );
    }

    // Mutual exclusion validation. `anonFingerprint` is a fourth mode
    // alongside `apiKey` / `tokenEndpoint` / `tokenProvider` -- all four
    // are mutually exclusive.
    const hasProvider = !!(opts.apiKey || opts.tokenEndpoint || opts.tokenProvider);
    const hasAnonFingerprint = !!opts.anonFingerprint;
    const providerCount = [
      opts.apiKey,
      opts.tokenEndpoint,
      opts.tokenProvider,
      opts.anonFingerprint,
    ].filter(Boolean).length;
    if (providerCount > 1) {
      throw new Error('Only one token provider mode may be specified');
    }

    // Anonymous mode is Playground-only. Refuse before any CDM fetch so
    // the caller gets a clear message, not a confusing keyset mismatch
    // further down.
    if (hasAnonFingerprint && billingMode !== 'free') {
      throw new Error(
        "TaskClient.create() with anonFingerprint requires billingMode: 'free'",
      );
    }

    // Fetch CDM config
    const cdmUrl = opts.cdmUrl ?? getEnv('BLOCKS_CDM_URL');
    const cdm = await fetchCdmConfig(cdmUrl);

    // Map billingMode to keyset
    const keyset = billingMode === 'paid' ? cdm.network : cdm.playground;

    // Resolve config: explicit > BLOCKS_* env > CDM
    const subscribeKey = opts.subscribeKey ?? getEnv('BLOCKS_SUBSCRIBE_KEY') ?? keyset.subscribeKey;
    const publishKey = opts.publishKey ?? getEnv('BLOCKS_PUBLISH_KEY') ?? keyset.publishKey;
    const baseUrl = opts.baseUrl ?? getEnv('BLOCKS_BACKEND_URL') ?? cdm.api.baseUrl;

    if (!subscribeKey) {
      throw new Error('TaskClient.create() could not resolve subscribeKey from options, env, or CDM');
    }

    if (!baseUrl) {
      throw new Error(
        'TaskClient.create() could not resolve baseUrl. Set baseUrl option, ' +
        'BLOCKS_BACKEND_URL env var, or ensure CDM config has api.baseUrl.',
      );
    }

    const sessionPubNubFactory = () => {
      const sessionId = `blocks-task-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`;
      return new PubNub({
        subscribeKey,
        publishKey: publishKey || undefined,
        userId: sessionId,
        enableEventEngine: true,
        ...buildPubNubLogConfig(),
      });
    };

    // Create ConsumerAuth if a provider mode is specified
    if (hasProvider) {
      const consumerAuth = new ConsumerAuth({
        apiKey: opts.apiKey,
        tokenEndpoint: opts.tokenEndpoint,
        tokenProvider: opts.tokenProvider,
        baseUrl,
        onAuthError: opts.onAuthError,
      });
      await consumerAuth.init();

      const client = new TaskClient({
        billingMode,
        subscribeKey,
        publishKey,
        baseUrl,
        createSessionPubNub: sessionPubNubFactory,
        defaultOwnerId: consumerAuth.getUserId() ?? undefined,
      });
      client.config.authProvider = consumerAuth;
      client._consumerAuth = consumerAuth;
      return client;
    }

    return new TaskClient({
      billingMode,
      subscribeKey,
      publishKey,
      baseUrl,
      createSessionPubNub: sessionPubNubFactory,
      anonFingerprint: opts.anonFingerprint,
    });
  }

  /** Returns the authenticated user ID from ConsumerAuth, or null. */
  getUserId(): string | null {
    return this._consumerAuth?.getUserId() ?? null;
  }

  /**
   * Look up an agent's card by name from the registry.
   * Returns null if the agent is not found or has no card.
   */
  async getAgentCard(agentName: string): Promise<AgentCard | null> {
    const entry = await getAgent(agentName, {
      baseUrl: this.config.baseUrl,
    });
    return entry?.card ?? null;
  }

  /**
   * Update keyset keys after an environment switch.
   * Updates both the RPC config and the keys used for PubNub client creation.
   */
  updateKeys(subscribeKey: string, publishKey?: string): void {
    this.config.subscribeKey = subscribeKey;
    this._subscribeKey = subscribeKey;
    this._publishKey = publishKey ?? '';
  }

  /**
   * Lazily resolve the PubNub instance for subscribe operations.
   * If a direct `pubnub` was provided, use it. Otherwise, call the factory.
   */
  private getPubNub(): PubNub {
    if (!this._pubnub && this._createPubNub) {
      this._pubnub = this._createPubNub();
    }
    if (!this._pubnub) {
      throw new Error(
        'TaskClient requires a pubnub instance for subscribe. Pass pubnub or createPubNub in TaskClientOptions.',
      );
    }
    return this._pubnub;
  }

  /**
   * Clean up the PubNub instance if it was created by this TaskClient (via createPubNub factory).
   * Externally-provided instances are left untouched.
   * Stops ConsumerAuth refresh timer if active. Token remains readable
   * so active sessions can still call cancel/terminate using the stale token.
   */
  destroy(): void {
    if (this._pubnub && this._ownsPubNub) {
      this._pubnub.destroy();
      this._pubnub = undefined;
    }
    if (this._consumerAuth) {
      this._consumerAuth.destroy();
    }
  }

  [Symbol.dispose](): void {
    this.destroy();
  }

  /**
   * Create a per-session PubNub subscribe client with the given T4 token.
   * Each TaskSession gets its own client to prevent token stomping.
   *
   * Uses the dedicated `createSessionPubNub` factory when available.
   * Falls back to an internal fresh-client construction otherwise.
   * Never uses the shared `createPubNub` factory -- that is reserved
   * for low-level `subscribeToTask()` operations.
   */
  private createPerSessionPubNub(readToken: string | null, subscribeKey?: string, publishKey?: string): PubNub {
    let pn: PubNub;

    const effectiveSubscribeKey = subscribeKey ?? this._subscribeKey;
    const effectivePublishKey = publishKey ?? this._publishKey;

    if (this._createSessionPubNub && effectiveSubscribeKey === this._subscribeKey) {
      // Dedicated session factory -- must return a fresh client per call.
      // Only used when the subscribe key matches (same keyset). Cross-keyset
      // sessions need a fresh client with the target's subscribe key.
      pn = this._createSessionPubNub();
    } else {
      const sessionId = `blocks-task-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`;
      pn = new PubNub({
        subscribeKey: effectiveSubscribeKey,
        publishKey: effectivePublishKey || undefined,
        userId: sessionId,
        enableEventEngine: true,
        ...buildPubNubLogConfig(),
      });
    }

    if (readToken) {
      pn.setToken(readToken);
    }
    return pn;
  }

  // --------------------------------------------------------------------------
  // Internal helper: acquire consumer read token
  // --------------------------------------------------------------------------

  private async fetchConsumerReadToken(taskId: string, role: 'consumer' | 'provider' = 'consumer'): Promise<{
    pamToken: string;
    channel: string;
    ttlMinutes: number;
  }> {
    if (!this.config.baseUrl) {
      throw new Error('connect() requires a backend baseUrl. Set baseUrl in TaskClientOptions.');
    }

    const url = `${this.config.baseUrl.replace(/\/+$/, '')}/api/v1/auth/task-read-token`;

    const doFetch = async (): Promise<Response> => {
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
      };
      const authHeader = this.config.authProvider?.getAuthHeader();
      if (authHeader) {
        headers['Authorization'] = authHeader;
      }
      injectAffinity(headers);
      return fetch(url, {
        method: 'POST',
        headers,
        body: JSON.stringify({ taskId, role }),
      });
    };

    let response = await doFetch();
    captureAffinity(response.headers);

    // 401 reactive refresh
    if (response.status === 401 && this.config.authProvider) {
      const refreshed = await this.config.authProvider.onAuthFailure();
      if (refreshed) {
        response = await doFetch();
        captureAffinity(response.headers);
      }
    }

    if (!response.ok) {
      const body = await response.text().catch(() => '');
      throw new Error(`task-read-token failed: HTTP ${response.status}${body ? ` ${body}` : ''}`);
    }

    return response.json() as Promise<{ pamToken: string; channel: string; ttlMinutes: number }>;
  }

  /**
   * Anonymous Playground twin of `fetchConsumerReadToken`.
   *
   * Hits `POST /api/v1/auth/anon-task-read-token` with
   * `{ taskId, fingerprint }`. Intentionally sends no `Authorization`
   * header -- the endpoint is unauthenticated and relies solely on the
   * fingerprint-equality check against the persisted
   * `task.submitterFingerprint` column for ownership proof.
   *
   * On HTTP 403, throws `AnonTaskAccessDeniedError` with a message
   * containing the substring "403" so the Playground frontend's
   * `/\b403\b/` regex catches it and falls back to the sanitized
   * public-record view.
   */
  private async fetchAnonConsumerReadToken(taskId: string): Promise<{
    pamToken: string;
    channel: string;
    ttlMinutes: number;
  }> {
    if (!this.config.baseUrl) {
      throw new Error('connect() requires a backend baseUrl. Set baseUrl in TaskClientOptions.');
    }
    if (!this._anonFingerprint) {
      // Defensive: this method is only called from the anon branch of
      // connect(), which has already checked `_anonFingerprint`.
      throw new Error('fetchAnonConsumerReadToken called without anonFingerprint');
    }

    const url = `${this.config.baseUrl.replace(/\/+$/, '')}/api/v1/auth/anon-task-read-token`;

    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
    };
    injectAffinity(headers);

    const response = await fetch(url, {
      method: 'POST',
      headers,
      body: JSON.stringify({ taskId, fingerprint: this._anonFingerprint }),
    });
    captureAffinity(response.headers);

    if (response.status === 403) {
      const body = await response.text().catch(() => '');
      throw new AnonTaskAccessDeniedError(
        `anon-task-read-token denied: HTTP 403${body ? ` ${body}` : ''}`,
      );
    }

    if (!response.ok) {
      const body = await response.text().catch(() => '');
      throw new Error(
        `anon-task-read-token failed: HTTP ${response.status}${body ? ` ${body}` : ''}`,
      );
    }

    return response.json() as Promise<{ pamToken: string; channel: string; ttlMinutes: number }>;
  }

  // --------------------------------------------------------------------------
  // Primary method
  // --------------------------------------------------------------------------

  /**
   * Send a message (task) to an agent via JSON-RPC "SendMessage".
   * Returns a TaskSession that eagerly subscribes to the task channel.
   *
   * When request parts include `file` data:
   * - Small files (<= 16 KB): inlined as base64 artifactRef on the part
   * - Large files (> 16 KB): uploaded via pre-signed URL flow, then
   *   SendMessage includes the uploadSessionId to bind files to the task
   */
  async sendMessage(params: SendMessageParams): Promise<TaskSession> {
    // Anon-mode TaskClients are strictly read-only. The Playground
    // submits via the JSON-RPC `useSendTask` path, not through this
    // SDK; calling sendMessage here would fail with a confusing 401
    // downstream. Fail fast with a typed, descriptive error.
    if (this._anonFingerprint) {
      throw new Error('anon-mode TaskClient does not support sendMessage()');
    }
    if (this.config.authProvider?.ensureReady) {
      await this.config.authProvider.ensureReady();
    }
    const ownerId = params.ownerId || this.defaultOwnerId || '';
    const taskKind = params.taskKind;
    const duration = params.duration;

    if (taskKind === 'pipe') {
      if (
        duration === undefined ||
        duration === null ||
        !Number.isInteger(duration) ||
        duration < 1 ||
        duration > 43200
      ) {
        throw new Error(
          'Pipe tasks require a duration between 1 and 43200 minutes',
        );
      }
    } else if (duration !== undefined && duration !== null) {
      throw new Error(
        'Request tasks must not include a duration. Duration is only valid for pipe tasks.',
      );
    }

    // Process file-bearing request parts before sending the RPC call.
    // Build wire-format parts: inline small files, upload large ones.
    let uploadSessionId: string | undefined;
    const wireParts: Record<string, unknown>[] = [];

    for (const part of params.requestParts) {
      if (part.file) {
        if (!part.partId) {
          throw new Error('partId is required for file-bearing request parts');
        }
        // Normalize browser-native or Node-native file shapes into a
        // lazy {size, uploadBody, getBytes()} handle. `getBytes()` is
        // invoked only on the inline branch — keeps a multi-GB Blob
        // from being read into memory when we're just going to
        // multipart-upload it. See file-input.ts.
        const normalized = normalizeFileInput(part.file);
        const fileName = part.fileName ?? 'unnamed';
        const mimeType = (part.contentType as string) ?? 'application/octet-stream';

        if (shouldInlineArtifact(normalized.size)) {
          // Small file: build inline artifactRef client-side. Only
          // now do we materialize the bytes; for Blob inputs this is
          // the single arrayBuffer() read.
          const bytes = await normalized.getBytes();
          const artifactRef = buildArtifactRef({
            data: bytes,
            mimeType,
            fileName,
          });
          const { file: _f, fileName: _fn, text: _t, ...rest } = part;
          wireParts.push({ ...rest, artifactRef });
        } else {
          // Large file: pre-signed URL upload flow
          if (!this.config.baseUrl) {
            throw new Error(
              'File upload requires a backend baseUrl. Set baseUrl in TaskClientOptions.',
            );
          }
          const auth: FileUploadAuth = {
            baseUrl: this.config.baseUrl,
            authProvider: this.config.authProvider,
            agentAuth: this.config.agentAuth,
          };
          const uploadParams: ConsumerUploadParams = {
            role: 'consumer-input',
            agentName: params.agentName,
            fileName,
            fileSize: normalized.size,
            mimeType,
            partId: part.partId,
            uploadSessionId,
          };
          const result = await uploadFile(auth, uploadParams, normalized.uploadBody);
          if (result.uploadSessionId) {
            uploadSessionId = result.uploadSessionId;
          }
          // Uploaded-file parts carry partId + contentType; the
          // backend reconstructs `artifactRef` from the task_file row
          // the upload session persisted. `contentType` is preserved
          // so agent handlers that branch on `part.contentType`
          // continue to work for files above the inline threshold
          // without having to fall back to `artifactRef.mimeType`.
          // Other descriptive keys (text, etc.) are dropped — file
          // parts don't carry a text payload.
          const { file: _f, fileName: _fn, ...rest } = part;
          wireParts.push({
            partId: rest.partId,
            ...(rest.contentType
              ? { contentType: rest.contentType }
              : {}),
          });
        }
      } else {
        // Non-file part: pass through as-is (strip file/fileName if somehow present)
        const { file: _f, fileName: _fn, ...rest } = part;
        wireParts.push(rest);
      }
    }

    const rpcParams: Record<string, unknown> = {
      agentName: params.agentName,
      billingMode: this._billingMode,
      requestParts: wireParts,
    };
    if (uploadSessionId) rpcParams.uploadSessionId = uploadSessionId;
    if (params.idempotencyKey) rpcParams.idempotencyKey = params.idempotencyKey;
    rpcParams.ownerId = ownerId;
    const blocksExt: Record<string, unknown> = {};
    if (taskKind) blocksExt.taskKind = taskKind;
    if (duration !== undefined && duration !== null) blocksExt.duration = duration;
    if (params.consumerPublicKey) blocksExt.consumerPublicKey = params.consumerPublicKey;

    if (Object.keys(blocksExt).length > 0) {
      rpcParams.extensions = { blocks: blocksExt };
    }
    if (params.pushNotificationConfig) {
      rpcParams.pushNotificationConfig = params.pushNotificationConfig;
    }
    if (params.retryPolicy) rpcParams.retryPolicy = params.retryPolicy;

    const result = await callRpc<{
      taskId: string;
      orgId?: string;
      idempotent?: boolean;
      state?: string;
      queued?: boolean;
      pushConfigId?: string;
      extensions?: {
        blocks?: {
          readToken?: string | null;
          subscribeKey?: string;
          publishKey?: string;
          streamChannels?: { status?: string };
        };
      };
    }>(this.config, 'SendMessage', rpcParams);

    const blocks = result.extensions?.blocks;
    const readToken = blocks?.readToken ?? null;
    const statusChannel = blocks?.streamChannels?.status ?? undefined;
    const taskSubscribeKey = blocks?.subscribeKey ?? this._subscribeKey;
    const taskPublishKey = blocks?.publishKey ?? this._publishKey;

    // Detect terminal idempotent hit: task already completed/failed/canceled.
    // Create a pre-closed session that never subscribes to PubNub.
    const isTerminalIdempotentHit =
      result.idempotent === true && !!result.state && TERMINAL_STATES.has(result.state);

    // Skip PubNub allocation for terminal idempotent hits -- the session
    // is already closed and will never subscribe. Matches Python SDK behavior.
    if (isTerminalIdempotentHit) {
      return new TaskSession({
        taskId: result.taskId,
        ownerId,
        orgId: result.orgId ?? ownerId,
        readToken,
        statusChannel,
        agentName: params.agentName,
        pubnub: null,
        ownsSubscribeClient: false,
        sdkOptions: {
          subscribeKey: taskSubscribeKey,
          publishKey: taskPublishKey,
        },
        rpcConfig: this.config,
        idempotent: result.idempotent,
        queued: result.queued,
        pushConfigId: result.pushConfigId,
        preClosed: true,
        state: result.state,
      });
    }

    // Create a per-session PubNub client using the target agent's subscribe
    // key (returned by the backend) so cross-billing-mode A2A sessions
    // subscribe on the correct keyset.
    const sessionPubnub = this.createPerSessionPubNub(readToken, taskSubscribeKey, taskPublishKey);
    // sdkOptions carries the target keyset for the streaming session (StreamRef/StreamClient);
    // rpcConfig (this.config) keeps the caller's keyset for HTTP RPC and read-token minting.
    const sdkOptions = {
      subscribeKey: taskSubscribeKey,
      publishKey: taskPublishKey,
    };

    // History-based catch-up: fetch events that may have been published
    // between the RPC dispatch and now (closes the subscribe race for
    // fast handlers — see BN-455). Same pattern as connect().
    const resolvedChannel = statusChannel ?? taskChannel(result.taskId, result.orgId ?? ownerId);
    try {
      const timeResult = await sessionPubnub.time();
      const serverTimetoken = String(timeResult.timetoken);

      const fetcher = asPubNubFetcher(sessionPubnub);
      let preloadedStreams = new Map<string, StreamRef>();
      let preloadedArtifacts: ArtifactRef[] = [];
      let highWaterMark = '0';
      let historyTerminalState: 'completed' | 'failed' | 'canceled' | undefined;

      if (fetcher?.fetchMessages) {
        const messages = await fetchAllHistory(fetcher, resolvedChannel);
        const parsed = parseHistoryMessages(messages, result.taskId, params.agentName, sdkOptions);
        preloadedStreams = parsed.streams;
        preloadedArtifacts = parsed.artifacts;
        highWaterMark = parsed.highWaterMark;
        historyTerminalState = parsed.terminalState;
      }

      // Fast handler already finished: return a pre-populated terminal session.
      if (historyTerminalState && TERMINAL_STATES.has(historyTerminalState)) {
        return new TaskSession({
          taskId: result.taskId,
          ownerId,
          orgId: result.orgId ?? ownerId,
          readToken,
          statusChannel: resolvedChannel,
          agentName: params.agentName,
          pubnub: sessionPubnub,
          ownsSubscribeClient: true,
          sdkOptions,
          rpcConfig: this.config,
          idempotent: result.idempotent,
          queued: result.queued,
          pushConfigId: result.pushConfigId,
          autoDrain: params.autoDrain,
          drainWindowMs: params.drainWindowMs,
          state: historyTerminalState,
          skipSubscription: true,
          preloadedStreams,
          preloadedArtifacts,
        });
      }

      // Subscribe from cursor so there is no gap between history and live events.
      const subscribeCursor = highWaterMark !== '0' ? highWaterMark : serverTimetoken;

      const buffer: Array<{ message: SessionTaskEvent; timetoken: string }> = [];
      let dispatching = false;
      let dispatchRef: ((event: SessionTaskEvent, timetoken?: string) => void) | undefined;

      const listener = {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        message: (event: any) => {
          if (event.channel !== resolvedChannel) return;
          const msg = event.message as SessionTaskEvent | undefined;
          if (!msg || typeof msg !== 'object' || !msg.type) return;
          const tt = String(event.timetoken ?? '0');
          if (dispatching && dispatchRef) {
            dispatchRef(msg, tt);
          } else {
            buffer.push({ message: msg, timetoken: tt });
          }
        },
      };

      sessionPubnub.addListener(listener);
      sessionPubnub.subscribe({ channels: [resolvedChannel], timetoken: subscribeCursor });

      const session = new TaskSession({
        taskId: result.taskId,
        ownerId,
        orgId: result.orgId ?? ownerId,
        readToken,
        statusChannel: resolvedChannel,
        agentName: params.agentName,
        pubnub: sessionPubnub,
        ownsSubscribeClient: true,
        sdkOptions,
        rpcConfig: this.config,
        idempotent: result.idempotent,
        queued: result.queued,
        pushConfigId: result.pushConfigId,
        autoDrain: params.autoDrain,
        drainWindowMs: params.drainWindowMs,
        preloadedStreams,
        preloadedArtifacts,
        externalSubscription: {
          listener,
          channel: resolvedChannel,
          onReady: (dispatch) => {
            dispatchRef = dispatch;
          },
        },
      });

      // Drain buffer through session, dedup by timetoken.
      if (dispatchRef) {
        const dispatch = dispatchRef;
        for (const entry of buffer) {
          if (entry.timetoken > subscribeCursor) {
            dispatch(entry.message, entry.timetoken);
          }
        }
      }
      buffer.length = 0;
      dispatching = true;

      return session;
    } catch {
      // History/subscribe catch-up failed, but the task was already created.
      // Fall back to a basic session so the caller always gets a taskId.
      return new TaskSession({
        taskId: result.taskId,
        ownerId,
        orgId: result.orgId ?? ownerId,
        readToken,
        statusChannel: resolvedChannel,
        agentName: params.agentName,
        pubnub: sessionPubnub,
        ownsSubscribeClient: true,
        sdkOptions,
        rpcConfig: this.config,
        idempotent: result.idempotent,
        queued: result.queued,
        pushConfigId: result.pushConfigId,
        autoDrain: params.autoDrain,
        drainWindowMs: params.drainWindowMs,
      });
    }
  }

  // --------------------------------------------------------------------------
  // connect() -- P1-2
  // --------------------------------------------------------------------------

  /**
   * Connect to an existing task. Returns a TaskSession pre-populated
   * with stream refs, artifact refs, and task state from history.
   *
   * For active tasks: the session subscribes to the task channel and
   * live events flow through callbacks from that point forward.
   *
   * For terminal tasks: the session is pre-populated but does not
   * subscribe. The consumer reads listArtifacts(), listStreams(), and
   * session.state, then calls close().
   *
   * Uses the task-read-token endpoint with role:'consumer' to acquire
   * a fresh T4 read token. The caller does not need to persist or
   * supply readToken, orgId, ownerId, or agentName.
   */
  async connect(params: {
    taskId: string;
    autoDrain?: boolean;
    /**
     * Duration in milliseconds the session waits for already-open streams
     * to finish draining naturally after a terminal event. Defaults to
     * 30000 ms (30 seconds). Ignored when `autoDrain` is false.
     *
     * Only applies to streams that were opened while the task was still
     * active. Unopened streams on a terminal session throw
     * `StreamUnavailableError` per the merged t7c baseline.
     */
    drainWindowMs?: number;
    /**
     * Role to request when minting the read token. Defaults to 'consumer'
     * (task submitter). Set to 'provider' when the caller is the agent
     * owner viewing a received task.
     */
    role?: 'consumer' | 'provider';
  }): Promise<TaskSession> {
    const { taskId } = params;

    // Anonymous Playground branch: short-circuit the authProvider JWT
    // gate. `getTask` works for anon on public+free tasks, and the read
    // token is minted against the caller's fingerprint instead of a JWT.
    // Everything downstream (history preload, terminal detection, live
    // subscribe) is identical -- routed through `_openSessionFromToken`.
    if (this._anonFingerprint) {
      const task = await this.getTask(taskId);
      if (!task) {
        throw new Error(`Task not found: ${taskId}`);
      }
      const agentName = (task.agentName as string) ?? '';
      const taskState = (task.state as string) ?? '';
      const tokenResult = await this.fetchAnonConsumerReadToken(taskId);
      return this._openSessionFromToken({
        task,
        agentName,
        taskState,
        tokenResult,
        autoDrain: params.autoDrain,
        drainWindowMs: params.drainWindowMs,
      });
    }

    // Step 1: Validate auth -- connect() requires JWT auth (via authProvider)
    if (!this.config.authProvider?.getAuthHeader()) {
      throw new Error(
        'connect() requires an authenticated TaskClient. Use apiKey, tokenEndpoint, or tokenProvider. AgentAuth is not supported for consumer task connections.',
      );
    }

    // Step 2: Fetch task state
    const task = await this.getTask(taskId);
    if (!task) {
      throw new Error(`Task not found: ${taskId}`);
    }
    const agentName = (task.agentName as string) ?? '';
    const taskState = (task.state as string) ?? '';

    // Step 3: Acquire fresh read token (consumer or provider)
    const tokenResult = await this.fetchConsumerReadToken(taskId, params.role);

    // Step 4+: Shared session assembly (history-authoritative terminal
    // detection, terminal short-circuit, or live subscribe with
    // buffer-drain handshake) is in `_openSessionFromToken`.
    return this._openSessionFromToken({
      task,
      agentName,
      taskState,
      tokenResult,
      autoDrain: params.autoDrain,
      drainWindowMs: params.drainWindowMs,
    });
  }

  /**
   * Shared session assembly used by both branches of `connect()`:
   *
   * - Builds a per-session PubNub client bound to the read token.
   * - Fetches history once up front; derives `highWaterMark` and a
   *   possible terminal state from it.
   * - History is authoritative for terminal state (there is a real lag
   *   between the PubNub terminal event and the backend `taskFanout`
   *   DB write). If either history OR the RPC state is terminal, the
   *   session is terminal.
   * - Terminal path returns a pre-populated, non-subscribed session.
   * - Active path subscribes from the cursor (or server timetoken when
   *   history is empty), buffers messages until the session exists,
   *   then drains the buffer (deduped by timetoken > cursor) before
   *   flipping to live mode.
   *
   * On any thrown error inside this helper, the session PubNub client
   * is destroyed before the error is rethrown.
   */
  private async _openSessionFromToken(args: {
    task: TaskInfo;
    agentName: string;
    taskState: string;
    tokenResult: { pamToken: string; channel: string; ttlMinutes: number };
    autoDrain?: boolean;
    drainWindowMs?: number;
  }): Promise<TaskSession> {
    const { task, agentName, taskState, tokenResult, autoDrain, drainWindowMs } = args;
    const taskId = task.taskId;
    const { pamToken, channel } = tokenResult;

    const sdkOptions = {
      subscribeKey: this._subscribeKey,
      publishKey: this._publishKey,
    };

    const sessionPubnub = this.createPerSessionPubNub(pamToken);
    try {
      const timeResult = await sessionPubnub.time();
      const serverTimetoken = String(timeResult.timetoken);

      const fetcher = asPubNubFetcher(sessionPubnub);
      let preloadedStreams = new Map<string, StreamRef>();
      let preloadedArtifacts: ArtifactRef[] = [];
      let preloadedEvents: SessionTaskEvent[] = [];
      let highWaterMark = '0';
      let historyTerminalState: string | undefined;

      if (fetcher?.fetchMessages) {
        const messages = await fetchAllHistory(fetcher, channel);
        const parsed = parseHistoryMessages(messages, taskId, agentName, sdkOptions);
        preloadedStreams = parsed.streams;
        preloadedArtifacts = parsed.artifacts;
        preloadedEvents = parsed.events;
        highWaterMark = parsed.highWaterMark;
        historyTerminalState = parsed.terminalState;
      }

      // Prefer the history-derived terminal state when the RPC hasn't
      // caught up. If either source reports terminal, the session is
      // terminal.
      const effectiveState =
        (historyTerminalState && TERMINAL_STATES.has(historyTerminalState))
          ? historyTerminalState
          : taskState;
      const isTerminal = TERMINAL_STATES.has(effectiveState);

      if (isTerminal) {
        // Terminal path: no live subscription; session holds client only
        // for downloadArtifact().
        return new TaskSession({
          taskId,
          ownerId: (task.owner as string) ?? '',
          readToken: pamToken,
          statusChannel: channel,
          agentName,
          pubnub: sessionPubnub,
          ownsSubscribeClient: true,
          sdkOptions,
          rpcConfig: this.config,
          autoDrain,
          drainWindowMs,
          state: effectiveState,
          skipSubscription: true,
          preloadedStreams,
          preloadedArtifacts,
          preloadedEvents,
        });
      }

      // Active path: subscribe with timetoken after history.

      // Use the server timetoken as fallback when history is empty,
      // so we always subscribe from a known point in time.
      const subscribeCursor = highWaterMark !== '0' ? highWaterMark : serverTimetoken;

      // Step 4f: Subscribe from the cursor timetoken so there is no
      // gap between history and live events. Buffer incoming messages
      // until the session is constructed.
      const buffer: Array<{ message: SessionTaskEvent; timetoken: string }> = [];
      let dispatching = false;
      let dispatchRef: ((event: SessionTaskEvent, timetoken?: string) => void) | undefined;

      const listener = {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        message: (event: any) => {
          if (event.channel !== channel) return;
          const msg = event.message as SessionTaskEvent | undefined;
          if (!msg || typeof msg !== 'object' || !msg.type) return;
          const tt = String(event.timetoken ?? '0');
          if (dispatching && dispatchRef) {
            // Forward the PubNub timetoken to the session so its dedup
            // layer (SDK_CONTRACT §10.4.1a) can drop replay duplicates.
            dispatchRef(msg, tt);
          } else {
            buffer.push({ message: msg, timetoken: tt });
          }
        },
      };

      sessionPubnub.addListener(listener);
      sessionPubnub.subscribe({ channels: [channel], timetoken: subscribeCursor });

      // Step 4g: Construct TaskSession with externalSubscription
      const session = new TaskSession({
        taskId,
        ownerId: (task.owner as string) ?? '',
        readToken: pamToken,
        statusChannel: channel,
        agentName,
        pubnub: sessionPubnub,
        ownsSubscribeClient: true,
        sdkOptions,
        rpcConfig: this.config,
        autoDrain,
        drainWindowMs,
        state: taskState,
        preloadedStreams,
        preloadedArtifacts,
        preloadedEvents,
        externalSubscription: {
          listener,
          channel,
          onReady: (dispatch) => {
            dispatchRef = dispatch;
          },
        },
      });

      // Step 4h: Drain buffer through session, dedup by timetoken.
      // Also append each drained event to the history snapshot so
      // listEvents() covers the full pre-caller window (history + gap).
      if (dispatchRef) {
        const dispatch = dispatchRef;
        for (const entry of buffer) {
          if (entry.timetoken > subscribeCursor) {
            session._appendHistoryEvent(entry.message as TaskEvent, entry.timetoken);
            dispatch(entry.message, entry.timetoken);
          }
        }
      }
      buffer.length = 0;

      // Step 4i: Switch to live mode
      dispatching = true;

      return session;
    } catch (err) {
      sessionPubnub.destroy();
      throw err;
    }
  }

  // --------------------------------------------------------------------------
  // Task lifecycle
  // --------------------------------------------------------------------------

  async getTask(taskId: string): Promise<TaskInfo> {
    const result = await callRpc<{ task: TaskInfo }>(this.config, 'GetTask', { taskId });
    return result.task;
  }

  async listTasks(params?: ListTasksParams): Promise<ListTasksResult> {
    return callRpc<ListTasksResult>(this.config, 'ListTasks', { ...params });
  }

  async cancelTask(taskId: string): Promise<void> {
    await callRpc<void>(this.config, 'CancelTask', { taskId });
  }

  async pauseTask(taskId: string): Promise<void> {
    await callRpc<void>(this.config, 'PauseTask', { taskId });
  }

  async resumeTask(taskId: string): Promise<void> {
    await callRpc<void>(this.config, 'ResumeTask', { taskId });
  }

  async retryTask(taskId: string): Promise<void> {
    await callRpc<void>(this.config, 'RetryTask', { taskId });
  }

  async terminateTask(taskId: string): Promise<void> {
    await callRpc<void>(this.config, 'TerminateTask', { taskId });
  }

  // --------------------------------------------------------------------------
  // Subscribe helper (also available as standalone)
  // --------------------------------------------------------------------------

  /**
   * Subscribe to real-time task events via PubNub.
   * Requires pubnub instance in TaskClientOptions.
   */
  subscribeToTask(
    taskId: string,
    orgId: string,
    callbacks: TaskEventCallbacks,
  ): TaskSubscription {
    return subscribeToTask(this.getPubNub(), taskId, orgId, callbacks);
  }
}

// ============================================================================
// Standalone subscribe helper
// ============================================================================

/** Route a callback error through onError or the SDK logger. */
function routeSubscribeError(
  err: unknown,
  callbackType: CallbackErrorContext['callbackType'],
  event: TaskEvent,
  onError?: (error: Error, context: CallbackErrorContext) => void,
): void {
  const error = err instanceof Error ? err : new Error(String(err));
  if (onError) {
    try {
      onError(error, { entryPoint: 'subscribeToTask', callbackType, event });
    } catch {
      // Prevent infinite loop if error handler itself throws
    }
  } else {
    log('warn', `subscribeToTask callback error in ${callbackType}`, {
      event: 'subscribe_callback_error',
      callbackType,
      error: error.message,
    });
  }
}

/**
 * Subscribe to real-time task events on channel `u.{orgId}.{taskId}`.
 *
 * Dispatches to typed callbacks based on event type:
 * - "progress" -> onProgress
 * - "artifact" -> onArtifact
 * - "terminal" -> onTerminal
 * - "system"   -> onSystem
 * - (any)      -> onEvent (catch-all)
 *
 * Returns a TaskSubscription with `unsubscribe()` for cleanup.
 */
function subscribeToTask(
  pubnub: PubNub,
  taskId: string,
  orgId: string,
  callbacks: TaskEventCallbacks,
): TaskSubscription {
  const channel = taskChannel(taskId, orgId);

  const listener = {
    message: (event: { channel?: string; message?: unknown }): void => {
      if (event.channel !== channel) return;

      const msg = event.message as TaskEvent | undefined;
      if (!msg || typeof msg !== 'object' || !msg.type) return;

      // Catch-all
      if (callbacks.onEvent) {
        try { callbacks.onEvent(msg); } catch (err) {
          routeSubscribeError(err, 'onEvent', msg, callbacks.onError);
        }
      }

      // Typed dispatch
      switch (msg.type) {
        case 'progress':
          if (callbacks.onProgress) {
            try { callbacks.onProgress(msg); } catch (err) {
              routeSubscribeError(err, 'onProgress', msg, callbacks.onError);
            }
          }
          break;
        case 'artifact':
          if (callbacks.onArtifact) {
            try { callbacks.onArtifact(msg); } catch (err) {
              routeSubscribeError(err, 'onArtifact', msg, callbacks.onError);
            }
          }
          break;
        case 'terminal':
          if (callbacks.onTerminal) {
            try { callbacks.onTerminal(msg); } catch (err) {
              routeSubscribeError(err, 'onTerminal', msg, callbacks.onError);
            }
          }
          break;
        case 'system':
          if (callbacks.onSystem) {
            try { callbacks.onSystem(msg); } catch (err) {
              routeSubscribeError(err, 'onSystem', msg, callbacks.onError);
            }
          }
          break;
      }
    },
  };

  pubnub.addListener(listener);
  // timetoken: 1000 asks PubNub to replay everything still in the
  // channel's in-memory cache (per SDK_CONTRACT §10.4.1a). Using 0
  // would mean "initial subscribe, no catch-up" and leaves the
  // publish-before-subscribe race unfixed.
  //
  // Note: this standalone helper does not dedup replayed messages.
  // Consumers who care about exactly-once delivery should use
  // TaskSession (via TaskClient.connect / sendMessage), which tracks
  // seen timetokens in its handleEvent layer.
  pubnub.subscribe({ channels: [channel], timetoken: 1000 });

  return {
    unsubscribe(): void {
      pubnub.removeListener(listener);
      pubnub.unsubscribe({ channels: [channel] });
    },
  };
}
