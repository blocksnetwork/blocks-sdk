import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ACTIVE_SESSIONS_KEY, STORAGE_KEY_PREFIX } from '../src/constants.js';
import {
  computePartitionKey,
  createStorageBackend,
  type StorageBackend,
} from '../src/storage.js';
import type { SessionData } from '../src/types.js';

function freshSession(over: Partial<SessionData> = {}): SessionData {
  return {
    refreshToken: 'r-1',
    agentIds: ['11111111-1111-1111-1111-111111111111'],
    agents: [
      {
        name: 'translator',
        id: '11111111-1111-1111-1111-111111111111',
        billingMode: 'free',
      },
    ],
    orgId: 'org-1',
    userId: 'user-1',
    pageOrigin: 'https://partner.example',
    backendBaseUrl: 'https://blocks.ai',
    ...over,
  };
}

function rawSnapshot(): Record<string, string> {
  const snap: Record<string, string> = {};
  for (let i = 0; i < localStorage.length; i++) {
    const key = localStorage.key(i)!;
    snap[key] = localStorage.getItem(key)!;
  }
  return snap;
}

describe('storage roundtrip', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('setSession then getSession round-trips the persistent fields', () => {
    const backend = createStorageBackend(localStorage);
    const data = freshSession();
    backend.setSession('pk-1', data);
    const got = backend.getSession('pk-1');
    expect(got).toEqual(data);
  });

  it('setSession appends to the active-sessions index', () => {
    const backend = createStorageBackend(localStorage);
    backend.setSession('pk-1', freshSession({ pageOrigin: 'https://a' }));
    backend.setSession('pk-2', freshSession({ pageOrigin: 'https://a' }));
    const indexRaw = localStorage.getItem(ACTIVE_SESSIONS_KEY);
    expect(indexRaw).toBeTruthy();
    const index = JSON.parse(indexRaw!);
    expect(Array.isArray(index)).toBe(true);
    expect(index).toHaveLength(2);
    expect(index.map((e: { partitionKey: string }) => e.partitionKey).sort()).toEqual([
      'pk-1',
      'pk-2',
    ]);
  });

  it('clear removes both the partition AND the active-sessions index entry', () => {
    const backend = createStorageBackend(localStorage);
    backend.setSession('pk-1', freshSession());
    backend.setSession('pk-2', freshSession());
    backend.clear('pk-1');
    expect(backend.getSession('pk-1')).toBeNull();
    const index = JSON.parse(localStorage.getItem(ACTIVE_SESSIONS_KEY) ?? '[]');
    expect(index.map((e: { partitionKey: string }) => e.partitionKey)).toEqual(['pk-2']);
  });

  it('listActiveSessions filters by pageOrigin', () => {
    const backend = createStorageBackend(localStorage);
    backend.setSession('pk-a', freshSession({ pageOrigin: 'https://a' }));
    backend.setSession('pk-b', freshSession({ pageOrigin: 'https://b' }));
    const aSessions = backend.listActiveSessions('https://a');
    expect(aSessions).toHaveLength(1);
    expect(aSessions[0].partitionKey).toBe('pk-a');
  });

  it('listActiveSessions prunes stale index entries (no matching session blob)', () => {
    const backend = createStorageBackend(localStorage);
    backend.setSession('pk-live', freshSession({ pageOrigin: 'https://x' }));
    // Inject an orphan index entry whose partition key has no session blob.
    const indexRaw = localStorage.getItem(ACTIVE_SESSIONS_KEY)!;
    const index = JSON.parse(indexRaw);
    index.push({
      pageOrigin: 'https://x',
      partitionKey: 'pk-orphan',
      createdAt: Date.now(),
      backendBaseUrl: 'https://blocks.ai',
    });
    localStorage.setItem(ACTIVE_SESSIONS_KEY, JSON.stringify(index));

    const live = backend.listActiveSessions('https://x');
    expect(live.map((e) => e.partitionKey)).toEqual(['pk-live']);
    // The orphan must be pruned from the underlying index after the read.
    const after = JSON.parse(localStorage.getItem(ACTIVE_SESSIONS_KEY) ?? '[]');
    expect(after.map((e: { partitionKey: string }) => e.partitionKey)).toEqual([
      'pk-live',
    ]);
  });

  it('updateScope narrows agentIds without touching other fields', () => {
    const backend = createStorageBackend(localStorage);
    const seed = freshSession({
      agentIds: ['a-1', 'a-2', 'a-3'],
      agents: [
        { name: 'A', id: 'a-1', billingMode: 'free' },
        { name: 'B', id: 'a-2', billingMode: 'free' },
        { name: 'C', id: 'a-3', billingMode: 'paid' },
      ],
    });
    backend.setSession('pk-1', seed);
    backend.updateScope('pk-1', { agentIds: ['a-1', 'a-2'], userId: 'user-1', refreshToken: seed.refreshToken });
    const got = backend.getSession('pk-1')!;
    expect(got.agentIds).toEqual(['a-1', 'a-2']);
    // Other fields untouched.
    expect(got.refreshToken).toBe(seed.refreshToken);
    expect(got.orgId).toBe(seed.orgId);
    expect(got.userId).toBe(seed.userId);
    expect(got.pageOrigin).toBe(seed.pageOrigin);
    expect(got.backendBaseUrl).toBe(seed.backendBaseUrl);
    expect(got.agents).toEqual(seed.agents);
  });

  it('updateScope on missing partition is a no-op', () => {
    const backend = createStorageBackend(localStorage);
    backend.updateScope('pk-missing', { agentIds: ['a'], userId: 'u', refreshToken: 'r' });
    expect(backend.getSession('pk-missing')).toBeNull();
  });
});

