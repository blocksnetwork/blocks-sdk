/**
 * Shared Stream Registry
 *
 * Instance-level map of active embedded and externally-coordinated streams.
 * Named streams are ref-counted across tasks by a `taskIds: Set<string>`
 * set — one entry per ref-holding task. Unnamed streams have a single
 * taskId and are scoped to their task.
 *
 * Compatibility checks on named stream reuse:
 * - direction must match
 * - format must match
 * - external flag must match
 *
 * First creator wins for: onActivate, transport tuning options, affinity.
 * Duplicate onActivate callbacks for existing streams are silently ignored.
 *
 * `acquire()` is idempotent within a task: a second call with the same
 * `(streamId, taskId)` returns `{ isNew: false, isNewForTask: false }`
 * and does not grow the set. See the shared-stream lifecycle work, Fix (e).
 */

import type { StreamClient } from '../stream/index.js';
import type { StreamAffinity } from '../stream/descriptor.js';

export interface StreamRegistryEntry {
  streamId: string;
  direction: 'outbound' | 'inbound' | 'bidirectional';
  format: 'bytes' | 'events';
  external: boolean;
  /** Per-entry task tracking — the set of tasks that currently hold a ref. */
  taskIds: Set<string>;
  /** Affinity captured at first acquire (constant for the life of the entry). */
  affinity: StreamAffinity;
  streamClient: StreamClient | null;
  activated: boolean;
  /** The running onActivate promise, if any. */
  activatePromise: Promise<void> | null;
  /**
   * First-acquirer setup promise. Installed synchronously on the entry
   * before the first acquirer awaits `performStreamSetup`, resolved
   * once `streamClient` is installed (or rejected if setup fails).
   * Concurrent second acquirers on the same shared entry MUST await
   * this promise before consulting `streamClient`; otherwise a race
   * between first-acquirer setup and second-acquirer attach either
   * throws "Stream exists but has no client" (Node) or silently creates
   * a duplicate writer (Python). Null when setup is not in flight.
   */
  setupPromise: Promise<void> | null;
  /**
   * Derived reference count. Kept as a getter so existing callers /
   * tests that inspect `entry.refCount` continue to work after the
   * shape change (see the shared-stream lifecycle Risk note "Registry
   * shape change ripples").
   */
  readonly refCount: number;
}

/** Result of an `acquire` call: distinguishes fresh entry / fresh-for-task / idempotent. */
export interface AcquireResult {
  entry: StreamRegistryEntry;
  /** First time this entry was created (no prior ref-holders). */
  isNew: boolean;
  /**
   * First time THIS task attached to the entry. True on new entries,
   * true on existing entries when the taskId was not already tracked,
   * false when the same task re-acquires idempotently.
   */
  isNewForTask: boolean;
}

interface AcquireOpts {
  direction: 'outbound' | 'inbound' | 'bidirectional';
  format: 'bytes' | 'events';
  external: boolean;
  affinity?: StreamAffinity;
}

function buildEntry(
  streamId: string,
  taskId: string,
  opts: AcquireOpts,
): StreamRegistryEntry {
  const taskIds = new Set<string>([taskId]);
  const entry: StreamRegistryEntry = {
    streamId,
    direction: opts.direction,
    format: opts.format,
    external: opts.external,
    taskIds,
    affinity: opts.affinity ?? 'dedicated',
    streamClient: null,
    activated: false,
    activatePromise: null,
    setupPromise: null,
    // Derived getter defined below via defineProperty so TS sees the
    // field in the interface above.
    get refCount(): number {
      return taskIds.size;
    },
  };
  return entry;
}

export class StreamRegistry {
  private readonly entries = new Map<string, StreamRegistryEntry>();

