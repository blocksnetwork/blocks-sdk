/**
 * `localStorage` abstraction with try/catch fallback to in-memory storage
 * (Safari private mode + quota errors).
 *
 * **Storage discipline (impl_03 §R4.7 / C345-3-1).** JWTs are NEVER persisted
 * on disk. The persisted shape is `SessionData` from `types.ts` — refresh
 * token, cached agent scope, and resume metadata only. The widget's in-
 * memory `EmbeddedAuthSessionManager` owns the JWT for the page lifetime;
 * on reload, the resume path silent-refreshes from the persisted refresh
 * token to mint a fresh JWT.
 *
 * Active-sessions index: a single key (`ACTIVE_SESSIONS_KEY`) holds an
 * array of `{ pageOrigin, partitionKey, createdAt, backendBaseUrl }`
 * entries. `setSession` appends; `clear` removes; `listActiveSessions`
 * filters by `pageOrigin` and prunes stale entries.
 */
import { ACTIVE_SESSIONS_KEY, STORAGE_KEY_PREFIX } from './constants.js';
import type { SessionData } from './types.js';

/** Active-sessions index entry. Used by `signOut` (R4.8) for partition discovery. */
export interface ActiveSessionEntry {
  pageOrigin: string;
  partitionKey: string;
  createdAt: number;
  backendBaseUrl: string;
}

/** Storage backend abstraction. Bound to one raw `Storage` (or in-memory fallback). */
export interface StorageBackend {
  setSession(partitionKey: string, data: SessionData): void;
  getSession(partitionKey: string): SessionData | null;
  /**
   * Refresh-time scope update. Persists `agentIds` (may NARROW post grant
   * revocation), `userId`, and the rotated `refreshToken` (the submitted
   * token is dead server-side after a successful refresh; persisting the
   * new one is required for subsequent refreshes). MUST NOT write the
   * JWT, expiresAt, or any in-memory-only field to disk (C345-3-1).
   */
  updateScope(
    partitionKey: string,
    scope: { agentIds: string[]; userId: string; refreshToken: string },
  ): void;
  clear(partitionKey: string): void;
  listActiveSessions(pageOrigin: string): ActiveSessionEntry[];
}

/** Build a `localStorage`-shaped object backed by a Map for the in-memory fallback. */
function createMemoryStorage(): Storage {
  const map = new Map<string, string>();
  const storage: Storage = {
    get length() {
      return map.size;
    },
    clear(): void {
      map.clear();
    },
    getItem(key: string): string | null {
      return map.has(key) ? map.get(key)! : null;
    },
    key(index: number): string | null {
      return Array.from(map.keys())[index] ?? null;
    },
    removeItem(key: string): void {
      map.delete(key);
    },
    setItem(key: string, value: string): void {
      map.set(key, value);
    },
  };
  return storage;
}

/** Resolve the partition key into the namespaced `localStorage` key. */
function partitionedKey(partitionKey: string): string {
  return `${STORAGE_KEY_PREFIX}:${partitionKey}`;
}

/**
 * Compute the partition key for a `(backendBaseUrl, pageOrigin, agentNames)`
 * tuple. `agentNames` is case-sensitively sorted so `[A,B]` and `[B,A]`
 * collapse to the same hash (order independence is a correct invariant —
 * agent order shouldn't matter) but `[Foo]` and `[foo]` do NOT collapse.
 * The names are bare `agentName` values (`^[a-zA-Z0-9_]+$`) per C345-2-1
 * — no `<org>/<agent>` form anywhere in the embed surface.
 *
 * **Case sensitivity (reviewer #B):** the backend agent-name validator
 * is `^[a-zA-Z0-9_]+$` and the DB unique index is on plain `text`, so
 * `Foo` and `foo` are legitimately distinct agents. Lowercasing here
 * would collapse them into one storage partition, letting a session for
 * `Foo` be resumed when the page asks for `foo` (and vice versa).
 *
 * Any session previously persisted under the old lowercased key becomes
 * unreachable after this change — the widget will treat it as "not
 * signed in" and re-popup. That's acceptable: the feature has not yet
 * shipped to GA.
 *
 * Returns a hex-encoded SHA-256 digest. Uses `crypto.subtle.digest`.
 */
