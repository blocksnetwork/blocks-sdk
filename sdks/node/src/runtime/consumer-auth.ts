/**
 * ConsumerAuth -- consumer-side token acquisition and transparent refresh.
 *
 * Implements AuthProvider with three token acquisition modes:
 *   Mode 1 (apiKey): exchange API key for consumer JWT via backend
 *   Mode 2 (tokenEndpoint): POST to customer-owned proxy endpoint
 *   Mode 3 (tokenProvider): call developer-supplied async function
 *
 * Proactive refresh at 80% TTL with exponential backoff on failure.
 * Reactive refresh on 401 via onAuthFailure() with Promise dedup.
 */

import type { AuthProvider } from './auth-provider.js';
import { log as baseLog } from './logger.js';
import { CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER } from './protocol-version.js';
import { captureAffinity, injectAffinity } from './write-affinity.js';

const log = (
  level: 'debug' | 'info' | 'warn' | 'error',
  message: string,
  meta?: Record<string, unknown>,
): void => baseLog('[ConsumerAuth]', level, message, meta);

// ============================================================================
// Types
// ============================================================================

/** Result from any token acquisition call. */
export interface TokenResult {
  token: string;
  expiresIn: number; // seconds
  userId?: string;
}

/**
 * Credentials mode for the fetch call to a token endpoint.
 *
 * Mirrors the DOM/undici `RequestCredentials` union. Inlined here so the
 * Node SDK's TypeScript configuration (no `"DOM"` lib) can reference the
 * type without depending on the DOM ambient typings.
 */
export type TokenEndpointCredentials = 'omit' | 'same-origin' | 'include';

/**
 * Configuration for Mode 2 (token endpoint) token acquisition.
 *
 * Accepts either a bare URL string (the legacy form) or a config object
 * with fetch-init overrides (`credentials`, `headers`, `body`). The config
 * form is additive: SDK defaults (`method: 'POST'`, `Content-Type: application/json`,
 * empty-object body) still apply; supplied fields override the defaults.
 *
 * The `body` value (when provided) is passed through `JSON.stringify`, so it
 * must be JSON-serializable.
 */
export type TokenEndpointConfig =
  | string
  | {
      url: string;
      credentials?: TokenEndpointCredentials;
      headers?: Record<string, string>;
      body?: unknown;
    };

/** Options for constructing a ConsumerAuth instance. */
export interface ConsumerAuthOptions {
  apiKey?: string;
  tokenEndpoint?: TokenEndpointConfig;
  tokenProvider?: () => Promise<TokenResult>;
  baseUrl?: string;
  onAuthError?: (error: Error) => void;
}

// ============================================================================
// Constants
// ============================================================================

const PROACTIVE_REFRESH_FACTOR = 0.8;
const BACKOFF_BASE_MS = 5000;
const BACKOFF_MAX_MS = 30000;
const MAX_RETRIES = 3;
const ERROR_CODE_REFRESH_TOKEN_INVALID = 'REFRESH_TOKEN_INVALID';
const ERROR_CODE_API_KEY_INVALID = 'API_KEY_INVALID';

interface ErrorResponse {
  error?: string;
  code?: string;
}

// ============================================================================
// AuthRefreshFailedError
// ============================================================================

/**
 * Set when the underlying `ConsumerAuth` enters a known-broken refresh state —
 * proactive refresh permanently failed after 3 retries, or a reactive refresh
 * failed.
 *
 * Thrown by any authenticated call that runs the `preflightAuthOrThrow` gate:
 * the RPC methods on `TaskClient` and `TaskSession`, `connect()`, and the
 * file-upload helpers. Enumerating them here would drift the moment one is
 * added, so the rule is the gate rather than a list.
 *
 * Also thrown by `getAgentCard()`, for a different reason: it cannot tell a
 * rejected credential from a missing agent on its own, because the registry read
 * is on optional auth, so a stale bearer degrades to anonymous and 404s rather
 * than 401ing.
 *
 * Carries the original failure as `.cause`. Cleared by a subsequent successful
 * token apply.
 */
export class AuthRefreshFailedError extends Error {
  readonly cause: Error;
  constructor(cause: Error) {
    super(`Consumer auth refresh permanently failed: ${cause.message}`);
    this.name = 'AuthRefreshFailedError';
    this.cause = cause;
  }
}

// ============================================================================
// ConsumerAuth
// ============================================================================

