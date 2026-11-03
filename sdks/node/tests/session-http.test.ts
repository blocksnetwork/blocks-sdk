// The consumer session endpoints' response validation, at the fetch boundary.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ConsumerAuth } from '../src/runtime/consumer-auth.js';
import { retryAfterOf } from '../src/runtime/rate-limit.js';
import { SessionHttp, isRefusal } from '../src/runtime/sessions/session-http.js';

const http = new SessionHttp({ baseUrl: 'https://api.test' });
const answer = (body: unknown) =>
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(body), { status: 200 })),
  );
const exchange = { exchangeId: 'x1', state: 'completed', error: null, artifactRefs: [] };
const snapshot = { sessionId: 's1', state: 'ready', reason: null, error: null, exchanges: [] };

afterEach(() => vi.unstubAllGlobals());

describe('SessionHttp.getSession', () => {
  it('returns a snapshot with every session state and its recorded outcomes', async () => {
    for (const state of ['open', 'ready', 'closing', 'closed', 'failed', 'canceled']) {
      answer({ ...snapshot, state, exchanges: [exchange] });
      await expect(http.getSession('s1')).resolves.toEqual({
        ...snapshot,
        state,
        exchanges: [exchange],
      });
    }
  });

  it.each([
    ['an unknown session state', { ...snapshot, state: 'paused' }],
    ['a missing sessionId', { ...snapshot, sessionId: undefined }],
    ['exchanges that are not a list', { ...snapshot, exchanges: {} }],
    ['an unknown outcome state', { ...snapshot, exchanges: [{ ...exchange, state: 'done' }] }],
    ['an outcome without an exchangeId', { ...snapshot, exchanges: [{ ...exchange, exchangeId: 7 }] }],
    ['an outcome without artifactRefs', { ...snapshot, exchanges: [{ ...exchange, artifactRefs: null }] }],
    ['a recorded ref without a fileId', { ...snapshot, exchanges: [{ ...exchange, artifactRefs: [{ kind: 'file' }] }] }],
    ['a recorded ref with a numeric fileId', { ...snapshot, exchanges: [{ ...exchange, artifactRefs: [{ kind: 'file', fileId: 7 }] }] }],
    ['a null recorded ref', { ...snapshot, exchanges: [{ ...exchange, artifactRefs: [null] }] }],
    ['an empty body', null],
  ])('refuses %s', async (_name, body) => {
    answer(body);
    await expect(http.getSession('s1')).rejects.toThrow(/invalid snapshot/);
  });

  it('refuses the snapshot of another session', async () => {
    answer({ ...snapshot, sessionId: 's2' });
    await expect(http.getSession('s1')).rejects.toThrow(/another session/);
  });

  it('reads a missing close reason or error as null', async () => {
    answer({ sessionId: 's1', state: 'failed', exchanges: [{ ...exchange, error: undefined }] });
    await expect(http.getSession('s1')).resolves.toMatchObject({
      reason: null,
      error: null,
      exchanges: [{ error: null }],
    });
  });
});

const accessManager = {
  streamId: 'st1',
  subscribeKey: 'sub-foreign',
  origin: null,
  channel: 'live.eu',
  mode: 'access_manager',
  token: 'tok',
  ttlMinutes: 60,
  expiresAt: '2030-01-01T00:00:00.000Z',
};
const withoutKey = (key: string) => {
  const { [key]: _omitted, ...rest } = accessManager as Record<string, unknown>;
  return rest;
};

const wildcard = { ...withoutKey('channel'), wildcard: 'live.*' };
const consent = {
  ...accessManager,
  mode: 'consent',
  token: undefined,
  ttlMinutes: undefined,
  expiresAt: undefined,
};
const withOrigin = { ...accessManager, origin: 'ps.example.test' };

