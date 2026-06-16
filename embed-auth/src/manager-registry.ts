/**
 * Module-local registry of live `EmbeddedAuthSessionManager` instances,
 * keyed by partition key.
 *
 * Purpose: `signOut` must invalidate not just persisted state but every
 * in-memory JWT held by managers handed to live `TaskClient`s during this
 * page's lifetime. Without this registry, `signOut` clears storage but the
 * manager keeps its cached JWT until TTL — `tokenProvider` would happily
 * return the still-fresh JWT for up to ~5 minutes, contradicting the
 * documented "next request fails after signOut" contract.
 *
 * Scope: single page context. The widget runs in one window; no
 * cross-frame / cross-tab sharing needed. The map is reset only by test
 * code via `__clearForTesting`.
 */
import type { EmbeddedAuthSessionManager } from './refresh.js';

const managers = new Map<string, EmbeddedAuthSessionManager>();

/**
 * Register a manager under its partition key. If a manager was already
 * registered for the same partition, the previous one is replaced — the
 * new manager owns the live JWT for that partition.
 */
export function register(
  partitionKey: string,
  manager: EmbeddedAuthSessionManager,
): void {
  managers.set(partitionKey, manager);
}

/** Drop the registration for `partitionKey`. No-op if absent. */
export function unregister(partitionKey: string): void {
  managers.delete(partitionKey);
}

/** Look up the manager bound to `partitionKey`, if any. */
export function get(
  partitionKey: string,
): EmbeddedAuthSessionManager | undefined {
  return managers.get(partitionKey);
}

/**
 * Snapshot every `(partitionKey, manager)` currently registered. Callers
 * iterate the snapshot so mutating the registry during iteration is safe.
 */
export function getAll(): Array<[string, EmbeddedAuthSessionManager]> {
  return Array.from(managers.entries());
}

/** Test seam — reset the registry between specs. */
export function __clearForTesting(): void {
  managers.clear();
}
