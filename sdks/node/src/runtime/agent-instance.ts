/**
 * Agent Instance Runtime - Phase 3 Three-Tier Connection Model
 *
 * Implements the three-tier connection architecture:
 * - controlClient: long-lived, subscribes to control channel
 * - taskClient: per-task, holds T2, destroyed after handler exit
 * - streamClient: per-stream, holds T7a, managed by StreamClient from Stream SDK
 *
 * Key features:
 * - Unified createStream() with direction, onActivate, metadata, external, format
 * - Shared stream registry with ref-counting
 * - Task credential cache for post-handler operations
 * - Instance-level publishTerminal() and failStream()
 * - onActivate processing model
 * - Consumer session API (TaskSession)
 */

import PubNub from 'pubnub';
import { StreamClient } from '../stream/index.js';
import { buildArtifactRef, shouldInlineArtifact, decodeInlineArtifact, type ArtifactRef } from './artifacts.js';
import { createPubNubClient } from './pubnub-client.js';
import { createChannelManager } from './channel-manager.js';
import { toPayload, type JsonValue } from './pubnub-types.js';
import { connectAgent, getAgent, type AgentCard } from './agent-registry.js';
import { parseStreamSetupResponse, parseStreamSetupError } from './stream-setup-helper.js';
import { DEFAULTS } from '../defaults.js';
import { getEnv } from '../env.js';
import {
  log as baseLog,
  isDebugSubsystemEnabled,
  _resolveLogLevel,
  _LOG_LEVEL_ORDER,
} from './logger.js';
import {
  mapTransportCategory,
  mapTransportOperation,
  isAccessDeniedStatus,
  DEGRADED_TRANSPORT_CATEGORIES,
  RESTORED_TRANSPORT_CATEGORIES,
  type TransportStatusPayload,
  type TransportOperation,
} from './transport-categories.js';
import { uploadFile, type FileUploadAuth } from './file-upload.js';
import { StaticAuthProvider } from './auth-provider.js';
import { StreamRegistry } from './stream-registry.js';
import { CredentialCache } from './credential-cache.js';
import {
  createStreamObject,
  createExternalStreamObject,
  runOnActivate,
  type StreamObject,
  type OnActivateCallback,
} from './stream-context.js';
import { TaskClient } from './task-client.js';
import { ConsumerAuth } from './consumer-auth.js';
import {
  fetchCdmConfig,
  type CdmConfig,
  type CdmKeyset,
  type PnEnvironment,
} from './cdm-config.js';
import { AgentAuth, AgentAuthFatalError } from './agent-auth.js';
import {
  CURRENT_PROTOCOL_VERSION,
  SUPPORTED_PROTOCOL_VERSIONS,
  isProtocolVersionSupported,
} from './protocol-version.js';

/**
 * A single artifact entry within HandlerResult.artifacts.
 */
export type ArtifactEntry = {
  data: Buffer | string;
  mimeType: string;
  fileName?: string;
  /** Declared output ID from the card's io.outputs[].id. Included in artifact events. */
  outputId?: string;
};

/**
 * Return type for agent task handlers.
 */
export type HandlerResult = {
  /** Zero or more output artifacts. Published in array order before the terminal event. */
  artifacts?: ArtifactEntry[];
};

/**
 * Handler function signature for processing agent tasks.
 */
export type HandlerFn = (task: StartTaskMessage, ctx?: TaskContext) => Promise<HandlerResult>;

/**
 * Presence state for agent instances, used for load tracking.
 */
export interface AgentInstancePresenceState {
  instanceId: string;
  activeTasks: number;
  activeStreams: number;
  concurrency: number;
  startedAt: number;
  preferredProtocolVersion: string;
  protocolVersions: string[];
}

export interface AgentInstanceOptions {
  pubnub?: PubNub;
  token?: string;
  userId?: string;
  agentName: string;
  description?: string;
  /** @internal SDK-only hook for custom task setup — receives raw message including PAM tokens. */
  onStartTask?: (task: InternalStartTaskMessage, pubnub: PubNub) => Promise<void>;
  onCancelTask?: (taskId: string, pubnub: PubNub) => Promise<void>;
  artifactBasePath?: string;
  handler?: HandlerFn;
  onError?: (taskId: string, error: Error) => Promise<void>;
  logChannel?: string;
  /** @internal Used by tests only — not a public API. Always auto-generated at runtime. */
  instanceId?: string;
  concurrency?: number;
  expectedInstances?: number;
  maxPendingBacklog?: number;
  maxRunningTimeSec?: number;
  card: AgentCard;
  cardRef?: string;
  cardSummary?: string;
  listing?: 'private' | 'public';
  baseUrl?: string;
  cdmUrl?: string;
}

/**
 * Request part item within a StartTask message.
 * Each part may include a `partId` referencing a declared input in io.inputs[].id.
 * Explicit optional fields match the Python SDK's RequestPart for cross-SDK parity.
 * The index signature allows forward-compatible wire evolution.
 */
export interface RequestPart {
  partId?: string;
  text?: string;
  contentType?: string;
  /** Artifact reference attached by the backend (for uploaded files) or
   *  the consumer SDK (for inline files). Use ctx.downloadInputArtifact(part)
   *  to get the raw bytes. */
  artifactRef?: ArtifactRef;
  [key: string]: JsonValue | ArtifactRef | undefined;
}

export interface StartTaskMessage {
  type: 'StartTask';
  taskId: string;
  agentName?: string;
  ownerId: string;
  orgId?: string;
  taskKind?: string;
  duration?: number;
  durationExpiresAtMs?: number; // epoch ms -- server-computed pipe-task deadline
  requestParts?: RequestPart[];
  callerClaims?: Record<string, JsonValue>;
  requestSummary?: Record<string, JsonValue>;
  hasStream?: boolean;
  /** Consumer's public key for E2E encryption (passed through from SendMessage). */
  consumerPublicKey?: string;
  /** Protocol version pinned at task creation. */
  protocolVersion?: string;
}

/** Wire-format StartTask with PAM tokens — SDK-internal only, never exposed to handlers. */
interface InternalStartTaskMessage extends StartTaskMessage {
  writeToken?: string;
  controlToken?: string;
}

export interface CreateStreamOptions {
  direction?: 'outbound' | 'inbound' | 'bidirectional';
  onActivate?: OnActivateCallback;
  metadata?: Record<string, unknown>;
  external?: boolean;
  format?: 'bytes' | 'events';
  bundleSizeBytes?: number;
  maxLatencyMs?: number;
  /** Key from the card's streams block. Defaults to "_default". */
  declaredStream?: string;
  /**
   * Grace period in ms to wait after stream_started is published before
   * returning the StreamClient for outbound/bidirectional streams. Gives
   * the consumer time to subscribe. Defaults to 1000ms. Set to 0 to skip.
   */
  subscribeGraceMs?: number;
}

/**
 * Context object passed to handlers for reporting status during execution.
 */
export interface TaskContext {
  reportStatus: (message: string) => void;
  taskId: string;
  /** Typed request parts from the StartTask message (empty array when none provided). */
  readonly requestParts: RequestPart[];
  createStream: (options?: CreateStreamOptions) => Promise<StreamObject>;
  taskClient: TaskClient;
  cancelSignal: AbortSignal;
  readonly isCancelled: boolean;
  readonly isExpired: boolean;
  readonly hasStream: boolean;
  /** Consumer's public key for E2E encryption, if provided in the task submission. */
  readonly consumerPublicKey: string | undefined;
  /** Download the file/data from a request part's artifactRef.
   *  For inline: decodes base64 data. For file: uses pubnub.downloadFile(). */
  downloadInputArtifact: (part: RequestPart) => Promise<Buffer>;
  /** Publish an artifact mid-execution. Small data is inlined; large data
   *  uses the pre-signed URL upload flow (backend publishes the event). */
  publishArtifact: (
    data: Buffer | string,
    options?: { mimeType?: string; fileName?: string; outputId?: string },
  ) => Promise<void>;
}

export interface CancelTaskMessage {
  type: 'CancelTask';
  taskId: string;
  reason?: string;
  caller?: string;
  protocolVersion?: string;
}

export interface ExpireTaskMessage {
  type: 'ExpireTask';
  taskId: string;
  agentName?: string;
  reason?: string;
  protocolVersion?: string;
}

export interface SwitchEnvironmentMessage {
  type: 'SwitchEnvironment';
  environment: string;
  pamToken?: string;
  protocolVersion?: string;
}

export interface ControlMessage {
  type: 'PauseTask' | 'ResumeTask' | 'RetryTask' | 'TerminateTask';
  taskId: string;
  caller?: string;
  protocolVersion?: string;
}

type AnyControlMessage = InternalStartTaskMessage | CancelTaskMessage | ExpireTaskMessage | ControlMessage;

const extractOwnerId = (ownerId?: string, callerClaims?: Record<string, JsonValue>): string => {
  if (typeof ownerId === 'string' && ownerId.length > 0) return ownerId;
  const sub = callerClaims?.sub;
  if (typeof sub === 'string' && sub.length > 0) return sub;
  return 'anonymous';
};

const publishTaskEvent = async (
  pubnub: PubNub,
  taskId: string,
  ownerId: string,
  agentName: string,
  message: Record<string, JsonValue>,
  protocolVersion: string = CURRENT_PROTOCOL_VERSION,
): Promise<void> => {
  const cm = createChannelManager(agentName);
  try {
    await pubnub.publish({
      channel: cm.taskChannel(taskId, ownerId),
      message: toPayload({ ...message, protocolVersion }),
      storeInHistory: true,
      sendByPost: true,
      meta: toPayload({ agentName, taskId, protocolVersion }),
    });
  } catch {
    /* best effort */
  }
};

/**
 * True when the diag entry is "parked" — past the staleness threshold
 * AND not currently in the connected state. A long-silent
 * 'connected' entry is healthy by definition: at LOG_LEVEL=info
 * the SDK suppresses successful-heartbeat status events
 * (announceSuccessfulHeartbeats=false), so lastStatusAt does not
 * advance on a healthy idle client. Without the category gate, the
 * 10s diag timer would log "STALE clients present" every tick after
 * 60s of normal idleness — noisy false positive.
 *
 * Exported under the `_` convention for unit tests; not part of the
 * public SDK surface.
 */
export function _isDiagEntryStale(args: {
  lastStatusAt: number | null;
  lastCategory: string | null;
  now: number;
  thresholdMs: number;
}): boolean {
  const { lastStatusAt, lastCategory, now, thresholdMs } = args;
  if (lastStatusAt === null) return false;
  if (lastCategory === 'connected') return false;
  return now - lastStatusAt > thresholdMs;
}