export async function computePartitionKey(args: {
  backendBaseUrl: string;
  pageOrigin: string;
  agentNames: string[];
}): Promise<string> {
  const sorted = [...args.agentNames].sort();
  const input = `${args.backendBaseUrl}|${args.pageOrigin}|${sorted.join(',')}`;
  const bytes = new TextEncoder().encode(input);
  const digest = await crypto.subtle.digest('SHA-256', bytes);
  return bytesToHex(new Uint8Array(digest));
}

function bytesToHex(bytes: Uint8Array): string {
  let out = '';
  for (let i = 0; i < bytes.length; i++) {
    out += bytes[i].toString(16).padStart(2, '0');
  }
  return out;
}

/**
 * Create a storage backend wrapping the supplied raw `Storage` (defaults to
 * `localStorage`). Every access is try/catch wrapped: on the first throw,
 * the backend swaps to an in-memory `Map` for the page lifetime so callers
 * see a uniform interface regardless of Safari private mode / quota
 * exhaustion.
 */
export function createStorageBackend(raw?: Storage): StorageBackend {
  // eslint-disable-next-line prefer-const
  let store: Storage = raw ?? defaultRawStorage();

  // Probe the supplied storage; if it throws on read OR write, swap to memory.
  try {
    const probeKey = '__blocks_auth_probe__';
    store.setItem(probeKey, '1');
    store.removeItem(probeKey);
  } catch {
    store = createMemoryStorage();
  }

  function safeGet(key: string): string | null {
    try {
      return store.getItem(key);
    } catch {
      store = createMemoryStorage();
      return null;
    }
  }

  function safeSet(key: string, value: string): void {
    try {
      store.setItem(key, value);
    } catch {
      // Swap to memory fallback and retry once. The memory store cannot
      // throw, so a second failure would be a true bug.
      const memory = createMemoryStorage();
      // Migrate any prior keys we can read to keep the page-lifetime view consistent.
      try {
        for (let i = 0; i < store.length; i++) {
          const k = store.key(i);
          if (!k) continue;
          const v = store.getItem(k);
          if (v != null) memory.setItem(k, v);
        }
      } catch {
        // If iteration itself fails, just start fresh.
      }
      store = memory;
      store.setItem(key, value);
    }
  }

  function safeRemove(key: string): void {
    try {
      store.removeItem(key);
    } catch {
      store = createMemoryStorage();
    }
  }

  function readIndex(): ActiveSessionEntry[] {
    const raw = safeGet(ACTIVE_SESSIONS_KEY);
    if (raw == null) return [];
    try {
      const parsed = JSON.parse(raw);
      if (!Array.isArray(parsed)) return [];
      return parsed.filter(isActiveSessionEntry);
    } catch {
      // Corrupt index — drop it.
      safeRemove(ACTIVE_SESSIONS_KEY);
      return [];
    }
  }

  function writeIndex(entries: ActiveSessionEntry[]): void {
    safeSet(ACTIVE_SESSIONS_KEY, JSON.stringify(entries));
  }

  return {
    setSession(partitionKey, data): void {
      // SessionData.refreshToken / agentIds / agents / orgId / userId /
      // pageOrigin / backendBaseUrl / cdmUrl only. NO token / jwt /
      // expiresAt — those live in memory only (C345-3-1).
      const sanitized: SessionData = {
        refreshToken: data.refreshToken,
        agentIds: [...data.agentIds],
        agents: data.agents.map((a) => ({ ...a })),
        orgId: data.orgId,
        userId: data.userId,
        pageOrigin: data.pageOrigin,
        backendBaseUrl: data.backendBaseUrl,
        ...(data.cdmUrl !== undefined ? { cdmUrl: data.cdmUrl } : {}),
      };
      safeSet(partitionedKey(partitionKey), JSON.stringify(sanitized));

      const index = readIndex();
      const filtered = index.filter((e) => e.partitionKey !== partitionKey);
      filtered.push({
        pageOrigin: data.pageOrigin,
        partitionKey,
        createdAt: Date.now(),
        backendBaseUrl: data.backendBaseUrl,
      });
      writeIndex(filtered);
    },

    getSession(partitionKey): SessionData | null {
      const raw = safeGet(partitionedKey(partitionKey));
      if (raw == null) return null;
      try {
        const parsed = JSON.parse(raw);
        if (!isSessionData(parsed)) {
          // Corrupt; drop it so we don't keep handing it back.
          safeRemove(partitionedKey(partitionKey));
          return null;
        }
        return parsed;
      } catch {
        safeRemove(partitionedKey(partitionKey));
        return null;
      }
    },

    updateScope(partitionKey, scope): void {
      const existing = this.getSession(partitionKey);
      if (!existing) return;
      const next: SessionData = {
        ...existing,
        agentIds: [...scope.agentIds],
        userId: scope.userId,
        refreshToken: scope.refreshToken,
      };
      // Re-route through setSession-like path WITHOUT touching the index.
      // (Index entry already exists for this partition.) DO NOT write the
      // JWT or expiresAt to disk — C345-3-1 invariant. The refresh token
      // rotation IS persisted: the submitted refresh token is revoked
      // server-side, so the new one must replace it locally for the next
      // refresh call to succeed.
      safeSet(partitionedKey(partitionKey), JSON.stringify(next));
    },

    clear(partitionKey): void {
      safeRemove(partitionedKey(partitionKey));
      const index = readIndex();
      const filtered = index.filter((e) => e.partitionKey !== partitionKey);
      if (filtered.length === index.length) return;
      writeIndex(filtered);
    },

    listActiveSessions(pageOrigin): ActiveSessionEntry[] {
      const index = readIndex();
      const live: ActiveSessionEntry[] = [];
      const dead: string[] = [];
      for (const entry of index) {
        if (entry.pageOrigin !== pageOrigin) {
          live.push(entry);
          continue;
        }
        const blob = safeGet(partitionedKey(entry.partitionKey));
        if (blob == null) {
          dead.push(entry.partitionKey);
        } else {
          live.push(entry);
        }
      }
      if (dead.length > 0) {
        const pruned = index.filter((e) => !dead.includes(e.partitionKey));
        writeIndex(pruned);
      }
      return live.filter((e) => e.pageOrigin === pageOrigin);
    },
  };
}

function defaultRawStorage(): Storage {
  // jsdom + browser: `globalThis.localStorage` is present. If absent (Node
  // server-side render of consumer code), fall through to in-memory.
  try {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const ls = (globalThis as any).localStorage as Storage | undefined;
    if (ls) return ls;
  } catch {
    // ignore
  }
  return createMemoryStorage();
}

function isActiveSessionEntry(v: unknown): v is ActiveSessionEntry {
  if (typeof v !== 'object' || v === null) return false;
  const o = v as Record<string, unknown>;
  return (
    typeof o.pageOrigin === 'string' &&
    typeof o.partitionKey === 'string' &&
    typeof o.createdAt === 'number' &&
    typeof o.backendBaseUrl === 'string'
  );
}

function isSessionData(v: unknown): v is SessionData {
  if (typeof v !== 'object' || v === null) return false;
  const o = v as Record<string, unknown>;
  return (
    typeof o.refreshToken === 'string' &&
    Array.isArray(o.agentIds) &&
    Array.isArray(o.agents) &&
    typeof o.orgId === 'string' &&
    typeof o.userId === 'string' &&
    typeof o.pageOrigin === 'string' &&
    typeof o.backendBaseUrl === 'string'
  );
}
