import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  BlocksClient,
  SessionBackpressureError,
  SessionError,
  SessionFailedError,
  SessionNoRouteError,
  SessionPaidAgentError,
  SessionsDisabledError,
  type AuthProvider,
  type OpenSessionOptions,
} from '../src/index.js';

interface FakePubNub {
  config: { userId: string };
  listeners: Array<{ message?: (e: unknown) => void; status?: (s: unknown) => void }>;
  setToken: ReturnType<typeof vi.fn>;
  subscribe: ReturnType<typeof vi.fn>;
  publish: ReturnType<typeof vi.fn>;
  reconnect: ReturnType<typeof vi.fn>;
  destroy: ReturnType<typeof vi.fn>;
  fetchMessages: ReturnType<typeof vi.fn>;
}

const pubnubs: FakePubNub[] = [];

vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation((config: { userId: string }) => {
    const fake: FakePubNub = {
      config,
      listeners: [],
      setToken: vi.fn(),
      subscribe: vi.fn(),
      publish: vi.fn(async () => ({ timetoken: '1' })),
      reconnect: vi.fn(),
      destroy: vi.fn(),
      fetchMessages: vi.fn(async () => ({ channels: {} })),
    };
    pubnubs.push(fake);
    return {
      ...fake,
      addListener: (l: FakePubNub['listeners'][number]) => fake.listeners.push(l),
      removeListener: vi.fn(),
      unsubscribeAll: vi.fn(),
    };
  }),
}));

vi.mock('../src/runtime/cdm-config.js', () => ({
  DEFAULT_CDM_URL: 'https://cdm.test/config.json',
  fetchCdmConfig: vi.fn().mockResolvedValue({
    playground: { publishKey: 'pub-test', subscribeKey: 'sub-test' },
    network: { publishKey: 'pub-test', subscribeKey: 'sub-test' },
    api: { baseUrl: 'https://api.test' },
  }),
}));

const OPENED = {
  sessionId: 'ses1',
  sessionChannel: 'sess.rt1.ses1',
  pamToken: 'pam-token',
  ttlMinutes: 60,
  consumerTransportUuid: 'consumer-uuid',
  providerTransportUuid: 'provider-uuid',
  backendTransportUuid: 'backend-uuid',
};

const authProvider: AuthProvider = {
  getAuthHeader: () => 'Bearer consumer-jwt',
  onAuthFailure: async () => false,
};

let fetchMock: ReturnType<typeof vi.fn>;
let openResponse: (init?: RequestInit) => Response | Promise<Response>;

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const createClient = (auth: AuthProvider = authProvider) =>
  BlocksClient.create({
    billingMode: 'free',
    // Explicit, so BLOCKS_* env in CI cannot redirect the client.
    baseUrl: 'https://api.test',
    subscribeKey: 'sub-test',
    publishKey: 'pub-test',
    authProvider: auth,
    rpcHeaders: { 'X-CSRF-Token': 'csrf' },
  });

const latest = () => pubnubs[pubnubs.length - 1];
const flush = () => new Promise((r) => setTimeout(r, 0));

function connect(pn = latest()) {
  for (const l of pn.listeners) l.status?.({ category: 'PNConnectedCategory' });
}

let providerSeq = 0;
function fromProvider(fields: Record<string, unknown>, pn = latest()) {
  const message = {
    protocolVersion: '2026-05-01',
    sessionId: OPENED.sessionId,
    seq: ++providerSeq,
    ...fields,
  };
  for (const l of pn.listeners) {
    l.message?.({
      channel: OPENED.sessionChannel,
      publisher: OPENED.providerTransportUuid,
      timetoken: String(17000000000000000n + BigInt(message.seq as number)),
      message,
    });
  }
}

function fromBackend(fields: Record<string, unknown>, pn = latest()) {
  const message = { protocolVersion: '2026-05-01', sessionId: OPENED.sessionId, seq: 0, ...fields };
  for (const l of pn.listeners) {
    l.message?.({
      channel: OPENED.sessionChannel,
      publisher: OPENED.backendTransportUuid,
      message,
    });
  }
}

const published = (pn = latest()) =>
  pn.publish.mock.calls.map(([p]) => (p as { message: Record<string, unknown> }).message);

/** Opens a session through the provider's `session_ready`. */
async function openReady(client: BlocksClient, options?: OpenSessionOptions) {
  const opening = client.openSession('echo-agent', options);
  await flush();
  connect();
  await flush();
  fromProvider({ type: 'session_ready' });
  return opening;
}

/** An open request that never answers, failing only when its signal aborts, as fetch does. */
const hangingOpen = (init?: RequestInit) =>
  new Promise<Response>((_, reject) =>
    init?.signal?.addEventListener('abort', () =>
      reject(new DOMException('aborted', 'AbortError')),
    ),
  );