const log = (
  level: 'debug' | 'info' | 'warn' | 'error',
  message: string,
  meta?: Record<string, unknown>,
): void => baseLog('[AgentInstance]', level, message, meta);

/**
 * Resolve the effective `maxRunningTimeSec` for an agent instance from its
 * two possible sources: the constructor option (`opts.maxRunningTimeSec`)
 * and the agent card's declared value (`card.runtime.maxRunningTimeSec`).
 *
 * Precedence is opts-first, card-fallback. When both are set and disagree,
 * opts wins (it's an explicit programmatic override) and the SDK emits a
 * one-time info-level log so the divergence is visible rather than silent.
 *
 * Exported for unit testing; callers inside this module should use the
 * resolved `effectiveMaxRunningTimeSec` constant rather than reading either
 * source directly.
 */
export function resolveMaxRunningTimeSec(
  optsValue: number | undefined,
  cardValue: number | undefined,
): number | undefined {
  if (
    optsValue !== undefined &&
    cardValue !== undefined &&
    optsValue !== cardValue
  ) {
    baseLog(
      '[AgentInstance]',
      'info',
      `opts.maxRunningTimeSec (${optsValue}) overrides card.runtime.maxRunningTimeSec (${cardValue})`,
      { event: 'max_running_time_override', optsValue, cardValue },
    );
  }
  return optsValue ?? cardValue;
}

/**
 * Compute the `durationMinutes` the SDK passes to the `streamSetup` Function
 * for a given task. Pure helper with no side effects — exported for testing.
 *
 * Rules:
 * - If the StartTask message carries an explicit `duration`, it wins.
 * - Pipe tasks with no `task.duration` fall back to 60 minutes.
 * - Request tasks with no `task.duration` derive from
 *   `effectiveMaxRunningTimeSec` via `Math.ceil(... / 60)`; when that value
 *   is absent the final fallback is 60 minutes (3600 seconds).
 */
export function computeStreamDurationMinutes(
  taskDuration: number | undefined,
  isPipeTask: boolean,
  effectiveMaxRunningTimeSec: number | undefined,
): number {
  if (typeof taskDuration === 'number') return taskDuration;
  if (isPipeTask) return 60;
  return Math.ceil((effectiveMaxRunningTimeSec ?? 3600) / 60);
}

/**
 * Publish a single artifact (inline or via pre-signed URL upload).
 * For inline artifacts: publishes the artifact event directly.
 * For uploaded artifacts: the backend publishes the event on confirm-upload.
 */
const publishOrUploadArtifact = async (
  taskPubNub: PubNub,
  taskId: string,
  orgId: string,
  agentName: string,
  data: Buffer,
  mimeType: string,
  fileName: string | undefined,
  outputId: string | undefined,
  auth: FileUploadAuth | undefined,
): Promise<void> => {
  const artifactBase: Record<string, JsonValue> = {
    type: 'artifact',
    taskId,
  };
  if (outputId) artifactBase.outputId = outputId;

  if (shouldInlineArtifact(data.length)) {
    // Small: build inline artifactRef and publish directly
    await publishTaskEvent(taskPubNub, taskId, orgId, agentName, {
      ...artifactBase,
      artifactRef: buildArtifactRef({ data, mimeType, fileName }) as unknown as JsonValue,
    });
  } else {
    // Large: pre-signed URL upload. Backend publishes the artifact event.
    if (!auth) {
      throw new Error(
        'Artifact size exceeds the inline threshold but no baseUrl is configured. ' +
        'Set baseUrl in AgentInstanceOptions to enable the pre-signed URL upload flow.',
      );
    }
    await uploadFile(
      auth,
      {
        role: 'provider-output',
        taskId,
        fileName: fileName ?? `${taskId}-artifact`,
        fileSize: data.length,
        mimeType,
        outputId,
      },
      data,
    );
    // Backend has published the typed artifact event on confirm-upload.
  }
};

/**
 * Perform the stream setup handshake by publishing to the setup channel
 * and extracting the T7a token from the 403 abort response.
 *
 * `affinity` is now required on the wire (see ssl-wire): every
 * stream_setup publish MUST carry it or the Function validator will
 * reject the message with `InvalidArgument`.
 */
async function performStreamSetup(
  taskPubNub: PubNub,
  opts: {
    taskId: string;
    orgId: string;
    agentName: string;
    streamId: string;
    channel: string;
    direction: string;
    format: string;
    taskKind: string;
    durationMinutes: number;
    affinity: 'dedicated' | 'shared';
    phase?: string;
    metadata?: Record<string, unknown>;
    declaredStream?: string;
  },
): Promise<{ token?: string; tokenTtlMinutes: number }> {
  const setupChannel = `setup.${opts.orgId}.${opts.taskId}`;
  const setupMsg: Record<string, unknown> = {
    type: 'stream_setup',
    taskId: opts.taskId,
    orgId: opts.orgId,
    agentName: opts.agentName,
    streamId: opts.streamId,
    channel: opts.channel,
    direction: opts.direction,
    format: opts.format,
    taskKind: opts.taskKind,
    durationMinutes: opts.durationMinutes,
    affinity: opts.affinity,
    declaredStream: opts.declaredStream ?? '_default',
    protocolVersion: CURRENT_PROTOCOL_VERSION,
  };
  if (opts.phase) setupMsg.phase = opts.phase;
  if (opts.metadata) setupMsg.metadata = opts.metadata;

  try {
    await taskPubNub.publish({
      channel: setupChannel,
      message: toPayload(setupMsg as Record<string, JsonValue>),
      storeInHistory: false,
      sendByPost: true,
    });
    throw new Error('stream_setup publish should have been aborted by Function');
  } catch (err: unknown) {
    // The 403 is expected -- the Function aborts with the response payload
    const setupError = parseStreamSetupError(err);
    if (setupError) {
      throw new Error(`Stream setup failed: [${setupError.code}] ${setupError.message}`);
    }
    const result = parseStreamSetupResponse(err);
    if (result) {
      return { token: result.token, tokenTtlMinutes: result.tokenTtlMinutes };
    }
    // Re-throw if we can't parse the response
    throw err;
  }
}

export interface AgentInstanceHandle {
  stop: () => void;
  agentName: string;
  instanceId: string;
  controlChannel: string | undefined;
  pubnub: PubNub;
  subscribeKey: string;
  taskClient: TaskClient;
  publishTerminal: (taskId: string, event: Record<string, JsonValue>) => Promise<void>;
  failStream: (streamId: string, reason: string) => Promise<void>;
  cdmConfig: CdmConfig | null;
}

