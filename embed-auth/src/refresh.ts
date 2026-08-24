/**
 * `EmbeddedAuthSessionManager` — single shared refresh loop across every
 * `TaskClient` returned by `signInAndGetClients`.
 *
 * Owns:
 *   - the in-memory JWT (`token` + `expiresAt` ms)
 *   - the partition-bound storage facade (refresh-token / scope persistence)
 *   - one in-flight refresh promise (concurrent-refresh dedup, R4.6)
 *
 * There is intentionally **no** independent proactive-refresh timer: refresh
 * timing is owned by the consumer SDK (`ConsumerAuth`), which calls
 * `tokenProvider`. A background timer here would rotate (and the backend would
 * immediately revoke) the refresh-token row while a consumer still holds the
 * JWT bound to it, producing intermittent "refresh token revoked" RPC errors.
 *
 * The class exposes `tokenProvider` as the SDK Mode 3 callback: pass it
 * directly to `TaskClient.create({ tokenProvider })`.
 *
 * **JWT discipline.** The JWT and its `expiresAt` live in memory
 * only. On every successful refresh, the manager calls
 * `storage.updateScope` with `{ agentIds, userId }` — never `token`. Tests
 * iterate `localStorage` and assert no `token`/`jwt`/`expiresAt` field
 * appears at any depth.
 */
import {
  CURRENT_PROTOCOL_VERSION,
  PROTOCOL_VERSION_HEADER,
} from './protocol-version.js';
import { BlocksAuthError, type TokenResult } from './types.js';
import type { StorageBackend } from './storage.js';

/**
 * Staleness threshold for `tokenProvider`: a JWT with less than this remaining
 * triggers a refresh on the next token request. The consumer SDK
 * (`ConsumerAuth`) drives refresh timing — this manager has no independent
 * refresh timer, so it can never rotate the refresh-token row out from under a
 * consumer that is still holding the matching JWT.
 */
const REFRESH_LEEWAY_MS = 10_000;

export interface EmbeddedAuthSessionManagerOptions {
  /** `${backendBaseUrl}/api/v1/auth/embed/refresh`. */
  refreshUrl: string;
  storage: StorageBackend;
  partitionKey: string;
  /** Invoked exactly once when refresh permanently fails (e.g. 401). */
  onAuthError?: (error: BlocksAuthError) => void;
}

export interface SessionSeed {
  jwt: string;
  /** Unix epoch milliseconds. */
  expiresAt: number;
  agentIds: string[];
  userId: string;
}

export class EmbeddedAuthSessionManager {
  private readonly refreshUrl: string;
  private readonly storage: StorageBackend;
  private readonly partitionKey: string;
  private readonly onAuthError?: (error: BlocksAuthError) => void;

  private currentToken: string | null = null;
  /** Unix epoch milliseconds. */
  private currentExpiresAt = 0;
  private currentAgentIds: string[] = [];
  private currentUserId: string | null = null;

  private inFlight: Promise<TokenResult> | null = null;
  private cleared = false;

  constructor(opts: EmbeddedAuthSessionManagerOptions) {
    this.refreshUrl = opts.refreshUrl;
    this.storage = opts.storage;
    this.partitionKey = opts.partitionKey;
    this.onAuthError = opts.onAuthError;
  }

  /**
   * SDK Mode 3 callback. Returns the current JWT if still fresh enough;
   * otherwise triggers a refresh. Concurrent calls share one in-flight
   * promise (R4.6) — exactly one network request for N concurrent calls.
   *
   * Bound (arrow) so callers can pass it as a free function:
   * `TaskClient.create({ tokenProvider: manager.tokenProvider })`.
   */
  readonly tokenProvider = async (): Promise<TokenResult> => {
    if (this.cleared) {
      throw new BlocksAuthError('NO_REFRESH_TOKEN');
    }
    // Join any in-flight refresh BEFORE returning the cached snapshot. A
    // refresh rotates the refresh-token row, which the backend revokes
    // immediately; the JWT is bound to that row for liveness. Handing out the
    // cached JWT while a rotation is running would return a token whose row is
    // about to be revoked → "Embedded refresh token revoked or expired" on the
    // next RPC. Waiting yields the rotated, live token instead.
    if (this.inFlight) {
      return this.inFlight;
    }
    if (this.isFresh()) {
      return this.snapshot();
    }
    return this.refreshDeduped();
  };

  /**
   * Seed the manager with the JWT pair from the popup envelope. Called once
   * after popup completion. The JWT is held in memory only.
   */
  seedFromEnvelope(seed: SessionSeed): void {
    this.cleared = false;
    this.currentToken = seed.jwt;
    this.currentExpiresAt = seed.expiresAt;
    this.currentAgentIds = [...seed.agentIds];
    this.currentUserId = seed.userId;
  }

  /** Force a refresh regardless of TTL. */
  forceRefresh(): Promise<TokenResult> {
    if (this.cleared) {
      return Promise.reject(new BlocksAuthError('NO_REFRESH_TOKEN'));
    }
    return this.refreshDeduped();
  }

