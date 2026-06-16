import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  CURRENT_PROTOCOL_VERSION,
  PROTOCOL_VERSION_HEADER,
} from '../src/protocol-version.js';
import { EmbeddedAuthSessionManager } from '../src/refresh.js';
import { signOut } from '../src/signout.js';
import {
  computePartitionKey,
  createStorageBackend,
  type StorageBackend,
} from '../src/storage.js';
import type { SessionData } from '../src/types.js';

const PAGE_ORIGIN = 'https://partner.example';
const BACKEND = 'https://blocks.ai';

interface MockFetchCall {
  url: string;
  init: RequestInit;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyFn = (...args: any[]) => any;

function setupFetch(
  responder: (call: MockFetchCall) => Response | Promise<Response>,
): { fn: AnyFn; calls: MockFetchCall[] } {
  const calls: MockFetchCall[] = [];
  const fn = vi.fn((url: string, init: RequestInit) => {
    calls.push({ url, init });
    return Promise.resolve(responder({ url, init }));
  });
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).fetch = fn;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return { fn: fn as any, calls };
}

async function seedPartition(
  storage: StorageBackend,
  agentNames: string[],
  over: Partial<SessionData> = {},
): Promise<string> {
  const pk = await computePartitionKey({
    backendBaseUrl: BACKEND,
    pageOrigin: PAGE_ORIGIN,
    agentNames,
  });
  const session: SessionData = {
    refreshToken: `r-${agentNames.join('-')}`,
    agentIds: agentNames.map((_, i) => `id-${i}`),
    agents: agentNames.map((n, i) => ({
      name: n,
      id: `id-${i}`,
      billingMode: 'free',
    })),
    orgId: 'org-1',
    userId: 'user-1',
    pageOrigin: PAGE_ORIGIN,
    backendBaseUrl: BACKEND,
    ...over,
  };
  storage.setSession(pk, session);
  return pk;
}

beforeEach(() => {
  localStorage.clear();
  // jsdom default origin is `http://localhost:3000`. Reroute via a mocked
  // window.location.origin by stubbing the property only when needed; tests
  // that depend on `window.location.origin === PAGE_ORIGIN` will instead
  // seed sessions using `pageOrigin: window.location.origin`.
});

afterEach(() => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).fetch = undefined;
});

