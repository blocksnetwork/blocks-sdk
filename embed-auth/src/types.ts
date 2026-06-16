/**
 * Public type surface for `@blocks-network/embed-auth`.
 *
 * Phase 2 stub: types only. Popup, refresh, storage, and api logic land in
 * later phases.
 */

export type BlocksAuthErrorCode =
  | 'INVALID_INPUT'
  | 'POPUP_BLOCKED'
  | 'POPUP_REPLACED'
  | 'USER_CANCELLED'
  | 'AGENT_ARCHIVED'
  | 'AGENT_DISABLED'
  | 'AGENT_KILLED'
  | 'MULTI_ORG_PRIVATE_AGENTS_NOT_SUPPORTED'
  | 'TOO_MANY_AGENTS'
  | 'EMBEDDED_AUTH_DAILY_QUOTA_EXCEEDED'
  | 'NO_REFRESH_TOKEN'
  | 'REFRESH_FAILED'
  | 'REFRESH_NETWORK_ERROR'
  | 'PROTOCOL_VERSION_REJECTED'
  | 'ENVELOPE_VALIDATION_FAILED'
  | 'STATE_MISMATCH'
  | 'AGENT_SET_MISMATCH';

/**
 * Typed widget error. Every rejected promise from this package is an
 * instance of `BlocksAuthError` with one of the codes above.
 */
export class BlocksAuthError extends Error {
  readonly code: BlocksAuthErrorCode;
  readonly agent?: string;

  constructor(code: BlocksAuthErrorCode, message?: string, agent?: string) {
    super(message ?? code);
    this.name = 'BlocksAuthError';
    this.code = code;
    if (agent !== undefined) this.agent = agent;
    // Maintain prototype chain across down-leveled targets.
    Object.setPrototypeOf(this, BlocksAuthError.prototype);
  }
}

/** Per-agent metadata returned by the popup envelope. */
export interface Agent {
  /** Bare `agentName` (`^[a-zA-Z0-9_]+$`, no slashes). */
  name: string;
  /** Agent UUID. */
  id: string;
  /** Snapshot billing mode at popup time. */
  billingMode: 'free' | 'paid';
}

/** Options for `signInAndGetClient` (single-agent form). */
export interface SignInSingleOptions {
  /** Bare agent name. */
  agent: string;
  /** Override Blocks backend origin; defaults to compiled-in `BACKEND_BASE_URL_DEFAULT`. */
  backendBaseUrl?: string;
  /**
   * Override the CDM URL passed to `TaskClient.create`. When unset,
   * the widget reads `__BLOCKS_EMBED_DEV__.cdmUrl` (set by `blocks dev`)
   * if present; otherwise the SDK falls through to its compiled-in
   * default. Pass to point a partner page at a non-default CDM
   * deployment (staging, on-prem).
   */
  cdmUrl?: string;
  /** Optional handler for fatal auth errors after sign-in. */
  onAuthError?: (error: BlocksAuthError) => void;
}

/** Options for `signInAndGetClients` (multi-agent form). */
export interface SignInMultiOptions {
  /** Bare agent names. Length 1..25, all distinct. */
  agents: string[];
  /** Override Blocks backend origin; defaults to compiled-in `BACKEND_BASE_URL_DEFAULT`. */
  backendBaseUrl?: string;
  /**
   * Override the CDM URL passed to `TaskClient.create`. When unset,
   * the widget reads `__BLOCKS_EMBED_DEV__.cdmUrl` (set by `blocks dev`)
   * if present; otherwise the SDK falls through to its compiled-in
   * default. Pass to point a partner page at a non-default CDM
   * deployment (staging, on-prem).
   */
  cdmUrl?: string;
  /** Optional handler for fatal auth errors after sign-in. */
  onAuthError?: (error: BlocksAuthError) => void;
}

/**
 * Token result returned by the manager's `tokenProvider` callback. Matches
 * the SDK's `TokenResult` AND the wire schema for
 * `embed-refresh-response.schema.json`. Note `userId` is REQUIRED here; the
 * SDK's own type leaves it optional, but the embed wire schema makes it
 * required and so does our persistence path.
 */
export interface TokenResult {
  token: string;
  /** Seconds. */
  expiresIn: number;
  /** Agent UUIDs the JWT covers. May NARROW on refresh. */
  agentIds: string[];
  userId: string;
}

/**
 * Persistent session payload. JWTs are NEVER persisted on disk (TTL is
 * ~60s). The manager keeps the in-memory JWT and `expiresAt`; only the
 * refresh token, agent scope, and resume metadata go to `localStorage`.
 */
export interface SessionData {
  refreshToken: string;
  agentIds: string[];
  agents: Agent[];
  orgId: string;
  userId: string;
  pageOrigin: string;
  backendBaseUrl: string;
  /**
   * CDM URL the SDK should fetch when this session resumes after a
   * page reload. Persisted alongside `backendBaseUrl` so the `blocks
   * dev` flow's local-CDM override survives reload. Absent on
   * production sessions; the SDK falls through to its baked-in default.
   */
  cdmUrl?: string;
}

/** Success postMessage envelope (matches `postmessage-envelope.success.schema.json`). */
export interface BlocksAuthSuccessEnvelope {
  type: 'blocks-auth-success';
  version: 1;
  state: string;
  jwt: string;
  refreshToken: string;
  /** Unix epoch milliseconds. */
  expiresAt: number;
  agentIds: string[];
  agents: Agent[];
  orgId: string;
  userId: string;
}

/** Error postMessage envelope (matches `postmessage-envelope.error.schema.json`). */
export interface BlocksAuthErrorEnvelope {
  type: 'blocks-auth-error';
  version: 1;
  state: string;
  code:
    | 'AGENT_ARCHIVED'
    | 'AGENT_DISABLED'
    | 'AGENT_KILLED'
    | 'USER_CANCELLED'
    | 'MULTI_ORG_PRIVATE_AGENTS_NOT_SUPPORTED'
    | 'TOO_MANY_AGENTS'
    | 'EMBEDDED_AUTH_DAILY_QUOTA_EXCEEDED';
  message: string;
  /** Bare `agentName` for per-agent failures. Omitted otherwise. */
  agent?: string;
}
