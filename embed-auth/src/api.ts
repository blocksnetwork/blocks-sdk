/**
 * Public API surface for `@blocks-network/embed-auth`.
 *
 * Compose-only:
 *   - `popup.ts` owns popup orchestration.
 *   - `refresh.ts` owns the in-memory JWT, proactive refresh, and dedup.
 *   - `storage.ts` owns the partitioned `localStorage` + active-sessions index.
 *   - `signout.ts` owns the disambiguation + best-effort revoke.
 *
 * This module is the only place that wires those primitives together for
 * page consumers.
 */
import { TaskClient } from '@blocks-network/sdk';

import { resolveBackendBaseUrl, resolveCdmUrl } from './config.js';
import { MAX_AGENTS } from './constants.js';
import * as managerRegistry from './manager-registry.js';
import { openPopupAndAwaitEnvelope } from './popup.js';
import { EmbeddedAuthSessionManager } from './refresh.js';
import { signOut as signOutImpl } from './signout.js';
import {
  computePartitionKey,
  createStorageBackend,
  type StorageBackend,
} from './storage.js';
import {
  BlocksAuthError,
  type BlocksAuthSuccessEnvelope,
  type SignInMultiOptions,
  type SignInSingleOptions,
} from './types.js';

const AGENT_NAME_PATTERN = /^[a-zA-Z0-9_]+$/;

/**
 * Storage backend test seam. `__setStorageBackendForTesting` lets the api
 * test suite swap in a deterministic in-memory backend without touching the
 * real `localStorage` (and without requiring callers to thread storage
 * through the public API). Production code paths read this seam exactly
 * once per `signInAndGetClients` call.
 */
let storageOverride: StorageBackend | null = null;
export function __setStorageBackendForTesting(s: StorageBackend | null): void {
  storageOverride = s;
}

function getStorage(): StorageBackend {
  return storageOverride ?? createStorageBackend();
}

/** TaskClient factory test seam — unit tests stub SDK creation. */
type TaskClientFactory = typeof TaskClient.create;
let taskClientFactoryOverride: TaskClientFactory | null = null;
export function __setTaskClientFactoryForTesting(
  f: TaskClientFactory | null,
): void {
  taskClientFactoryOverride = f;
}

function getTaskClientFactory(): TaskClientFactory {
  return taskClientFactoryOverride ?? TaskClient.create.bind(TaskClient);
}

function resolvePageOrigin(): string {
  if (typeof window === 'undefined' || !window.location?.origin) {
    throw new BlocksAuthError(
      'INVALID_INPUT',
      'window.location.origin is unavailable; widget requires a browser context',
    );
  }
  return window.location.origin;
}

function validateAgents(opts: SignInMultiOptions): string[] {
  if (!Array.isArray(opts.agents)) {
    throw new BlocksAuthError('INVALID_INPUT', 'agents must be an array');
  }
  if (opts.agents.length === 0) {
    throw new BlocksAuthError('INVALID_INPUT', 'agents must be non-empty');
  }
  if (opts.agents.length > MAX_AGENTS) {
    throw new BlocksAuthError(
      'INVALID_INPUT',
      `agents may not exceed ${MAX_AGENTS} entries`,
    );
  }
  // Names are case-sensitive across the wire contract (`storage.ts` partition
  // key, popup envelope set-equality check, backend validator + DB unique
  // index). Duplicate detection MUST match that contract — `Foo` and `foo`
  // are legitimately distinct agents and may both appear in the array.
  const seen = new Set<string>();
  for (const a of opts.agents) {
    if (typeof a !== 'string' || !AGENT_NAME_PATTERN.test(a)) {
      throw new BlocksAuthError(
        'INVALID_INPUT',
        `invalid agent name: ${String(a)}`,
      );
    }
    if (seen.has(a)) {
      throw new BlocksAuthError(
        'INVALID_INPUT',
        `duplicate agent name: ${a}`,
      );
    }
    seen.add(a);
  }
  return [...opts.agents];
}