  /**
   * Get or create a registry entry for a stream.
   *
   * Three-case matrix (per the shared-stream lifecycle Fix (e)):
   *   1. Entry doesn't exist  -> create, taskIds = {taskId}, isNew: true,  isNewForTask: true
   *   2. Entry exists + taskId already tracked -> idempotent no-op, isNew: false, isNewForTask: false
   *   3. Entry exists + taskId is new          -> add to set,       isNew: false, isNewForTask: true
   */
  acquire(
    streamId: string,
    taskId: string,
    opts: AcquireOpts,
  ): AcquireResult {
    const existing = this.entries.get(streamId);

    if (existing) {
      // Compatibility checks (unchanged)
      if (existing.external !== opts.external) {
        throw new Error(
          `Stream "${streamId}" incompatible: cannot mix embedded and external`,
        );
      }
      if (existing.direction !== opts.direction) {
        throw new Error(
          `Stream "${streamId}" incompatible: direction mismatch ` +
          `(existing: ${existing.direction}, requested: ${opts.direction})`,
        );
      }
      if (existing.format !== opts.format) {
        throw new Error(
          `Stream "${streamId}" incompatible: format mismatch ` +
          `(existing: ${existing.format}, requested: ${opts.format})`,
        );
      }
      const requestedAffinity = opts.affinity ?? 'dedicated';
      if (existing.affinity !== requestedAffinity) {
        throw new Error(
          `Stream "${streamId}" incompatible: affinity mismatch ` +
          `(existing: ${existing.affinity}, requested: ${requestedAffinity})`,
        );
      }

      if (existing.taskIds.has(taskId)) {
        // Case 2: idempotent. Same task, same stream — no mutation.
        return { entry: existing, isNew: false, isNewForTask: false };
      }

      // Case 3: new task attaches to an existing entry.
      existing.taskIds.add(taskId);
      return { entry: existing, isNew: false, isNewForTask: true };
    }

    // Case 1: fresh entry.
    const entry = buildEntry(streamId, taskId, opts);
    this.entries.set(streamId, entry);
    return { entry, isNew: true, isNewForTask: true };
  }

  /** Get a registry entry by stream ID. */
  get(streamId: string): StreamRegistryEntry | undefined {
    return this.entries.get(streamId);
  }

  /**
   * Release a task's reference to a stream.
   *
   * Pure bookkeeping: removes ``taskId`` from the entry's set and, on
   * last-ref, removes the entry from the registry map. Does NOT tear
   * down the underlying ``streamClient`` — callers snapshot the entry
   * before this call (via ``get()``) and invoke ``streamClient.end()``
   * themselves. This keeps the registry side-effect-free and lets
   * agent-level teardown (logging, presence updates) live in one
   * place. Matches the Python SDK's ``release()`` shape.
   *
   * Returns the remaining refCount (0 means the entry was removed).
   */
  release(streamId: string, taskId: string): number {
    const entry = this.entries.get(streamId);
    if (!entry) return 0;

    entry.taskIds.delete(taskId);

    if (entry.taskIds.size === 0) {
      this.entries.delete(streamId);
      return 0;
    }

    return entry.taskIds.size;
  }

  /**
   * Force-remove a stream entry (for failStream).
   *
   * Deletes the entry from the registry and returns it. `taskIds` on
   * the returned entry is LEFT INTACT so `failStream` can iterate the
   * set of tasks to publish terminal failure to. The returned entry is
   * disowned — `refCount` as exposed by the getter reflects the
   * current size of the retained task set (the getter reads
   * `taskIds.size` live, so a caller that mutates the set after
   * removal sees the mutation), but no one can `release` it.
   *
   * DO NOT clear `taskIds` here "for hygiene": `failStream` reads the
   * set to fan out `state: 'failed'` terminals, and Python's prior
   * implementation silently broke that fan-out by clearing the set
   * before returning. The single reader lives at
   * `agent-instance.ts#failStreamImpl` — verify its loop still works
   * before changing this behavior.
   */
  forceRemove(streamId: string): StreamRegistryEntry | undefined {
    const entry = this.entries.get(streamId);
    if (entry) {
      this.entries.delete(streamId);
    }
    return entry;
  }

  /**
   * Release all streams for a given task.
   * Returns entries whose refCount reached 0 (removed from registry).
   * Callers should end() the streamClient on each returned entry.
   */
  releaseAllForTask(taskId: string): StreamRegistryEntry[] {
    const destroyed: StreamRegistryEntry[] = [];
    for (const [streamId, entry] of this.entries) {
      if (entry.taskIds.has(taskId)) {
        entry.taskIds.delete(taskId);
        if (entry.taskIds.size === 0) {
          this.entries.delete(streamId);
          destroyed.push(entry);
        }
      }
    }
    return destroyed;
  }

  /** Count of active embedded stream processing contexts. */
  get activeStreamCount(): number {
    let count = 0;
    for (const entry of this.entries.values()) {
      if (!entry.external) {
        count++;
      }
    }
    return count;
  }

  /** All stream IDs in the registry. */
  streamIds(): string[] {
    return [...this.entries.keys()];
  }

  /** Clear the entire registry. */
  clear(): void {
    this.entries.clear();
  }
}