export const startAgentInstance = async (
  opts: AgentInstanceOptions,
): Promise<AgentInstanceHandle> => {
  const agentName = opts.agentName;
  if (!agentName) throw new Error('agentName is required: provide opts.agentName');
  if (!/^[a-zA-Z0-9_]+$/.test(agentName)) {
    throw new Error('agentName must contain only alphanumeric characters and underscores (no hyphens)');
  }

  const cm = createChannelManager(agentName);
  const instanceId = opts.instanceId ?? `AG-${agentName}-${crypto.randomUUID()}`;
  const token = opts.token;

  // -- Fetch CDM config -------------------------------------------------------
  let cdmConfig: CdmConfig | null = null;
  let envKeysets: Record<PnEnvironment, CdmKeyset>;
  let primaryEnv: PnEnvironment = 'playground';
  let activeEnv: PnEnvironment = primaryEnv;

  if (opts.pubnub) {
    // External PubNub client — extract its keys so per-task clients can
    // connect to the same PubNub app. Both environments use the same
    // keyset since an injected client doesn't support environment switching.
    // Uses PubNub JS SDK internal _configuration.keySet (verified against pubnub@8.x).
    // No public getter exists as of pubnub@8.x.
    const keySet = (opts.pubnub as unknown as { _configuration?: { keySet?: { publishKey?: string; subscribeKey?: string } } })._configuration?.keySet;
    const keyset: CdmKeyset = {
      publishKey: keySet?.publishKey ?? '',
      subscribeKey: keySet?.subscribeKey ?? '',
    };
    envKeysets = { playground: keyset, network: keyset };
  } else {
    cdmConfig = await fetchCdmConfig(opts.cdmUrl);
    envKeysets = {
      playground: cdmConfig.playground,
      network: cdmConfig.network,
    };
    log('info', 'CDM config loaded — environment switching enabled', {
      event: 'cdm_config_loaded',
    });
  }

  // -- Initialize AgentAuth (API key-based auth) — BLOCKS_API_KEY is required --
  const apiKey = getEnv('BLOCKS_API_KEY');
  if (!apiKey) {
    throw new Error(
      'BLOCKS_API_KEY is required. Run \'blocks login --write-env\' to set up credentials.',
    );
  }
  let agentAuth: AgentAuth | undefined;
  const baseUrl = opts.baseUrl ?? cdmConfig?.api.baseUrl;
  if (apiKey && baseUrl) {
    agentAuth = new AgentAuth(apiKey, baseUrl);
    log('info', 'AgentAuth created — will authenticate via connect', {
      event: 'agent_auth_created',
    });
  }

  // Resolve billingMode from the registry. The registry is authoritative for
  // billing mode. To change billing mode, update the agent in the registry
  // and restart the process — there is no provider-side override path.
  //
  // The resolved value drives:
  //   1. Keyset selection (`free` -> playground, `paid` -> network)
  //   2. The `billingMode` field forwarded into the connect payload
  //
  // When an external PubNub client is injected (opts.pubnub), CDM config is
  // skipped, the registry lookup is skipped, and we default to 'free'.
  // Production agents always run via the CDM path which performs the
  // registry GET and surfaces missing billingMode as a startup error.
  let registryBillingMode: 'free' | 'paid' | undefined;
  let registryListing: 'private' | 'public' | undefined;
  if (cdmConfig) {
    const registryBaseUrl = opts.baseUrl ?? cdmConfig.api.baseUrl;
    const agentEntry = await getAgent(agentName, {
      baseUrl: registryBaseUrl,
      apiKey,
    });
    if (!agentEntry) {
      throw new Error(
        `[AgentInstance] Agent "${agentName}" not found in registry. ` +
          'Register the agent (e.g. via `blocks publish`) before starting an instance.',
      );
    }
    if (!agentEntry.billingMode) {
      throw new Error(
        `[AgentInstance] Agent "${agentName}" registry entry is missing billingMode. ` +
          'Re-register the agent so the registry persists an explicit billingMode.',
      );
    }
    registryBillingMode = agentEntry.billingMode;
    registryListing = agentEntry.listing;
    primaryEnv = registryBillingMode === 'paid' ? 'network' : 'playground';
    log('info', `registry billingMode: ${registryBillingMode} — using ${primaryEnv} environment`, {
      event: 'registry_billing_mode_resolved',
      billingMode: registryBillingMode,
      environment: primaryEnv,
    });
    activeEnv = primaryEnv;
  }
  // Effective billingMode for the connect payload + consumer TaskClient.
  // Falls back to 'free' only on the injected-PubNub test path (no CDM,
  // no registry GET). Production paths always populate registryBillingMode
  // from the registry above and fail loudly if it's missing.
  const effectiveBillingMode: 'free' | 'paid' = registryBillingMode ?? 'free';

  // === Connectivity diagnostics (gated) ===
  //
  // Reconnect-investigation surface. Default OFF — set
  // BLOCKS_DEBUG_INTERNAL=diagnostics to enable. When OFF every hook
  // below is a no-op: no listener attached, no timer armed, no
  // per-status emission, no transport_diagnostics_armed boot line. This
  // keeps transport-internal vocabulary out of default-level production
  // logs and removes the diag listener/timer overhead from the
  // steady-state path.
  interface ClientDiag {
    label: string;
    pn: PubNub;
    /**
     * The diagnostic listener handle. Stored so untrackClient can
     * removeListener — without this, the listener leaks on the PubNub
     * client and keeps emitting after switchEnvironment() / stop().
     * Especially load-bearing when opts.pubnub is externally supplied
     * because the SDK does not own the client's lifetime.
     */
    listener: Parameters<PubNub['addListener']>[0];
    lastStatusAt: number | null;
    lastConnectedAt: number | null;
    lastMessageAt: number | null;
    lastCategory: string | null;
    lastOperation: TransportOperation | null;
    lastStatusCode: number | null;
  }

  const diagRegistry: ClientDiag[] = [];
  let diagAliveTimer: ReturnType<typeof setInterval> | null = null;
  const DIAG_SNAPSHOT_INTERVAL_MS = 10_000;
  const DIAG_STALE_THRESHOLD_MS = 60_000;
  const diagEnabled = isDebugSubsystemEnabled('diagnostics');

  // Edge-triggered: emit `transport_degraded` only on the entry into the
  // degraded set, and `transport_restored` only on the first non-degraded
  // status after a degraded one. PubNub's Event Engine fires status events
  // repeatedly during a sustained outage (per failed handshake / receive),
  // so a stateless listener would spam warn lines and dilute the
  // "agent retrying vs. dead" signal these events exist to provide.
  const buildConnectivityListener = () => {
    let degraded = false;
    return {
      status: (event: unknown) => {
        const e = event as Record<string, unknown>;
        const category = mapTransportCategory(e as TransportStatusPayload);
        const isDegraded = DEGRADED_TRANSPORT_CATEGORIES.has(category);
        if (isDegraded && !degraded) {
          degraded = true;
          log('warn', `connectivity degraded: ${category}`, {
            event: 'transport_degraded',
            category,
            instanceId,
          });
        } else if (!isDegraded && degraded && RESTORED_TRANSPORT_CATEGORIES.has(category)) {
          degraded = false;
          log('info', 'connectivity restored', {
            event: 'transport_restored',
            category,
            instanceId,
          });
        }
      },
    };
  };

  const buildDiagListener = (entry: ClientDiag) => ({
    status: (event: unknown) => {
      const e = event as Record<string, unknown>;
      const category = mapTransportCategory(e as TransportStatusPayload);
      const operation = mapTransportOperation(
        typeof e.operation === 'string' ? e.operation : undefined,
      );
      const statusCode = typeof e.statusCode === 'number' ? e.statusCode : null;
      const now = Date.now();
      const previousCategory = entry.lastCategory;
      entry.lastStatusAt = now;
      entry.lastCategory = category;
      entry.lastOperation = operation;
      entry.lastStatusCode = statusCode;
      if (category === 'connected') entry.lastConnectedAt = now;

      log('debug', 'transport status event', {
        event: 'transport_status',
        client: entry.label,
        category,
        operation,
        statusCode,
        error: Boolean(e.error),
        affectedChannels: Array.isArray(e.affectedChannels)
          ? e.affectedChannels.length
          : undefined,
        instanceId,
      });
      if (category && category !== previousCategory) {
        log(
          'info',
          `transport status transition [${entry.label}]: ${previousCategory ?? '(none)'} -> ${category}`,
          {
            event: 'transport_status_transition',
            client: entry.label,
            from: previousCategory,
            to: category,
            operation,
            statusCode,
            instanceId,
          },
        );
      }
    },
    message: () => {
      entry.lastMessageAt = Date.now();
    },
  });

  const trackClient = (label: string, pn: PubNub): void => {
    if (!diagEnabled) return;
    const entry: ClientDiag = {
      label,
      pn,
      listener: undefined as unknown as ClientDiag['listener'],
      lastStatusAt: null,
      lastConnectedAt: null,
      lastMessageAt: null,
      lastCategory: null,
      lastOperation: null,
      lastStatusCode: null,
    };
    entry.listener = buildDiagListener(entry);
    diagRegistry.push(entry);
    pn.addListener(entry.listener);
  };

  const untrackClient = (pn: PubNub): void => {
    if (!diagEnabled) return;
    const idx = diagRegistry.findIndex((e) => e.pn === pn);
    if (idx < 0) return;
    const entry = diagRegistry[idx];
    try {
      pn.removeListener(entry.listener);
    } catch {
      /* listener may already be detached if pn.destroy() was called */
    }
    diagRegistry.splice(idx, 1);
  };

  const startDiagAliveTimer = (): void => {
    if (!diagEnabled) return;
    if (diagAliveTimer !== null) return;
    diagAliveTimer = setInterval(() => {
      const now = Date.now();
      let anyStale = false;
      const snapshots = diagRegistry.map((e) => {
        const msSinceStatus = e.lastStatusAt !== null ? now - e.lastStatusAt : null;
        const stale = _isDiagEntryStale({
          lastStatusAt: e.lastStatusAt,
          lastCategory: e.lastCategory,
          now,
          thresholdMs: DIAG_STALE_THRESHOLD_MS,
        });
        if (stale) anyStale = true;
        const subscribed =
          typeof (e.pn as unknown as { getSubscribedChannels?: () => string[] })
            .getSubscribedChannels === 'function'
            ? (e.pn as unknown as { getSubscribedChannels: () => string[] })
                .getSubscribedChannels().length
            : null;
        return {
          client: e.label,
          lastCategory: e.lastCategory,
          lastOperation: e.lastOperation,
          lastStatusCode: e.lastStatusCode,
          msSinceStatus,
          msSinceConnected:
            e.lastConnectedAt !== null ? now - e.lastConnectedAt : null,
          msSinceMessage:
            e.lastMessageAt !== null ? now - e.lastMessageAt : null,
          subscribedChannels: subscribed,
          stale,
        };
      });
      const meta = { event: 'transport_alive_snapshot', instanceId, clients: snapshots };
      if (anyStale) {
        log('info', 'transport alive snapshot — STALE clients present', meta);
      } else {
        log('debug', 'transport alive snapshot', meta);
      }
    }, DIAG_SNAPSHOT_INTERVAL_MS);
    if (typeof diagAliveTimer.unref === 'function') diagAliveTimer.unref();
  };

  // === TIER 1: Single Active Control Client ===
  let controlClient: PubNub;
  let ownsControlClient = false;

  if (opts.pubnub) {
    controlClient = opts.pubnub;
    ownsControlClient = false;
    if (token) controlClient.setToken(token);
  } else {
    const ks = envKeysets[activeEnv];
    controlClient = createPubNubClient({
      publishKey: ks.publishKey,
      subscribeKey: ks.subscribeKey,
      userId: instanceId,
      presenceTimeout: 20,
      announceSuccessfulHeartbeats: _resolveLogLevel() >= _LOG_LEVEL_ORDER.debug,
      subscribeRetryUnbounded: true,
    });
    ownsControlClient = true;
    if (token) controlClient.setToken(token);
  }
  // NOTE: trackClient('control', ...) is invoked after the main listener
  // is registered so test fixtures that index into listeners[0] continue
  // to hit the message handler, not the diag passthrough.

  let subscribeKey = envKeysets[activeEnv].subscribeKey;
  let publishKey = envKeysets[activeEnv].publishKey;

  // Consumer-facing TaskClient (reusable across tasks).
  // Uses ConsumerAuth (lazy-initialized via ensureReady) so A2A calls
  // authenticate as the agent's owning user via /api/v1/auth/agent/consumer-token.
  // Only created when baseUrl is available (production path via CDM config).
  // Tests that inject opts.pubnub skip CDM, so baseUrl may be undefined.
  const consumerAuth = baseUrl
    ? new ConsumerAuth({ apiKey, baseUrl })
    : undefined;

  const consumerTaskClient = new TaskClient({
    billingMode: effectiveBillingMode,
    subscribeKey,
    publishKey,
    authProvider: consumerAuth,
    baseUrl,
    createPubNub: () => {
      const pn = createPubNubClient({
        publishKey,
        subscribeKey,
        userId: `${instanceId}-taskclient`,
        subscribeRetryUnbounded: false,
      });
      trackClient('consumer-task', pn);
      return pn;
    },
  });

  const concurrency = opts.concurrency ?? DEFAULTS.concurrency;
  const expectedInstances = opts.expectedInstances ?? DEFAULTS.expectedInstances;
  const maxPendingBacklog = opts.maxPendingBacklog;
  // Single source of truth for max-running-time: reconcile opts with the
  // card's declared value at startup so the connect scaling payload and
  // the per-task stream TTL derivation can't drift. See Fix B in
  // dev_docs/initiative/t7c_token_lifecycle/T7C_TOKEN_LIFECYCLE_IMPL.md.
  const effectiveMaxRunningTimeSec = resolveMaxRunningTimeSec(
    opts.maxRunningTimeSec,
    opts.card?.runtime?.maxRunningTimeSec,
  );
  const startedAt = Date.now();

  // Instance-level state
  let activeTaskCount = 0;
  const streamRegistry = new StreamRegistry();
  const credentialCache = new CredentialCache();
  // Per-agent idempotent handle cache for shared-affinity streams.
  // Keyed by streamId -> taskId -> StreamObject so a second
  // `createStream()` call from the SAME task for the SAME shared stream
  // short-circuits at the cache layer (fix e). Populated on the new /
  // new-for-task path in createStream, evicted at every cleanup
  // boundary (local end(), releaseAllForTask, failStream, stop()).
  const sharedStreamHandles = new Map<string, Map<string, StreamObject>>();
  const taskOwnerMap = new Map<string, string>();
  const taskOrgMap = new Map<string, string>();
  const taskStatusMap = new Map<string, string>();
  const taskCancelControllers = new Map<string, AbortController>();
  const expiredTasks = new Set<string>();
  const terminatedTasks = new Set<string>();
  const durationTimers = new Map<string, ReturnType<typeof setTimeout>>();
  const inflight = new Set<string>();
  // Per-task unnamed stream counters
  const taskStreamCounters = new Map<string, number>();

  void opts.logChannel;

  let latestControlToken: string | undefined;
  let controlChannel: string | undefined;

  const updatePresenceState = async (): Promise<void> => {
    if (typeof controlClient.setState !== 'function') return;
    if (!controlChannel) return;
    try {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      await (controlClient as any).setState({
        channels: [controlChannel],
        state: {
          instanceId,
          activeTasks: activeTaskCount,
          activeStreams: streamRegistry.activeStreamCount,
          concurrency,
          startedAt,
          preferredProtocolVersion: CURRENT_PROTOCOL_VERSION,
          protocolVersions: [...SUPPORTED_PROTOCOL_VERSIONS],
        },
      });
    } catch {
      /* best effort */
    }
  };

  // === Shared-stream handle cache helpers ===
  //
  // These helpers centralize cache-eviction at every cleanup boundary
  // listed in IMPL §Fix (d). Keeping the mutations behind named helpers
  // makes it easy to audit that every boundary maintains the invariant
  // "cache entry iff registry entry still holds the (streamId, taskId)".
  const evictSharedHandle = (streamId: string, taskId: string): void => {
    const perStream = sharedStreamHandles.get(streamId);
    if (!perStream) return;
    perStream.delete(taskId);
    if (perStream.size === 0) sharedStreamHandles.delete(streamId);
  };

  const evictSharedHandlesForStream = (streamId: string): void => {
    sharedStreamHandles.delete(streamId);
  };

  const evictSharedHandlesForTask = (taskId: string): void => {
    for (const [streamId, perStream] of sharedStreamHandles) {
      if (perStream.has(taskId)) {
        perStream.delete(taskId);
        if (perStream.size === 0) sharedStreamHandles.delete(streamId);
      }
    }
  };

  // Atomic "release every stream this task holds" helper. Bundles the
  // three operations that every cleanup boundary must pair — handle-cache
  // eviction, registry release, and last-ref StreamClient.end() — into
  // a single call so a new cleanup site cannot forget one leg. See
  // QUESTIONS.md D4 (shared_stream_lifecycle).
  const releaseAllStreamsForTask = async (taskId: string): Promise<void> => {
    evictSharedHandlesForTask(taskId);
    const destroyed = streamRegistry.releaseAllForTask(taskId);
    for (const entry of destroyed) {
      if (entry.streamClient) {
        try {
          await entry.streamClient.end();
        } catch {
          /* best-effort teardown */
        }
      }
    }
  };

  // === releaseStream implementation (fix d task-scoped end()) ===
  //
  // Routed here by `StreamObject.end()` via the hooks passed into
  // createStreamObject. On a shared stream this decrements the registry
  // refcount and evicts the handle from the cache; the writer is torn
  // down ONLY when the last ref-holder releases, and even then WITHOUT
  // publishing `stream_end` (the StreamClient.end() affinity gate
  // suppresses the marker — see stream-client.ts).
  //
  // On dedicated streams this is indistinguishable from the pre-fix
  // path: registry.release finds a single task in taskIds, the snapshot
  // lets us end() the writer, and the marker flows normally.
  const releaseStreamImpl = async (streamId: string, taskId: string): Promise<void> => {
    evictSharedHandle(streamId, taskId);
    // Snapshot the entry before the pure-bookkeeping release so we
    // can tear down at the agent-instance layer when the last ref
    // drops. Mirrors the Python SDK's release_stream shape.
    const entry = streamRegistry.get(streamId);
    const remaining = streamRegistry.release(streamId, taskId);
    if (remaining === 0 && entry?.streamClient) {
      // Distinguish last-ref teardown from non-last release for ops.
      // On shared-affinity streams StreamClient.end() suppresses the
      // marker publish; the teardown still closes the local writer.
      // See QUESTIONS.md I1 (shared_stream_lifecycle).
      log('info', 'last-ref teardown', {
        event: 'stream_registry_last_ref_teardown',
        streamId,
        affinity: entry.affinity,
        releasingTaskId: taskId,
      });
      try {
        await entry.streamClient.end();
      } catch {
        /* best-effort teardown */
      }
    }
    // Publish an updated presence state so `activeStreams` on the
    // control channel reflects the drop. Matches Python's
    // `release_stream` behavior (cross-SDK parity). Fire-and-forget
    // with a catch so a transient presence publish failure doesn't
    // surface as an unhandled rejection.
    updatePresenceState().catch(() => {});
  };

  // === failStream implementation ===
  const failStreamImpl = async (streamId: string, reason: string): Promise<void> => {
    const entry = streamRegistry.forceRemove(streamId);
    // Cache eviction fires regardless of whether the entry existed
    // (belt-and-suspenders: stale cache is a latent bug).
    evictSharedHandlesForStream(streamId);
    if (!entry) return;

    // End the stream client
    if (entry.streamClient) {
      try {
        await entry.streamClient.end();
      } catch {
        /* ignore */
      }
    }

    // Publish failed terminal to all mapped tasks
    for (const taskId of entry.taskIds) {
      const creds = credentialCache.get(taskId);
      if (!creds) continue;
      try {
        const envKeys =
          envKeysets[(creds.environment as PnEnvironment) ?? primaryEnv] ?? envKeysets[primaryEnv];
        const ephemeral = createPubNubClient({
          publishKey: envKeys.publishKey,
          subscribeKey: envKeys.subscribeKey,
          userId: instanceId,
          subscribeRetryUnbounded: false,
        });
        ephemeral.setToken(creds.writeToken);
        await publishTaskEvent(ephemeral, taskId, creds.orgId, creds.agentName, {
          type: 'terminal',
          taskId,
          state: 'failed',
          error: reason,
        });
        ephemeral.destroy();
      } catch {
        /* best effort */
      }
      credentialCache.remove(taskId);
    }

    await updatePresenceState();
  };

  // === publishTerminal implementation ===
  const publishTerminalImpl = async (
    taskId: string,
    event: Record<string, JsonValue>,
  ): Promise<void> => {
    const creds = credentialCache.get(taskId);
    if (!creds) {
      throw new Error(`No cached credentials for task ${taskId}`);
    }

    // Create ephemeral connection using the environment from cached credentials
    const envKeys =
      envKeysets[(creds.environment as PnEnvironment) ?? primaryEnv] ?? envKeysets[primaryEnv];
    const ephemeral = createPubNubClient({
      publishKey: envKeys.publishKey,
      subscribeKey: envKeys.subscribeKey,
      userId: instanceId,
      subscribeRetryUnbounded: false,
    });
    ephemeral.setToken(creds.writeToken);

    try {
      await publishTaskEvent(ephemeral, taskId, creds.orgId, creds.agentName, {
        ...event,
        type: 'terminal',
        taskId,
      });
    } finally {
      ephemeral.destroy();
    }

    await releaseAllStreamsForTask(taskId);

    credentialCache.remove(taskId);
    await updatePresenceState();
  };

  // === Handler execution ===
  const executeHandler = async (
    task: StartTaskMessage,
    taskPubNub: PubNub,
    ownerId: string,
    orgId: string,
    controller: AbortController,
  ): Promise<void> => {
    const effectiveAgentName = agentName;
    const isPipeTask = task.taskKind === 'pipe';
    const taskKind = isPipeTask ? 'pipe' : 'request';
    // Use duration from StartTask message (set by messageSend for pipe tasks),
    // fall back to defaults when not provided. Request-task default derives
    // from the card's runtime.maxRunningTimeSec (via the resolver above);
    // 60 minutes is the final fallback when neither opts nor card set it.
    const durationMinutes = computeStreamDurationMinutes(
      task.duration,
      isPipeTask,
      effectiveMaxRunningTimeSec,
    );

    // Start local duration timer for pipe tasks.
    // Uses server-computed durationExpiresAtMs for clock alignment.
    if (isPipeTask && typeof task.durationExpiresAtMs === 'number') {
      const delayMs = Math.max(0, task.durationExpiresAtMs - Date.now());
      if (delayMs > 0) {
        const timer = setTimeout(() => {
          expiredTasks.add(task.taskId);
          controller.abort();
        }, delayMs);
        durationTimers.set(task.taskId, timer);
      }
    }

    await publishTaskEvent(taskPubNub, task.taskId, orgId, effectiveAgentName, {
      type: 'progress',
      taskId: task.taskId,
      progress: 0,
      state: 'running',
    });

    // Per-task unnamed stream counter
    taskStreamCounters.set(task.taskId, 0);

    // reportStatus throttle state: publish at most once per second,
    // buffer the latest message so it is never silently dropped.
    let lastStatusTime = 0;
    let pendingStatusMsg: string | null = null;
    let statusTimer: ReturnType<typeof setTimeout> | null = null;

    const flushStatus = (): void => {
      if (pendingStatusMsg === null) return;
      const msg = pendingStatusMsg;
      pendingStatusMsg = null;
      lastStatusTime = Date.now();
      publishTaskEvent(taskPubNub, task.taskId, orgId, effectiveAgentName, {
        type: 'progress',
        taskId: task.taskId,
        message: msg,
      }).catch(() => { /* best effort */ });
    };

    const taskContext: TaskContext = {
      taskId: task.taskId,
      requestParts: task.requestParts ?? [],
      reportStatus: (message: string) => {
        taskStatusMap.set(task.taskId, message);
        const now = Date.now();
        if (now - lastStatusTime >= 1000) {
          // Outside throttle window -- publish immediately.
          pendingStatusMsg = null;
          lastStatusTime = now;
          publishTaskEvent(taskPubNub, task.taskId, orgId, effectiveAgentName, {
            type: 'progress',
            taskId: task.taskId,
            message,
          }).catch(() => { /* best effort */ });
        } else {
          // Inside throttle window -- buffer the latest and schedule flush.
          pendingStatusMsg = message;
          if (!statusTimer) {
            const delay = 1000 - (now - lastStatusTime);
            statusTimer = setTimeout(() => {
              statusTimer = null;
              flushStatus();
            }, delay);
          }
        }
      },
      get isCancelled(): boolean {
        return controller.signal.aborted;
      },
      get isExpired(): boolean {
        return expiredTasks.has(task.taskId);
      },
      hasStream: !!task.hasStream,
      cancelSignal: controller.signal,
      taskClient: consumerTaskClient,
      consumerPublicKey: task.consumerPublicKey,

      // Download file data from a request part's artifactRef
      downloadInputArtifact: async (part: RequestPart): Promise<Buffer> => {
        const ref = part.artifactRef;
        if (!ref) {
          throw new Error(`Request part has no artifactRef (partId: ${part.partId ?? 'none'})`);
        }
        if (ref.kind === 'inline') {
          return Buffer.from(decodeInlineArtifact(ref));
        }
        // kind === 'file': download via PubNub SDK
        if (!ref.channel || !ref.fileId || !ref.fileName) {
          throw new Error('File artifactRef missing channel, fileId, or fileName');
        }
        if (!taskPubNub.downloadFile) {
          throw new Error('PubNub client does not support downloadFile');
        }
        const result = await taskPubNub.downloadFile({
          channel: ref.channel,
          id: ref.fileId,
          name: ref.fileName,
        });
        // PubNub downloadFile returns { data: Readable | Buffer | Blob | ... }
        // depending on platform and SDK version.
        const fileData = result?.data as unknown;
        if (Buffer.isBuffer(fileData)) return fileData;
        if (fileData instanceof Uint8Array) return Buffer.from(fileData);
        // Handle Blob or Readable with arrayBuffer method (Node 18+ fetch compat)
        if (fileData && typeof (fileData as { arrayBuffer?: unknown }).arrayBuffer === 'function') {
          const ab = await (fileData as { arrayBuffer: () => Promise<ArrayBuffer> }).arrayBuffer();
          return Buffer.from(ab);
        }
        throw new Error('Unexpected downloadFile response format');
      },

      // Publish artifact mid-execution
      publishArtifact: async (
        data: Buffer | string,
        options?: { mimeType?: string; fileName?: string; outputId?: string },
      ): Promise<void> => {
        const buf = typeof data === 'string' ? Buffer.from(data, 'utf-8') : data;
        const mimeType = options?.mimeType ?? 'application/octet-stream';
        const fileUploadAuth: FileUploadAuth | undefined = baseUrl
          ? { baseUrl, authProvider: token ? new StaticAuthProvider(token) : undefined, agentAuth }
          : undefined;
        await publishOrUploadArtifact(
          taskPubNub,
          task.taskId,
          orgId,
          effectiveAgentName,
          buf,
          mimeType,
          options?.fileName,
          options?.outputId,
          fileUploadAuth,
        );
      },

      // Unified createStream API
      createStream: async (
        csOpts?: CreateStreamOptions,
      ): Promise<StreamObject> => {
        if (
          csOpts !== undefined &&
          (typeof csOpts !== 'object' || csOpts === null || Array.isArray(csOpts))
        ) {
          throw new TypeError(
            'createStream expects an options object. The positional streamId ' +
              'argument was removed; move any declared-stream key into ' +
              'CreateStreamOptions.declaredStream. See SDK_CONTRACT §8.2.1.',
          );
        }
        if (!task.hasStream) {
          throw new Error(
            'Streaming was not negotiated for this task. ' +
              'Ensure the agent card is registered with streaming capability.',
          );
        }

        // -- Card stream affinity enforcement (Fix 10) --
        const cardStreams = opts.card.streams;
        if (!cardStreams || Object.keys(cardStreams).length === 0) {
          throw new Error(
            'Agent card has no streams block. Streaming requires declared streams in the card.',
          );
        }

        const streamKeys = Object.keys(cardStreams);

        // Resolve declaredStream key
        let declaredStream: string;
        if (csOpts?.declaredStream) {
          declaredStream = csOpts.declaredStream;
        } else if (streamKeys.length === 1) {
          // Single stream allows omitting declaredStream
          declaredStream = streamKeys[0];
        } else {
          throw new Error(
            `Card declares multiple streams (${streamKeys.join(', ')}). ` +
            `Specify declaredStream in CreateStreamOptions.`,
          );
        }

        // Look up declared stream in card
        const cardDecl = cardStreams[declaredStream];
        if (!cardDecl) {
          throw new Error(
            `Stream "${declaredStream}" is not declared in the agent card. ` +
            `Available streams: ${streamKeys.join(', ')}`,
          );
        }

        // Use card values as defaults, allow explicit overrides only if they match
        const direction = csOpts?.direction ?? (cardDecl.direction as 'outbound' | 'inbound' | 'bidirectional');
        const format = csOpts?.format ?? (cardDecl.format as 'bytes' | 'events');

        // Validate that explicit values don't conflict with card
        if (csOpts?.direction && csOpts.direction !== cardDecl.direction) {
          throw new Error(
            `Direction "${csOpts.direction}" conflicts with card declaration "${cardDecl.direction}" ` +
            `for stream "${declaredStream}".`,
          );
        }
        if (csOpts?.format && csOpts.format !== cardDecl.format) {
          throw new Error(
            `Format "${csOpts.format}" conflicts with card declaration "${cardDecl.format}" ` +
            `for stream "${declaredStream}".`,
          );
        }

        const external = csOpts?.external ?? false;
        const metadata = csOpts?.metadata;
        const onActivate = csOpts?.onActivate;

        // Resolve affinity up-front so fix (g) / fix (e) can consult it.
        const affinity: 'dedicated' | 'shared' =
          (cardDecl.affinity as 'dedicated' | 'shared' | undefined) ?? 'dedicated';

        // Fix (h): shared-affinity + external is a design contradiction.
        // Shared affinity is "one broadcast writer, many ref-holding
        // tasks"; external streams delegate the writer to an
        // external process entirely. In the shared+external
        // combination there is no single writer in the SDK's registry
        // model — each task would hand its own T7a to a different
        // external process, all writing the same broadcast channel.
        // Fail fast before the registry / handshake state gets
        // touched. A future initiative can model external broadcast
        // explicitly (see GitHub #516). Rejects BOTH pipe and request
        // task kinds; the request-task reject below also catches the
        // request variant but throws with a different message — keep
        // this check above the task-kind branch so either kind hits a
        // clear error.
        if (affinity === 'shared' && external) {
          throw new Error(
            `Shared-affinity external streams are not supported. ` +
            `Declared stream '${declaredStream}' has affinity: 'shared' ` +
            `and createStream was called with external: true. Shared ` +
            `affinity requires a single SDK-managed writer with ` +
            `per-task ref-counting; external streams delegate the ` +
            `writer entirely. Use affinity: 'dedicated' with ` +
            `external: true, or affinity: 'shared' without external.`,
          );
        }

        // Request-task constraints
        if (!isPipeTask) {
          // Fix (g): shared-affinity streams are inherently multi-task
          // (broadcast/fan-out). Request tasks are single-shot; cross-
          // task sharing is moot. Fail fast with a clear error before
          // any registry / handshake state is touched.
          if (affinity === 'shared') {
            throw new Error(
              `Shared-affinity streams are not supported on request tasks. ` +
              `Declared stream '${declaredStream}' has affinity: 'shared'. ` +
              `Request tasks are single-shot; cross-task broadcast is ` +
              `inherently a pipe-task concept. Use affinity: 'dedicated' ` +
              `or remove 'request' from the agent card's taskKinds.`,
            );
          }
          if (direction !== 'outbound') {
            throw new Error('Request tasks only support outbound streams');
          }
          if (external) {
            throw new Error('Request tasks cannot use external streams');
          }
        }

        // Determine stream ID based on affinity.
        // streamId is always SDK-derived, never caller-supplied:
        //   - shared affinity    -> card-declared key (constant across tasks)
        //   - dedicated affinity -> `${taskId}-${counter}` (per-task auto-scoped)
        let streamId: string;

        if (affinity === 'shared') {
          streamId = declaredStream;
        } else {
          const counter = (taskStreamCounters.get(task.taskId) ?? 0) + 1;
          taskStreamCounters.set(task.taskId, counter);
          streamId = `${task.taskId}-${counter}`;
        }

        const streamChannel = cm.streamChannel(streamId);

        // Fix (e): idempotent shortcut. A second createStream() call
        // from the same task for the same shared stream returns the
        // cached StreamObject without re-entering the registry. This
        // guarantees repeat calls never grow taskIds, never re-publish
        // `stream_setup`, and never overwrite the Functions KV
        // consumerToken slot. Dedicated streams don't cache here
        // because each call produces a unique streamId via the counter.
        if (affinity === 'shared') {
          const cachedForTask = sharedStreamHandles.get(streamId)?.get(task.taskId);
          if (cachedForTask) return cachedForTask;
        }

        // Check registry for existing entry
        const { entry, isNew, isNewForTask } = streamRegistry.acquire(streamId, task.taskId, {
          direction,
          format,
          external,
          affinity,
        });

        // Track in credential cache
        credentialCache.addStream(task.taskId, streamId);

        // Fix (b) helper: publish stream_setup { phase: 'activate' } for
        // a task attaching to an EXISTING shared writer so the Function
        // mints a per-task T7c and emits stream_started on this task's
        // status channel.
        const publishSharedActivate = async (): Promise<void> => {
          await performStreamSetup(taskPubNub, {
            taskId: task.taskId,
            orgId,
            agentName: effectiveAgentName,
            streamId,
            channel: streamChannel,
            direction,
            format,
            taskKind,
            durationMinutes,
            affinity,
            phase: 'activate',
            metadata,
            declaredStream,
          });
        };

        const cacheSharedHandle = (so: StreamObject): void => {
          if (affinity !== 'shared') return;
          let perStream = sharedStreamHandles.get(streamId);
          if (!perStream) {
            perStream = new Map();
            sharedStreamHandles.set(streamId, perStream);
          }
          perStream.set(task.taskId, so);
        };

        if (!isNewForTask) {
          // Same-task reacquire on an existing entry. Fix (e) says a
          // second `createStream()` from the SAME task for the SAME
          // shared stream returns the EXACT same `StreamObject` the
          // first call returned, with no registry mutation and no
          // extra setup publish.
          //
          // The top-of-function cache lookup (`sharedStreamHandles`)
          // handles sequential same-task reacquires because the first
          // call populates the cache before returning. CONCURRENT
          // same-task calls (e.g. `Promise.all([ctx.createStream(...),
          // ctx.createStream(...)])`) land here because the second
          // call enters createStream before the first's setup has
          // cached its handle — cache miss, then registry.acquire
          // correctly reports isNewForTask=false.
          //
          // We MUST NOT fall through to the `!isNew` branch below —
          // that publishes `phase: 'activate'` for this task, which
          // duplicates the first call's setup and mints a second T7c
          // for the SAME task. Wait for the first acquirer's setup
          // instead, then return the handle it cached.
          if (entry.setupPromise) {
            await entry.setupPromise;
          }
          const cached = sharedStreamHandles.get(streamId)?.get(task.taskId);
          if (cached) return cached;
          // Post-wait fallback: streamClient is installed but handle
          // not cached. Should be unreachable because the first
          // acquirer caches BEFORE resolveSetup fires, but guard with
          // a defensive wrap rather than falling through to a
          // duplicate activate publish.
          if (entry.streamClient) {
            const so = createStreamObject(streamId, entry.streamClient, task.taskId, {
              releaseStream: releaseStreamImpl,
            });
            cacheSharedHandle(so);
            return so;
          }
          // External + shared is blocked by fix (h), so streamClient
          // must exist here on a fresh shared entry. Throw explicitly
          // rather than fall through to the `!isNew` activate path.
          throw new Error(
            `Stream "${streamId}" same-task reacquire reached an ` +
            `impossible state: setup completed but no client installed. ` +
            `This indicates a logic error in the shared-stream handle cache.`,
          );
        }

        if (!isNew) {
          // Entry existed, but THIS task is new to it (fix b).
          // Shared-affinity path: publish activate so the consumer's
          // status channel gets a per-task stream_started with a T7c
          // minted from THIS task's durationMinutes. Then return a
          // fresh wrapper bound to the shared StreamClient.
          //
          // CRITICAL: wait for the first acquirer's setup to finish
          // before touching entry.streamClient. performStreamSetup is
          // async; if Task B enters between Task A's registry.acquire
          // and Task A's `entry.streamClient = client` assignment,
          // entry.streamClient is still null at the check below and
          // we would throw "exists but has no client". Awaiting the
          // first-acquirer's setupPromise serializes attach-after-setup.
          //
          // Roll back this task's registry ref on any throw — either
          // the awaited setupPromise rejects (first acquirer's setup
          // failed), or publishSharedActivate throws (Function
          // rejected our per-task activate). Without this rollback a
          // failed activate leaves a zombie ref on the shared entry
          // and every subsequent task re-throws the same error (same
          // brick failure mode as the first-acquirer leak).
          try {
            if (entry.setupPromise) {
              await entry.setupPromise;
            }
            if (affinity === 'shared') {
              await publishSharedActivate();
            }
            if (entry.streamClient) {
              const so = createStreamObject(streamId, entry.streamClient, task.taskId, {
                releaseStream: releaseStreamImpl,
              });
              cacheSharedHandle(so);
              return so;
            }
            if (external) {
              return createExternalStreamObject(streamId, streamChannel, '', async () => {
                throw new Error('External stream already activated');
              });
            }
            // Should not reach here
            throw new Error(`Stream "${streamId}" exists but has no client`);
          } catch (err) {
            evictSharedHandle(streamId, task.taskId);
            try {
              await streamRegistry.release(streamId, task.taskId);
            } catch {
              /* best-effort rollback */
            }
            throw err;
          }
        }

        // First acquirer path: install a deferred setupPromise on the
        // entry synchronously, BEFORE awaiting performStreamSetup, so
        // concurrent second acquirers serialize on it.
        let resolveSetup!: () => void;
        let rejectSetup!: (err: unknown) => void;
        entry.setupPromise = new Promise<void>((res, rej) => {
          resolveSetup = res;
          rejectSetup = rej;
        });
        // Swallow the rejection at the source so an error thrown here
        // doesn't bubble as an unhandled rejection when no second
        // acquirer is awaiting. The throw below still propagates to
        // the caller; this only prevents a false unhandled-rejection
        // report on Node < 15 or when `process.on('unhandledRejection')`
        // is strict.
        entry.setupPromise.catch(() => { /* awaited by second acquirers only */ });

        // New stream: perform setup handshake
        const phase = external ? 'token_request' : 'embedded';

        let setupResult: Awaited<ReturnType<typeof performStreamSetup>>;
        try {
          setupResult = await performStreamSetup(taskPubNub, {
            taskId: task.taskId,
            orgId,
            agentName: effectiveAgentName,
            streamId,
            channel: streamChannel,
            direction,
            format,
            taskKind,
            durationMinutes,
            affinity,
            phase,
            metadata,
            declaredStream,
          });
        } catch (err) {
          // Roll back the registry ref this task just acquired. Shared
          // streams reuse the same streamId across tasks, so leaving a
          // failed first-acquirer entry in the registry would brick the
          // channel for every subsequent task on this agent instance —
          // each new acquire would find the zombie entry, `await` the
          // rejected setupPromise, and re-throw the original error until
          // the agent restarts. Release the ref locally (and evict any
          // cached handle) before propagating. Dedicated streams are
          // also covered: `streamId` is task-scoped so the release
          // simply removes the lone ref.
          rejectSetup(err);
          evictSharedHandle(streamId, task.taskId);
          try {
            await streamRegistry.release(streamId, task.taskId);
          } catch {
            /* best-effort rollback */
          }
          throw err;
        }

        if (external) {
          // External stream: no StreamClient. Resolve the setupPromise
          // so any concurrent second acquirer unblocks; they'll fall
          // through to the external handle path on their side.
          resolveSetup();
          entry.setupPromise = null;
          const t7a = setupResult.token ?? '';
          const activateFn = async (activateOpts?: { metadata?: Record<string, unknown> }) => {
            await performStreamSetup(taskPubNub, {
              taskId: task.taskId,
              orgId,
              agentName: effectiveAgentName,
              streamId,
              channel: streamChannel,
              direction,
              format,
              taskKind,
              durationMinutes,
              affinity,
              phase: 'activate',
              metadata: activateOpts?.metadata ?? metadata,
              declaredStream,
            });
          };

          return createExternalStreamObject(streamId, streamChannel, t7a, activateFn);
        }

        // Embedded stream: create StreamClient with T7a
        const t7a = setupResult.token;
        if (!t7a) {
          const err = new Error('No T7a token received from stream setup handshake');
          rejectSetup(err);
          evictSharedHandle(streamId, task.taskId);
          try {
            await streamRegistry.release(streamId, task.taskId);
          } catch {
            /* best-effort rollback */
          }
          throw err;
        }

        const client = new StreamClient({
          subscribeKey,
          publishKey,
          token: t7a,
          agentName: effectiveAgentName,
          streamId,
          channel: streamChannel,
          format,
          direction,
          affinity,
          gating: isPipeTask,
          bundleSizeBytes: csOpts?.bundleSizeBytes,
          maxLatencyMs: csOpts?.maxLatencyMs,
        });

        entry.streamClient = client;

        // Build + cache the per-task handle synchronously BEFORE we
        // resolve setupPromise and BEFORE the subscribe-grace sleep.
        // Order matters: concurrent second acquirers (same task, or
        // cross-task for shared) wake on setupPromise resolution and
        // immediately read `sharedStreamHandles` — they must see a
        // populated cache, otherwise the same-task fix (e) idempotent
        // path degrades to a duplicate activate publish.
        const streamObj = createStreamObject(streamId, client, task.taskId, {
          releaseStream: releaseStreamImpl,
        });
        cacheSharedHandle(streamObj);

        // Release concurrent second acquirers now that streamClient
        // is installed AND the handle cache is populated. Fires
        // BEFORE the subscribe-grace sleep so cross-task second
        // acquirers can attach during the grace window rather than
        // waiting for it to elapse.
        resolveSetup();
        // Null out the setupPromise now that setup has settled — no
        // future caller needs to await it (the cached handle + the
        // installed streamClient are the authoritative signals).
        // Keeps the entry's dead state minimal for any readers
        // introspecting the registry.
        entry.setupPromise = null;

        // Subscribe grace period for outbound/bidirectional streams (Fix 9).
        // Gives the consumer time to subscribe before the provider starts writing.
        if (direction === 'outbound' || direction === 'bidirectional') {
          const graceMs = csOpts?.subscribeGraceMs ?? 1000;
          if (graceMs > 0) {
            await new Promise(r => setTimeout(r, graceMs));
          }
        }

        // Run onActivate on the stream processing context
        if (onActivate) {
          entry.activated = true;
          entry.activatePromise = runOnActivate(streamId, streamObj, onActivate, failStreamImpl);
        }

        return streamObj;
      },
    };

    // Execute the user-supplied handler
    let result: HandlerResult | undefined;
    try {
      result = opts.handler ? await opts.handler(task, taskContext) : undefined;
    } finally {
      taskStreamCounters.delete(task.taskId);
      // Flush any buffered status message and clear the throttle timer.
      if (statusTimer) {
        clearTimeout(statusTimer);
        statusTimer = null;
      }
      flushStatus();
    }

    // Handle returned artifacts (plural, in array order, before terminal)
    const fileUploadAuth: FileUploadAuth | undefined = baseUrl
      ? { baseUrl, authProvider: token ? new StaticAuthProvider(token) : undefined, agentAuth }
      : undefined;

    if (result?.artifacts && result.artifacts.length > 0) {
      for (const entry of result.artifacts) {
        const buf = typeof entry.data === 'string'
          ? Buffer.from(entry.data, 'utf-8')
          : entry.data;
        await publishOrUploadArtifact(
          taskPubNub,
          task.taskId,
          orgId,
          effectiveAgentName,
          buf,
          entry.mimeType,
          entry.fileName,
          entry.outputId,
          fileUploadAuth,
        );
      }
    }

    // Determine terminal state
    const wasCancelled = controller.signal.aborted;

    if (isPipeTask) {
      const wasExpired = expiredTasks.has(task.taskId);
      const wasTerminated = terminatedTasks.has(task.taskId);

      if (wasCancelled || wasExpired || wasTerminated) {
        // Task was cancelled/expired/terminated during handler execution.
        // Clean up streams and publish terminal AFTER the artifact
        // (published above) so consumers see correct event ordering.
        await releaseAllStreamsForTask(task.taskId);

        const terminalState = wasCancelled && !wasExpired ? 'canceled' : 'completed';
        await publishTaskEvent(taskPubNub, task.taskId, orgId, effectiveAgentName, {
          type: 'terminal',
          taskId: task.taskId,
          state: terminalState,
          ...(wasExpired ? { completionReason: 'duration_expired' } : {}),
          ...(wasTerminated ? { reason: 'terminated' } : {}),
        });

        credentialCache.remove(task.taskId);
        log('info', `Task ${task.taskId} ${terminalState}`, {
          taskId: task.taskId,
          agentName: effectiveAgentName,
          owner: ownerId,
        });
      } else {
        // Voluntary return: credentials cached, streams continue running
        log('info', `Task ${task.taskId} handler returned (pipe, no auto-terminal)`, {
          taskId: task.taskId,
          agentName: effectiveAgentName,
          owner: ownerId,
        });
      }
    } else {
      // Request tasks: auto-complete on handler return
      const wasTerminatedReq = terminatedTasks.has(task.taskId);

      // End all streams for this task (+ evict shared-handle cache).
      await releaseAllStreamsForTask(task.taskId);

      const terminalState = wasCancelled ? 'canceled' : 'completed';
      await publishTaskEvent(taskPubNub, task.taskId, orgId, effectiveAgentName, {
        type: 'terminal',
        taskId: task.taskId,
        state: terminalState,
        ...(wasTerminatedReq ? { reason: 'terminated' } : {}),
      });

      credentialCache.remove(task.taskId);
      log('info', `Task ${task.taskId} ${terminalState}`, {
        taskId: task.taskId,
        agentName: effectiveAgentName,
        owner: ownerId,
      });
    }
  };

  // === Environment Switching ===
  const switchEnvironment = (newEnv: PnEnvironment, pamToken?: string): void => {
    log('info', `SwitchEnvironment requested: ${activeEnv} -> ${newEnv}`, {
      pamToken: pamToken ? 'present' : 'absent',
    });

    // Require pamToken — reject SwitchEnvironment without it
    if (!pamToken) {
      log('error', `SwitchEnvironment rejected: pamToken is required but was absent. The backend must include a pamToken in SwitchEnvironment messages.`);
      return;
    }

    const previousControlClient = controlClient;
    const previousControlClientOwned = ownsControlClient;

    // Remove listener and unsubscribe from current client
    try {
      previousControlClient.removeListener(listener);
      previousControlClient.removeListener(connectivityListener);
      if (controlChannel) previousControlClient.unsubscribe({ channels: [controlChannel] });
    } catch {
      /* ignore */
    }
    untrackClient(previousControlClient);

    // Create new client with new environment's keys
    const ks = envKeysets[newEnv];
    controlClient = createPubNubClient({
      publishKey: ks.publishKey,
      subscribeKey: ks.subscribeKey,
      userId: instanceId,
      presenceTimeout: 20,
      announceSuccessfulHeartbeats: _resolveLogLevel() >= _LOG_LEVEL_ORDER.debug,
      subscribeRetryUnbounded: true,
    });
    ownsControlClient = true;

    // Clear stale token from previous environment
    latestControlToken = undefined;

    // Apply the provided PAM token
    controlClient.setToken(pamToken);

    // Set subscribe filter for instance routing (skip when expectedInstances === 0, agent manages its own routing)
    if (expectedInstances !== 0 && typeof controlClient.setFilterExpression === 'function') {
      controlClient.setFilterExpression(
        `meta.instance == '${instanceId}' || meta.broadcast == "true"`,
      );
    }

    // Add listener and subscribe; diag listener is registered after the
    // primary listener so listeners[0] still points at the message handler.
    connectivityListener = buildConnectivityListener();
    controlClient.addListener(listener);
    controlClient.addListener(connectivityListener);
    trackClient('control', controlClient);
    if (controlChannel) controlClient.subscribe({ channels: [controlChannel] });

    if (previousControlClientOwned) {
      try {
        previousControlClient.destroy();
      } catch {
        /* ignore */
      }
    }

    activeEnv = newEnv;
    subscribeKey = ks.subscribeKey;
    publishKey = ks.publishKey;

    // Update TaskClient RPC keys so that post-switch RPC calls
    // (sendMessage, getTask, cancelTask, etc.) target the new keyset.
    consumerTaskClient.updateKeys(ks.subscribeKey, ks.publishKey);

    log('info', `Switched to ${newEnv} environment`);
    updatePresenceState().catch(() => {});
  };

  // === Control Message Handler ===
  const handleControlMessage = async (
    msg: AnyControlMessage,
    meta?: Record<string, unknown>,
  ): Promise<void> => {
    if (msg.type === 'StartTask') {
      const taskEnv = activeEnv; // capture environment at task receipt
      const ownerId = extractOwnerId(msg.ownerId, msg.callerClaims);
      const orgId = msg.orgId ?? ownerId;

      if (inflight.has(msg.taskId)) {
        log('warn', `Ignoring duplicate StartTask for in-flight task ${msg.taskId}`, {
          taskId: msg.taskId,
        });
        return;
      }

      const isBroadcast = Boolean(meta?.broadcast);

      // Protocol version compatibility check
      if (msg.protocolVersion && !isProtocolVersionSupported(msg.protocolVersion)) {
        if (isBroadcast) {
          // Broadcast unsupported: silently ignore
          log('debug', `Ignoring broadcast StartTask ${msg.taskId} with unsupported protocolVersion ${msg.protocolVersion}`, {
            taskId: msg.taskId,
          });
          return;
        }
        // Targeted unsupported: fail with unsupported_protocol_version
        await publishTaskEvent(controlClient, msg.taskId, orgId, agentName, {
          type: 'terminal',
          taskId: msg.taskId,
          state: 'failed',
          error: 'unsupported_protocol_version',
        }, msg.protocolVersion);
        log('warn', `Task ${msg.taskId} rejected: unsupported protocolVersion ${msg.protocolVersion}`, {
          taskId: msg.taskId,
          instanceId,
        });
        return;
      }

      // Defensive guard: reject pipe StartTask with missing/invalid duration.
      // The backend and scanner should prevent this, but if a malformed
      // StartTask arrives, do not start the handler without a duration timer.
      const msgTaskKind = msg.taskKind || 'request';
      if (msgTaskKind === 'pipe') {
        const dur = msg.duration;
        const expiresAt = msg.durationExpiresAtMs;
        if (
          dur === undefined ||
          dur === null ||
          !Number.isInteger(dur) ||
          dur < 1 ||
          dur > 43200 ||
          typeof expiresAt !== 'number' ||
          expiresAt <= 0
        ) {
          log('error', `Task ${msg.taskId} rejected: pipe StartTask with invalid duration (duration=${dur}, durationExpiresAtMs=${expiresAt})`, {
            taskId: msg.taskId,
            instanceId,
          });
          await publishTaskEvent(controlClient, msg.taskId, orgId, agentName, {
            type: 'terminal',
            taskId: msg.taskId,
            state: 'failed',
            error: 'invalid_start_task',
          }, msg.protocolVersion);
          return;
        }
      }

      const totalActive = activeTaskCount + streamRegistry.activeStreamCount;

      if (concurrency > 0 && totalActive >= concurrency) {
        if (isBroadcast) {
          log(
            'debug',
            `Task ${msg.taskId} skipped (broadcast, at capacity ${totalActive}/${concurrency})`,
            {
              taskId: msg.taskId,
              instanceId,
            },
          );
          return;
        }
        await publishTaskEvent(controlClient, msg.taskId, orgId, agentName, {
          type: 'terminal',
          taskId: msg.taskId,
          state: 'failed',
          error: 'agent_at_capacity',
        });
        log('warn', `Task ${msg.taskId} rejected: agent at capacity`, {
          taskId: msg.taskId,
          instanceId,
          activeTasks: activeTaskCount,
        });
        return;
      }

      taskOwnerMap.set(msg.taskId, ownerId);
      taskOrgMap.set(msg.taskId, orgId);
      inflight.add(msg.taskId);
      activeTaskCount++;

      if (msg.controlToken) latestControlToken = msg.controlToken;

      const controller = new AbortController();
      taskCancelControllers.set(msg.taskId, controller);

      // === TIER 2: Task Client (per-task PubNub) ===
      let taskPubNub: PubNub | null = null;

      try {
        const taskKeys = envKeysets[taskEnv];
        if (!taskKeys.publishKey || !taskKeys.subscribeKey) {
          throw new Error(
            'Per-task PubNub client requires valid pub/sub keys. ' +
            'When using an injected PubNub client, ensure it was created with publishKey and subscribeKey.',
          );
        }
        taskPubNub = createPubNubClient({
          publishKey: taskKeys.publishKey,
          subscribeKey: taskKeys.subscribeKey,
          userId: instanceId,
          subscribeRetryUnbounded: false,
        });
        if (msg.writeToken) taskPubNub.setToken(msg.writeToken);
        trackClient(`task-sub:${msg.taskId}`, taskPubNub);

        // Cache credentials for post-handler operations
        credentialCache.set(msg.taskId, {
          ownerId,
          orgId,
          writeToken: msg.writeToken ?? '',
          agentName,
          environment: taskEnv,
        });

        await updatePresenceState();

        // Strip PAM tokens before passing to handler code
        const { writeToken: _w, controlToken: _c, ...handlerTask } = msg;

        if (opts.onStartTask) {
          await opts.onStartTask(msg, taskPubNub);
        } else {
          await executeHandler(handlerTask as StartTaskMessage, taskPubNub, ownerId, orgId, controller);
        }
      } catch (e) {
        const errMsg = (e as Error)?.message ?? 'Agent instance error';
        const errPubNub = taskPubNub ?? controlClient;
        // Release any streams this task acquired before we published
        // the failed terminal. Belt-and-suspenders with the rollback
        // inside `createStream` itself: if a createStream call
        // succeeded and was later followed by an unrelated handler
        // failure, the stream's registry ref would otherwise leak
        // until the next agent restart (or brick a shared channel per
        // PR#515 review finding).
        try {
          await releaseAllStreamsForTask(msg.taskId);
        } catch {
          /* best-effort cleanup */
        }
        await publishTaskEvent(errPubNub, msg.taskId, orgId, agentName, {
          type: 'terminal',
          taskId: msg.taskId,
          state: 'failed',
          error: errMsg,
        });
        if (opts.onError) await opts.onError(msg.taskId, e as Error);
        log('error', errMsg, { taskId: msg.taskId });
        credentialCache.remove(msg.taskId);
      } finally {
        const pendingTimer = durationTimers.get(msg.taskId);
        if (pendingTimer) {
          clearTimeout(pendingTimer);
          durationTimers.delete(msg.taskId);
        }
        inflight.delete(msg.taskId);
        taskOwnerMap.delete(msg.taskId);
        taskOrgMap.delete(msg.taskId);
        taskStatusMap.delete(msg.taskId);
        taskCancelControllers.delete(msg.taskId);
        expiredTasks.delete(msg.taskId);
        terminatedTasks.delete(msg.taskId);
        // Destroy per-task PubNub client
        if (taskPubNub) {
          untrackClient(taskPubNub);
          try {
            taskPubNub.destroy();
          } catch {
            /* ignore */
          }
        }
        activeTaskCount--;
        if (activeTaskCount === 0 && latestControlToken) {
          controlClient.setToken(latestControlToken);
        }
        await updatePresenceState();
      }
    } else if (msg.type === 'CancelTask') {
      const controller = taskCancelControllers.get(msg.taskId);
      if (controller) {
        controller.abort();
        log('info', `Task ${msg.taskId} cancel requested (cooperative)`, {
          taskId: msg.taskId,
          instanceId,
        });
      } else {
        // Not in flight (external stream outlived handler).
        // Use publishTerminalImpl — it reads credentialCache for ownerId
        // and writeToken, creates an ephemeral PubNub, publishes, cleans up.
        if (credentialCache.get(msg.taskId)) {
          await publishTerminalImpl(msg.taskId, {
            state: 'canceled' as unknown as JsonValue,
          });
        }
        // No creds: server safety net handles it
      }
    } else if (msg.type === 'ExpireTask') {
      if (taskCancelControllers.has(msg.taskId)) {
        expiredTasks.add(msg.taskId);
        taskCancelControllers.get(msg.taskId)?.abort();
        log('info', `Task ${msg.taskId} expired`, { taskId: msg.taskId, instanceId });
      } else {
        if (credentialCache.get(msg.taskId)) {
          await publishTerminalImpl(msg.taskId, {
            state: 'completed' as unknown as JsonValue,
            completionReason: 'duration_expired' as unknown as JsonValue,
          });
        }
        // No creds: server safety net (Phase 4) handles it
      }
    } else if (msg.type === 'TerminateTask') {
      if (taskCancelControllers.has(msg.taskId)) {
        terminatedTasks.add(msg.taskId);
        taskCancelControllers.get(msg.taskId)?.abort();
      } else {
        if (credentialCache.get(msg.taskId)) {
          await publishTerminalImpl(msg.taskId, {
            state: 'canceled' as unknown as JsonValue,
            reason: 'terminated' as unknown as JsonValue,
          });
        }
        // No creds: server safety net handles it
      }
    } else if (msg.type === 'PauseTask') {
      if (inflight.has(msg.taskId)) {
        const pauseOrgId = taskOrgMap.get(msg.taskId) ?? 'anonymous';
        await publishTaskEvent(controlClient, msg.taskId, pauseOrgId, agentName, {
          type: 'system',
          taskId: msg.taskId,
          status: 'paused',
        });
      }
    } else if (msg.type === 'ResumeTask') {
      if (inflight.has(msg.taskId)) {
        const resumeOrgId = taskOrgMap.get(msg.taskId) ?? 'anonymous';
        await publishTaskEvent(controlClient, msg.taskId, resumeOrgId, agentName, {
          type: 'system',
          taskId: msg.taskId,
          status: 'resumed',
        });
      }
    } else if (msg.type === 'RetryTask') {
      log('info', 'RetryTask received (no-op)', { taskId: msg.taskId });
    }
  };

  // === Message Listener ===
  interface MessageEvent {
    message?: unknown;
    channel?: string;
    userMetadata?: Record<string, unknown>;
  }

  const accessDeniedHandler = (() => {
    let handled = false;
    return (
      payload: TransportStatusPayload,
      operation?: string,
    ): void => {
      const category = mapTransportCategory(payload);
      const statusCode =
        typeof payload.statusCode === 'number' ? payload.statusCode : null;
      log('debug', 'access-denied handler invoked', {
        event: 'access_denied_handler_invoked',
        category,
        operation: mapTransportOperation(operation),
        statusCode,
        alreadyHandled: handled,
        instanceId,
      });
      if (isAccessDeniedStatus(payload) && !handled) {
        handled = true;
        log('error', 'access denied — destroying control client (agent will go silent)', {
          event: 'access_denied_destroy',
          category,
          operation: mapTransportOperation(operation),
          statusCode,
          instanceId,
          controlChannel,
        });
        log('error', 'access token expired or revoked — agent is no longer receiving tasks. Re-register the agent to resume.', {
          event: 'access_denied_user_message',
          instanceId,
        });
        try {
          controlClient.destroy();
        } catch {
          /* ignore */
        }
      }
    };
  })();

  // === Single Message Listener ===
  // Status transitions and per-event echoes are emitted by the diag
  // listener registered via trackClient(). This listener handles
  // legacy human-friendly "connected" output and access-denied routing.
  let connectivityListener = buildConnectivityListener();
  const listener = {
    status: (event: unknown) => {
      const e = event as Record<string, unknown>;
      const category = String(e.category ?? '');
      const operation = String(e.operation ?? '');
      if (category === 'PNConnectedCategory') {
        log('debug', 'control client connected', {
          event: 'control_connected',
          controlChannel,
        });
      }
      accessDeniedHandler(e, operation);
    },
    message: (event: MessageEvent): void => {
      const raw = event.message as Record<string, unknown> | undefined;
      if (!raw) return;

      // Handle SwitchEnvironment before standard parsing
      if (raw.type === 'SwitchEnvironment') {
        const switchMsg = raw as unknown as SwitchEnvironmentMessage;
        const newEnv = switchMsg.environment as PnEnvironment;
        if (newEnv && newEnv !== activeEnv && newEnv in envKeysets) {
          switchEnvironment(newEnv, switchMsg.pamToken);
        }
        return;
      }

      const msg = raw as unknown as AnyControlMessage;
      const meta =
        event.userMetadata && typeof event.userMetadata === 'object'
          ? (event.userMetadata as Record<string, unknown>)
          : undefined;

      handleControlMessage(msg, meta).catch((err) => {
        log('error', 'unhandled error in message handler', {
          event: 'message_handler_error',
          error: err instanceof Error ? err.message : String(err),
          stack: err instanceof Error ? err.stack : undefined,
        });
      });
    },
  };

  controlClient.addListener(listener);
  controlClient.addListener(connectivityListener);
  trackClient('control', controlClient);

  startDiagAliveTimer();
  if (diagEnabled) {
    log('info', 'transport diagnostics armed', {
      event: 'transport_diagnostics_armed',
      snapshotIntervalMs: DIAG_SNAPSHOT_INTERVAL_MS,
      staleThresholdMs: DIAG_STALE_THRESHOLD_MS,
      instanceId,
    });
  }

  // Register and subscribe — use registryListing (from DB) so the backend mints
  // the PAM token for the correct keyset; fall back to opts.listing or SDK default.
  //
  // `billingMode` is forwarded UNCONDITIONALLY from the registry-resolved
  // value above (effectiveBillingMode). There is no caller-supplied override.
  // Provider must update the registry and restart to change billing mode.
  connectAgent(agentName, {
    instanceId,
    billingMode: effectiveBillingMode,
    description: opts.description,
    scaling: {
      expectedInstances,
      concurrency,
      maxPendingBacklog,
      maxRunningTimeSec: effectiveMaxRunningTimeSec,
    },
    card: opts.card,
    cardRef: opts.cardRef,
    cardSummary: opts.cardSummary,
    listing: registryListing ?? opts.listing,
    actor: `agent-instance:${instanceId}`,
    baseUrl: opts.baseUrl ?? cdmConfig?.api.baseUrl,
    agentAuth,
  })
    .then((result) => {
      log('info', `registered agent: ${agentName} (instance: ${instanceId})`, {
        event: 'agent_registered',
        agentName,
        instanceId,
      });
      if (!result.controlChannel) {
        throw new Error('Connect response missing controlChannel — server may be outdated');
      }
      controlChannel = result.controlChannel;
      if (result.pamToken) {
        controlClient.setToken(result.pamToken);
      }
      // Set subscribe filter for instance routing (skip when expectedInstances === 0, agent manages its own routing)
      if (expectedInstances !== 0 && typeof controlClient.setFilterExpression === 'function') {
        controlClient.setFilterExpression(
          `meta.instance == '${instanceId}' || meta.broadcast == "true"`,
        );
      }
      controlClient.subscribe({ channels: [controlChannel!] });
      updatePresenceState().catch(() => {});
    })
    .catch((err) => {
      const message = err instanceof Error ? err.message : String(err);
      log('error', `failed to register agent: ${agentName} — ${message}`, {
        event: 'agent_registration_failed',
        agentName,
        instanceId,
        error: message,
      });
      if (err instanceof AgentAuthFatalError) {
        process.exit(1);
      }
    });

  log('info', `Agent instance ${instanceId} started (agent name: ${agentName})`, {
    agentName,
    event: 'agent_instance_started',
    instanceId,
  });

  const stop = (): void => {
    try {
      if (diagAliveTimer !== null) {
        clearInterval(diagAliveTimer);
        diagAliveTimer = null;
      }
      // Remove every diag listener before clearing the registry.
      // Especially important when opts.pubnub was externally supplied —
      // we don't own pn.destroy(), so the only way the listener stops
      // emitting is if we removeListener it explicitly.
      for (const entry of diagRegistry) {
        try {
          entry.pn.removeListener(entry.listener);
        } catch {
          /* listener may already be detached if pn.destroy() was called */
        }
      }
      diagRegistry.length = 0;
      if (diagEnabled) {
        log('info', 'transport diagnostics disarmed', {
          event: 'transport_diagnostics_disarmed',
          instanceId,
        });
      }
      consumerAuth?.destroy();
      consumerTaskClient.destroy();
      controlClient.removeListener(listener);
      controlClient.removeListener(connectivityListener);
      if (controlChannel) controlClient.unsubscribe({ channels: [controlChannel] });
      if (ownsControlClient) {
        controlClient.destroy();
      }
      // End all streams
      for (const sid of streamRegistry.streamIds()) {
        const entry = streamRegistry.get(sid);
        if (entry?.streamClient) {
          try {
            entry.streamClient.end();
          } catch {
            /* ignore */
          }
        }
      }
      streamRegistry.clear();
      sharedStreamHandles.clear();
      credentialCache.clear();
    } catch {
      /* ignore */
    }
  };

  return {
    stop,
    agentName,
    instanceId,
    get controlChannel() {
      return controlChannel;
    },
    get pubnub() {
      return controlClient;
    },
    get subscribeKey() {
      return envKeysets[activeEnv].subscribeKey;
    },
    taskClient: consumerTaskClient,
    publishTerminal: publishTerminalImpl,
    failStream: failStreamImpl,
    cdmConfig,
  };
};

export const artifactUtils = { buildArtifactRef, shouldInlineArtifact };
