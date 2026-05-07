/**
 * Task Credential Cache
 *
 * In-memory map that survives pipe-task handler exit. Caches ownerId,
 * writeToken (T2), and associated streamIds for each task so that
 * instance-level APIs (publishTerminal, failStream) can operate after
 * the per-task PubNub client is destroyed.
 *
 * Lifecycle:
 * - Populated during task setup from StartTask message.
 * - StreamIds added via addStream() during createStream() calls.
 * - Survives pipe-task handler exit.
 * - Removed when the task reaches terminal state.
 * - Not persisted across process restart.
 */

export interface CachedCredentials {
  ownerId: string;
  orgId: string;
  writeToken: string;
  agentName: string;
  environment: string;
  streamIds: Set<string>;
}

export class CredentialCache {
  private readonly entries = new Map<string, CachedCredentials>();

  /** Store credentials for a task. */
  set(taskId: string, creds: Omit<CachedCredentials, 'streamIds'>): void {
    const existing = this.entries.get(taskId);
    if (existing) {
      existing.ownerId = creds.ownerId;
      existing.orgId = creds.orgId;
      existing.writeToken = creds.writeToken;
      existing.agentName = creds.agentName;
      existing.environment = creds.environment;
      return;
    }
    this.entries.set(taskId, {
      ...creds,
      streamIds: new Set(),
    });
  }

  /** Get cached credentials for a task. */
  get(taskId: string): CachedCredentials | undefined {
    return this.entries.get(taskId);
  }

  /** Add a stream ID to a task's cache entry. */
  addStream(taskId: string, streamId: string): void {
    const entry = this.entries.get(taskId);
    if (entry) {
      entry.streamIds.add(streamId);
    }
  }

  /** Remove a task's cache entry entirely. */
  remove(taskId: string): void {
    this.entries.delete(taskId);
  }

  /** Check if a task has cached credentials. */
  has(taskId: string): boolean {
    return this.entries.has(taskId);
  }

  /** Get all task IDs with cached credentials. */
  taskIds(): string[] {
    return [...this.entries.keys()];
  }

  /** Clear all entries. */
  clear(): void {
    this.entries.clear();
  }
}