describe('SessionHttp.fetchStreamToken', () => {
  it.each([
    ['an access_manager stream on one channel', accessManager, accessManager],
    ['a wildcard stream', wildcard, wildcard],
    ['a consent stream, which carries no token', consent, consent],
    ['an origin', withOrigin, withOrigin],
    ['a missing origin, read as null', withoutKey('origin'), accessManager],
  ])('accepts %s', async (_name, body, expected) => {
    answer(body);
    await expect(http.fetchStreamToken('s1', 'st1')).resolves.toEqual(expected);
  });

  it.each([
    ['a channel and a wildcard', { ...accessManager, wildcard: 'live.*' }],
    ['neither a channel nor a wildcard', withoutKey('channel')],
    ['an unknown mode', { ...accessManager, mode: 'open' }],
    ['no streamId', withoutKey('streamId')],
    ['no subscribeKey', withoutKey('subscribeKey')],
    ['an empty origin', { ...accessManager, origin: '' }],
    ['no token in access_manager mode', withoutKey('token')],
    ['a zero ttlMinutes', { ...accessManager, ttlMinutes: 0 }],
    ['a textual ttlMinutes', { ...accessManager, ttlMinutes: '60' }],
    ['no expiresAt in access_manager mode', withoutKey('expiresAt')],
    ['an empty body', {}],
  ])('refuses %s', async (_name, body) => {
    answer(body);
    await expect(http.fetchStreamToken('s1', 'st1')).rejects.toThrow(
      /stream credentials are invalid/,
    );
  });
});

describe('SessionHttp.postExchange', () => {
  const sent = { exchangeId: 'x1', requestParts: [] };
  const settled = { status: 200, exchangeId: 'x1', state: 'completed' };

  it('returns the status, state and stream credentials of an answer', async () => {
    answer({ exchangeId: 'x1', state: 'completed', stream: accessManager });
    await expect(http.postExchange('s1', sent)).resolves.toEqual({
      ...settled,
      stream: accessManager,
    });
  });

  it('drops malformed stream credentials rather than failing, so the token endpoint serves them', async () => {
    answer({ exchangeId: 'x1', state: 'completed', stream: { ...accessManager, mode: 'open' } });
    await expect(http.postExchange('s1', sent)).resolves.toEqual(settled);
  });

  it('reads an empty answer as a status alone', async () => {
    answer({});
    await expect(http.postExchange('s1', sent)).resolves.toEqual({ status: 200 });
  });
});

describe('SessionHttp.openSession', () => {
  const opened = {
    sessionId: 's1',
    sessionChannel: 'sess.r.s1',
    pamToken: 'pam',
    ttlMinutes: 60,
    consumerTransportUuid: 'c',
    providerTransportUuid: 'p',
    backendTransportUuid: 'b',
  };

  it.each([
    ['a missing managed flag', undefined],
    ['managed true', true],
  ])('reads %s', async (_name, managed) => {
    answer({ ...opened, managed });
    expect((await http.openSession('agent')).managed).toBe(managed);
  });

  it('refuses a managed flag that is not a boolean', async () => {
    answer({ ...opened, managed: 'yes' });
    await expect(http.openSession('agent')).rejects.toThrow(/managed/);
  });
});

describe('SessionHttp with a rate-limited credential refresh', () => {
  it('reports a 401 it could not refresh past a 429 as a wait, not a refusal', async () => {
    const respond = (status: number, body: unknown, headers?: Record<string, string>) =>
      new Response(JSON.stringify(body), { status, headers });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url.endsWith('/auth/agent/consumer-token')) {
          return respond(200, { accessToken: 'jwt-1', refreshToken: 'rt-1', expiresIn: 60, userId: 'u' });
        }
        if (url.endsWith('/auth/agent/refresh')) {
          return respond(429, { code: 'RATE_LIMITED' }, { 'Retry-After': '90' });
        }
        return respond(401, { error: 'expired' });
      }),
    );
    const authProvider = new ConsumerAuth({ apiKey: 'bk_key', baseUrl: 'https://api.test' });
    const limited = new SessionHttp({ baseUrl: 'https://api.test', authProvider });

    const err = await limited.refreshToken('s1').catch((e: unknown) => e);

    expect(isRefusal(err)).toBe(false);
    expect(retryAfterOf(err)).toBe(90_000);
    authProvider.destroy();
  });
});