describe('signOut', () => {
  it('no-args: revokes every session under window.location.origin', async () => {
    const storage = createStorageBackend(localStorage);
    const origin = window.location.origin;
    const pk1 = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['translator'],
    });
    const pk2 = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['summarizer'],
    });
    storage.setSession(pk1, {
      refreshToken: 'r-1',
      agentIds: ['id-1'],
      agents: [{ name: 'translator', id: 'id-1', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });
    storage.setSession(pk2, {
      refreshToken: 'r-2',
      agentIds: ['id-2'],
      agents: [{ name: 'summarizer', id: 'id-2', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });
    const { calls } = setupFetch(() => new Response(null, { status: 204 }));

    await signOut({ storage });

    expect(calls).toHaveLength(2);
    const tokens = calls.map((c) => JSON.parse(c.init.body as string).refreshToken).sort();
    expect(tokens).toEqual(['r-1', 'r-2']);
    expect(storage.getSession(pk1)).toBeNull();
    expect(storage.getSession(pk2)).toBeNull();
  });

  it('signOut() with no partitions on this origin is a no-op (no fetch, no error)', async () => {
    const storage = createStorageBackend(localStorage);
    const { calls } = setupFetch(() => new Response(null, { status: 204 }));
    await expect(signOut({ storage })).resolves.toBeUndefined();
    expect(calls).toHaveLength(0);
  });

  it('signOut() revokes every partition on the origin (no per-agent selector exists)', async () => {
    // impl_07 follow-up #4: per-agent and per-set selectors were
    // removed. signOut is always whole-Blocks-on-this-page. This test
    // locks in the new contract by setting two partitions on the same
    // origin and asserting BOTH get revoked, not just one.
    const storage = createStorageBackend(localStorage);
    const origin = window.location.origin;
    const pkSolo = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['translator'],
    });
    const pkDuo = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['translator', 'summarizer'],
    });
    storage.setSession(pkSolo, {
      refreshToken: 'r-solo',
      agentIds: ['id-1'],
      agents: [{ name: 'translator', id: 'id-1', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });
    storage.setSession(pkDuo, {
      refreshToken: 'r-duo',
      agentIds: ['id-1', 'id-2'],
      agents: [
        { name: 'translator', id: 'id-1', billingMode: 'free' },
        { name: 'summarizer', id: 'id-2', billingMode: 'free' },
      ],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });
    const { calls } = setupFetch(() => new Response(null, { status: 204 }));

    await signOut({ storage });

    expect(calls).toHaveLength(2);
    const revokedTokens = new Set(
      calls.map((c) => JSON.parse(c.init.body as string).refreshToken),
    );
    expect(revokedTokens).toEqual(new Set(['r-solo', 'r-duo']));
    expect(storage.getSession(pkSolo)).toBeNull();
    expect(storage.getSession(pkDuo)).toBeNull();
  });

  it('partial fetch failure: failing partition still cleared, other sessions revoke independently', async () => {
    const storage = createStorageBackend(localStorage);
    const origin = window.location.origin;
    const pk1 = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['agentX'],
    });
    const pk2 = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['agentY'],
    });
    storage.setSession(pk1, {
      refreshToken: 'r-1',
      agentIds: ['id-x'],
      agents: [{ name: 'agentX', id: 'id-x', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });
    storage.setSession(pk2, {
      refreshToken: 'r-2',
      agentIds: ['id-y'],
      agents: [{ name: 'agentY', id: 'id-y', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });

    setupFetch(({ init }) => {
      const body = JSON.parse(init.body as string);
      if (body.refreshToken === 'r-1') {
        return new Response('boom', { status: 500 });
      }
      return new Response(null, { status: 204 });
    });

    await signOut({ storage });
    // Both partitions cleared regardless of network outcome.
    expect(storage.getSession(pk1)).toBeNull();
    expect(storage.getSession(pk2)).toBeNull();
  });

  it('every outbound revoke request carries the protocol-version header', async () => {
    const storage = createStorageBackend(localStorage);
    const origin = window.location.origin;
    const pk = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['translator'],
    });
    storage.setSession(pk, {
      refreshToken: 'r-1',
      agentIds: ['id-1'],
      agents: [{ name: 'translator', id: 'id-1', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });
    const { calls } = setupFetch(() => new Response(null, { status: 204 }));

    await signOut({ storage });

    expect(calls).toHaveLength(1);
    const headers = calls[0].init.headers as Record<string, string>;
    expect(headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
    expect(headers['Content-Type']).toBe('application/json');
    expect(calls[0].url).toBe(`${BACKEND}/api/v1/auth/embed/revoke`);
    expect(calls[0].init.method).toBe('POST');
    expect(calls[0].init.credentials).toBe('omit');
  });

  it('does not forward X-Blocks-Dev-Origin-Grant on revoke', async () => {
    // Per-origin allowlist removed — revoke headers are exactly
    // Content-Type + Blocks-Protocol-Version. The dev-origin header is
    // gone from the public surface.
    const storage = createStorageBackend(localStorage);
    const origin = window.location.origin;
    const pk = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['translator'],
    });
    storage.setSession(pk, {
      refreshToken: 'r-1',
      agentIds: ['id-1'],
      agents: [{ name: 'translator', id: 'id-1', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });
    const { calls } = setupFetch(() => new Response(null, { status: 204 }));

    await signOut({ storage });

    const headers = calls[0].init.headers as Record<string, string>;
    expect(headers['X-Blocks-Dev-Origin-Grant']).toBeUndefined();
    expect(Object.keys(headers).sort()).toEqual(
      ['Blocks-Protocol-Version', 'Content-Type'].sort(),
    );
  });

  it('clears in-memory managers via the registry before clearing storage (reviewer #A)', async () => {
    // Bug being locked in: signOut used to revoke the refresh token + clear
    // storage but leave any live `EmbeddedAuthSessionManager` (held by a
    // `TaskClient` returned from `signInAndGetClients`) untouched. The
    // manager's cached JWT would keep satisfying `tokenProvider` for ~5min
    // until TTL — contradicting the documented "next request fails after
    // signOut" promise.
    //
    // Fix: `signOut` walks the registry, calls `manager.clear()` for each
    // matched partition (making `tokenProvider` reject with
    // `NO_REFRESH_TOKEN` per `refresh.ts` clear() contract), then drops
    // the registration before clearing storage.
    const storage = createStorageBackend(localStorage);
    const origin = window.location.origin;
    const pk = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['translator'],
    });
    storage.setSession(pk, {
      refreshToken: 'r-live',
      agentIds: ['id-1'],
      agents: [{ name: 'translator', id: 'id-1', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });

    // Seed a real manager and put it in a fake registry.
    const manager = new EmbeddedAuthSessionManager({
      refreshUrl: `${BACKEND}/api/v1/auth/embed/refresh`,
      storage,
      partitionKey: pk,
    });
    manager.seedFromEnvelope({
      jwt: 'h.p.s',
      expiresAt: Date.now() + 60_000,
      agentIds: ['id-1'],
      userId: 'user-1',
    });
    // tokenProvider is fresh before signOut.
    await expect(manager.tokenProvider()).resolves.toMatchObject({
      token: 'h.p.s',
    });

    const unregisterSpy = vi.fn();
    const fakeRegistry = {
      get: (key: string) => (key === pk ? manager : undefined),
      unregister: unregisterSpy,
    };

    setupFetch(() => new Response(null, { status: 204 }));

    await signOut({ storage, registry: fakeRegistry });

    // Manager was unregistered.
    expect(unregisterSpy).toHaveBeenCalledWith(pk);
    // tokenProvider now rejects with NO_REFRESH_TOKEN (per refresh.ts:113
    // contract): the cleared manager refuses to hand out a token even
    // though the JWT had not yet expired.
    await expect(manager.tokenProvider()).rejects.toMatchObject({
      name: 'BlocksAuthError',
      code: 'NO_REFRESH_TOKEN',
    });
    // Storage was also cleared.
    expect(storage.getSession(pk)).toBeNull();
  });

  it('signOut with no registry adapter still clears storage (backwards-compatible deps shape)', async () => {
    // Defensive: the registry adapter is optional. If a caller wires
    // signOut without one (e.g. an isolated test), the storage path must
    // still run as before.
    const storage = createStorageBackend(localStorage);
    const origin = window.location.origin;
    const pk = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['translator'],
    });
    storage.setSession(pk, {
      refreshToken: 'r-1',
      agentIds: ['id-1'],
      agents: [{ name: 'translator', id: 'id-1', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });
    setupFetch(() => new Response(null, { status: 204 }));
    await signOut({ storage });
    expect(storage.getSession(pk)).toBeNull();
  });

  it('runs revoke calls in parallel via Promise.all', async () => {
    const storage = createStorageBackend(localStorage);
    const origin = window.location.origin;
    const pk1 = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['agentP'],
    });
    const pk2 = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: origin,
      agentNames: ['agentQ'],
    });
    storage.setSession(pk1, {
      refreshToken: 'r-1',
      agentIds: ['id-p'],
      agents: [{ name: 'agentP', id: 'id-p', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });
    storage.setSession(pk2, {
      refreshToken: 'r-2',
      agentIds: ['id-q'],
      agents: [{ name: 'agentQ', id: 'id-q', billingMode: 'free' }],
      orgId: 'org-1',
      userId: 'user-1',
      pageOrigin: origin,
      backendBaseUrl: BACKEND,
    });

    let inFlight = 0;
    let observedMaxInFlight = 0;
    setupFetch(async () => {
      inFlight += 1;
      observedMaxInFlight = Math.max(observedMaxInFlight, inFlight);
      await new Promise((r) => setTimeout(r, 5));
      inFlight -= 1;
      return new Response(null, { status: 204 });
    });

    await signOut({ storage });
    expect(observedMaxInFlight).toBe(2);
  });
});
