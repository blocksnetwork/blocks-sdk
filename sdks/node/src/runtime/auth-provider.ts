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
