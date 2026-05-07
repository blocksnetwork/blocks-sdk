/**
 * AgentAuth — API key-based authentication for agent instances.
 *
 * Authenticates by connecting the agent via POST /api/v1/auth/agent/connect
 * with the API key in the Bearer header. The connect response includes
 * a short-lived JWT and refresh token. Handles transparent token refresh
 * with Promise-based mutex to prevent concurrent refresh requests, and
 * provides authenticatedFetch() for automatic 401 retry.
 *
 * On refresh token invalidation, re-connects using the stored connect
 * payload (idempotent).
 */

import { CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER } from './protocol-version.js';
import { captureAffinity, injectAffinity } from './write-affinity.js';

// ============================================================================
// Error codes returned by the backend
// ============================================================================

const ERROR_CODE_API_KEY_INVALID = 'API_KEY_INVALID';
const ERROR_CODE_REFRESH_TOKEN_INVALID = 'REFRESH_TOKEN_INVALID';

// ============================================================================
// Types
// ============================================================================

/**
 * Connect payload sent to POST /api/v1/auth/agent/connect.
 * Contains instance-specific fields needed for runtime credential issuance.
 *
 * `billingMode` is required: the backend validates it against the persisted
 * agent row and uses it to pick the PAM keyset. The SDK populates this
 * value from the registry GET at boot — providers do NOT override it.
 */
export interface RegistrationPayload {
  agentName: string;
  instanceId: string;
  billingMode: 'free' | 'paid';
  listing?: 'private' | 'public';
  expectedInstances?: number;
  concurrency?: number;
  maxPendingBacklog?: number;
  maxRunningTimeSec?: number;
  deviceOs?: string;
  sdkLanguage?: string;
  sdkVersion?: string;
  cliVersion?: string;
  protocolVersions?: string[];
  preferredProtocolVersion?: string;
  [key: string]: unknown;
}

/**
 * Full response from the registration endpoint, including agent data and tokens.
 */
export interface RegistrationResult {
  accessToken: string;
  refreshToken: string;
  expiresIn: number;
  pamToken?: string;
  agentId?: string;
  controlChannel?: string;
  [key: string]: unknown;
}

interface ErrorResponse {
  error: string;
  code?: string;
}

/**
 * Error thrown when the API key is permanently invalid (revoked, expired, etc.).
 * This is a fatal error — the agent should shut down.
 */
export class AgentAuthFatalError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'AgentAuthFatalError';
  }
}

// ============================================================================
// AgentAuth
// ============================================================================

export class AgentAuth {
  private readonly apiKey: string;
  private readonly baseUrl: string;
  private accessToken: string | null = null;
  private refreshTokenValue: string | null = null;
  private refreshLock: Promise<void> | null = null;
  private registrationPayload: RegistrationPayload | null = null;

  constructor(apiKey: string, baseUrl: string) {
    if (!apiKey) throw new Error('apiKey is required');
    if (!baseUrl) throw new Error('baseUrl is required');
    this.apiKey = apiKey;
    this.baseUrl = baseUrl.replace(/\/+$/, '');
  }