export async function signInAndGetClient(
  opts: SignInSingleOptions,
): Promise<TaskClient> {
  if (typeof opts !== 'object' || opts === null) {
    throw new BlocksAuthError('INVALID_INPUT', 'opts must be an object');
  }
  if (typeof opts.agent !== 'string' || opts.agent.length === 0) {
    throw new BlocksAuthError('INVALID_INPUT', 'agent must be a non-empty string');
  }
  // Reject the multi-agent shape on the single-agent surface — common
  // confusion bug, fail fast with a clear message.
  if ((opts as unknown as SignInMultiOptions).agents !== undefined) {
    throw new BlocksAuthError(
      'INVALID_INPUT',
      'pass `agents` to signInAndGetClients, not signInAndGetClient',
    );
  }
  const { agent, ...rest } = opts;
  const map = await signInAndGetClients({ agents: [agent], ...rest });
  const client = map[agent];
  if (!client) {
    // Should never happen — popup envelope validation would have thrown.
    throw new BlocksAuthError(
      'AGENT_SET_MISMATCH',
      `signInAndGetClient: requested agent ${agent} missing from envelope`,
    );
  }
  return client;
}

export async function signInAndGetClients(
  opts: SignInMultiOptions,
): Promise<Record<string, TaskClient>> {
  if (typeof opts !== 'object' || opts === null) {
    throw new BlocksAuthError('INVALID_INPUT', 'opts must be an object');
  }
  // `agent` is single-agent-only; reject mixing on the multi surface.
  if ((opts as unknown as SignInSingleOptions).agent !== undefined) {
    throw new BlocksAuthError(
      'INVALID_INPUT',
      'pass `agent` to signInAndGetClient, not signInAndGetClients',
    );
  }
  const requestedAgents = validateAgents(opts);

  const backendBaseUrl = resolveBackendBaseUrl(opts);
  const cdmUrl = resolveCdmUrl(opts);
  const pageOrigin = resolvePageOrigin();
  const storage = getStorage();
  const partitionKey = await computePartitionKey({
    backendBaseUrl,
    pageOrigin,
    agentNames: requestedAgents,
  });

  const refreshUrl = `${backendBaseUrl}/api/v1/auth/embed/refresh`;

  // Resume path.
  const existing = storage.getSession(partitionKey);
  if (existing) {
    // Reuse a manager already live for this partition. Two concurrent
    // `signInAndGetClients` for the same partition must NOT each build a
    // manager and fire their own network refresh: the backend rotates +
    // revokes the single-use refresh token on first use, so the second
    // refresh would 401, clear the partition (wiping the winner's freshly
    // rotated token), and fire a spurious `onAuthError` — logging out a
    // signed-in user. Sharing one instance routes the second caller through
    // the same in-flight refresh promise (`refresh.ts` dedup).
    const manager =
      managerRegistry.get(partitionKey) ??
      new EmbeddedAuthSessionManager({
        refreshUrl,
        storage,
        partitionKey,
        onAuthError: opts.onAuthError,
      });
    // Register before the network call so a concurrent `signOut` can clear
    // this manager's in-memory state mid-refresh. Idempotent if the manager
    // was already registered.
    managerRegistry.register(partitionKey, manager);
    try {
      // Force a refresh: nothing in memory yet, so this runs the network call.
      // The TokenResult's `agentIds` is the live ground truth — backend may
      // have narrowed scope (e.g., post grant revocation), so filter the
      // stored `agents` catalog before building clients. Otherwise a revoked
      // agent could still appear in the returned map.
      const tokenResult = await manager.tokenProvider();
      const liveAgentIds = new Set(tokenResult.agentIds);
      const liveAgents = existing.agents.filter((a) => liveAgentIds.has(a.id));

      // The popup grants a reachable SUBSET — it intersects the requested
      // agents with the set the user can actually reach. So a
      // stored session that is legitimately narrower than the current request
      // MUST still resume silently. Forcing a re-popup just because the live
      // set is smaller breaks auto-resume (no user gesture → POPUP_BLOCKED) and
      // makes every reload re-prompt forever for the unreachable agents.
      //
      // Only clear and fall through to the popup when NOTHING is live (all
      // requested agents revoked / unreachable), which is the genuine
      // stale-partition case. Recovering grants that were restored after
      // sign-in is an explicit user action (re-invoke sign-in), not something
      // auto-resume should force.
      if (liveAgents.length === 0) {
        manager.clear();
        storage.clear(partitionKey);
        managerRegistry.unregister(partitionKey);
        // fall through to popup
      } else {
        // Resume reads `cdmUrl` from the persisted session (preferred — that's
        // what the original sign-in saw) and falls back to the live resolution
        // for sessions persisted before this field was added.
        const resumeCdmUrl = existing.cdmUrl ?? cdmUrl;
        return await buildClientMap(liveAgents, manager, {
          ...opts,
          cdmUrl: resumeCdmUrl,
        });
      }
    } catch (err) {
      // 401 / NO_REFRESH_TOKEN → partition was cleared by the manager;
      // fall through to popup. Other errors propagate.
      if (
        err instanceof BlocksAuthError &&
        (err.code === 'REFRESH_FAILED' || err.code === 'NO_REFRESH_TOKEN')
      ) {
        // Defensive: ensure the partition is cleared even if the manager
        // hit `NO_REFRESH_TOKEN` before its 401 cleanup.
        storage.clear(partitionKey);
        managerRegistry.unregister(partitionKey);
        // continue to popup
      } else {
        throw err;
      }
    }
  }

  // Popup path.
  const { envelope } = await openPopupAndAwaitEnvelope({
    agents: requestedAgents,
    backendBaseUrl,
    pageOrigin,
  });

  storage.setSession(partitionKey, {
    refreshToken: envelope.refreshToken,
    agentIds: envelope.agentIds,
    agents: envelope.agents,
    orgId: envelope.orgId,
    userId: envelope.userId,
    pageOrigin,
    backendBaseUrl,
    ...(cdmUrl !== undefined ? { cdmUrl } : {}),
  });

  const manager = new EmbeddedAuthSessionManager({
    refreshUrl,
    storage,
    partitionKey,
    onAuthError: opts.onAuthError,
  });
  manager.seedFromEnvelope({
    jwt: envelope.jwt,
    expiresAt: envelope.expiresAt,
    agentIds: envelope.agentIds,
    userId: envelope.userId,
  });
  managerRegistry.register(partitionKey, manager);

  return buildClientMap(envelope.agents, manager, { ...opts, cdmUrl });
}

