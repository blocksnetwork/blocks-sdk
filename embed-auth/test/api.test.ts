/**
 * Mocked end-to-end smoke for `signInAndGetClient(s)` + `signOut`.
 *
 * Stubs `window.open` to synthesize a postMessage envelope on the next tick,
 * mocks `fetch` for the resume-path refresh tests, and swaps in a stub
 * `TaskClient.create` via the test-seam exports from `api.ts`.
 */
import {
  describe,
  it,
  expect,
  beforeEach,
  afterEach,
  vi,
  type MockInstance,
} from 'vitest';

import {
  signInAndGetClient,
  signInAndGetClients,
  signOut,
  __setStorageBackendForTesting,
  __setTaskClientFactoryForTesting,
} from '../src/api.js';
import * as managerRegistry from '../src/manager-registry.js';
import { __testing as popupTesting } from '../src/popup.js';
import { computePartitionKey, createStorageBackend } from '../src/storage.js';
import { BlocksAuthError } from '../src/types.js';

const BACKEND = 'https://blocks.ai';
const BACKEND_ORIGIN = 'https://blocks.ai';
const PAGE_ORIGIN = 'https://partner.example';
const REFRESH_URL = `${BACKEND}/api/v1/auth/embed/refresh`;

const AGENT_IDS = [
  '11111111-1111-1111-1111-111111111111',
  '22222222-2222-2222-2222-222222222222',
  '33333333-3333-3333-3333-333333333333',
];
const ORG_ID = '44444444-4444-4444-4444-444444444444';
const USER_ID = '55555555-5555-5555-5555-555555555555';

function makeSuccessEnvelope(state: string, agents: Array<{ name: string; billingMode: 'free' | 'paid' }>) {
  const ids = agents.map((_, i) => AGENT_IDS[i]!);
  return {
    type: 'blocks-auth-success' as const,
    version: 1 as const,
    state,
    jwt: 'h.p.s',
    refreshToken: 'r'.repeat(32),
    expiresAt: Date.now() + 60_000,
    agentIds: ids,
    agents: agents.map((a, i) => ({ name: a.name, id: ids[i]!, billingMode: a.billingMode })),
    orgId: ORG_ID,
    userId: USER_ID,
  };
}

function dispatchMessage(data: unknown, origin: string = BACKEND_ORIGIN): void {
  // `source` must be the same handle `window.open` returned — the popup
  // listener now drops envelopes whose `event.source` isn't the opened
  // window (sender-identity check).
  const event = new MessageEvent('message', {
    data,
    origin,
    source: popupHandle as unknown as MessageEventSource,
  });
  window.dispatchEvent(event);
}

let openSpy: MockInstance<Parameters<Window['open']>, ReturnType<Window['open']>>;
let fetchSpy: MockInstance<Parameters<typeof fetch>, ReturnType<typeof fetch>>;
let factorySpy: ReturnType<typeof vi.fn>;
let popupHandle: { closed: boolean; close: () => void } | null;

beforeEach(() => {
  popupTesting.inFlightPopups.clear();
  managerRegistry.__clearForTesting();
  __setStorageBackendForTesting(createStorageBackend());
  popupHandle = { closed: false, close: () => {} };
  openSpy = vi.spyOn(window, 'open').mockImplementation(() => popupHandle as unknown as Window);

  factorySpy = vi.fn().mockImplementation(async (opts: { billingMode: 'free' | 'paid'; tokenProvider?: () => Promise<unknown> }) => {
    return {
      __mock: true,
      billingMode: opts.billingMode,
      tokenProvider: opts.tokenProvider,
    };
  }) as unknown as ReturnType<typeof vi.fn>;
  __setTaskClientFactoryForTesting(factorySpy as unknown as typeof import('@blocks-network/sdk').TaskClient.create);

  // Page origin via jsdom — patch when needed.
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { origin: PAGE_ORIGIN },
  });

  fetchSpy = vi.spyOn(globalThis, 'fetch') as unknown as MockInstance<
    Parameters<typeof fetch>,
    ReturnType<typeof fetch>
  >;
});

afterEach(() => {
  vi.useRealTimers();
  __setStorageBackendForTesting(null);
  __setTaskClientFactoryForTesting(null);
  vi.restoreAllMocks();
  // Clear localStorage between tests so partition lookups don't bleed.
  try {
    localStorage.clear();
  } catch {
    // ignore
  }
});