export class ConsumerAuth implements AuthProvider {
  private readonly _apiKey?: string;
  private readonly _tokenEndpoint?: TokenEndpointConfig;
  private readonly _tokenProvider?: () => Promise<TokenResult>;
  private readonly _baseUrl: string;
  private readonly _onAuthError?: (error: Error) => void;

  private _token: string | null = null;
  private _refreshToken: string | null = null;
  private _userId: string | null = null;
  private _expiresIn = 0;
  private _destroyed = false;
  private _refreshTimer: ReturnType<typeof setTimeout> | null = null;
  private _refreshPromise: Promise<boolean> | null = null;
  private _initialized = false;
  private _initPromise: Promise<void> | null = null;
  private _lastAuthError: AuthRefreshFailedError | null = null;

  constructor(options: ConsumerAuthOptions) {
    this._apiKey = options.apiKey;
    this._tokenEndpoint = options.tokenEndpoint;
    this._tokenProvider = options.tokenProvider;
    this._baseUrl = (options.baseUrl ?? '').replace(/\/+$/, '');
    this._onAuthError = options.onAuthError;
  }

  /**
   * Acquire the initial token. Must be called before any other method.
   */
  async init(): Promise<void> {
    if (this._apiKey) {
      await this._initApiKey();
    } else if (this._tokenEndpoint) {
      await this._initTokenEndpoint();
    } else if (this._tokenProvider) {
      await this._initTokenProvider();
    } else {
      throw new Error('ConsumerAuth requires one of: apiKey, tokenEndpoint, or tokenProvider');
    }
    this._scheduleRefresh();
    this._initialized = true;
  }

  getAuthHeader(): string | null {
    if (!this._token) return null;
    return `Bearer ${this._token}`;
  }

  getUserId(): string | null {
    return this._userId;
  }

  /**
   * Returns the last refresh failure, or null. Set when proactive refresh
   * exhausts its 3 retries, and when a reactive refresh fails; cleared
   * atomically on the next successful token apply.
   * Callers (TaskClient.sendMessage / connect) use this to fail fast before
   * making any authenticated request.
   */
  getLastAuthError(): AuthRefreshFailedError | null {
    return this._lastAuthError;
  }

  /**
   * Reactive refresh triggered by a 401 response. Returns true if a new
   * token was acquired. Concurrent callers share a single in-flight
   * refresh via Promise dedup.
   */
  async onAuthFailure(): Promise<boolean> {
    if (this._destroyed) return false;
    return this._doRefreshDedup();
  }

  /**
   * Stop proactive refresh timer. Does NOT clear the token -- active
   * sessions can still use the last-known token for its remaining TTL.
   */
  destroy(): void {
    this._destroyed = true;
    if (this._refreshTimer) {
      clearTimeout(this._refreshTimer);
      this._refreshTimer = null;
    }
  }

  async ensureReady(): Promise<void> {
    if (this._initialized) return;
    if (this._initPromise) {
      await this._initPromise;
      return;
    }
    this._initPromise = this.init().then(
      () => { this._initialized = true; },
      (err) => { this._initPromise = null; throw err; },
    );
    await this._initPromise;
  }

  // --------------------------------------------------------------------------
  // Mode 1: API key
  // --------------------------------------------------------------------------

  private async _initApiKey(): Promise<void> {
    const url = `${this._baseUrl}/api/v1/auth/agent/consumer-token`;
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
    };
    injectAffinity(headers);

    const response = await fetch(url, {
      method: 'POST',
      headers,
      body: JSON.stringify({ apiKey: this._apiKey }),
    });

    captureAffinity(response.headers);

    if (!response.ok) {
      const text = await response.text().catch(() => '');
      throw new Error(`consumer-token failed: HTTP ${response.status}${text ? ` ${text}` : ''}`);
    }

    const data = (await response.json()) as {
      accessToken: string;
      refreshToken: string;
      expiresIn: number;
      userId: string;
    };

