/**
 * Shared JSON-RPC infrastructure for calling the Blocks backend RPC endpoint.
 *
 * Provides:
 * - `withRetry()` — exponential-backoff retry for transient network errors
 * - `rpcEndpoint()` — builds the RPC URL from a backend base URL
 * - `callRpc()` — sends a JSON-RPC 2.0 request with retry and error handling
 * - `RpcError` — structured error for JSON-RPC error responses
 */

import type { AgentAuth } from './agent-auth.js';
import type { AuthProvider } from './auth-provider.js';
import { preflightAuthOrThrow } from './auth-provider.js';
import { CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER } from './protocol-version.js';
import { captureAffinity, injectAffinity } from './write-affinity.js';

// ============================================================================
// Types
// ============================================================================

export interface RpcClientConfig {
  subscribeKey: string;
  authProvider?: AuthProvider;
  baseUrl?: string;
  agentAuth?: AgentAuth;
}

// ============================================================================
// Retry Helper
// ============================================================================

/**
 * Retry helper for transient network errors (DNS failures, timeouts, etc.)
 */
export async function withRetry<T>(
  fn: () => Promise<T>,
  maxRetries = 3,
  baseDelayMs = 500,
): Promise<T> {
  let lastError: unknown;
  for (let attempt = 0; attempt < maxRetries; attempt++) {
    try {
      return await fn();
    } catch (err) {
      lastError = err;
      const errorStr = String(err);
      const isTransient =
        errorStr.includes('ENOTFOUND') ||
        errorStr.includes('ETIMEDOUT') ||
        errorStr.includes('ECONNRESET') ||
        errorStr.includes('NetworkIssues');
      if (!isTransient || attempt === maxRetries - 1) {
        throw err;
      }
      const delay = baseDelayMs * Math.pow(2, attempt) + Math.random() * 100;
      await new Promise((r) => setTimeout(r, delay));
    }
  }
  throw lastError;
}

// ============================================================================
// RPC Endpoint
// ============================================================================

/**
 * Build the RPC gateway URL from a backend base URL.
 * baseUrl must be a root URL (e.g. http://localhost:3001).
 */
export function rpcEndpoint(subscribeKey: string, baseUrl?: string): string {
  if (baseUrl) {
    return `${baseUrl.replace(/\/+$/, '')}/api/v1/rpc`;
  }
  // Fallback to PubNub Functions RPC gateway when no backend baseUrl is configured
  return `https://ps.pndsn.com/v1/blocks/sub-key/${subscribeKey}/rpc`;
}

// ============================================================================
// RPC Error
// ============================================================================

/**
 * Structured error for JSON-RPC error responses.
 */
export class RpcError extends Error {
  code?: number;
  rpcMessage: string;
  data?: unknown;

  constructor(rpcMessage: string, code?: number, data?: unknown) {
    super(`[RPC] ${rpcMessage}`);
    this.name = 'RpcError';
    this.rpcMessage = rpcMessage;
    this.code = code;
    this.data = data;
  }
}

/**
 * Backend AppError codes that mean the JWT's underlying session was rotated/
 * superseded and a refresh will recover. The embedded-auth flow rotates the
 * refresh-token row and the backend revokes the old row the instant a new
 * token is minted, so a request that raced the rotation comes back with
 * `EMBEDDED_JWT_REVOKED`; refreshing yields the rotated token and the retry
 * succeeds. These are raised in auth middleware BEFORE the task is processed,
 * so retrying once cannot double-submit.
 *
 * The other embedded-JWT liveness codes are likewise refresh-recoverable and
 * must be retried so a dead session clears immediately instead of lingering
 * until JWT expiry (~60s):
 *   - `EMBEDDED_JWT_LOGOUT`: the refresh endpoint re-checks the logout
 *     watermark and 401s, so the refresh path clears the session at once.
 *   - `EMBEDDED_JWT_KILLED`: refresh drops killed agents from the scope;
 *     surviving agents re-mint a narrowed token, all-killed 401s and clears.
 *   - `EMBEDDED_JWT_SCOPE_DRIFT`: refresh re-mints scoped to the current
 *     refresh-token row, so the new JWT matches and the retry succeeds.
 *   - `AGENT_OUT_OF_SCOPE`: one of several scoped agents had its grant
 *     revoked; refresh narrows the token to the still-reachable subset so
 *     requests to the surviving agents recover (all-revoked 401s and clears).
 */
const AUTH_REFRESH_RETRYABLE_RPC_DATA_CODES = new Set([
  'EMBEDDED_JWT_REVOKED',
  'EMBEDDED_JWT_LOGOUT',
  'EMBEDDED_JWT_KILLED',
  'EMBEDDED_JWT_SCOPE_DRIFT',
  'AGENT_OUT_OF_SCOPE',
]);

/**
 * Whether an RpcError should trigger one reactive token refresh + retry.
 * Matches either a transport 401, or a backend auth-revoke code carried in the
 * JSON-RPC error `data.code` (where the transport `code` is a generic JSON-RPC
 * code rather than 401 — which is why a plain `err.code === 401` check misses
 * embedded-auth revocations).
 */
function isAuthRefreshRetryable(err: unknown): boolean {
  if (!(err instanceof RpcError)) return false;
  if (err.code === 401) return true;
  const data = err.data as { code?: unknown } | null | undefined;
  return (
    typeof data?.code === 'string' &&
    AUTH_REFRESH_RETRYABLE_RPC_DATA_CODES.has(data.code)
  );
}

