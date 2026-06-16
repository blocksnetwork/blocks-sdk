import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  CURRENT_PROTOCOL_VERSION,
  PROTOCOL_VERSION_HEADER,
} from '../src/protocol-version.js';
import { EmbeddedAuthSessionManager } from '../src/refresh.js';
import { createStorageBackend, type StorageBackend } from '../src/storage.js';
import type { SessionData } from '../src/types.js';
import { BlocksAuthError } from '../src/types.js';

const REFRESH_URL = 'https://blocks.ai/api/v1/auth/embed/refresh';
const PARTITION = 'pk-test';

function seedSession(
  storage: StorageBackend,
  over: Partial<SessionData> = {},
): void {
  storage.setSession(PARTITION, {
    refreshToken: 'r-1',
    agentIds: ['a-1'],
    agents: [{ name: 'translator', id: 'a-1', billingMode: 'free' }],
    orgId: 'org-1',
    userId: 'user-1',
    pageOrigin: 'https://partner.example',
    backendBaseUrl: 'https://blocks.ai',
    ...over,
  });
}

interface MockFetchCall {
  url: string;
  init: RequestInit;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnyFn = (...args: any[]) => any;

function mockFetch(
  responder: (call: MockFetchCall) => Response | Promise<Response>,
): { fn: AnyFn & { mock: { calls: unknown[][] } }; calls: MockFetchCall[] } {
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

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function emptyResponse(status: number): Response {
  return new Response(null, { status });
}

describe('EmbeddedAuthSessionManager', () => {
  let storage: StorageBackend;

  beforeEach(() => {
    localStorage.clear();
    storage = createStorageBackend(localStorage);
  });

  afterEach(() => {
    vi.useRealTimers();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (globalThis as any).fetch = undefined;
  });

  it('parses the refresh response into TokenResult { token, expiresIn, agentIds, userId }', async () => {
    seedSession(storage);
    mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-1',
        refreshToken: 'r-rotated-jwt-1',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    const result = await mgr.forceRefresh();
    expect(result).toEqual({
      token: 'jwt-1',
      refreshToken: 'r-rotated-jwt-1',
      expiresIn: 60,
      agentIds: ['a-1'],
      userId: 'user-1',
    });
  });

  it('rejects malformed refresh response shapes', async () => {
    seedSession(storage);
    mockFetch(() => jsonResponse(200, { token: 'x', expiresIn: 60 }));
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    await expect(mgr.forceRefresh()).rejects.toMatchObject({
      code: 'REFRESH_NETWORK_ERROR',
    });
  });

  it('dedupes 5 concurrent tokenProvider calls into ONE network request', async () => {
    seedSession(storage);
    let resolveBody!: () => void;
    const gate = new Promise<void>((res) => {
      resolveBody = res;
    });
    const { fn } = mockFetch(async () => {
      await gate;
      return jsonResponse(200, {
        token: 'jwt-shared',
        refreshToken: 'r-rotated-jwt-shared',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      });
    });
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    // 5 concurrent callers, none have a fresh token yet → all need refresh.
    const promises = Array.from({ length: 5 }, () => mgr.tokenProvider());
    // Let microtasks settle so all 5 attempt to start.
    await Promise.resolve();
    resolveBody();
    const results = await Promise.all(promises);
    expect(fn).toHaveBeenCalledTimes(1);
    for (const r of results) {
      expect(r.token).toBe('jwt-shared');
    }
  });

  it('does NOT run an independent refresh timer (consumer drives refresh)', async () => {
    // Regression guard: the manager must not rotate the refresh-token row on
    // its own timer. A background rotation revokes the row the consumer's
    // still-held JWT is bound to, causing intermittent "refresh token revoked"
    // RPC errors. The only refresh trigger is a (non-fresh) tokenProvider call.
    vi.useFakeTimers();
    seedSession(storage);
    const { fn } = mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-rotated',
        refreshToken: 'rt-2',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    mgr.seedFromEnvelope({
      jwt: 'seed-jwt',
      expiresAt: Date.now() + 60_000,
      agentIds: ['a-1'],
      userId: 'user-1',
    });
    // Advancing time well past any old proactive window must NOT trigger a
    // network refresh — nothing pulled a token.
    await vi.advanceTimersByTimeAsync(120_000);
    expect(fn).toHaveBeenCalledTimes(0);
    vi.useRealTimers();
  });

  it('tokenProvider joins an in-flight refresh instead of returning a stale snapshot', async () => {
    // While a refresh is rotating the row, a concurrent tokenProvider call
    // must wait for the rotated token rather than hand back the cached JWT
    // (whose row is about to be revoked).
    seedSession(storage);
    let resolveFetch: (() => void) | null = null;
    const gate = new Promise<void>((r) => {
      resolveFetch = r;
    });
    let calls = 0;
    const { fn } = mockFetch(async () => {
      calls += 1;
      await gate; // hold the refresh "in flight"
      return jsonResponse(200, {
        token: 'jwt-rotated',
        refreshToken: 'rt-2',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      });
    });
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    // Seed a token that is already stale so the first call refreshes.
    mgr.seedFromEnvelope({
      jwt: 'seed-jwt',
      expiresAt: Date.now() + 1_000, // < REFRESH_LEEWAY → not fresh
      agentIds: ['a-1'],
      userId: 'user-1',
    });
    const first = mgr.tokenProvider(); // starts the in-flight refresh
    const second = mgr.tokenProvider(); // must join, not return the stale snapshot
    resolveFetch!();
    const [a, b] = await Promise.all([first, second]);
    expect(fn).toHaveBeenCalledTimes(1); // one network refresh shared
    expect(a.token).toBe('jwt-rotated');
    expect(b.token).toBe('jwt-rotated'); // joined the in-flight refresh
  });

  it('tokenProvider returns Promise<TokenResult> (Mode 3 callback shape)', async () => {
    seedSession(storage);
    mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-1',
        refreshToken: 'r-rotated-jwt-1',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    const result = await mgr.tokenProvider();
    expect(typeof result.token).toBe('string');
    expect(typeof result.expiresIn).toBe('number');
    expect(Array.isArray(result.agentIds)).toBe(true);
    expect(typeof result.userId).toBe('string');
  });

  it('returns the cached JWT without refresh when fresh', async () => {
    seedSession(storage);
    const { fn } = mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-fresh',
        refreshToken: 'r-rotated-jwt-fresh',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    mgr.seedFromEnvelope({
      jwt: 'seed-jwt',
      expiresAt: Date.now() + 60_000,
      agentIds: ['a-1'],
      userId: 'user-1',
    });
    const r = await mgr.tokenProvider();
    expect(r.token).toBe('seed-jwt');
    expect(fn).toHaveBeenCalledTimes(0);
  });

  it('on 401: clears partition, rejects REFRESH_FAILED, calls onAuthError', async () => {
    seedSession(storage);
    mockFetch(() => emptyResponse(401));
    const onAuthError = vi.fn();
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
      onAuthError,
    });
    await expect(mgr.forceRefresh()).rejects.toMatchObject({
      code: 'REFRESH_FAILED',
    });
    expect(onAuthError).toHaveBeenCalledTimes(1);
    const calledWith = onAuthError.mock.calls[0][0];
    expect(calledWith).toBeInstanceOf(BlocksAuthError);
    expect((calledWith as BlocksAuthError).code).toBe('REFRESH_FAILED');
    expect(storage.getSession(PARTITION)).toBeNull();
  });

  it('on 412: rejects PROTOCOL_VERSION_REJECTED, request carried protocol-version header', async () => {
    seedSession(storage);
    const { calls } = mockFetch(() => emptyResponse(412));
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    await expect(mgr.forceRefresh()).rejects.toMatchObject({
      code: 'PROTOCOL_VERSION_REJECTED',
    });
    expect(calls).toHaveLength(1);
    const headers = calls[0].init.headers as Record<string, string>;
    expect(headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
  });

  it('on network error: rejects REFRESH_NETWORK_ERROR', async () => {
    seedSession(storage);
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (globalThis as any).fetch = vi.fn(async () => {
      throw new TypeError('network down');
    });
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    await expect(mgr.forceRefresh()).rejects.toMatchObject({
      code: 'REFRESH_NETWORK_ERROR',
    });
  });

  it('refresh-time agentIds narrowing persists via storage.updateScope and JWT never reaches disk', async () => {
    seedSession(storage, {
      agentIds: ['a-1', 'a-2', 'a-3'],
      agents: [
        { name: 'A', id: 'a-1', billingMode: 'free' },
        { name: 'B', id: 'a-2', billingMode: 'free' },
        { name: 'C', id: 'a-3', billingMode: 'free' },
      ],
    });
    mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-narrowed',
        refreshToken: 'r-rotated-jwt-narrowed',
        expiresIn: 60,
        agentIds: ['a-1', 'a-2'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    await mgr.forceRefresh();
    const after = storage.getSession(PARTITION);
    expect(after?.agentIds).toEqual(['a-1', 'a-2']);

    // JWT-never-on-disk regression at the refresh boundary.
    const forbidden = ['token', 'jwt', 'expiresAt'];
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)!;
      const raw = localStorage.getItem(key)!;
      for (const f of forbidden) {
        expect(raw, `forbidden field "${f}" appeared in ${key}`).not.toContain(
          `"${f}"`,
        );
      }
    }
  });

  it('every outbound refresh request carries the protocol-version header', async () => {
    seedSession(storage);
    const { calls } = mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-1',
        refreshToken: 'r-rotated-jwt-1',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    await mgr.forceRefresh();
    await mgr.forceRefresh();
    expect(calls).toHaveLength(2);
    for (const call of calls) {
      const headers = call.init.headers as Record<string, string>;
      expect(headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
      expect(headers['Content-Type']).toBe('application/json');
    }
  });

  it('does not forward X-Blocks-Dev-Origin-Grant on refresh', async () => {
    // Per-origin allowlist removed — refresh sends only Content-Type and
    // Blocks-Protocol-Version. The dev-origin header is gone.
    seedSession(storage);
    const { calls } = mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-1',
        refreshToken: 'r-rotated-jwt-1',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    await mgr.forceRefresh();
    const headers = calls[0].init.headers as Record<string, string>;
    expect(headers['X-Blocks-Dev-Origin-Grant']).toBeUndefined();
    expect(headers['Content-Type']).toBe('application/json');
    expect(headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
  });

  it('clear() causes subsequent tokenProvider() to reject NO_REFRESH_TOKEN', async () => {
    seedSession(storage);
    mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-1',
        refreshToken: 'r-rotated-jwt-1',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    mgr.seedFromEnvelope({
      jwt: 'seed-jwt',
      expiresAt: Date.now() + 60_000,
      agentIds: ['a-1'],
      userId: 'user-1',
    });
    mgr.clear();
    await expect(mgr.tokenProvider()).rejects.toMatchObject({
      code: 'NO_REFRESH_TOKEN',
    });
  });

  it('clear() during an in-flight refresh does NOT revive the cleared session', async () => {
    // MAJOR (review #1): signOut() calls manager.clear() while a refresh
    // may already be in flight. doRefresh() checks `cleared` only before
    // its awaits; if it resumes after clear() it would write the rotated
    // token, flip cleared=false, and re-persist a fresh refresh token into
    // the partition signOut just wiped — reviving a signed-out session.
    seedSession(storage, { refreshToken: 'r-original' });
    let resolveFetch!: () => void;
    const gate = new Promise<void>((r) => {
      resolveFetch = r;
    });
    mockFetch(async () => {
      await gate; // hold the refresh in flight
      return jsonResponse(200, {
        token: 'jwt-rotated',
        refreshToken: 'r-rotated',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      });
    });
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    // Seed a stale JWT so the first tokenProvider call triggers a refresh.
    mgr.seedFromEnvelope({
      jwt: 'seed-jwt',
      expiresAt: Date.now() + 1_000, // < REFRESH_LEEWAY → not fresh
      agentIds: ['a-1'],
      userId: 'user-1',
    });
    const inflight = mgr.tokenProvider(); // suspends at `await fetch`
    // signOut() path: clear the manager while the refresh is in flight.
    mgr.clear();
    resolveFetch();
    // The resumed refresh must not resurrect the session. Its own promise
    // may resolve or reject — we only care that the session stays dead.
    await inflight.catch(() => undefined);

    // 1. cleared must remain true → next tokenProvider rejects.
    await expect(mgr.tokenProvider()).rejects.toMatchObject({
      code: 'NO_REFRESH_TOKEN',
    });
    // 2. the rotated refresh token must NOT have been persisted over the
    //    cleared session.
    expect(storage.getSession(PARTITION)?.refreshToken).not.toBe('r-rotated');
  });

  it('refreshes when the seeded JWT is past the refresh leeway', async () => {
    seedSession(storage);
    const { fn } = mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-refreshed',
        refreshToken: 'r-rotated-jwt-refreshed',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    // Seed a JWT that has already expired — leeway forces refresh.
    mgr.seedFromEnvelope({
      jwt: 'seed-jwt',
      expiresAt: Date.now() - 1_000,
      agentIds: ['a-1'],
      userId: 'user-1',
    });
    const r = await mgr.tokenProvider();
    expect(r.token).toBe('jwt-refreshed');
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it('refresh body uses the stored refresh token in the JSON body form', async () => {
    seedSession(storage, { refreshToken: 'r-XYZ' });
    const { calls } = mockFetch(() =>
      jsonResponse(200, {
        token: 'jwt-1',
        refreshToken: 'r-rotated-jwt-1',
        expiresIn: 60,
        agentIds: ['a-1'],
        userId: 'user-1',
      }),
    );
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    await mgr.forceRefresh();
    const body = JSON.parse(calls[0].init.body as string);
    expect(body).toEqual({ refreshToken: 'r-XYZ' });
    expect(calls[0].init.method).toBe('POST');
    expect(calls[0].init.credentials).toBe('omit');
  });

  it('rejects when the partition has no refreshToken', async () => {
    // Don't seed.
    const mgr = new EmbeddedAuthSessionManager({
      refreshUrl: REFRESH_URL,
      storage,
      partitionKey: PARTITION,
    });
    await expect(mgr.forceRefresh()).rejects.toMatchObject({
      code: 'NO_REFRESH_TOKEN',
    });
  });
});