beforeEach(() => {
  pubnubs.length = 0;
  providerSeq = 0;
  openResponse = () => json(201, OPENED);
  fetchMock = vi.fn(async (url: string, init?: RequestInit) =>
    url.endsWith('/api/v1/sessions') ? openResponse(init) : new Response(null, { status: 204 }),
  );
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('BlocksClient.openSession', () => {
  it('opens on the backend, connects as the issued UUID, and publishes session_open only once connected', async () => {
    const client = await createClient();
    const opening = client.openSession('echo-agent', { idempotencyKey: 'k1' });
    await flush();

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('https://api.test/api/v1/sessions');
    // Bearer transport: a same-origin browser must not attach the dashboard cookie.
    expect(init.credentials).toBe('omit');
    expect(JSON.parse(init.body as string)).toEqual({
      agentName: 'echo-agent',
      capabilities: ['managed'],
      idempotencyKey: 'k1',
    });
    expect(init.headers).toMatchObject({
      Authorization: 'Bearer consumer-jwt',
      'Blocks-Protocol-Version': '2026-05-01',
      'X-CSRF-Token': 'csrf',
    });

    const pn = latest();
    expect(pn.config.userId).toBe(OPENED.consumerTransportUuid);
    expect(pn.setToken).toHaveBeenCalledWith(OPENED.pamToken);
    expect(pn.subscribe).toHaveBeenCalledWith({ channels: [OPENED.sessionChannel] });
    expect(pn.publish).not.toHaveBeenCalled();

    connect();
    await flush();
    expect(pn.publish).toHaveBeenCalledWith(
      expect.objectContaining({
        channel: OPENED.sessionChannel,
        storeInHistory: true,
        meta: { sessionId: OPENED.sessionId, protocolVersion: '2026-05-01' },
      }),
    );
    expect(published()).toEqual([
      expect.objectContaining({ type: 'session_open', sessionId: OPENED.sessionId, seq: 1 }),
    ]);
    expect(published()[0]).not.toHaveProperty('exchange');
    expect(fetchMock).toHaveBeenCalledWith(
      'https://api.test/api/v1/sessions/ses1/beacons',
      expect.objectContaining({ body: JSON.stringify({ beacon: 'opened' }) }),
    );

    let resolved = false;
    void opening.then(() => (resolved = true));
    await flush();
    expect(resolved).toBe(false);

    fromProvider({ type: 'session_ready' });
    const session = await opening;
    expect(session.sessionId).toBe(OPENED.sessionId);
    expect(session.state).toBe('ready');
  });

  it.each([
    [404, 'SESSIONS_DISABLED', SessionsDisabledError],
    [422, 'SESSION_PAID_AGENT_UNSUPPORTED', SessionPaidAgentError],
    [503, 'SESSION_NO_ROUTE', SessionNoRouteError],
  ])('rejects HTTP %i %s with its typed SessionError', async (status, code, ErrorClass) => {
    openResponse = () => json(status, { error: 'refused', code });
    const client = await createClient();
    const err = await client.openSession('echo-agent').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ErrorClass);
    expect(err).toBeInstanceOf(SessionError);
    expect((err as SessionError).code).toBe(code);
    expect(pubnubs).toHaveLength(0);
  });

  it('rejects with SessionFailedError when the session fails before it is ready, and releases the client', async () => {
    const client = await createClient();
    const opening = client.openSession('echo-agent');
    await flush();
    connect();
    await flush();
    fromBackend({ type: 'session_failed', error: 'provider_at_capacity' });

    const err = await opening.catch((e: unknown) => e);
    expect(err).toBeInstanceOf(SessionFailedError);
    expect((err as SessionError).code).toBe('provider_at_capacity');
    expect(latest().destroy).toHaveBeenCalled();
    expect(published().map((m) => m.type)).toEqual(['session_open']);
  });

  it('rejects with the real failure when the session fails in the tick it became ready', async () => {
    const client = await createClient();
    const opening = client.openSession('echo-agent');
    await flush();
    connect();
    await flush();

    fromProvider({ type: 'session_ready' });
    fromBackend({ type: 'session_failed', error: 'route_lost' });

    const err = await opening.catch((e: unknown) => e);
    expect(err).toBeInstanceOf(SessionFailedError);
    expect((err as SessionError).code).toBe('route_lost');
  });

  it('cancels the session when the provider is not ready in time', async () => {
    const client = await createClient();
    const opening = client.openSession('echo-agent', { readyTimeoutMs: 50 });
    await flush();
    connect();

    await expect(opening).rejects.toThrow(/not ready within 50 ms/);
    await flush();
    expect(published().map((m) => m.type)).toEqual(['session_open', 'session_close']);
    expect(latest().destroy).toHaveBeenCalled();
  });

  it('accepts readyTimeoutMs at its 2-minute ceiling and asks the backend', async () => {
    const client = await createClient();

    const opening = client.openSession('echo-agent', { readyTimeoutMs: 120_000 });
    await flush();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    client.destroy();
    await opening.catch(() => {});
  });

  it.each<unknown>([0, 120_001, Number.NaN, true, '1000'])(
    'refuses readyTimeoutMs %s before asking the backend for a session',
    async (readyTimeoutMs) => {
      const client = await createClient();

      await expect(
        client.openSession('echo-agent', { readyTimeoutMs: readyTimeoutMs as number }),
      ).rejects.toBeInstanceOf(
        RangeError,
      );
      expect(fetchMock).not.toHaveBeenCalled();
    },
  );

  it('counts the open request against readyTimeoutMs', async () => {
    openResponse = hangingOpen;
    const client = await createClient();

    await expect(client.openSession('echo-agent', { readyTimeoutMs: 50 })).rejects.toMatchObject({
      code: 'session_canceled',
      message: expect.stringMatching(/not ready within 50 ms/),
    });
    expect(pubnubs).toHaveLength(0);
  });

  it('counts a stalled credential refresh against readyTimeoutMs', async () => {
    const client = await BlocksClient.create({
      billingMode: 'free',
      baseUrl: 'https://api.test',
      subscribeKey: 'sub-test',
      publishKey: 'pub-test',
      authProvider: { ...authProvider, ensureReady: () => new Promise<void>(() => {}) },
    });

    await expect(client.openSession('echo-agent', { readyTimeoutMs: 50 })).rejects.toMatchObject({
      code: 'session_canceled',
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('gives up on an open whose session_open publish never completes', async () => {
    vi.useFakeTimers();
    const client = await createClient();
    const opening = client.openSession('echo-agent', { readyTimeoutMs: 50 });
    const rejected = expect(opening).rejects.toThrow(/not ready within 50 ms/);
    await vi.advanceTimersByTimeAsync(0);
    latest().publish.mockImplementation(() => new Promise(() => {}));
    connect();

    await vi.advanceTimersByTimeAsync(50);
    await rejected;
    expect(latest().destroy).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(5_000);
    expect(latest().destroy).toHaveBeenCalled();
  });

  it('keeps the SDK credential when a caller header differs only in case', async () => {
    const client = await BlocksClient.create({
      billingMode: 'free',
      baseUrl: 'https://api.test',
      subscribeKey: 'sub-test',
      publishKey: 'pub-test',
      authProvider,
      rpcHeaders: { authorization: 'Bearer other', 'blocks-protocol-version': '1999-01-01' },
    });
    void client.openSession('echo-agent');
    await flush();

    const headers = (fetchMock.mock.calls[0][1] as RequestInit).headers as Record<string, string>;
    expect(new Headers(headers).get('authorization')).toBe('Bearer consumer-jwt');
    expect(new Headers(headers).get('blocks-protocol-version')).toBe('2026-05-01');
  });

  it('refuses to create a client without exactly one credential', async () => {
    await expect(BlocksClient.create({ billingMode: 'free' })).rejects.toThrow(/exactly one/);
    await expect(
      BlocksClient.create({ billingMode: 'free', apiKey: 'bk_x', authProvider }),
    ).rejects.toThrow(/exactly one/);
  });
});

describe('A replayed open', () => {
  const replayed = (state: string) => () => json(200, { ...OPENED, idempotent: true, state });

  // The backend answers an ended session with SESSION_NOT_FOUND; a terminal replay is refused too.
  it.each(['ready', 'closing', 'closed', 'failed', 'canceled'])(
    'refuses a replay of a %s session at once, leaves it alone and frees the key',
    async (state) => {
      openResponse = replayed(state);
      const client = await createClient();
      const opens = () => fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/api/v1/sessions'));

      await expect(client.openSession('echo-agent', { idempotencyKey: 'k1' })).rejects.toMatchObject({
        code: 'session_already_open',
      });
      expect(pubnubs).toHaveLength(0);
      expect(
        fetchMock.mock.calls.filter(([url]) => String(url).includes('/beacons')),
      ).toHaveLength(0);
      await expect(client.openSession('echo-agent', { idempotencyKey: 'k1' })).rejects.toMatchObject({
        code: 'session_already_open',
      });
      expect(opens()).toHaveLength(2);
      client.destroy();
    },
  );

  it('refuses a second open under a key while its session is held here, sending nothing', async () => {
    const client = await createClient();
    const opens = () => fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/api/v1/sessions'));
    const again = () => client.openSession('echo-agent', { idempotencyKey: 'k1' });
    const first = again();

    await expect(again()).rejects.toMatchObject({ code: 'session_already_open' });
    await flush();
    connect();
    await flush();
    fromProvider({ type: 'session_ready' });
    const session = await first;
    await expect(again()).rejects.toMatchObject({ code: 'session_already_open' });
    expect(opens()).toHaveLength(1);

    const closing = session.close();
    await flush();
    fromProvider({ type: 'session_closed', reason: 'consumer_closed' });
    await closing;
    void again().catch(() => {});
    await flush();
    expect(opens()).toHaveLength(2);
    client.destroy();
  });

  it('keeps a key held while a session that failed to open is still tearing down', async () => {
    vi.useFakeTimers();
    const client = await createClient();
    const opens = () => fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/api/v1/sessions'));
    const again = () => client.openSession('echo-agent', { idempotencyKey: 'k1', readyTimeoutMs: 50 });
    const rejected = expect(again()).rejects.toThrow(/not ready within 50 ms/);
    await vi.advanceTimersByTimeAsync(0);
    latest().publish.mockImplementation(() => new Promise(() => {}));
    connect();
    await vi.advanceTimersByTimeAsync(50);
    await rejected;

    await expect(again()).rejects.toMatchObject({ code: 'session_already_open' });
    await vi.advanceTimersByTimeAsync(5_000);
    expect(latest().destroy).toHaveBeenCalled();
    void again().catch(() => {});
    await vi.advanceTimersByTimeAsync(0);
    expect(opens()).toHaveLength(2);
    client.destroy();
  });

  it('answers session_canceled, not session_already_open, once the client is destroyed', async () => {
    openResponse = hangingOpen;
    const client = await createClient();
    const first = client.openSession('echo-agent', { idempotencyKey: 'k1' }).catch((e) => e);
    client.destroy();

    await expect(client.openSession('echo-agent', { idempotencyKey: 'k1' })).rejects.toMatchObject({
      code: 'session_canceled',
    });
    await first;
  });

  it('keeps a key held until an abandoned open request settles', async () => {
    let answer = (_: Response) => {};
    openResponse = () =>
      new Promise<Response>((resolve) => {
        answer = resolve;
      });
    const client = await createClient();
    const opens = () => fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/api/v1/sessions'));
    const again = () => client.openSession('echo-agent', { idempotencyKey: 'k1', readyTimeoutMs: 20 });
    await expect(again()).rejects.toMatchObject({ code: 'session_canceled' });

    await expect(again()).rejects.toMatchObject({ code: 'session_already_open' });
    answer(json(201, OPENED));
    await flush();
    await flush();
    void again().catch(() => {});
    await flush();
    expect(opens()).toHaveLength(2);
    client.destroy();
  });

  it('sends no closed beacon when a replay answers after its open was abandoned', async () => {
    let answer = (_: Response) => {};
    openResponse = () =>
      new Promise<Response>((resolve) => {
        answer = resolve;
      });
    const client = await createClient();
    const opening = client.openSession('echo-agent', { idempotencyKey: 'k1', readyTimeoutMs: 20 });
    await expect(opening).rejects.toBeDefined();

    answer(json(200, { ...OPENED, idempotent: true, state: 'ready' }));
    await flush();
    await flush();

    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/beacons'))).toHaveLength(0);
    client.destroy();
  });

  it.each([
    ['a non-boolean idempotent', { idempotent: 'true', state: 'ready' }],
    ['a replay without a known state', { idempotent: true, state: 'paused' }],
  ])('refuses an open response with %s', async (_name, fields) => {
    openResponse = () => json(200, { ...OPENED, ...fields });
    const client = await createClient();

    await expect(client.openSession('echo-agent', { idempotencyKey: 'k1' })).rejects.toThrow(
      /invalid session/,
    );
    expect(pubnubs).toHaveLength(0);
    client.destroy();
  });

  it.each([
    ['a replay of a session that never reached ready', { idempotent: true, state: 'open' }],
    ['an explicit first open', { idempotent: false }],
  ])('opens normally on %s', async (_name, fields) => {
    openResponse = () => json(200, { ...OPENED, ...fields });
    const client = await createClient();

    const session = await openReady(client, { idempotencyKey: 'k1' });

    expect(session.state).toBe('ready');
    expect(published().map((m) => m.type)).toContain('session_open');
    client.destroy();
  });

  it('refuses call() on a replay of a ready session, sending nothing', async () => {
    openResponse = replayed('ready');
    const client = await createClient();

    await expect(client.call('echo-agent', 'hi', { idempotencyKey: 'k1' })).rejects.toMatchObject({
      code: 'session_already_open',
    });
    expect(pubnubs).toHaveLength(0);
    client.destroy();
  });
});

describe('Session exchanges', () => {
  it('publishes each exchange with the next seq and resolves its outcome with every artifact', async () => {
    const session = await openReady(await createClient());
    const exchange = session.send('hello');
    await flush();
    expect(published()[1]).toMatchObject({
      type: 'exchange',
      seq: 2,
      exchangeId: exchange.exchangeId,
      requestParts: [{ partId: 'text', text: 'hello' }],
    });

    const progress: unknown[] = [];
    exchange.onProgress((p) => progress.push(p));
    const artifact = { kind: 'inline', data: 'aGk=', mimeType: 'text/plain' };
    fromProvider({
      type: 'exchange_progress',
      exchangeId: exchange.exchangeId,
      message: 'working',
    });
    fromProvider({
      type: 'exchange_artifact',
      exchangeId: exchange.exchangeId,
      artifactRef: artifact,
    });

    const late: unknown[] = [];
    exchange.onArtifact((ref) => late.push(ref));
    expect(late).toEqual([artifact]);

    fromProvider({ type: 'exchange_outcome', exchangeId: exchange.exchangeId, state: 'completed' });
    await expect(exchange.waitForOutcome()).resolves.toEqual({
      exchangeId: exchange.exchangeId,
      state: 'completed',
      artifacts: [artifact],
    });
    expect(progress).toEqual([{ exchangeId: exchange.exchangeId, message: 'working' }]);
  });

  it('throws SessionBackpressureError on a ninth pending exchange without publishing it', async () => {
    const session = await openReady(await createClient());
    const exchanges = Array.from({ length: 8 }, (_, i) => session.send(`m${i}`));
    await flush();
    const publishes = latest().publish.mock.calls.length;

    expect(() => session.send('m8')).toThrow(SessionBackpressureError);
    await flush();
    expect(latest().publish.mock.calls.length).toBe(publishes);

    fromProvider({
      type: 'exchange_outcome',
      exchangeId: exchanges[0].exchangeId,
      state: 'completed',
    });
    expect(() => session.send('m8')).not.toThrow();
  });

  it('settles an unsettled exchange canceled when close() completes', async () => {
    const session = await openReady(await createClient());
    const exchange = session.send('hello');
    const closing = session.close();
    await flush();
    expect(session.state).toBe('closing');
    expect(published().at(-1)).toMatchObject({ type: 'session_close', seq: 3 });

    fromProvider({ type: 'session_closed', reason: 'consumer_closed' });
    await closing;
    await expect(exchange.waitForOutcome()).resolves.toMatchObject({ state: 'canceled' });
    expect(session.state).toBe('closed');
    expect(latest().destroy).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith(
      'https://api.test/api/v1/sessions/ses1/beacons',
      expect.objectContaining({ body: JSON.stringify({ beacon: 'closed' }) }),
    );
    await session.close();
    expect(published().filter((m) => m.type === 'session_close')).toHaveLength(1);
  });

  it('refuses a send made right after close()', async () => {
    const session = await openReady(await createClient());

    void session.close();

    expect(() => session.send('late')).toThrow(SessionError);
    await flush();
    expect(published().map((m) => m.type)).toEqual(['session_open', 'session_close']);
  });

  it('publishes one session_close when a state callback closes the session again', async () => {
    const session = await openReady(await createClient());
    session.onStateChange((state) => {
      if (state === 'closing') void session.close();
    });

    const closing = session.close();
    await flush();
    fromProvider({ type: 'session_closed', reason: 'consumer_closed' });
    await closing;

    expect(published().filter((m) => m.type === 'session_close')).toHaveLength(1);
  });

  it('ends the session locally when session_closed does not arrive within 5 s', async () => {
    const session = await openReady(await createClient());
    vi.useFakeTimers();
    const closing = session.close();
    await vi.advanceTimersByTimeAsync(5_000);
    await closing;
    expect(session.state).toBe('closed');
    expect(latest().destroy).toHaveBeenCalled();
  });

  it('reports a backend failure to onStateChange once and stops publishing', async () => {
    const session = await openReady(await createClient());
    const states: string[] = [];
    session.onStateChange((s) => states.push(s));
    const exchange = session.send('hello');

    fromBackend({ type: 'session_failed', error: 'route_lost' });
    fromBackend({ type: 'session_failed', error: 'route_lost' });
    await flush();

    expect(states).toEqual(['failed']);
    await expect(exchange.waitForOutcome()).resolves.toMatchObject({
      state: 'failed',
      error: 'route_lost',
    });
    expect(latest().destroy).toHaveBeenCalled();
    expect(() => session.send('again')).toThrow(SessionError);
  });
});

describe('BlocksClient.call', () => {
  it('sends the input inline with session_open, returns the outcome, and closes the session', async () => {
    const client = await createClient();
    const calling = client.call('echo-agent', 'hi');
    await flush();
    connect();
    await flush();

    const open = published()[0];
    const inline = open.exchange as { exchangeId: string; requestParts: unknown[] };
    expect(open.type).toBe('session_open');
    expect(inline.requestParts).toEqual([{ partId: 'text', text: 'hi' }]);

    fromProvider({ type: 'session_ready' });
    fromProvider({ type: 'exchange_outcome', exchangeId: inline.exchangeId, state: 'completed' });
    await flush();
    fromProvider({ type: 'session_closed', reason: 'consumer_closed' });

    await expect(calling).resolves.toEqual({
      exchangeId: inline.exchangeId,
      state: 'completed',
      artifacts: [],
    });
    expect(published().map((m) => m.type)).toEqual(['session_open', 'session_close']);
    expect(latest().destroy).toHaveBeenCalled();
  });

  it('rejects input it cannot send before opening anything on the backend', async () => {
    const client = await createClient();
    const file = { partId: 'doc', file: new Uint8Array([1]), fileName: 'a.bin' };
    await expect(client.call('echo-agent', [file])).rejects.toThrow(TypeError);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(pubnubs).toHaveLength(0);
  });
});

describe('BlocksClient.destroy', () => {
  const closedBeacon = () =>
    fetchMock.mock.calls.filter(
      ([url, init]) =>
        url === 'https://api.test/api/v1/sessions/ses1/beacons' &&
        (init as RequestInit).body === JSON.stringify({ beacon: 'closed' }),
    );

  it('rejects an open whose session_ready arrives while destroy() is closing it', async () => {
    const client = await createClient();
    const opening = client.openSession('echo-agent');
    await flush();
    connect();
    await flush();
    latest().publish.mockImplementationOnce(() => new Promise(() => {}));

    client.destroy();
    await flush();
    fromProvider({ type: 'session_ready' });

    await expect(opening).rejects.toMatchObject({ code: 'session_canceled' });
  });

  it('ends a session still subscribing, publishing nothing and telling the backend', async () => {
    const client = await createClient();
    const opening = client.openSession('echo-agent');
    await flush();

    client.destroy();

    await expect(opening).rejects.toMatchObject({ code: 'session_canceled' });
    expect(latest().publish).not.toHaveBeenCalled();
    expect(latest().destroy).toHaveBeenCalled();
    expect(closedBeacon()).toHaveLength(1);
  });

  it('ends a session whose open request is still in flight', async () => {
    let answer!: (response: Response) => void;
    openResponse = () => new Promise<Response>((r) => (answer = r));
    const client = await createClient();
    const opening = client.openSession('echo-agent');
    await flush();

    client.destroy();
    answer(json(201, OPENED));

    await expect(opening).rejects.toMatchObject({ code: 'session_canceled' });
    expect(pubnubs).toHaveLength(0);
    await flush();
    expect(closedBeacon()).toHaveLength(1);
    await expect(client.openSession('echo-agent')).rejects.toMatchObject({
      code: 'session_canceled',
    });
  });

  it('abandons an open request that has not answered', async () => {
    openResponse = hangingOpen;
    const client = await createClient();
    const opening = client.openSession('echo-agent');
    await flush();

    client.destroy();

    await expect(opening).rejects.toMatchObject({ code: 'session_canceled' });
    expect(pubnubs).toHaveLength(0);
  });
});

describe('Session transport failures', () => {
  it('drops a publish still queued when the session is released', async () => {
    const session = await openReady(await createClient());
    let resume!: () => void;
    latest().publish.mockImplementationOnce(() => new Promise<void>((r) => (resume = r)));
    session.send('stalled');
    const queued = session.send('queued');
    await flush();
    const calls = latest().publish.mock.calls.length;

    fromBackend({ type: 'session_failed', error: 'route_lost' });
    await flush();
    resume();
    await flush();

    expect(latest().publish.mock.calls.length).toBe(calls);
    await expect(queued.waitForOutcome()).resolves.toMatchObject({ state: 'failed' });
  });

  it('settles an exchange failed when its publish fails', async () => {
    const session = await openReady(await createClient());
    latest().publish.mockRejectedValueOnce(new Error('network down'));

    const exchange = session.send('hello');

    await expect(exchange.waitForOutcome()).resolves.toMatchObject({
      state: 'failed',
      error: 'publish_failed',
    });
    expect(session.state).toBe('ready');
  });
});

describe('Session token rotation', () => {
  const TOKEN_URL = 'https://api.test/api/v1/sessions/ses1/token';
  let tokenResponses: Array<() => Response | Promise<Response>>;

  const tokenCalls = () => fetchMock.mock.calls.filter(([url]) => url === TOKEN_URL);
  const deny = (pn = latest()) => {
    for (const l of pn.listeners) l.status?.({ category: 'PNAccessDeniedCategory' });
  };

  /** Faked before opening, so the rotation timer the open arms is under test control. */
  async function openWithFakeTimers() {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
    const opening = createClient().then((client) => client.openSession('echo-agent'));
    await vi.advanceTimersByTimeAsync(0);
    connect();
    await vi.advanceTimersByTimeAsync(0);
    fromProvider({ type: 'session_ready' });
    return opening;
  }

  beforeEach(() => {
    tokenResponses = [];
    const answerOpen = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) =>
      url === TOKEN_URL
        ? (tokenResponses.shift() ?? (() => json(200, { pamToken: 'fresh', ttlMinutes: 60 })))()
        : answerOpen(url, init),
    );
  });

  it('fetches a fresh token at 80% of ttlMinutes and applies it without resubscribing', async () => {
    const session = await openWithFakeTimers();
    const pn = latest();
    tokenResponses.push(() => json(200, { pamToken: 'rotated', ttlMinutes: 10 }));

    await vi.advanceTimersByTimeAsync(48 * 60_000 - 1);
    expect(tokenCalls()).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(tokenCalls()).toHaveLength(1);
    expect(pn.setToken).toHaveBeenLastCalledWith('rotated');
    expect(pn.subscribe).toHaveBeenCalledTimes(1);

    // The next rotation follows the TTL the refresh answered.
    await vi.advanceTimersByTimeAsync(8 * 60_000);
    expect(tokenCalls()).toHaveLength(2);
    expect(session.state).toBe('ready');
  });

  it.each([
    ['a server error', () => json(503, { error: 'unavailable' })],
    ['rate limiting', () => json(429, { error: 'slow down' })],
  ])('retries a rotation that failed with %s, with backoff', async (_, answer) => {
    await openWithFakeTimers();
    tokenResponses.push(answer);

    await vi.advanceTimersByTimeAsync(48 * 60_000);
    expect(tokenCalls()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(tokenCalls()).toHaveLength(2);
    expect(latest().setToken).toHaveBeenLastCalledWith('fresh');
  });

  it('waits out the Retry-After of a rate-limited rotation before retrying', async () => {
    await openWithFakeTimers();
    tokenResponses.push(
      () =>
        new Response(JSON.stringify({ error: 'slow down' }), {
          status: 429,
          headers: { 'Content-Type': 'application/json', 'Retry-After': '90' },
        }),
    );

    await vi.advanceTimersByTimeAsync(48 * 60_000);
    expect(tokenCalls()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(90_000 - 1);
    expect(tokenCalls()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(tokenCalls()).toHaveLength(2);
    expect(latest().setToken).toHaveBeenLastCalledWith('fresh');
  });

  it('stops retrying a failing rotation once the token has expired', async () => {
    const session = await openWithFakeTimers();
    for (let i = 0; i < 50; i++) tokenResponses.push(() => json(503, { error: 'unavailable' }));

    await vi.advanceTimersByTimeAsync(60 * 60_000);
    const attempts = tokenCalls().length;
    expect(attempts).toBeGreaterThan(1);
    await vi.advanceTimersByTimeAsync(60 * 60_000);
    expect(tokenCalls()).toHaveLength(attempts);
    expect(session.state).toBe('ready');
  });

  it.each([
    ['answers SESSION_NOT_FOUND', () => json(404, { code: 'SESSION_NOT_FOUND', error: 'gone' })],
    ['refuses it', () => json(403, { error: 'forbidden' })],
  ])('does not retry a rotation the backend %s', async (_, answer) => {
    const session = await openWithFakeTimers();
    tokenResponses.push(answer);

    await vi.advanceTimersByTimeAsync(48 * 60_000);
    expect(tokenCalls()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(12 * 60_000);
    expect(tokenCalls()).toHaveLength(1);
    expect(session.state).toBe('ready');
  });

  it('refreshes once on a denial and resumes the parked subscription', async () => {
    const session = await openWithFakeTimers();
    const pn = latest();

    deny();
    deny();
    await vi.advanceTimersByTimeAsync(0);

    expect(tokenCalls()).toHaveLength(1);
    expect(pn.setToken).toHaveBeenLastCalledWith('fresh');
    expect(pn.reconnect).toHaveBeenCalledTimes(1);
    expect(session.state).toBe('ready');
  });

  it.each([
    ['answers SESSION_NOT_FOUND', () => json(404, { code: 'SESSION_NOT_FOUND', error: 'gone' })],
    ['refuses the refresh', () => json(403, { error: 'forbidden' })],
  ])('fails the session with session_access_revoked when the backend %s', async (_, answer) => {
    const session = await openWithFakeTimers();
    const exchange = session.send('hello');
    tokenResponses.push(answer);

    deny();
    await vi.advanceTimersByTimeAsync(0);

    expect(session.state).toBe('failed');
    await expect(exchange.waitForOutcome()).resolves.toMatchObject({
      state: 'failed',
      error: 'session_access_revoked',
    });
    expect(latest().destroy).toHaveBeenCalled();
  });

  it('holds proactive rotation while a rate-limited denial refresh waits', async () => {
    await openWithFakeTimers();
    tokenResponses.push(
      () =>
        new Response(JSON.stringify({ error: 'slow down' }), {
          status: 429,
          headers: { 'Content-Type': 'application/json', 'Retry-After': '20' },
        }),
    );

    await vi.advanceTimersByTimeAsync(48 * 60_000 - 10_000);
    deny();
    await vi.advanceTimersByTimeAsync(0);
    expect(tokenCalls()).toHaveLength(1);

    // Rotation was due 10 s in; the denial's 20 s wait still holds.
    await vi.advanceTimersByTimeAsync(20_000 - 1);
    expect(tokenCalls()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(tokenCalls()).toHaveLength(2);
    expect(latest().setToken).toHaveBeenLastCalledWith('fresh');
  });

  it('resumes the subscription when a rotation lands during a rate-limited denial wait', async () => {
    const session = await openWithFakeTimers();
    const pn = latest();
    let landRotation!: (r: Response) => void;
    tokenResponses.push(
      () => new Promise<Response>((resolve) => (landRotation = resolve)),
      () =>
        new Response(JSON.stringify({ error: 'slow down' }), {
          status: 429,
          headers: { 'Content-Type': 'application/json', 'Retry-After': '600' },
        }),
    );

    // A rotation is in flight when the denial's own refresh is held for 600 s.
    await vi.advanceTimersByTimeAsync(48 * 60_000);
    deny();
    await vi.advanceTimersByTimeAsync(0);
    expect(tokenCalls()).toHaveLength(2);
    expect(pn.reconnect).not.toHaveBeenCalled();

    landRotation(json(200, { pamToken: 'rotated', ttlMinutes: 60 }));
    await vi.advanceTimersByTimeAsync(0);
    expect(pn.setToken).toHaveBeenLastCalledWith('rotated');
    expect(pn.reconnect).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(600_000);
    expect(tokenCalls()).toHaveLength(2);
    expect(session.state).toBe('ready');
  });

  it('leaves the retries to a rate-limited denial wait when an in-flight rotation fails', async () => {
    await openWithFakeTimers();
    let landRotation!: (r: Response) => void;
    tokenResponses.push(
      () => new Promise<Response>((resolve) => (landRotation = resolve)),
      () =>
        new Response(JSON.stringify({ error: 'slow down' }), {
          status: 429,
          headers: { 'Content-Type': 'application/json', 'Retry-After': '20' },
        }),
    );

    await vi.advanceTimersByTimeAsync(48 * 60_000);
    deny();
    await vi.advanceTimersByTimeAsync(0);
    landRotation(json(503, { error: 'unavailable' }));
    await vi.advanceTimersByTimeAsync(0);
    expect(tokenCalls()).toHaveLength(2);

    // Rotation's own 5 s retry would land inside the 20 s hold.
    await vi.advanceTimersByTimeAsync(20_000 - 1);
    expect(tokenCalls()).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(tokenCalls()).toHaveLength(3);
  });

  it('ignores a denial refresh that fails after a rotation already answered the denial', async () => {
    const session = await openWithFakeTimers();
    const pn = latest();
    let answerDenial!: (r: Response) => void;
    tokenResponses.push(
      () => new Promise<Response>((resolve) => (answerDenial = resolve)),
      () => json(200, { pamToken: 'rotated', ttlMinutes: 60 }),
    );

    await vi.advanceTimersByTimeAsync(48 * 60_000 - 1);
    deny();
    await vi.advanceTimersByTimeAsync(1);
    expect(pn.setToken).toHaveBeenLastCalledWith('rotated');
    expect(pn.reconnect).toHaveBeenCalledTimes(1);

    answerDenial(json(503, { error: 'unavailable' }));
    await vi.advanceTimersByTimeAsync(0);
    expect(session.state).toBe('ready');
  });

  it('sends no denial retry once the session closes during the wait', async () => {
    const session = await openWithFakeTimers();
    tokenResponses.push(
      () =>
        new Response(JSON.stringify({ error: 'slow down' }), {
          status: 429,
          headers: { 'Content-Type': 'application/json', 'Retry-After': '20' },
        }),
    );

    deny();
    await vi.advanceTimersByTimeAsync(0);
    const closing = session.close();
    await vi.advanceTimersByTimeAsync(5_000);
    await closing;
    const calls = tokenCalls().length;

    await vi.advanceTimersByTimeAsync(60_000);
    expect(tokenCalls()).toHaveLength(calls);
  });

  it('waits out a rate-limited refresh after a denial instead of revoking', async () => {
    const session = await openWithFakeTimers();
    const pn = latest();
    tokenResponses.push(
      () =>
        new Response(JSON.stringify({ error: 'slow down' }), {
          status: 429,
          headers: { 'Content-Type': 'application/json', 'Retry-After': '20' },
        }),
    );

    deny();
    await vi.advanceTimersByTimeAsync(0);
    expect(tokenCalls()).toHaveLength(1);
    expect(session.state).toBe('ready');

    deny();
    await vi.advanceTimersByTimeAsync(20_000 - 1);
    expect(tokenCalls()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);

    expect(tokenCalls()).toHaveLength(2);
    expect(pn.setToken).toHaveBeenLastCalledWith('fresh');
    expect(pn.reconnect).toHaveBeenCalledTimes(1);
    expect(session.state).toBe('ready');
  });

  it('fails the session when the refreshed token is denied before it reconnects', async () => {
    const session = await openWithFakeTimers();

    deny();
    await vi.advanceTimersByTimeAsync(0);
    deny();
    await vi.advanceTimersByTimeAsync(0);

    expect(tokenCalls()).toHaveLength(1);
    expect(session.state).toBe('failed');
  });

  it('rejects the open with session_access_revoked when its token is denied and cannot be refreshed', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
    tokenResponses.push(() => json(404, { code: 'SESSION_NOT_FOUND', error: 'gone' }));
    const opening = createClient().then((client) => client.openSession('echo-agent'));
    const rejected = opening.catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(0);

    deny();
    await vi.advanceTimersByTimeAsync(0);

    const err = await rejected;
    expect(err).toBeInstanceOf(SessionFailedError);
    expect((err as SessionError).code).toBe('session_access_revoked');
  });

  it('stops rotating once the session is closed', async () => {
    const session = await openWithFakeTimers();
    const closing = session.close();
    await vi.advanceTimersByTimeAsync(5_000);
    await closing;

    await vi.advanceTimersByTimeAsync(60 * 60_000);
    expect(tokenCalls()).toHaveLength(0);
  });
});

describe('Session recovery', () => {
  let snapshot: Record<string, unknown>;

  beforeEach(() => {
    snapshot = { sessionId: OPENED.sessionId, state: 'ready', exchanges: [] };
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/api/v1/sessions')) return openResponse(init);
      if (url.endsWith(`/api/v1/sessions/${OPENED.sessionId}`) && init?.method === 'GET') {
        return json(200, snapshot);
      }
      return new Response(null, { status: 204 });
    });
  });

  const providerMessage = (seq: number, fields: Record<string, unknown>) => ({
    timetoken: String(17000000000000000n + BigInt(seq)),
    uuid: OPENED.providerTransportUuid,
    message: { protocolVersion: '2026-05-01', sessionId: OPENED.sessionId, seq, ...fields },
  });

  it('refills a gap from history, so a lost artifact and outcome still arrive', async () => {
    const client = await createClient();
    const session = await openReady(client);
    const exchange = session.send('hi');
    await flush();
    const exchangeId = published().find((m) => m.type === 'exchange')?.exchangeId as string;
    const artifactRef = { kind: 'inline', mimeType: 'text/plain', data: 'aGk=' };
    latest().fetchMessages.mockResolvedValue({
      channels: {
        [OPENED.sessionChannel]: [
          providerMessage(2, { type: 'exchange_artifact', exchangeId, artifactRef }),
          providerMessage(3, { type: 'exchange_outcome', exchangeId, state: 'completed' }),
        ],
      },
    });
    providerSeq = 3;
    fromProvider({ type: 'exchange_progress', exchangeId, message: 'later' });

    const outcome = await exchange.waitForOutcome(5_000);

    expect(outcome).toMatchObject({ state: 'completed', artifacts: [artifactRef] });
    // From session_ready, the last message before the gap, not the progress held behind it.
    expect(latest().fetchMessages.mock.calls[0][0]).toMatchObject({ end: '17000000000000001' });
    client.destroy();
  });

  it('settles from the backend record an outcome history no longer holds, after a reconnect', async () => {
    const client = await createClient();
    const session = await openReady(client);
    const exchange = session.send('hi');
    await flush();
    const exchangeId = published().find((m) => m.type === 'exchange')?.exchangeId as string;
    snapshot.exchanges = [{ exchangeId, state: 'completed', error: null, artifactRefs: [] }];

    for (const l of latest().listeners) l.status?.({ category: 'PNReconnectedCategory' });

    await expect(exchange.waitForOutcome(5_000)).resolves.toMatchObject({ state: 'completed' });
    expect(latest().fetchMessages).toHaveBeenCalled();
    client.destroy();
  });

  it('fails the session the backend recorded as failed, after settling what it recorded', async () => {
    const client = await createClient();
    const session = await openReady(client);
    const done = session.send('one');
    const lost = session.send('two');
    await flush();
    const [doneId] = published()
      .filter((m) => m.type === 'exchange')
      .map((m) => m.exchangeId as string);
    snapshot = {
      sessionId: OPENED.sessionId,
      state: 'failed',
      error: 'route_lost',
      exchanges: [{ exchangeId: doneId, state: 'completed', error: null, artifactRefs: [] }],
    };

    await session.recover();

    await expect(done.waitForOutcome(1_000)).resolves.toMatchObject({ state: 'completed' });
    await expect(lost.waitForOutcome(1_000)).resolves.toMatchObject({
      state: 'failed',
      error: 'route_lost',
    });
    expect(session.state).toBe('failed');
    client.destroy();
  });

  it('still settles from the backend record when the history fetch fails', async () => {
    const client = await createClient();
    const session = await openReady(client);
    const exchange = session.send('hi');
    await flush();
    const exchangeId = published().find((m) => m.type === 'exchange')?.exchangeId as string;
    latest().fetchMessages.mockRejectedValue(new Error('history 503'));
    snapshot.exchanges = [{ exchangeId, state: 'completed', error: null, artifactRefs: [] }];

    await session.recover();

    await expect(exchange.waitForOutcome(1_000)).resolves.toMatchObject({ state: 'completed' });
    client.destroy();
  });

  it('settles a recorded outcome before a replayed terminal notice ends the session', async () => {
    const client = await createClient();
    const session = await openReady(client);
    const exchange = session.send('hi');
    await flush();
    const exchangeId = published().find((m) => m.type === 'exchange')?.exchangeId as string;
    latest().fetchMessages.mockResolvedValue({
      channels: {
        [OPENED.sessionChannel]: [
          {
            timetoken: '17000000000000009',
            uuid: OPENED.backendTransportUuid,
            message: {
              type: 'session_closed',
              protocolVersion: '2026-05-01',
              sessionId: OPENED.sessionId,
              seq: 0,
              reason: 'idle_timeout',
            },
          },
        ],
      },
    });
    snapshot = {
      sessionId: OPENED.sessionId,
      state: 'closed',
      reason: 'idle_timeout',
      exchanges: [{ exchangeId, state: 'completed', error: null, artifactRefs: [] }],
    };

    await session.recover();

    await expect(exchange.waitForOutcome(1_000)).resolves.toMatchObject({ state: 'completed' });
    expect(session.state).toBe('closed');
    client.destroy();
  });

  it('settles a recorded outcome before a live terminal notice that arrives during recovery', async () => {
    const client = await createClient();
    const session = await openReady(client);
    const exchange = session.send('hi');
    await flush();
    const exchangeId = published().find((m) => m.type === 'exchange')?.exchangeId as string;
    snapshot = {
      sessionId: OPENED.sessionId,
      state: 'ready',
      exchanges: [{ exchangeId, state: 'completed', error: null, artifactRefs: [] }],
    };
    let releaseHistory = () => {};
    latest().fetchMessages.mockImplementation(
      () =>
        new Promise((resolve) => {
          releaseHistory = () => resolve({ channels: {} });
        }),
    );

    const recovering = session.recover();
    await vi.waitFor(() => expect(latest().fetchMessages).toHaveBeenCalled());
    fromBackend({ type: 'session_closed', reason: 'idle_timeout' });
    releaseHistory();
    await recovering;

    await expect(exchange.waitForOutcome(1_000)).resolves.toMatchObject({ state: 'completed' });
    client.destroy();
  });

  it('holds a live terminal notice that arrives while the snapshot is being fetched', async () => {
    const client = await createClient();
    const session = await openReady(client);
    const exchange = session.send('hi');
    await flush();
    const exchangeId = published().find((m) => m.type === 'exchange')?.exchangeId as string;
    let answerSnapshot = () => {};
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith(`/api/v1/sessions/${OPENED.sessionId}`) && init?.method === 'GET') {
        await new Promise<void>((resolve) => {
          answerSnapshot = resolve;
        });
        return json(200, {
          sessionId: OPENED.sessionId,
          state: 'closed',
          reason: 'idle_timeout',
          exchanges: [{ exchangeId, state: 'completed', error: null, artifactRefs: [] }],
        });
      }
      return new Response(null, { status: 204 });
    });

    const recovering = session.recover();
    await flush();
    fromBackend({ type: 'session_closed', reason: 'idle_timeout' });
    answerSnapshot();
    await recovering;

    await expect(exchange.waitForOutcome(1_000)).resolves.toMatchObject({ state: 'completed' });
    expect(session.state).toBe('closed');
    client.destroy();
  });

  it('does not move the replay point on a message it drops, such as its own echo', async () => {
    const client = await createClient();
    const session = await openReady(client);
    for (const l of latest().listeners) {
      l.message?.({
        channel: OPENED.sessionChannel,
        publisher: OPENED.consumerTransportUuid,
        timetoken: '17000000000000005',
        message: { type: 'exchange', sessionId: OPENED.sessionId, seq: 1 },
      });
    }

    await session.recover();

    expect(latest().fetchMessages.mock.calls[0][0]).toMatchObject({ end: '17000000000000001' });
    client.destroy();
  });

  it('replays history in full before a live terminal notice that arrives during recovery ends it', async () => {
    const client = await createClient();
    const session = await openReady(client);
    const exchange = session.send('hi');
    await flush();
    const exchangeId = published().find((m) => m.type === 'exchange')?.exchangeId as string;
    const artifactRef = { kind: 'inline', mimeType: 'text/plain', data: 'aGk=' };
    let answerSnapshot = () => {};
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith(`/api/v1/sessions/${OPENED.sessionId}`) && init?.method === 'GET') {
        await new Promise<void>((resolve) => {
          answerSnapshot = resolve;
        });
        return json(200, { sessionId: OPENED.sessionId, state: 'closed', exchanges: [] });
      }
      return new Response(null, { status: 204 });
    });
    latest().fetchMessages.mockResolvedValue({
      channels: {
        [OPENED.sessionChannel]: [
          providerMessage(2, { type: 'exchange_artifact', exchangeId, artifactRef }),
          providerMessage(3, { type: 'exchange_outcome', exchangeId, state: 'completed' }),
        ],
      },
    });

    const recovering = session.recover();
    await flush();
    fromBackend({ type: 'session_closed', reason: 'idle_timeout' });
    answerSnapshot();
    await recovering;

    await expect(exchange.waitForOutcome(1_000)).resolves.toMatchObject({
      state: 'completed',
      artifacts: [artifactRef],
    });
    expect(session.state).toBe('closed');
    client.destroy();
  });

  it('runs overlapping recoveries one after another', async () => {
    const client = await createClient();
    const session = await openReady(client);
    let inFlight = 0;
    let most = 0;
    latest().fetchMessages.mockImplementation(async () => {
      most = Math.max(most, ++inFlight);
      await flush();
      inFlight--;
      return { channels: {} };
    });

    await Promise.all([session.recover(), session.recover()]);

    expect(latest().fetchMessages).toHaveBeenCalledTimes(2);
    expect(most).toBe(1);
    client.destroy();
  });

  it('replays from where recovery began, though a live message arrives during the snapshot fetch', async () => {
    const client = await createClient();
    const session = await openReady(client);
    let answerSnapshot = () => {};
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.endsWith(`/api/v1/sessions/${OPENED.sessionId}`) && init?.method === 'GET') {
        await new Promise<void>((resolve) => {
          answerSnapshot = resolve;
        });
        return json(200, snapshot);
      }
      return new Response(null, { status: 204 });
    });

    const recovering = session.recover();
    await flush();
    fromProvider({ type: 'exchange_progress', exchangeId: 'x', message: 'live' });
    answerSnapshot();
    await recovering;

    expect(latest().fetchMessages.mock.calls[0][0]).toMatchObject({ end: '17000000000000001' });
    client.destroy();
  });

  /** Serves `messages` the way PubNub history does: newest first, `start` exclusive, `end` inclusive. */
  function historyOf(messages: ReturnType<typeof providerMessage>[]) {
    return async (params: { start?: string; end?: string; count?: number }) => {
      const inSlice = messages.filter(
        (m) =>
          (params.start === undefined || BigInt(m.timetoken) < BigInt(params.start)) &&
          (params.end === undefined || BigInt(m.timetoken) >= BigInt(params.end)),
      );
      const page = inSlice.slice(-(params.count ?? 100));
      return { channels: { [OPENED.sessionChannel]: page } };
    };
  }

  async function openWithExchange(auth?: AuthProvider) {
    const client = await createClient(auth);
    const session = await openReady(client);
    const exchange = session.send('hi');
    await flush();
    const exchangeId = published().find((m) => m.type === 'exchange')?.exchangeId as string;
    return { client, session, exchange, exchangeId };
  }

  it('delivers an artifact held behind a gap history cannot fill before the record settles its exchange', async () => {
    const { client, exchange, exchangeId } = await openWithExchange();
    const inline = { kind: 'inline', mimeType: 'text/plain', data: 'aGk=' };
    snapshot.exchanges = [{ exchangeId, state: 'completed', error: null, artifactRefs: [] }];
    // seq 2 is lost for good: history does not hold it either.
    providerSeq = 2;
    fromProvider({ type: 'exchange_artifact', exchangeId, artifactRef: inline });

    await expect(exchange.waitForOutcome(5_000)).resolves.toMatchObject({
      state: 'completed',
      artifacts: [inline],
    });
    client.destroy();
  });

  it('delivers an artifact behind a gap that only history reveals before the record settles', async () => {
    const { client, session, exchange, exchangeId } = await openWithExchange();
    const inline = { kind: 'inline', mimeType: 'text/plain', data: 'aGk=' };
    snapshot.exchanges = [{ exchangeId, state: 'completed', error: null, artifactRefs: [] }];
    // Nothing is held live; history holds seq 3 but has lost seq 2.
    latest().fetchMessages.mockImplementation(
      historyOf([providerMessage(3, { type: 'exchange_artifact', exchangeId, artifactRef: inline })]),
    );

    await session.recover();

    await expect(exchange.waitForOutcome(1_000)).resolves.toMatchObject({
      state: 'completed',
      artifacts: [inline],
    });
    client.destroy();
  });

  it('settles a recorded outcome before a session_closed held behind a gap ends the session', async () => {
    const { client, session, exchange, exchangeId } = await openWithExchange();
    snapshot = {
      sessionId: OPENED.sessionId,
      state: 'closed',
      reason: 'provider_closed',
      exchanges: [{ exchangeId, state: 'completed', error: null, artifactRefs: [] }],
    };
    providerSeq = 2;
    fromProvider({ type: 'session_closed', reason: 'provider_closed' });

    await expect(exchange.waitForOutcome(5_000)).resolves.toMatchObject({ state: 'completed' });
    expect(session.state).toBe('closed');
    client.destroy();
  });

  it('gives up on a snapshot that never answers, and still applies a held session_closed', async () => {
    const { client, session } = await openWithExchange();
    fetchMock.mockImplementation((url: string, init?: RequestInit) =>
      url.endsWith(`/api/v1/sessions/${OPENED.sessionId}`) && init?.method === 'GET'
        ? hangingOpen(init)
        : Promise.resolve(new Response(null, { status: 204 })),
    );
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });

    const recovering = session.recover();
    await vi.advanceTimersByTimeAsync(0);
    fromBackend({ type: 'session_closed', reason: 'idle_timeout' });
    await vi.advanceTimersByTimeAsync(10_000);
    await recovering;

    expect(session.state).toBe('closed');
    client.destroy();
  });

  it('replays every history page in order, each message once', async () => {
    const { client, session, exchange, exchangeId } = await openWithExchange();
    const progress = Array.from({ length: 250 }, (_, i) =>
      providerMessage(i + 2, { type: 'exchange_progress', exchangeId, message: `p${i + 2}` }),
    );
    latest().fetchMessages.mockImplementation(
      historyOf([
        ...progress,
        providerMessage(252, { type: 'exchange_outcome', exchangeId, state: 'completed' }),
      ]),
    );
    const seen: string[] = [];
    exchange.onProgress(({ message }) => seen.push(message as string));

    await session.recover();

    expect(latest().fetchMessages).toHaveBeenCalledTimes(3);
    expect(seen).toEqual(progress.map((m) => m.message.message));
    await expect(exchange.waitForOutcome(1_000)).resolves.toMatchObject({ state: 'completed' });
    await session.recover();
    expect(latest().fetchMessages.mock.calls[3][0]).toMatchObject({ end: '17000000000000252' });
    client.destroy();
  });

  it('stops after ten history pages', async () => {
    const { client, session, exchangeId } = await openWithExchange();
    latest().fetchMessages.mockImplementation(
      historyOf(
        Array.from({ length: 1_500 }, (_, i) =>
          providerMessage(i + 2, { type: 'exchange_progress', exchangeId, message: 'p' }),
        ),
      ),
    );

    await session.recover();

    expect(latest().fetchMessages).toHaveBeenCalledTimes(10);
    client.destroy();
  });

  it('lists a file artifact once when the record returns it with its keys reordered', async () => {
    const { client, session, exchange, exchangeId } = await openWithExchange();
    const live = { kind: 'file', fileId: 'f1', fileName: 'a.txt', channel: 'c', mimeType: 'text/plain' };
    const recorded = { mimeType: 'text/plain', channel: 'c', fileName: 'a.txt', fileId: 'f1', kind: 'file' };
    const recordedOnly = { kind: 'file', fileId: 'f2', fileName: 'b.txt', channel: 'c', mimeType: 'text/plain' };
    fromProvider({ type: 'exchange_artifact', exchangeId, artifactRef: live });
    snapshot.exchanges = [
      { exchangeId, state: 'completed', error: null, artifactRefs: [recorded, recordedOnly] },
    ];

    await session.recover();

    await expect(exchange.waitForOutcome(1_000)).resolves.toMatchObject({
      artifacts: [live, recordedOnly],
    });
    client.destroy();
  });

  it('merges a recorded file ref into an outcome released from behind a gap', async () => {
    const { client, exchange, exchangeId } = await openWithExchange();
    const fileRef = { kind: 'file', fileId: 'f9', fileName: 'r.txt', channel: 'c', mimeType: 'text/plain' };
    snapshot.exchanges = [{ exchangeId, state: 'completed', error: null, artifactRefs: [fileRef] }];
    // seq 2, the artifact message that carried the file, is lost for good.
    providerSeq = 2;
    fromProvider({ type: 'exchange_outcome', exchangeId, state: 'completed' });

    await expect(exchange.waitForOutcome(5_000)).resolves.toMatchObject({
      state: 'completed',
      artifacts: [fileRef],
    });
    client.destroy();
  });

  it('reports both errors when history and the snapshot both fail', async () => {
    const { client, session } = await openWithExchange();
    latest().fetchMessages.mockRejectedValue(new Error('history 503'));
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) =>
      url.endsWith(`/api/v1/sessions/${OPENED.sessionId}`) && init?.method === 'GET'
        ? json(500, { error: 'boom' })
        : new Response(null, { status: 204 }),
    );

    const failure = await session.recover().catch((err: unknown) => err);

    expect((failure as { errors?: unknown[] }).errors).toHaveLength(2);
    expect(String((failure as Error).message)).toMatch(/history 503/);
    client.destroy();
  });

  it('refuses a send from an artifact callback while the session is ending', async () => {
    const { client, session, exchange, exchangeId } = await openWithExchange();
    const inline = { kind: 'inline', mimeType: 'text/plain', data: 'aGk=' };
    let sendError: unknown;
    exchange.onArtifact(() => {
      try {
        session.send('more');
      } catch (err) {
        sendError = err;
      }
    });
    // seq 2 is missing, so the artifact waits behind a gap until the session ends.
    providerSeq = 2;
    fromProvider({ type: 'exchange_artifact', exchangeId, artifactRef: inline });
    const exchangesBefore = published().filter((m) => m.type === 'exchange').length;

    fromBackend({ type: 'session_closed', reason: 'idle_timeout' });
    await flush();

    expect(sendError).toBeInstanceOf(SessionError);
    expect(published().filter((m) => m.type === 'exchange')).toHaveLength(exchangesBefore);
    await expect(exchange.waitForOutcome(0)).resolves.toMatchObject({ artifacts: [inline] });
    client.destroy();
  });

  it('times out a snapshot whose auth preparation stalls', async () => {
    let stall = false;
    const { client, session } = await openWithExchange({
      ...authProvider,
      ensureReady: () => (stall ? new Promise<void>(() => {}) : Promise.resolve()),
    });
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    stall = true;

    const recovering = session.recover();
    await vi.advanceTimersByTimeAsync(0);
    fromBackend({ type: 'session_closed', reason: 'idle_timeout' });
    expect(session.state).toBe('ready');
    await vi.advanceTimersByTimeAsync(10_000);
    await recovering;

    expect(session.state).toBe('closed');
    client.destroy();
  });

  it('gives up on history that is still paging after ten seconds, and settles from the record', async () => {
    const { client, session, exchange, exchangeId } = await openWithExchange();
    snapshot.exchanges = [{ exchangeId, state: 'completed', error: null, artifactRefs: [] }];
    const history = historyOf(
      Array.from({ length: 1_500 }, (_, i) =>
        providerMessage(i + 2, { type: 'exchange_progress', exchangeId, message: 'p' }),
      ),
    );
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    // Each page answers well inside ten seconds; together they do not.
    latest().fetchMessages.mockImplementation(async (params) => {
      await new Promise((resolve) => setTimeout(resolve, 4_000));
      return history(params);
    });
    let recovered = false;
    const seen: unknown[] = [];
    exchange.onProgress((progress) => seen.push(progress));

    const recovering = session.recover().then(() => {
      recovered = true;
    });
    await vi.advanceTimersByTimeAsync(10_000);

    expect(recovered).toBe(true);
    await recovering;
    const pagesFetched = latest().fetchMessages.mock.calls.length;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(latest().fetchMessages).toHaveBeenCalledTimes(pagesFetched);
    expect(seen).toEqual([]);
    await expect(exchange.waitForOutcome(0)).resolves.toMatchObject({ state: 'completed' });
    client.destroy();
  });

  it('ends failed, not closed, when close() outlasts a recovery holding a session_failed', async () => {
    const { client, session, exchange } = await openWithExchange();
    fetchMock.mockImplementation((url: string, init?: RequestInit) =>
      url.endsWith(`/api/v1/sessions/${OPENED.sessionId}`) && init?.method === 'GET'
        ? hangingOpen(init)
        : Promise.resolve(new Response(null, { status: 204 })),
    );
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });

    const recovering = session.recover();
    await vi.advanceTimersByTimeAsync(0);
    fromBackend({ type: 'session_failed', error: 'route_lost' });
    const closing = session.close();
    await vi.advanceTimersByTimeAsync(5_000);
    await closing;

    expect(session.state).toBe('failed');
    await expect(exchange.waitForOutcome(0)).resolves.toMatchObject({
      state: 'failed',
      error: 'route_lost',
    });
    await vi.advanceTimersByTimeAsync(10_000);
    await recovering;
    client.destroy();
  });
});