/**
 * Stub `window.open` so it polls `inFlightPopups` for the new record (added
 * synchronously inside the popup's `new Promise(...)` body, right after
 * `window.open` returns) and then synthesizes a success envelope reply.
 * `window.open` runs synchronously *before* the Promise constructor body, so
 * we have to wait one tick for the record to be registered.
 */
function arrangePopupReply(agents: Array<{ name: string; billingMode: 'free' | 'paid' }>): void {
  openSpy.mockImplementation(() => {
    setTimeout(() => {
      const records = Array.from(popupTesting.inFlightPopups.values());
      const record = records[records.length - 1];
      if (!record) return;
      dispatchMessage(makeSuccessEnvelope(record.state, agents));
    }, 0);
    return popupHandle as unknown as Window;
  });
}

describe('signInAndGetClient (single-agent wrapper)', () => {
  it('opens a popup, returns a TaskClient', async () => {
    arrangePopupReply([{ name: 'translator', billingMode: 'free' }]);
    const client = await signInAndGetClient({ agent: 'translator', backendBaseUrl: BACKEND });
    expect(openSpy).toHaveBeenCalledTimes(1);
    expect(client).toBeDefined();
    expect((client as unknown as { billingMode: string }).billingMode).toBe('free');
    expect(factorySpy).toHaveBeenCalledTimes(1);
    // Asserts NO `agent` argument is passed to TaskClient.create.
    expect(factorySpy.mock.calls[0]![0]).not.toHaveProperty('agent');
  });

  it('rejects when called with an empty agent', async () => {
    await expect(
      signInAndGetClient({ agent: '', backendBaseUrl: BACKEND }),
    ).rejects.toMatchObject({ name: 'BlocksAuthError', code: 'INVALID_INPUT' });
  });

  it('rejects when the multi-agent shape is supplied to the single surface', async () => {
    await expect(
      signInAndGetClient({
        agent: 'translator',
        // @ts-expect-error — runtime defense
        agents: ['translator'],
        backendBaseUrl: BACKEND,
      }),
    ).rejects.toMatchObject({ name: 'BlocksAuthError', code: 'INVALID_INPUT' });
  });
});

describe('signInAndGetClients — input validation', () => {
  it('rejects an empty agents array', async () => {
    await expect(
      signInAndGetClients({ agents: [], backendBaseUrl: BACKEND }),
    ).rejects.toMatchObject({ code: 'INVALID_INPUT' });
  });

  it('rejects when both `agent` and `agents` are supplied', async () => {
    await expect(
      signInAndGetClients({
        agents: ['translator'],
        // @ts-expect-error — runtime defense for the wrong surface
        agent: 'translator',
        backendBaseUrl: BACKEND,
      }),
    ).rejects.toMatchObject({ code: 'INVALID_INPUT' });
  });

  it('rejects more than 25 agents', async () => {
    const tooMany = Array.from({ length: 26 }, (_, i) => `agent${i}`);
    await expect(
      signInAndGetClients({ agents: tooMany, backendBaseUrl: BACKEND }),
    ).rejects.toMatchObject({ code: 'INVALID_INPUT' });
  });

  it('rejects names with slashes (bare-agentName discipline)', async () => {
    await expect(
      signInAndGetClients({ agents: ['acme/translator'], backendBaseUrl: BACKEND }),
    ).rejects.toMatchObject({ code: 'INVALID_INPUT' });
  });

  it('rejects duplicates', async () => {
    await expect(
      signInAndGetClients({ agents: ['translator', 'translator'], backendBaseUrl: BACKEND }),
    ).rejects.toMatchObject({ code: 'INVALID_INPUT' });
  });

  it('accepts case-distinct names (`Foo` and `foo` are different agents)', async () => {
    // Backend treats agent names case-sensitively (validator
    // `^[a-zA-Z0-9_]+$`, no fold; DB unique index on plain `text`). The
    // widget MUST NOT collapse case-distinct entries into a duplicate.
    arrangePopupReply([
      { name: 'Foo', billingMode: 'free' },
      { name: 'foo', billingMode: 'free' },
    ]);
    const map = await signInAndGetClients({
      agents: ['Foo', 'foo'],
      backendBaseUrl: BACKEND,
    });
    expect(Object.keys(map).sort()).toEqual(['Foo', 'foo']);
  });

  it('rejects non-string entries', async () => {
    await expect(
      signInAndGetClients({
        // @ts-expect-error
        agents: [123],
        backendBaseUrl: BACKEND,
      }),
    ).rejects.toMatchObject({ code: 'INVALID_INPUT' });
  });
});