/**
 * Typed error for backend `BillingModeMismatch` responses.
 *
 * Backend wire shape (per Phase 1 `bmc-data` IMPL_REPORT):
 *
 *   {
 *     "jsonrpc": "2.0",
 *     "error": {
 *       "code": -32000,
 *       "message": "Billing mode mismatch: ...",
 *       "data": {
 *         "code": "BillingModeMismatch",
 *         "details": { "expected": "free" | "paid", "got": "free" | "paid" }
 *       }
 *     }
 *   }
 *
 * Extends `RpcError` for cross-language parity with the Python SDK's
 * `BillingModeMismatchError` (which extends `RpcError` for the same reason).
 *
 * The SDK does NOT auto-retry or auto-correct on this error. The caller
 * must update their `TaskClient` billing mode (typically by re-running
 * `TaskClient.create({ billingMode: ... })`) to match the agent's
 * persisted mode.
 */
export class BillingModeMismatchError extends RpcError {
  expected: 'free' | 'paid';
  got: 'free' | 'paid';

  constructor(
    rpcMessage: string,
    expected: 'free' | 'paid',
    got: 'free' | 'paid',
    code?: number,
    data?: unknown,
  ) {
    super(rpcMessage, code, data);
    this.name = 'BillingModeMismatchError';
    this.expected = expected;
    this.got = got;
  }
}

/**
 * Inspect a JSON-RPC error envelope's `data` field and return a typed
 * `BillingModeMismatchError` when the structured wire shape matches.
 * Returns `null` otherwise so the generic `RpcError` path handles it.
 */
function tryMapBillingModeMismatch(
  rpcMessage: string,
  code: number | undefined,
  data: unknown,
): BillingModeMismatchError | null {
  if (!data || typeof data !== 'object') return null;
  const d = data as { code?: unknown; details?: unknown };
  if (d.code !== 'BillingModeMismatch') return null;
  const details = d.details;
  if (!details || typeof details !== 'object') return null;
  const det = details as { expected?: unknown; got?: unknown };
  if (det.expected !== 'free' && det.expected !== 'paid') return null;
  if (det.got !== 'free' && det.got !== 'paid') return null;
  return new BillingModeMismatchError(
    rpcMessage,
    det.expected,
    det.got,
    code,
    data,
  );
}

// ============================================================================
// callRpc
// ============================================================================

/**
 * Send a JSON-RPC 2.0 request to the Blocks backend RPC endpoint.
 *
 * Handles:
 * - JSON-RPC envelope construction
 * - Authorization header (Bearer token) when an auth provider supplies one
 * - Transient-error retry via `withRetry()`
 * - HTTP and JSON-RPC error mapping to `RpcError`
 */
export async function callRpc<T>(
  config: RpcClientConfig,
  method: string,
  params: Record<string, unknown>,
): Promise<T> {
  const url = rpcEndpoint(config.subscribeKey, config.baseUrl);
  const requestId = `rpc-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;

  // agentAuth injects/captures affinity inside authenticatedFetch; we only
  // handle it directly on the non-agentAuth path below.
  const buildHeaders = (includeAffinity: boolean): Record<string, string> => {
    const h: Record<string, string> = {
      'Content-Type': 'application/json',
      [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
    };
    const authHeader = config.authProvider?.getAuthHeader();
    if (authHeader) {
      h['Authorization'] = authHeader;
    }
    if (includeAffinity) injectAffinity(h);
    return h;
  };

  const payload = {
    jsonrpc: '2.0' as const,
    id: requestId,
    method,
    params,
  };

  const doRequest = async (): Promise<T> => {
    let response: Response;

    if (config.agentAuth) {
      response = await config.agentAuth.authenticatedFetch(url, {
        method: 'POST',
        headers: buildHeaders(false),
        body: JSON.stringify(payload),
      });
    } else {
      response = await fetch(url, {
        method: 'POST',
        headers: buildHeaders(true),
        body: JSON.stringify(payload),
      });
      captureAffinity(response.headers);
    }

    if (!response.ok) {
      throw new RpcError(
        `HTTP ${response.status}`,
        response.status,
      );
    }

    const json = (await response.json()) as {
      result?: T;
      error?: {
        code?: number;
        message?: string;
        data?: { code?: string; message?: string; details?: unknown };
      };
    };

    if (json.error) {
      const message =
        json.error.data?.message ?? json.error.message ?? 'Unknown RPC error';
      // Map structured BillingModeMismatch errors to a typed subclass so
      // callers can `instanceof BillingModeMismatchError` and read
      // `expected`/`got` directly. No auto-retry; caller must fix their
      // billing mode.
      const typed = tryMapBillingModeMismatch(
        message,
        json.error.code,
        json.error.data,
      );
      if (typed) throw typed;
      throw new RpcError(message, json.error.code, json.error.data);
    }

    return json.result as T;
  };

  // Pre-flight: when the auth provider has a recorded permanent-refresh
  // error, attempt one reactive recovery. If the recovery succeeds the
  // error clears and we proceed with the new token; if it doesn't, the
  // typed AuthRefreshFailedError is thrown before any network attempt
  // so consumers don't see an opaque 401 from the doomed RPC.
  await preflightAuthOrThrow(config.authProvider);

  if (config.authProvider?.ensureReady) {
    await config.authProvider.ensureReady();
  }

  return withRetry(async () => {
    try {
      return await doRequest();
    } catch (err) {
      // Reactive refresh: a transport 401 OR a backend auth-revoke code
      // (e.g. EMBEDDED_JWT_REVOKED from a refresh-token rotation race) — if the
      // authProvider can refresh, retry once with the rotated token.
      if (
        isAuthRefreshRetryable(err) &&
        config.authProvider &&
        !config.agentAuth // agentAuth handles its own 401 retry
      ) {
        const refreshed = await config.authProvider.onAuthFailure();
        if (refreshed) {
          return doRequest();
        }
      }
      throw err;
    }
  });
}