  /**
   * Clear in-memory JWT + scheduled timer. Subsequent `tokenProvider` calls
   * reject with `NO_REFRESH_TOKEN`. Does NOT touch the persisted refresh
   * token; storage cleanup is the caller's responsibility (signOut path).
   */
  clear(): void {
    this.cleared = true;
    this.currentToken = null;
    this.currentExpiresAt = 0;
    this.currentAgentIds = [];
    this.currentUserId = null;
  }

  private isFresh(): boolean {
    if (!this.currentToken) return false;
    return Date.now() + REFRESH_LEEWAY_MS < this.currentExpiresAt;
  }

  private snapshot(): TokenResult {
    if (!this.currentToken || this.currentUserId === null) {
      // Defensive — `isFresh()` guard should prevent this path.
      throw new BlocksAuthError('NO_REFRESH_TOKEN');
    }
    const expiresIn = Math.max(
      1,
      Math.floor((this.currentExpiresAt - Date.now()) / 1000),
    );
    return {
      token: this.currentToken,
      expiresIn,
      agentIds: [...this.currentAgentIds],
      userId: this.currentUserId,
    };
  }

  private refreshDeduped(): Promise<TokenResult> {
    if (this.inFlight) return this.inFlight;
    const promise = this.doRefresh().finally(() => {
      this.inFlight = null;
    });
    this.inFlight = promise;
    return promise;
  }

  private async doRefresh(): Promise<TokenResult> {
    const session = this.storage.getSession(this.partitionKey);
    if (!session?.refreshToken) {
      throw new BlocksAuthError('NO_REFRESH_TOKEN');
    }

    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
    };

    let res: Response;
    try {
      res = await fetch(this.refreshUrl, {
        method: 'POST',
        credentials: 'omit',
        headers,
        body: JSON.stringify({ refreshToken: session.refreshToken }),
      });
    } catch {
      throw new BlocksAuthError('REFRESH_NETWORK_ERROR');
    }

    if (res.status === 401) {
      this.storage.clear(this.partitionKey);
      this.clear();
      const err = new BlocksAuthError('REFRESH_FAILED');
      if (this.onAuthError) {
        try {
          this.onAuthError(err);
        } catch {
          // Caller's handler must not block the auth-error rejection.
        }
      }
      throw err;
    }
    if (res.status === 412) {
      throw new BlocksAuthError('PROTOCOL_VERSION_REJECTED');
    }
    if (!res.ok) {
      throw new BlocksAuthError('REFRESH_NETWORK_ERROR');
    }

    let body: unknown;
    try {
      body = await res.json();
    } catch {
      throw new BlocksAuthError('REFRESH_NETWORK_ERROR');
    }
    const parsed = parseRefreshResponse(body);

    // A concurrent `clear()` (signOut path) may have fired while this
    // refresh was suspended on `await fetch` / `await res.json()`. The
    // guards at the top of the flow ran before those awaits, so without
    // re-checking here we would write the rotated token, flip `cleared`
    // back to false, and re-persist a fresh refresh token into the
    // partition signOut just wiped — reviving a signed-out session.
    // Discard the parsed result instead.
    if (this.cleared) {
      throw new BlocksAuthError('NO_REFRESH_TOKEN');
    }

    // Mutate in-memory JWT state.
    this.currentToken = parsed.token;
    this.currentExpiresAt = Date.now() + parsed.expiresIn * 1000;
    this.currentAgentIds = [...parsed.agentIds];
    this.currentUserId = parsed.userId;
    this.cleared = false;

    // Persist the rotated refresh token + narrowed scope. The submitted
    // refresh token is revoked server-side; subsequent refreshes MUST
    // use the new one. JWT itself remains in-memory only.
    this.storage.updateScope(this.partitionKey, {
      agentIds: parsed.agentIds,
      userId: parsed.userId,
      refreshToken: parsed.refreshToken,
    });

    return parsed;
  }
}

/**
 * Validate the refresh response shape against
 * `embed-refresh-response.schema.json` v2.0.0 required fields:
 * `{ token, refreshToken, expiresIn, agentIds, userId }`. The
 * `refreshToken` field carries the rotated value; the submitted
 * refresh token is dead server-side after a successful response.
 */
function parseRefreshResponse(
  body: unknown,
): TokenResult & { refreshToken: string } {
  if (typeof body !== 'object' || body === null) {
    throw new BlocksAuthError('REFRESH_NETWORK_ERROR');
  }
  const o = body as Record<string, unknown>;
  if (
    typeof o.token !== 'string' ||
    typeof o.refreshToken !== 'string' ||
    typeof o.expiresIn !== 'number' ||
    !Array.isArray(o.agentIds) ||
    !o.agentIds.every((x) => typeof x === 'string') ||
    typeof o.userId !== 'string'
  ) {
    throw new BlocksAuthError('REFRESH_NETWORK_ERROR');
  }
  return {
    token: o.token,
    refreshToken: o.refreshToken,
    expiresIn: o.expiresIn,
    agentIds: o.agentIds as string[],
    userId: o.userId,
  };
}