  /**
   * Connect the agent and obtain initial JWT + refresh token.
   * Connect is the auth entry point for runtime credential issuance.
   *
   * Sends POST to /api/v1/auth/agent/connect with the API key in the
   * Bearer header and the connect payload as JSON body.
   * Stores the payload for re-connect on refresh token invalidation.
   */
  async init(registrationPayload: RegistrationPayload): Promise<RegistrationResult> {
    const url = `${this.baseUrl}/api/v1/auth/agent/connect`;

    this.registrationPayload = registrationPayload;

    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.apiKey}`,
      'Content-Type': 'application/json',
      [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
    };
    injectAffinity(headers);

    const response = await fetch(url, {
      method: 'POST',
      headers,
      body: JSON.stringify(registrationPayload),
    });

    // Capture affinity even on non-OK responses; the server only sets the
    // header on 2xx/3xx, so this is a no-op otherwise.
    captureAffinity(response.headers);

    if (!response.ok) {
      const body = (await response.json().catch(() => ({}))) as ErrorResponse;
      throw new AgentAuthFatalError(
        `Agent registration failed: ${body.error ?? `HTTP ${response.status}`}`,
      );
    }

    const data = (await response.json()) as RegistrationResult;
    this.accessToken = data.accessToken;
    this.refreshTokenValue = data.refreshToken;
    return data;
  }

  /**
   * Get the current access token (JWT). Returns null if init() has not been called.
   */
  getAccessToken(): string | null {
    return this.accessToken;
  }

  /**
   * Get the API key.
   */
  getApiKey(): string {
    return this.apiKey;
  }

  /**
   * Refresh the JWT. Uses a Promise-based mutex so concurrent callers
   * share a single refresh request instead of racing.
   */
  async refresh(): Promise<void> {
    if (this.refreshLock) {
      // Another refresh is already in progress — wait for it
      await this.refreshLock;
      return;
    }

    this.refreshLock = this._doRefresh();
    try {
      await this.refreshLock;
    } finally {
      this.refreshLock = null;
    }
  }

  /**
   * Perform the actual refresh call. On REFRESH_TOKEN_INVALID, falls back
   * to full re-init. On API_KEY_INVALID, throws a fatal error.
   */
  private async _doRefresh(): Promise<void> {
    const url = `${this.baseUrl}/api/v1/auth/agent/refresh`;

    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.apiKey}`,
      'Content-Type': 'application/json',
    };
    injectAffinity(headers);

    const response = await fetch(url, {
      method: 'POST',
      headers,
      body: JSON.stringify({ refreshToken: this.refreshTokenValue }),
    });

    captureAffinity(response.headers);

    if (response.ok) {
      const data = (await response.json()) as { accessToken: string; refreshToken: string };
      this.accessToken = data.accessToken;
      this.refreshTokenValue = data.refreshToken;
      return;
    }

    // Parse error response
    const body = (await response.json().catch(() => ({}))) as ErrorResponse;

    if (body.code === ERROR_CODE_REFRESH_TOKEN_INVALID) {
      // Refresh token invalid — re-register (idempotent)
      if (!this.registrationPayload) {
        throw new Error(
          'Cannot re-register: no registration payload stored. Call init() with a payload first.',
        );
      }
      await this.init(this.registrationPayload);
      return;
    }

    if (body.code === ERROR_CODE_API_KEY_INVALID) {
      throw new AgentAuthFatalError(
        `API key invalid or revoked: ${body.error ?? 'API_KEY_INVALID'}`,
      );
    }

    // Unknown error
    throw new Error(
      `Token refresh failed: ${body.error ?? `HTTP ${response.status}`}`,
    );
  }

  /**
   * Fetch wrapper that automatically attaches the Bearer token and
   * retries once on 401 after refreshing the token. Injects the cached
   * `X-Write-Affinity` header on outgoing requests and captures any new
   * value from responses, so every call through AgentAuth participates
   * in read-replica routing automatically — no per-call-site wiring
   * required.
   */
  async authenticatedFetch(url: string, init?: RequestInit): Promise<Response> {
    const doFetch = async (token: string): Promise<Response> => {
      const headers = new Headers(init?.headers);
      headers.set('Authorization', `Bearer ${token}`);
      if (!headers.has(PROTOCOL_VERSION_HEADER)) {
        headers.set(PROTOCOL_VERSION_HEADER, CURRENT_PROTOCOL_VERSION);
      }
      const affinity: Record<string, string> = {};
      injectAffinity(affinity);
      for (const [k, v] of Object.entries(affinity)) {
        headers.set(k, v);
      }
      const response = await fetch(url, { ...init, headers });
      captureAffinity(response.headers);
      return response;
    };

    // First attempt
    const token = this.accessToken;
    if (!token) {
      throw new Error('AgentAuth not initialized — call init() first');
    }

    const response = await doFetch(token);

    if (response.status !== 401) {
      return response;
    }

    // 401 — refresh and retry once
    await this.refresh();

    const newToken = this.accessToken;
    if (!newToken) {
      throw new Error('Failed to obtain access token after refresh');
    }

    return doFetch(newToken);
  }
}
