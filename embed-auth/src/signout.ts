/**
 * `signOut` — revokes every embedded-auth session stored under the
 * current page's origin and clears the local storage partitions.
 *
 * impl_07 follow-up #4 — the prior per-agent (`signOut({ agent })`)
 * and per-set (`signOut({ agents })`) variants were deleted. Sign-out
 * is whole-Blocks-on-this-page: there is no UX for keeping one
 * embedded-auth session alive while killing another on the same page.
 * Cross-origin logout (across every site using embed-auth) is the
 * Blocks-side `blocks.ai` logout flow — that path sets the user's
 * `embedded_auth_revoked_after` watermark and invalidates every
 * refresh token issued before it, no widget cooperation required.
 *
 * Best-effort: a network error on revoke is swallowed (revoke is
 * idempotent + TTL caps damage), but `storage.clear` is ALWAYS called
 * for matched partitions so the local state cannot drift from the
 * user's intent.
 */
import {
  CURRENT_PROTOCOL_VERSION,
  PROTOCOL_VERSION_HEADER,
} from './protocol-version.js';
import type { EmbeddedAuthSessionManager } from './refresh.js';
import type { StorageBackend } from './storage.js';
import type { SessionData } from './types.js';

/**
 * Adapter for the in-memory manager registry. The default wiring in
 * `api.ts` plugs the module-local `manager-registry.ts` map in here;
 * tests can inject a stub to verify clear/unregister are called.
 */
export interface ManagerRegistryAdapter {
  /** Return the live manager bound to `partitionKey`, if any. */
  get(partitionKey: string): EmbeddedAuthSessionManager | undefined;
  /** Drop the registration for `partitionKey`. */
  unregister(partitionKey: string): void;
}

export interface SignOutDeps {
  storage: StorageBackend;
  registry?: ManagerRegistryAdapter;
}

/**
 * Sign out every active session under the current `window.location.origin`.
 * Resolves once every matched session has been revoked + cleared.
 *
 * Per-partition ordering:
 *   1. `manager.clear()` — invalidates the in-memory JWT so any concurrent
 *      `tokenProvider` call on a live `TaskClient` rejects with
 *      `NO_REFRESH_TOKEN` immediately. This is the contract the
 *      documented "next request fails after signOut" promise depends on;
 *      without it the cached JWT would keep succeeding until TTL.
 *   2. `revoke` — best-effort network call to invalidate the refresh
 *      token server-side. Failure is swallowed (idempotent + TTL caps
 *      damage).
 *   3. `storage.clear` — drops the persisted refresh token + active-
 *      sessions index entry.
 */
export async function signOut(deps: SignOutDeps): Promise<void> {
  const pageOrigin = resolvePageOrigin();
  const entries = deps.storage.listActiveSessions(pageOrigin);
  if (entries.length === 0) return;

  await Promise.all(
    entries.map(async (entry) => {
      // Clear the in-memory manager FIRST so concurrent tokenProvider
      // calls bound to this partition fail fast (`NO_REFRESH_TOKEN`)
      // rather than returning the still-fresh cached JWT until TTL.
      const manager = deps.registry?.get(entry.partitionKey);
      if (manager) {
        manager.clear();
        deps.registry?.unregister(entry.partitionKey);
      }
      const session = deps.storage.getSession(entry.partitionKey);
      if (session) {
        try {
          await revokeOne(session);
        } catch {
          // Best-effort. Revoke is idempotent; TTL caps damage.
        }
      }
      deps.storage.clear(entry.partitionKey);
    }),
  );
}

async function revokeOne(session: SessionData): Promise<void> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
  };
  await fetch(`${session.backendBaseUrl}/api/v1/auth/embed/revoke`, {
    method: 'POST',
    credentials: 'omit',
    headers,
    body: JSON.stringify({ refreshToken: session.refreshToken }),
  });
}

function resolvePageOrigin(): string {
  // jsdom + browser supply `window.location.origin`. In odd Node hosts this
  // can be undefined; `signOut` then trivially matches no entries (the
  // active-sessions index keys by origin), which is the right default.
  try {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const w = (globalThis as any).window as
      | { location?: { origin?: string } }
      | undefined;
    if (w?.location?.origin) return w.location.origin;
  } catch {
    // ignore
  }
  return '';
}