describe('JWT-never-on-disk regression', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('no token / jwt / expiresAt field appears anywhere in localStorage after setSession + updateScope', () => {
    const backend = createStorageBackend(localStorage);
    backend.setSession('pk-1', {
      refreshToken: 'r-1',
      agentIds: ['a-1', 'a-2', 'a-3'],
      agents: [
        { name: 'A', id: 'a-1', billingMode: 'free' },
        { name: 'B', id: 'a-2', billingMode: 'paid' },
        { name: 'C', id: 'a-3', billingMode: 'free' },
      ],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: 'https://partner.example',
      backendBaseUrl: 'https://blocks.ai',
    });
    backend.updateScope('pk-1', { agentIds: ['a-1', 'a-2'], userId: 'user-1', refreshToken: 'r-2' });

    const forbidden = new Set(['token', 'jwt', 'expiresAt']);
    const offending: string[] = [];

    function walk(node: unknown, path: string): void {
      if (Array.isArray(node)) {
        node.forEach((v, i) => walk(v, `${path}[${i}]`));
        return;
      }
      if (node && typeof node === 'object') {
        for (const [k, v] of Object.entries(node as Record<string, unknown>)) {
          if (forbidden.has(k)) {
            offending.push(`${path}.${k}`);
          }
          walk(v, `${path}.${k}`);
        }
      }
    }

    // Iterate every key under the actual localStorage and JSON.parse each.
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)!;
      const raw = localStorage.getItem(key)!;
      try {
        walk(JSON.parse(raw), key);
      } catch {
        // Non-JSON or corrupt — also walk the raw string for the literal field name.
        for (const f of forbidden) {
          if (raw.includes(`"${f}"`)) offending.push(`${key}::${f}`);
        }
      }
    }

    expect(offending, `forbidden fields found: ${offending.join(', ')}`).toEqual([]);
  });
});

describe('Safari private mode fallback', () => {
  let originalSetItem: typeof Storage.prototype.setItem;

  beforeEach(() => {
    localStorage.clear();
    originalSetItem = localStorage.setItem.bind(localStorage);
  });

  afterEach(() => {
    // Restore by replacing the prototype method with the original.
    Storage.prototype.setItem = originalSetItem;
    localStorage.clear();
  });

  it('falls back to in-memory when localStorage.setItem throws', () => {
    // Patch the prototype so the probe in createStorageBackend trips into memory mode.
    Storage.prototype.setItem = vi.fn(() => {
      throw new DOMException('QuotaExceededError', 'QuotaExceededError');
    });

    const backend: StorageBackend = createStorageBackend(localStorage);
    const data = freshSession();
    backend.setSession('pk-1', data);
    // Subsequent reads / writes succeed without throwing.
    const got = backend.getSession('pk-1');
    expect(got).toEqual(data);
    backend.updateScope('pk-1', {
      agentIds: data.agentIds,
      userId: data.userId,
      refreshToken: data.refreshToken,
    });
    backend.clear('pk-1');
    expect(backend.getSession('pk-1')).toBeNull();

    // Real localStorage was not written to (the probe failed).
    const snap = rawSnapshot();
    expect(snap).toEqual({});
  });
});

describe('computePartitionKey', () => {
  it('is stable for the same inputs', async () => {
    const a = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['translator', 'summarizer'],
    });
    const b = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['translator', 'summarizer'],
    });
    expect(a).toBe(b);
  });

  it('is order-insensitive (collapses [B,A] and [A,B] to the same hash)', async () => {
    const a = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['translator', 'summarizer'],
    });
    const b = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['summarizer', 'translator'],
    });
    expect(a).toBe(b);
  });

  it('is case-sensitive — Foo and foo are distinct agents and distinct partitions (reviewer #B)', async () => {
    // Backend agent-name validator is `^[a-zA-Z0-9_]+$` (case-sensitive)
    // and the DB unique index is on plain `text`, so `Foo` and `foo` can
    // exist as separate agents owned by separate orgs. The widget MUST
    // NOT collapse them — a session for `Foo` must never be resumable
    // under a request for `foo`.
    const foo = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['Foo'],
    });
    const lower = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['foo'],
    });
    expect(foo).not.toBe(lower);

    // Mixed case in a set also stays distinct from the all-lowercase set.
    const mixed = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['Summarizer', 'TRANSLATOR'],
    });
    const lowered = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['summarizer', 'translator'],
    });
    expect(mixed).not.toBe(lowered);
  });

  it('changes when any tuple member changes', async () => {
    const base = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['translator'],
    });
    const otherBackend = await computePartitionKey({
      backendBaseUrl: 'https://staging.blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['translator'],
    });
    const otherOrigin = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://other.example',
      agentNames: ['translator'],
    });
    const otherAgents = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['summarizer'],
    });
    expect(base).not.toBe(otherBackend);
    expect(base).not.toBe(otherOrigin);
    expect(base).not.toBe(otherAgents);
  });

  it('produces a 64-char hex SHA-256 digest', async () => {
    const k = await computePartitionKey({
      backendBaseUrl: 'https://blocks.ai',
      pageOrigin: 'https://partner.example',
      agentNames: ['translator'],
    });
    expect(k).toMatch(/^[0-9a-f]{64}$/);
  });
});

describe('storage key namespacing', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('per-partition keys are namespaced under STORAGE_KEY_PREFIX', () => {
    const backend = createStorageBackend(localStorage);
    backend.setSession('pk-x', freshSession());
    const expectedKey = `${STORAGE_KEY_PREFIX}:pk-x`;
    expect(localStorage.getItem(expectedKey)).toBeTruthy();
  });
});