describe('signInAndGetClients — multi-agent + dedupe', () => {
  it('returns a key per agent, deduping the underlying TaskClient by billingMode', async () => {
    const agents = [
      { name: 'a', billingMode: 'free' as const },
      { name: 'b', billingMode: 'free' as const },
      { name: 'c', billingMode: 'paid' as const },
    ];
    arrangePopupReply(agents);
    const map = await signInAndGetClients({
      agents: ['a', 'b', 'c'],
      backendBaseUrl: BACKEND,
    });
    expect(Object.keys(map).sort()).toEqual(['a', 'b', 'c']);
    // Only two underlying clients (one per distinct billingMode).
    expect(factorySpy).toHaveBeenCalledTimes(2);
    expect(map.a).toBe(map.b);
    expect(map.a).not.toBe(map.c);
    // All clients share the SAME tokenProvider (one manager).
    const tpA = (map.a as unknown as { tokenProvider: unknown }).tokenProvider;
    const tpC = (map.c as unknown as { tokenProvider: unknown }).tokenProvider;
    expect(tpA).toBe(tpC);
  });
});

describe('session resume flow', () => {
  it('returns clients from a stored session without opening a popup when refresh succeeds', async () => {
    const agents = [
      { name: 'translator', id: AGENT_IDS[0]!, billingMode: 'free' as const },
    ];
    const partitionKey = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: PAGE_ORIGIN,
      agentNames: ['translator'],
    });
    const storage = createStorageBackend();
    storage.setSession(partitionKey, {
      refreshToken: 'r'.repeat(32),
      agentIds: [AGENT_IDS[0]!],
      agents,
      orgId: ORG_ID,
      userId: USER_ID,
      pageOrigin: PAGE_ORIGIN,
      backendBaseUrl: BACKEND,
    });
    __setStorageBackendForTesting(storage);

    fetchSpy.mockImplementation(async () =>
      new Response(
        JSON.stringify({
          token: 'fresh.jwt.value',
          refreshToken: 'r-rotated-fresh.jwt.value',
          expiresIn: 60,
          agentIds: [AGENT_IDS[0]!],
          userId: USER_ID,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );

    const map = await signInAndGetClients({
      agents: ['translator'],
      backendBaseUrl: BACKEND,
    });
    expect(openSpy).not.toHaveBeenCalled();
    expect(Object.keys(map)).toEqual(['translator']);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url] = fetchSpy.mock.calls[0]!;
    expect(url).toBe(REFRESH_URL);
  });

  it('falls through to popup when the stored refresh fails 401', async () => {
    const agents = [
      { name: 'translator', id: AGENT_IDS[0]!, billingMode: 'free' as const },
    ];
    const partitionKey = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: PAGE_ORIGIN,
      agentNames: ['translator'],
    });
    const storage = createStorageBackend();
    storage.setSession(partitionKey, {
      refreshToken: 'expired.refresh',
      agentIds: [AGENT_IDS[0]!],
      agents,
      orgId: ORG_ID,
      userId: USER_ID,
      pageOrigin: PAGE_ORIGIN,
      backendBaseUrl: BACKEND,
    });
    __setStorageBackendForTesting(storage);

    fetchSpy.mockImplementation(async () => new Response('', { status: 401 }));

    arrangePopupReply([{ name: 'translator', billingMode: 'free' }]);
    const map = await signInAndGetClients({
      agents: ['translator'],
      backendBaseUrl: BACKEND,
    });
    expect(openSpy).toHaveBeenCalledTimes(1);
    expect(map.translator).toBeDefined();
    // The stored partition was cleared by the manager during 401 handling
    // before the popup re-seeded it.
    expect(storage.getSession(partitionKey)?.refreshToken).not.toBe('expired.refresh');
  });

  it('resumes silently with the reachable subset when refresh narrows scope', async () => {
    // Stored: [a, b, c]. Page requests [a, b, c]. Refresh returns a live JWT
    // for the reachable subset [a, b] (e.g. C's grant was revoked). The popup
    // itself only ever grants what's reachable, so re-prompting here can't
    // recover C and would break auto-resume (POPUP_BLOCKED). Resume silently
    // with [a, b] instead — NO popup.
    const agents = [
      { name: 'a', id: AGENT_IDS[0]!, billingMode: 'free' as const },
      { name: 'b', id: AGENT_IDS[1]!, billingMode: 'free' as const },
      { name: 'c', id: AGENT_IDS[2]!, billingMode: 'paid' as const },
    ];
    const partitionKey = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: PAGE_ORIGIN,
      agentNames: ['a', 'b', 'c'],
    });
    const storage = createStorageBackend();
    storage.setSession(partitionKey, {
      refreshToken: 'r'.repeat(32),
      agentIds: AGENT_IDS,
      agents,
      orgId: ORG_ID,
      userId: USER_ID,
      pageOrigin: PAGE_ORIGIN,
      backendBaseUrl: BACKEND,
    });
    __setStorageBackendForTesting(storage);

    fetchSpy.mockImplementation(async () =>
      new Response(
        JSON.stringify({
          token: 'fresh.jwt.value',
          refreshToken: 'r-rotated-fresh.jwt.value',
          expiresIn: 60,
          agentIds: [AGENT_IDS[0]!, AGENT_IDS[1]!],
          userId: USER_ID,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );

    const map = await signInAndGetClients({
      agents: ['a', 'b', 'c'],
      backendBaseUrl: BACKEND,
    });
    // No popup: resumed silently with the reachable subset.
    expect(openSpy).not.toHaveBeenCalled();
    expect(Object.keys(map).sort()).toEqual(['a', 'b']);
  });

  it('resumes with a previously-narrowed stored session (no forced re-popup)', async () => {
    // Page originally signed in for [a, b]; the session was narrowed to [a]
    // (B's grant revoked). On reload the page requests [a, b] again. Refresh
    // returns [a]. We resume silently with [a] — we do NOT clear and re-popup
    // (which on auto-resume would be POPUP_BLOCKED and re-prompt every reload).
    const partitionKey = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: PAGE_ORIGIN,
      agentNames: ['a', 'b'],
    });
    const storage = createStorageBackend();
    storage.setSession(partitionKey, {
      refreshToken: 'r'.repeat(32),
      agentIds: [AGENT_IDS[0]!],
      agents: [{ name: 'a', id: AGENT_IDS[0]!, billingMode: 'free' as const }],
      orgId: ORG_ID,
      userId: USER_ID,
      pageOrigin: PAGE_ORIGIN,
      backendBaseUrl: BACKEND,
    });
    __setStorageBackendForTesting(storage);

    fetchSpy.mockImplementation(async () =>
      new Response(
        JSON.stringify({
          token: 'fresh.jwt.value',
          refreshToken: 'r-rotated-fresh.jwt.value',
          expiresIn: 60,
          agentIds: [AGENT_IDS[0]!],
          userId: USER_ID,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );

    const map = await signInAndGetClients({
      agents: ['a', 'b'],
      backendBaseUrl: BACKEND,
    });
    expect(openSpy).not.toHaveBeenCalled();
    expect(Object.keys(map).sort()).toEqual(['a']);
  });

  it('clears and re-popups only when NO requested agent is live', async () => {
    // Stored [a]; refresh returns an empty live set (all revoked). Nothing to
    // resume → clear the stale partition and fall through to the popup.
    const partitionKey = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: PAGE_ORIGIN,
      agentNames: ['a', 'b'],
    });
    const storage = createStorageBackend();
    storage.setSession(partitionKey, {
      refreshToken: 'r'.repeat(32),
      agentIds: [AGENT_IDS[0]!],
      agents: [{ name: 'a', id: AGENT_IDS[0]!, billingMode: 'free' as const }],
      orgId: ORG_ID,
      userId: USER_ID,
      pageOrigin: PAGE_ORIGIN,
      backendBaseUrl: BACKEND,
    });
    __setStorageBackendForTesting(storage);

    fetchSpy.mockImplementation(async () =>
      new Response(
        JSON.stringify({
          token: 'fresh.jwt.value',
          refreshToken: 'r-rotated-fresh.jwt.value',
          expiresIn: 60,
          agentIds: [], // nothing reachable
          userId: USER_ID,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );
    arrangePopupReply([
      { name: 'a', billingMode: 'free' },
      { name: 'b', billingMode: 'free' },
    ]);

    const map = await signInAndGetClients({
      agents: ['a', 'b'],
      backendBaseUrl: BACKEND,
    });
    expect(openSpy).toHaveBeenCalledTimes(1);
    expect(Object.keys(map).sort()).toEqual(['a', 'b']);
  });

  it('two concurrent signInAndGetClients on one partition share a single refresh', async () => {
    const agents = [
      { name: 'translator', id: AGENT_IDS[0]!, billingMode: 'free' as const },
    ];
    const partitionKey = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: PAGE_ORIGIN,
      agentNames: ['translator'],
    });
    const storage = createStorageBackend();
    storage.setSession(partitionKey, {
      refreshToken: 'r'.repeat(32),
      agentIds: [AGENT_IDS[0]!],
      agents,
      orgId: ORG_ID,
      userId: USER_ID,
      pageOrigin: PAGE_ORIGIN,
      backendBaseUrl: BACKEND,
    });
    __setStorageBackendForTesting(storage);

    let refreshCalls = 0;
    fetchSpy.mockImplementation(async () => {
      refreshCalls++;
      if (refreshCalls > 1) {
        // A second concurrent network refresh on the single-use token → 401.
        return new Response('', { status: 401 });
      }
      return new Response(
        JSON.stringify({
          token: 'fresh.jwt.value',
          refreshToken: 'r-rotated-fresh.jwt.value',
          expiresIn: 60,
          agentIds: [AGENT_IDS[0]!],
          userId: USER_ID,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      );
    });

    const onAuthError = vi.fn();
    const [a, b] = await Promise.all([
      signInAndGetClients({ agents: ['translator'], backendBaseUrl: BACKEND, onAuthError }),
      signInAndGetClients({ agents: ['translator'], backendBaseUrl: BACKEND, onAuthError }),
    ]);

    expect(a.translator).toBeDefined();
    expect(b.translator).toBeDefined();
    expect(onAuthError).not.toHaveBeenCalled();
    expect(refreshCalls).toBe(1);
    expect(openSpy).not.toHaveBeenCalled();
  });

  it('resume reuses the registered manager for the same partition (no second construction)', async () => {
    const agents = [
      { name: 'translator', id: AGENT_IDS[0]!, billingMode: 'free' as const },
    ];
    const partitionKey = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: PAGE_ORIGIN,
      agentNames: ['translator'],
    });
    const storage = createStorageBackend();
    storage.setSession(partitionKey, {
      refreshToken: 'r'.repeat(32),
      agentIds: [AGENT_IDS[0]!],
      agents,
      orgId: ORG_ID,
      userId: USER_ID,
      pageOrigin: PAGE_ORIGIN,
      backendBaseUrl: BACKEND,
    });
    __setStorageBackendForTesting(storage);

    fetchSpy.mockImplementation(async () =>
      new Response(
        JSON.stringify({
          token: 'fresh.jwt.value',
          refreshToken: 'r-rotated-fresh.jwt.value',
          expiresIn: 60,
          agentIds: [AGENT_IDS[0]!],
          userId: USER_ID,
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    );

    await signInAndGetClients({ agents: ['translator'], backendBaseUrl: BACKEND });
    const first = managerRegistry.get(partitionKey);
    expect(first).toBeDefined();
    await signInAndGetClients({ agents: ['translator'], backendBaseUrl: BACKEND });
    const second = managerRegistry.get(partitionKey);
    expect(second).toBe(first); // SAME instance reused, not reconstructed
  });
});

describe('devGrant is no longer plumbed by the widget', () => {
  // The per-origin allowlist model is removed. `signInAndGetClient[s]`
  // does not accept `devGrant`; `__BLOCKS_EMBED_DEV__.devGrant` is
  // ignored; the popup URL never carries `devGrant=`.

  afterEach(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    delete (window as any).__BLOCKS_EMBED_DEV__;
  });

  it('does not forward devGrant from window.__BLOCKS_EMBED_DEV__', async () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__BLOCKS_EMBED_DEV__ = { devGrant: 'should-be-ignored' };

    arrangePopupReply([{ name: 'translator', billingMode: 'free' }]);
    await signInAndGetClient({ agent: 'translator', backendBaseUrl: BACKEND });

    const [popupUrl] = openSpy.mock.calls[0]!;
    expect(String(popupUrl)).not.toContain('devGrant=');
    expect(String(popupUrl)).not.toContain('should-be-ignored');
  });

  it('signInAndGetClient does not accept a devGrant option', async () => {
    arrangePopupReply([{ name: 'translator', billingMode: 'free' }]);
    // The public type no longer includes `devGrant`. Confirm at runtime
    // that even if a caller stuffed one in via `as any`, the popup URL
    // does not propagate it.
    await signInAndGetClient({
      agent: 'translator',
      backendBaseUrl: BACKEND,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      ...({ devGrant: 'opts-grant' } as any),
    });

    const [popupUrl] = openSpy.mock.calls[0]!;
    expect(String(popupUrl)).not.toContain('devGrant=');
    expect(String(popupUrl)).not.toContain('opts-grant');
  });

  it('absent in both opts and window → no devGrant in popup URL', async () => {
    arrangePopupReply([{ name: 'translator', billingMode: 'free' }]);
    await signInAndGetClient({ agent: 'translator', backendBaseUrl: BACKEND });

    const [popupUrl] = openSpy.mock.calls[0]!;
    expect(String(popupUrl)).not.toContain('devGrant=');
  });

  it('emits replyOrigin (not legacy origin) on the popup URL', async () => {
    arrangePopupReply([{ name: 'translator', billingMode: 'free' }]);
    await signInAndGetClient({ agent: 'translator', backendBaseUrl: BACKEND });

    const [popupUrl] = openSpy.mock.calls[0]!;
    const params = new URL(String(popupUrl)).searchParams;
    expect(params.get('replyOrigin')).toBe(PAGE_ORIGIN);
    expect(params.has('origin')).toBe(false);
  });
});