/**
 * Build the public `Record<string, TaskClient>` map. Dedupes the underlying
 * TaskClient by `billingMode`: one
 * underlying client per distinct billingMode the page uses, with the public
 * map aliasing by name. All clients share the single `manager.tokenProvider`
 * so multi-agent pages run one refresh loop.
 */
async function buildClientMap(
  agents: BlocksAuthSuccessEnvelope['agents'],
  manager: EmbeddedAuthSessionManager,
  opts: {
    onAuthError?: SignInMultiOptions['onAuthError'];
    /**
     * Plumbs to `TaskClient.create({ cdmUrl })` — the explicit-option
     * path the `explicit option → CDM → default` resolver
     * preserves. Set when the dev shim or the page caller wants the SDK
     * to fetch CDM (PubNub keys + `api.baseUrl`) from a non-default
     * source — typically the local backend in `blocks dev`.
     */
    cdmUrl?: string;
  },
): Promise<Record<string, TaskClient>> {
  const factory = getTaskClientFactory();
  const byBillingMode = new Map<'free' | 'paid', TaskClient>();
  const result: Record<string, TaskClient> = {};
  for (const agent of agents) {
    let client = byBillingMode.get(agent.billingMode);
    if (!client) {
      // Bridge the SDK's `(error: Error) => void` to the widget's
      // `(BlocksAuthError) => void`. SDK auth errors that aren't the
      // widget's typed error fall through unwrapped — the consumer's
      // handler is documented as widget-error-only, so unrecognized errors
      // are a no-op rather than a misleading code.
      const onAuthError = opts.onAuthError;
      const sdkOnAuthError = onAuthError
        ? (error: Error): void => {
            if (error instanceof BlocksAuthError) onAuthError(error);
          }
        : undefined;
      client = await factory({
        billingMode: agent.billingMode,
        tokenProvider: manager.tokenProvider,
        onAuthError: sdkOnAuthError,
        ...(opts.cdmUrl !== undefined ? { cdmUrl: opts.cdmUrl } : {}),
      });
      byBillingMode.set(agent.billingMode, client);
    }
    result[agent.name] = client;
  }
  return result;
}

/**
 * Sign the user out of every embedded-auth session on this page.
 *
 * Always argless — a later revision dropped the per-agent
 * (`signOut({ agent })`) and per-set (`signOut({ agents })`) selectors.
 * If the page wants to drop one agent's session while keeping another
 * alive, it should call `signIn*` again with the desired narrower set;
 * silently leaving a refresh token alive is not the right default.
 *
 * For full cross-origin logout (every site using embed-auth on every
 * device), the user signs out of `blocks.ai` directly — that path
 * raises the user's `embedded_auth_revoked_after` watermark
 * server-side and invalidates every still-live refresh token without
 * widget cooperation.
 */
export async function signOut(): Promise<void> {
  const storage = getStorage();
  await signOutImpl({
    storage,
    registry: {
      get: (partitionKey) => managerRegistry.get(partitionKey),
      unregister: (partitionKey) => managerRegistry.unregister(partitionKey),
    },
  });
}