    this._refreshToken = data.refreshToken;
    // Route token application through _applyTokenResult so _lastAuthError
    // is cleared atomically with the new token. _refreshApiKey() falls
    // back here on REFRESH_TOKEN_INVALID, and that recovery path must
    // not leave a stale fail-fast error behind.
    this._applyTokenResult({
      token: data.accessToken,
      expiresIn: data.expiresIn,
      userId: data.userId,
    });
  }

  private async _refreshApiKey(): Promise<void> {
    const url = `${this._baseUrl}/api/v1/auth/agent/refresh`;
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this._apiKey}`,
      'Content-Type': 'application/json',
    };
    injectAffinity(headers);

    const response = await fetch(url, {
      method: 'POST',
      headers,
      body: JSON.stringify({ refreshToken: this._refreshToken }),
    });

    captureAffinity(response.headers);

    if (!response.ok) {
      const body = (await response.json().catch(() => ({}))) as ErrorResponse;

      if (body.code === ERROR_CODE_REFRESH_TOKEN_INVALID) {
        // Symmetric with provider AgentAuth: if the refresh token was
        // rotated out or invalidated, re-bootstrap from the API key.
        await this._initApiKey();
        return;
      }

      if (body.code === ERROR_CODE_API_KEY_INVALID) {
        throw new Error(
          `API key invalid or revoked: ${body.error ?? ERROR_CODE_API_KEY_INVALID}`,
        );
      }

      throw new Error(
        `token refresh failed: ${body.error ?? `HTTP ${response.status}`}`,
      );
    }

    const data = (await response.json()) as {
      accessToken: string;
      refreshToken: string;
      expiresIn?: number;
    };

    this._token = data.accessToken;
    this._refreshToken = data.refreshToken;
    if (typeof data.expiresIn === 'number') {
      this._expiresIn = data.expiresIn;
    }
    // Atomic with token application — see _applyTokenResult.
    this._lastAuthError = null;
  }

  // --------------------------------------------------------------------------
  // Mode 2: Token endpoint (customer proxy)
  // --------------------------------------------------------------------------

  private async _initTokenEndpoint(): Promise<void> {
    const result = await this._fetchTokenEndpoint();
    this._applyTokenResult(result);
  }

  /**
   * Mode-2 token-endpoint traffic goes to a customer-owned proxy, not the
   * Blocks backend, so `X-Write-Affinity` is not exchanged in either
   * direction: echoing it outbound would leak internal routing state to a
   * third party, and capturing it inbound would let the proxy dictate this
   * SDK's DB routing. Affinity lives on SDK -> Blocks calls only.
   */
  private async _fetchTokenEndpoint(): Promise<TokenResult> {
    const cfg = this._tokenEndpoint!;
    const isString = typeof cfg === 'string';
    const url = isString ? cfg : cfg.url;
    // `url` is structurally required on `TokenEndpointConfig`, but since
    // JS callers bypass TypeScript we still validate explicitly — a
    // descriptive error is friendlier than `fetch(undefined, ...)` or a
    // bare TypeError on low-level URL parsing. Parity with Python's
    // _acquire_token_endpoint in blocks_network/consumer_auth.py.
    if (typeof url !== 'string' || !url) {
      throw new Error(
        'TokenEndpointConfig.url is required and must be a non-empty string',
      );
    }
    const credentials = isString ? undefined : cfg.credentials;
    const extraHeaders = isString ? {} : (cfg.headers ?? {});
    const body = isString ? {} : (cfg.body ?? {});

    // Shape as a loose record so we can conditionally attach `credentials`
    // without depending on DOM `RequestInit` (Node SDK tsconfig omits the
    // DOM lib). The runtime `fetch` ignores unknown keys anyway.
    const init: Record<string, unknown> = {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...extraHeaders,
      },
      body: JSON.stringify(body),
    };
    if (credentials !== undefined) {
      init.credentials = credentials;
    }

    const response = await fetch(url, init as Parameters<typeof fetch>[1]);

    if (!response.ok) {
      const text = await response.text().catch(() => '');
      throw new Error(`token endpoint failed: HTTP ${response.status}${text ? ` ${text}` : ''}`);
    }

    return response.json() as Promise<TokenResult>;
  }

  // --------------------------------------------------------------------------
  // Mode 3: Custom function
  // --------------------------------------------------------------------------

  private async _initTokenProvider(): Promise<void> {
    const result = await this._tokenProvider!();
    this._applyTokenResult(result);
  }

  // --------------------------------------------------------------------------
  // Shared helpers
  // --------------------------------------------------------------------------

  private _applyTokenResult(result: TokenResult): void {
    this._token = result.token;
    this._expiresIn = result.expiresIn;
    if (result.userId !== undefined) {
      this._userId = result.userId;
    }
    // Clear any recorded permanent-refresh error in the same step that
    // applies the new token. Doing this here (rather than in a separate
    // `.then` microtask of `_doRefreshDedup`) keeps the visible auth
    // state — token + lastAuthError — moving atomically, matching
    // Python's lock-protected clear at consumer_auth.py:_store_result.
    this._lastAuthError = null;
  }

  /**
   * Perform a single refresh attempt based on the active mode.
   */
  private async _refreshOnce(): Promise<void> {
    if (this._apiKey) {
      await this._refreshApiKey();
    } else if (this._tokenEndpoint) {
      const result = await this._fetchTokenEndpoint();
      this._applyTokenResult(result);
    } else if (this._tokenProvider) {
      const result = await this._tokenProvider!();
      this._applyTokenResult(result);
    }
  }

  /**
   * Deduplicated refresh: only one concurrent refresh runs at a time.
   * Additional callers share the in-flight Promise.
   */
  private _doRefreshDedup(): Promise<boolean> {
    if (this._refreshPromise) {
      return this._refreshPromise;
    }

    this._refreshPromise = this._refreshOnce()
      .then(() => {
        // _lastAuthError is cleared atomically in _applyTokenResult so
        // observers don't see a stale error between token application
        // and the .then microtask.
        this._scheduleRefresh();
        return true;
      })
      .catch((err) => {
        const error = err instanceof Error ? err : new Error(String(err));
        // Record it, not just log it. `onAuthFailure()` reports failure as a
        // bare `false`, which callers cannot tell apart from "this provider has
        // no refresh capability" — a static-token provider returns the same
        // thing without attempting anything. Leaving the error unrecorded meant
        // the only signal of a real auth outage was a log line, so a caller
        // acting on `getLastAuthError()` saw a healthy provider: the registry
        // card lookup reported a live outage as "no such agent".
        //
        // `getLastAuthError` is documented as the provider's known-broken state,
        // and a reactive refresh that failed is exactly that. Recovery is
        // unaffected — `_applyTokenResult` clears it atomically on the next
        // success, and `preflightAuthOrThrow` retries once before raising, so a
        // transient outage still self-heals on the following call.
        this._lastAuthError = new AuthRefreshFailedError(error);
        log('warn', 'reactive refresh failed', {
          event: 'consumer_auth_reactive_refresh_failed',
          error: error.message,
        });
        return false;
      })
      .finally(() => {
        this._refreshPromise = null;
      });

    return this._refreshPromise;
  }

  /**
   * Schedule proactive refresh at 80% of expiresIn. On failure, retry
   * with exponential backoff (max 3 retries). On permanent failure,
   * call onAuthError and stop scheduling.
   */
  private _scheduleRefresh(): void {
    if (this._destroyed || this._expiresIn <= 0) return;

    if (this._refreshTimer) {
      clearTimeout(this._refreshTimer);
    }

    const delayMs = this._expiresIn * 1000 * PROACTIVE_REFRESH_FACTOR;
    this._refreshTimer = setTimeout(() => {
      this._refreshTimer = null;
      this._proactiveRefreshWithRetry(0);
    }, delayMs);
  }

  private async _proactiveRefreshWithRetry(attempt: number): Promise<void> {
    if (this._destroyed) return;

    try {
      await this._refreshOnce();
      this._scheduleRefresh();
    } catch (err) {
      if (attempt < MAX_RETRIES - 1) {
        const backoff = Math.min(
          BACKOFF_BASE_MS * Math.pow(2, attempt),
          BACKOFF_MAX_MS,
        );
        if (!this._destroyed) {
          this._refreshTimer = setTimeout(() => {
            this._refreshTimer = null;
            this._proactiveRefreshWithRetry(attempt + 1);
          }, backoff);
        }
      } else {
        // Permanent failure: always log a warning so consumers without an
        // onAuthError callback aren't silent (matches Python parity and
        // the SDK contract 'either way' guarantee), then invoke the
        // callback if registered.
        const error = err instanceof Error ? err : new Error(String(err));
        this._lastAuthError = new AuthRefreshFailedError(error);
        log('warn', 'proactive refresh permanently failed', {
          event: 'consumer_auth_proactive_refresh_failed',
          error: error.message,
        });
        if (this._onAuthError) {
          this._onAuthError(error);
        }
      }
    }
  }
}