describe('signOut', () => {
  it('revokes every active session under window.location.origin (no-args form) and clears storage even on fetch failure', async () => {
    const storage = createStorageBackend();
    const partitionKey = await computePartitionKey({
      backendBaseUrl: BACKEND,
      pageOrigin: PAGE_ORIGIN,
      agentNames: ['translator'],
    });
    storage.setSession(partitionKey, {
      refreshToken: 'r'.repeat(32),
      agentIds: [AGENT_IDS[0]!],
      agents: [{ name: 'translator', id: AGENT_IDS[0]!, billingMode: 'free' }],
      orgId: ORG_ID,
      userId: USER_ID,
      pageOrigin: PAGE_ORIGIN,
      backendBaseUrl: BACKEND,
    });
    __setStorageBackendForTesting(storage);

    fetchSpy.mockImplementation(async () => {
      throw new Error('network blip');
    });

    await signOut();
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url] = fetchSpy.mock.calls[0]!;
    expect(url).toBe(`${BACKEND}/api/v1/auth/embed/revoke`);
    // Storage is cleared even though revoke fetch threw.
    expect(storage.getSession(partitionKey)).toBeNull();
  });
});

describe('cdmUrl plumbing into TaskClient.create (refined Option A)', () => {
  // The widget reads cdmUrl from `__BLOCKS_EMBED_DEV__` (set by `blocks dev`)
  // and forwards it to `TaskClient.create({ cdmUrl })` so the SDK fetches its
  // CDM (PubNub keys + api.baseUrl) from the local backend instead of the
  // production default. Plumbed via the explicit-option path so it's
  // forward-compatible with the
  // `explicit option → CDM → default` resolver chain.

  afterEach(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    delete (window as any).__BLOCKS_EMBED_DEV__;
  });

  it('forwards cdmUrl from __BLOCKS_EMBED_DEV__ to TaskClient.create', async () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__BLOCKS_EMBED_DEV__ = {
      cdmUrl: 'http://localhost:3001/api/v1/cdm',
    };
    arrangePopupReply([{ name: 'translator', billingMode: 'free' }]);
    await signInAndGetClient({ agent: 'translator', backendBaseUrl: BACKEND });

    expect(factorySpy).toHaveBeenCalledTimes(1);
    const factoryArg = factorySpy.mock.calls[0]![0] as { cdmUrl?: string };
    expect(factoryArg.cdmUrl).toBe('http://localhost:3001/api/v1/cdm');
  });

  it('opts.cdmUrl beats the dev shim', async () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__BLOCKS_EMBED_DEV__ = {
      cdmUrl: 'http://localhost:3001/api/v1/cdm',
    };
    arrangePopupReply([{ name: 'translator', billingMode: 'free' }]);
    await signInAndGetClient({
      agent: 'translator',
      backendBaseUrl: BACKEND,
      cdmUrl: 'https://staging.blocks.ai/api/v1/cdm',
    });
    const factoryArg = factorySpy.mock.calls[0]![0] as { cdmUrl?: string };
    expect(factoryArg.cdmUrl).toBe('https://staging.blocks.ai/api/v1/cdm');
  });

  it('absent in both opts and window → cdmUrl is NOT forwarded (SDK uses default)', async () => {
    arrangePopupReply([{ name: 'translator', billingMode: 'free' }]);
    await signInAndGetClient({ agent: 'translator', backendBaseUrl: BACKEND });
    const factoryArg = factorySpy.mock.calls[0]![0] as Record<string, unknown>;
    expect(factoryArg).not.toHaveProperty('cdmUrl');
  });
});
