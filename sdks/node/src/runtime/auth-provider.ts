/**
 * AuthProvider interface and StaticAuthProvider implementation.
 *
 * All authenticated SDK paths (RPC, file upload, task-read-token) use
 * AuthProvider to obtain the current Authorization header and to handle
 * 401 reactive refresh. Static JWT callers use StaticAuthProvider;
 * ConsumerAuth implements the same interface with proactive + reactive
 * token refresh.
 */

// ============================================================================
// Interface
// ============================================================================

/**
 * Provides the current Authorization header and handles 401 refresh.
 *
 * Implementations:
 * - StaticAuthProvider: wraps a fixed JWT string, no refresh capability
 * - ConsumerAuth: manages token acquisition and transparent refresh
 */
export interface AuthProvider {
  /**
   * Returns the current Authorization header value (e.g. "Bearer <jwt>").
   * Returns null if no token is available.
   */
  getAuthHeader(): string | null;

  /**
   * Called by the transport when a request receives a 401 response.
   * Returns true if a fresh token was acquired and the caller should retry.
   * Returns false if no refresh is possible and the 401 should propagate.
   */
  onAuthFailure(): Promise<boolean>;

  /**
   * Async initialization hook. Called by the transport before the first
   * request. Implementations that require an async bootstrap (e.g.
   * ConsumerAuth token exchange) perform it here. Idempotent — repeated
   * calls are no-ops after the first success.
   */
  ensureReady?(): Promise<void>;

  /**
   * Returns the recorded permanent-refresh error when the provider is in
   * a known-broken state (e.g. proactive refresh exhausted its retries),
   * or null otherwise. The transport calls this before issuing any
   * authenticated request and throws the error directly so consumers see
   * the typed `AuthRefreshFailedError` instead of an opaque downstream
   * 401. Implementations without proactive refresh return null / omit.
   */
  getLastAuthError?(): Error | null;
}

// ============================================================================
// Pre-flight helper
// ============================================================================

/**
 * Pre-flight gate used by authenticated SDK paths (RPC, file upload,
 * TaskClient methods) to surface a recorded permanent-refresh error
 * before issuing the request.
 *
 * Behavior:
 * - No `getLastAuthError` / no recorded error -> returns immediately.
 * - Error recorded -> attempts one reactive refresh via
 *   `onAuthFailure()`. On success the error clears atomically with the
 *   new token apply and this returns silently (caller proceeds with the
 *   refreshed token). On failure the error stays recorded and this
 *   throws it so the caller sees `AuthRefreshFailedError` instead of an
 *   opaque downstream 401.
 *
 * The two-phase shape (record-then-retry-then-throw) preserves the
 * documented reactive recovery path: a transient outage that exhausts
 * proactive retries and then resolves still lets the next authenticated
 * call recover, instead of permanently wedging the client until it is
 * rebuilt.
 */
export async function preflightAuthOrThrow(
  provider: AuthProvider | undefined,
): Promise<void> {
  if (!provider?.getLastAuthError) return;
  const initial = provider.getLastAuthError();
  if (!initial) return;
  await provider.onAuthFailure();
  const remaining = provider.getLastAuthError();
  if (remaining) throw remaining;
}

// ============================================================================
// StaticAuthProvider
// ============================================================================

/**
 * Wraps a static JWT string. No refresh capability -- onAuthFailure()
 * always returns false.
 */
export class StaticAuthProvider implements AuthProvider {
  private readonly token: string;

  constructor(token: string) {
    this.token = token;
  }

  getAuthHeader(): string {
    return `Bearer ${this.token}`;
  }

  async onAuthFailure(): Promise<boolean> {
    return false;
  }
}
