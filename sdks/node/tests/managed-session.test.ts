import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  BlocksClient,
  SessionFailedError,
  SessionStreamError,
  type AuthProvider,
  type Exchange,
  type Message,
  type Session,
} from '../src/index.js';
import { StreamQueue, streamSubscription } from '../src/runtime/sessions/stream-queue.js';

interface Listener {
  message?: (e: unknown) => void;
  status?: (s: unknown) => void;
}
const pubnubs: Array<{ listeners: Listener[]; publish: ReturnType<typeof vi.fn> }> = [];
const { startForeignStream } = vi.hoisted(() => ({ startForeignStream: vi.fn() }));

vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation(() => {
    const fake = { listeners: [] as Listener[], publish: vi.fn(async () => ({ timetoken: '1' })) };
    pubnubs.push(fake);
    return {
      ...fake,
      fetchMessages: vi.fn(async () => ({ channels: {} })),
      setToken: vi.fn(),
      subscribe: vi.fn(),
      reconnect: vi.fn(),
      destroy: vi.fn(),
      addListener: (l: Listener) => fake.listeners.push(l),
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

vi.mock('../src/runtime/sessions/stream-subscriber.js', () => ({ startForeignStream }));

const SESSION = {
  sessionId: 'ses1',
  sessionChannel: 'sess.rt1.ses1',
  pamToken: 'pam-token',
  ttlMinutes: 60,
  consumerTransportUuid: 'consumer-uuid',
  providerTransportUuid: 'provider-uuid',
  backendTransportUuid: 'backend-uuid',
};
const CREDENTIALS = {
  streamId: 'st1',
  subscribeKey: 'sub-foreign',
  channel: 'live.eu',
  mode: 'access_manager',
  token: 'stream-token',
  ttlMinutes: 60,
  expiresAt: '2030-01-01T00:00:00.000Z',
};

let onAuthFailure: () => Promise<boolean>;
const authProvider: AuthProvider = {
  getAuthHeader: () => 'Bearer consumer-jwt',
  onAuthFailure: () => onAuthFailure(),
};

interface Call {
  method: string;
  path: string;
  body: Record<string, unknown> | undefined;
  signal: AbortSignal | undefined;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const refusal = (status: number, code: string) => json(status, { code, error: code });

let managed: boolean;
let openExtra: Record<string, unknown>;
/** Replaces the open request's answer, to hold it back. */
let openAnswer: (() => Response | Promise<Response>) | undefined;
let calls: Call[];
/** Answers a call other than the open; each test replaces what it exercises. */
let answer: (call: Call) => Response | Promise<Response>;
let providerSeq: number;
let randomSpy: ReturnType<typeof vi.spyOn>;
const clients: BlocksClient[] = [];

const exchangeIdOf = (call: Call) => call.body?.exchangeId as string;

const beaconExchangeId = (call: Call) => (call.body?.exchange as { exchangeId: string }).exchangeId;

function defaultAnswer(call: Call): Response {
  if (call.path.endsWith('/cancel')) return json(202, { exchangeId: 'x', state: 'running' });
  if (call.method === 'GET') return json(200, { sessionId: 'ses1', state: 'ready', exchanges: [] });
  if (call.body?.exchange)
    return json(200, { exchangeId: beaconExchangeId(call), state: 'completed' });
  if (call.path.endsWith('/exchanges')) {
    return json(200, { exchangeId: exchangeIdOf(call), state: 'completed' });
  }
  return json(200, {});
}

const callsTo = (suffix: string) => calls.filter((c) => c.path.endsWith(suffix));
const exchangePosts = () => callsTo('/exchanges');
const cancelPosts = () => callsTo('/cancel');
const beacons = () => callsTo('/beacons').map((c) => c.body?.beacon);
const snapshotReads = () => calls.filter((c) => c.method === 'GET').length;

type Reply = (call: Call) => Response | Promise<Response>;

/** Answers the calls whose path ends with `suffix` by `reply`, and every other call as usual. */
const answerPath = (suffix: string, reply: Reply) => {
  answer = (call) => (call.path.endsWith(suffix) ? reply(call) : defaultAnswer(call));
};

/** Replies to the first call to `suffix`, then answers as usual. */
const answerFirst = (suffix: string, reply: Reply) => {
  let seen = 0;
  answerPath(suffix, (call) => (++seen === 1 ? reply(call) : defaultAnswer(call)));
};

/** Answers the opened beacon that carries the call's input with `reply`. */
const answerCallBeacon = (reply: Reply) =>
  answerPath('/beacons', (call) => (call.body?.exchange ? reply(call) : defaultAnswer(call)));

const accepted = (call: Call) =>
  json(202, {
    exchangeId: call.body?.exchangeId ?? (call.body?.exchange ? beaconExchangeId(call) : 'x'),
    state: 'running',
  });
const transportError = () => {
  throw new TypeError('fetch failed');
};

const latest = () => pubnubs[pubnubs.length - 1];
const flush = () =>
  vi.isFakeTimers() ? vi.advanceTimersByTimeAsync(0) : new Promise((r) => setTimeout(r, 0));

function connect() {
  for (const l of latest().listeners) l.status?.({ category: 'PNConnectedCategory' });
}

function deliver(
  publisher: string,
  seq: number,
  fields: Record<string, unknown>,
  timetoken?: string,
) {
  const message = { protocolVersion: '2026-05-01', sessionId: SESSION.sessionId, seq, ...fields };
  for (const l of latest().listeners) {
    l.message?.({ channel: SESSION.sessionChannel, publisher, timetoken, message });
  }
}

function fromProvider(fields: Record<string, unknown>) {
  const seq = ++providerSeq;
  deliver(SESSION.providerTransportUuid, seq, fields, String(17000000000000000n + BigInt(seq)));
}

const fromBackend = (fields: Record<string, unknown>) =>
  deliver(SESSION.backendTransportUuid, 0, fields);

const outcome = (exchangeId: string, state = 'completed', extra = {}) =>
  fromProvider({ type: 'exchange_outcome', exchangeId, state, ...extra });

/** A client that the test's `afterEach` destroys. */
async function createClient() {
  const client = await BlocksClient.create({
    billingMode: 'free',
    baseUrl: 'https://api.test',
    subscribeKey: 'sub-test',
    publishKey: 'pub-test',
    authProvider,
  });
  clients.push(client);
  return client;
}

async function openReady(client: BlocksClient, options?: { idempotencyKey: string }) {
  const opening = client.openSession('connector', options);
  await flush();
  connect();
  await flush();
  fromProvider({ type: 'session_ready' });
  return opening;
}

const readySession = async () => openReady(await createClient());

/** An open that is connected but not yet ready; its failure is observed by the test, not unhandled. */
async function beginOpen() {
  const opening = (await createClient()).openSession('connector');
  opening.catch(() => undefined);
  await flush();
  connect();
  return { opening };
}

/** A session with one exchange whose request went out. */
async function sentExchange(text = 'a') {
  const session = await readySession();
  const exchange = session.send(text);
  await flush();
  return { session, exchange };
}

const expectOutcome = (exchange: Exchange, fields: object, ms = 1_000) =>
  expect(exchange.waitForOutcome(ms)).resolves.toMatchObject(fields);

/** A started `call()`, past its subscription and its `opened` beacon request. */
async function startCall(client: BlocksClient, options?: { timeoutMs?: number }) {
  const calling = client.call('connector', 'input', options);
  calling.catch(() => undefined);
  await flush();
  connect();
  await vi.waitFor(() => expect(callsTo('/beacons').length).toBeGreaterThan(0));
  return { calling };
}

/** A started `call()` whose session the provider has made ready, with its exchange's id. */
async function readyCall(options?: { timeoutMs?: number }) {
  const { calling } = await startCall(await createClient(), options);
  await flush();
  fromProvider({ type: 'session_ready' });
  return { calling, exchangeId: beaconExchangeId(callsTo('/beacons')[0]) };
}

/** Never answers; fails when its request is aborted, as fetch does. */
const stalledRequest = (call: Call) =>
  new Promise<Response>((_, reject) =>
    call.signal?.addEventListener('abort', () => reject(call.signal?.reason)),
  );

const deferredResponse = () => {
  let resolve!: (response: Response) => void;
  const promise = new Promise<Response>((r) => (resolve = r));
  return { promise, resolve };
};

beforeEach(() => {
  pubnubs.length = 0;
  providerSeq = 0;
  calls = [];
  managed = true;
  openExtra = {};
  openAnswer = undefined;
  answer = defaultAnswer;
  onAuthFailure = async () => false;
  startForeignStream.mockReset();
  // No jitter: a retry follows its failure at once.
  randomSpy = vi.spyOn(Math, 'random').mockReturnValue(0);
  vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
    const call: Call = {
      method: init?.method ?? 'GET',
      path: new URL(url).pathname,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
      signal: init?.signal ?? undefined,
    };
    calls.push(call);
    if (call.path === '/api/v1/sessions') {
      return openAnswer?.() ?? json(201, { ...SESSION, managed, ...openExtra });
    }
    return answer(call);
  });
});

afterEach(async () => {
  // Closing a client sends beacons; let them land before the next test counts its own.
  answer = defaultAnswer;
  vi.useRealTimers();
  for (const client of clients.splice(0)) client.destroy();
  await new Promise((r) => setTimeout(r, 0));
  randomSpy.mockRestore();
  vi.unstubAllGlobals();
});

describe('A managed session', () => {
  it('declares the managed capability on every open, and publishes nothing to its channel', async () => {
    const session = await readySession();

    expect(calls[0].body).toMatchObject({ agentName: 'connector', capabilities: ['managed'] });
    const exchange = session.send('hello');
    await flush();
    await session.close();

    expect(beacons()).toEqual(['opened', 'closed']);
    expect(exchangePosts()[0].body).toMatchObject({
      exchangeId: exchange.exchangeId,
      requestParts: [{ partId: 'text', text: 'hello' }],
    });
    expect(latest().publish).not.toHaveBeenCalled();
    expect(session.state).toBe('closed');
  });

  it('carries the call input on the opened beacon and resends it after a transport error', async () => {
    answerFirst('/beacons', transportError);
    const client = await createClient();
    const { calling } = await startCall(client);
    await vi.waitFor(() => expect(callsTo('/beacons')).toHaveLength(2));

    const sent = callsTo('/beacons').map((c) => c.body);
    expect(sent[1]).toMatchObject({
      beacon: 'opened',
      exchange: { requestParts: [{ partId: 'text', text: 'input' }] },
    });
    expect(sent[1]).toEqual(sent[0]);
    expect(latest().publish).not.toHaveBeenCalled();

    // Ready and the result come from the channel alone, never from the beacon's answer.
    const { exchangeId } = sent[1]!.exchange as { exchangeId: string };
    fromProvider({ type: 'session_ready' });
    outcome(exchangeId);
    fromProvider({ type: 'session_closed', reason: 'provider_closed' });
    await expect(calling).resolves.toMatchObject({ exchangeId, state: 'completed' });
  });

  it('resends an opened beacon only after the Retry-After of the 429 that refused it', async () => {
    vi.useFakeTimers();
    answerFirst(
      '/beacons',
      () => new Response('', { status: 429, headers: { 'retry-after': '5' } }),
    );
    await startCall(await createClient());

    await vi.advanceTimersByTimeAsync(4_000);
    expect(callsTo('/beacons')).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(1_000);
    expect(callsTo('/beacons')).toHaveLength(2);
  });

  it('waits for no answer after the outcome of a one-shot call, whose beacon already answered', async () => {
    vi.useFakeTimers();
    const { calling, exchangeId } = await readyCall();
    const before = vi.getTimerCount();

    outcome(exchangeId);

    expect(vi.getTimerCount()).toBeLessThan(before);
    await expect(calling).resolves.toMatchObject({ state: 'completed' });
  });

  it('leaves no timer running when the session ends while a stream waits for its answer', async () => {
    vi.useFakeTimers();
    answerPath('/exchanges', stalledRequest);
    const session = await readySession();
    const exchange = session.send('input');
    await vi.waitFor(() => expect(exchangePosts()).toHaveLength(1));
    outcome(exchange.exchangeId);

    await session.close();
    await vi.advanceTimersByTimeAsync(0);

    expect(vi.getTimerCount()).toBe(0);
  });

  it('skips the closed beacon of a one-shot call once its beacon answered a final state', async () => {
    const { calling, exchangeId } = await readyCall();

    outcome(exchangeId);
    await expect(calling).resolves.toMatchObject({ state: 'completed' });

    expect(beacons()).toEqual(['opened']);
  });

  it('sends no closed beacon after session_closed ended a one-shot call that had no final answer', async () => {
    answerCallBeacon(accepted);
    const { calling, exchangeId } = await readyCall();

    outcome(exchangeId);
    fromProvider({ type: 'session_closed', reason: 'provider_closed' });
    await expect(calling).resolves.toMatchObject({ state: 'completed' });
    await flush();

    expect(beacons()).toEqual(['opened']);
  });

  it('sends no closed beacon once a one-shot call has its outcome, though its opened beacon is unanswered', async () => {
    answerCallBeacon(stalledRequest);
    const { calling, exchangeId } = await readyCall();

    outcome(exchangeId);
    await expect(calling).resolves.toMatchObject({ state: 'completed' });
    await flush();

    expect(beacons()).toEqual(['opened']);
  });

  it('sends the closed beacon of a call that stops waiting before its beacon answered a final state', async () => {
    answerCallBeacon(accepted);
    const { calling } = await readyCall({ timeoutMs: 20 });

    await expect(calling).rejects.toThrow(/did not settle/);

    expect(beacons()).toEqual(['opened', 'closed']);
  });

  it('settles a call whose input the backend turned down with publish_failed, then closes', async () => {
    answerCallBeacon(() => refusal(413, 'PAYLOAD_TOO_LARGE'));

    const { calling } = await startCall(await createClient());

    await expect(calling).resolves.toMatchObject({ state: 'failed', error: 'publish_failed' });
    await vi.waitFor(() => expect(beacons()).toEqual(['opened', 'closed']));
  });

  it('refuses an open whose opened beacon meets SESSION_ALREADY_OPEN, and sends no closed beacon', async () => {
    answerPath('/beacons', () => refusal(409, 'SESSION_ALREADY_OPEN'));
    const { opening } = await beginOpen();

    await expect(opening).rejects.toMatchObject({ code: 'session_already_open' });
    // The session is another holder's: closing it would end it for them.
    for (let i = 0; i < 3; i++) await flush();
    expect(beacons()).toEqual(['opened']);
  });

  it('treats a replayed open of a managed session as managed', async () => {
    openExtra = { idempotent: true, state: 'open' };
    await openReady(await createClient(), { idempotencyKey: 'k1' });

    await vi.waitFor(() => expect(beacons()).toEqual(['opened']));
    expect(latest().publish).not.toHaveBeenCalled();
  });

  it.each([401, 403, 404])(
    'fails an open whose opened beacon answers %i as revoked',
    async (status) => {
      answerPath('/beacons', () => json(status, { error: 'no' }));
      const { opening } = await beginOpen();

      const error = await opening.catch((e: unknown) => e);

      expect(error).toBeInstanceOf(SessionFailedError);
      expect(error).toMatchObject({ code: 'session_access_revoked' });
    },
  );
});

describe('A managed session that is closed while it opens', () => {
  it('ends canceled, not failed, when the opened beacon is refused after the close started', async () => {
    const opened = deferredResponse();
    const closed = deferredResponse();
    answerPath('/beacons', (call) =>
      call.body?.beacon === 'opened' ? opened.promise : closed.promise,
    );
    const client = await createClient();
    const failure = client.openSession('connector').catch((e: unknown) => e);
    await flush();
    connect();
    await vi.waitFor(() => expect(beacons()).toEqual(['opened']));

    client.destroy();
    await vi.waitFor(() => expect(beacons()).toEqual(['opened', 'closed']));
    opened.resolve(refusal(404, 'SESSION_NOT_FOUND'));
    await flush();
    closed.resolve(json(200, {}));

    expect(await failure).toMatchObject({ code: 'session_canceled' });
  });

  it('retries the closed beacon of a managed open that was abandoned before it answered', async () => {
    const open = deferredResponse();
    openAnswer = () => open.promise;
    answerPath('/beacons', (call) => {
      if (beacons().length === 1) transportError();
      return defaultAnswer(call);
    });
    const client = await createClient();
    const opening = client.openSession('connector');
    opening.catch(() => undefined);
    await vi.waitFor(() => expect(calls).toHaveLength(1));

    client.destroy();
    await expect(opening).rejects.toMatchObject({ code: 'session_canceled' });
    open.resolve(json(201, { ...SESSION, managed: true }));

    await vi.waitFor(() => expect(beacons()).toEqual(['closed', 'closed']));
  });
});

describe('A managed session send queue', () => {
  it('advances on a final 200 but never on a 202, which waits for the channel outcome', async () => {
    let posts = 0;
    answerPath('/exchanges', (call) => (++posts === 2 ? accepted(call) : defaultAnswer(call)));
    const session = await readySession();
    const [first, second, third] = ['a', 'b', 'c'].map((text) => session.send(text));
    await vi.waitFor(() => expect(exchangePosts()).toHaveLength(2));
    await flush();
    await flush();

    expect(exchangePosts().map(exchangeIdOf)).toEqual([first.exchangeId, second.exchangeId]);
    outcome(second.exchangeId);
    await vi.waitFor(() => expect(exchangePosts()).toHaveLength(3));

    expect(exchangePosts().map(exchangeIdOf)[2]).toBe(third.exchangeId);
  });

  it.each([
    ['SESSION_BUSY', () => refusal(409, 'SESSION_BUSY')],
    ['429', () => json(429, { error: 'slow down' })],
  ])('retries the same exchange after %s', async (_name, reply) => {
    answerFirst('/exchanges', reply);
    const { exchange } = await sentExchange();
    await flush();

    expect(exchangePosts().map(exchangeIdOf)).toEqual([exchange.exchangeId, exchange.exchangeId]);
  });

  it('gives up on a stalled exchange request after 30 s, settles it publish_failed and moves on', async () => {
    vi.useFakeTimers();
    answerFirst('/exchanges', stalledRequest);
    const session = await readySession();
    const stalled = session.send('a');
    const next = session.send('b');
    await flush();
    await vi.advanceTimersByTimeAsync(29_000);
    expect(exchangePosts()).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(1_000);

    await expectOutcome(stalled, { state: 'failed', error: 'publish_failed' }, 1);
    expect(exchangePosts().map(exchangeIdOf)).toEqual([stalled.exchangeId, next.exchangeId]);
    expect(session.state).toBe('ready');
  });

  it('gives up on an exchange request stalled in its auth refresh after 30 s', async () => {
    vi.useFakeTimers();
    onAuthFailure = () => new Promise(() => {});
    answerFirst('/exchanges', () => json(401, { error: 'expired' }));
    const session = await readySession();
    const stalled = session.send('a');
    const next = session.send('b');
    await flush();

    await vi.advanceTimersByTimeAsync(30_000);

    await expectOutcome(stalled, { state: 'failed', error: 'publish_failed' }, 1);
    expect(exchangePosts().map(exchangeIdOf)).toEqual([stalled.exchangeId, next.exchangeId]);
  });

  it('gives up on a cancel stalled in its auth refresh after 30 s', async () => {
    vi.useFakeTimers();
    onAuthFailure = () => new Promise(() => {});
    answer = (call) =>
      call.path.endsWith('/cancel') ? json(401, { error: 'expired' }) : defaultAnswer(call);
    const { exchange } = await sentExchange();
    let settled = false;
    void exchange.cancel().then(() => (settled = true));
    await flush();
    expect(settled).toBe(false);

    await vi.advanceTimersByTimeAsync(30_000);

    expect(settled).toBe(true);
  });

  it('cancels an exchange locally that was never sent, or is waiting out SESSION_BUSY', async () => {
    // Keeps the first POST busy, and its retry pending, while the cancels run.
    randomSpy.mockReturnValue(0.99);
    answerPath('/exchanges', () => refusal(409, 'SESSION_BUSY'));
    const session = await readySession();
    const busy = session.send('a');
    const queued = session.send('b');
    await vi.waitFor(() => expect(exchangePosts()).toHaveLength(1));
    await flush();

    await queued.cancel();
    await busy.cancel();

    await expectOutcome(queued, { state: 'canceled' });
    await expectOutcome(busy, { state: 'canceled' });
    await new Promise((r) => setTimeout(r, 300));
    expect(exchangePosts().map(exchangeIdOf)).toEqual([busy.exchangeId]);
    expect(cancelPosts()).toHaveLength(0);
  });

  it('sends a cancel again after a 404 while the exchange request is still unanswered', async () => {
    const post = deferredResponse();
    answer = (call) => {
      if (call.path.endsWith('/cancel')) {
        return cancelPosts().length < 3 ? refusal(404, 'EXCHANGE_NOT_FOUND') : accepted(call);
      }
      return call.path.endsWith('/exchanges') ? post.promise : defaultAnswer(call);
    };
    const { exchange } = await sentExchange();

    await exchange.cancel();

    expect(cancelPosts()).toHaveLength(3);
    post.resolve(json(200, { exchangeId: exchange.exchangeId, state: 'canceled' }));
  });

  it('sends a cancel again once the exchange request answers that it is running', async () => {
    const post = deferredResponse();
    const firstCancel = deferredResponse();
    answer = (call) => {
      if (call.path.endsWith('/cancel')) {
        return cancelPosts().length === 1 ? firstCancel.promise : accepted(call);
      }
      return call.path.endsWith('/exchanges') ? post.promise : defaultAnswer(call);
    };
    const { exchange } = await sentExchange();

    const canceling = exchange.cancel();
    await flush();
    post.resolve(accepted(exchangePosts()[0]));
    await flush();
    firstCancel.resolve(refusal(404, 'EXCHANGE_NOT_FOUND'));
    await canceling;

    expect(cancelPosts()).toHaveLength(2);
  });

  it('cancels locally an exchange whose request answers SESSION_BUSY after a 404 cancel', async () => {
    vi.useFakeTimers();
    randomSpy.mockReturnValue(0.99);
    const post = deferredResponse();
    const firstCancel = deferredResponse();
    answer = (call) => {
      if (call.path.endsWith('/cancel')) return firstCancel.promise;
      return call.path.endsWith('/exchanges') ? post.promise : defaultAnswer(call);
    };
    const { exchange } = await sentExchange();

    const canceling = exchange.cancel();
    await flush();
    firstCancel.resolve(refusal(404, 'EXCHANGE_NOT_FOUND'));
    await flush();
    randomSpy.mockReturnValue(0.01);
    post.resolve(refusal(409, 'SESSION_BUSY'));
    await flush();
    await vi.advanceTimersByTimeAsync(1_000);
    await canceling;

    await expectOutcome(exchange, { state: 'canceled' }, 1);
    expect(exchangePosts()).toHaveLength(1);
  });

  it('gives up quietly on a 404 cancel when nothing of the exchange is in flight', async () => {
    answer = (call) => {
      if (call.path.endsWith('/cancel')) return refusal(404, 'EXCHANGE_NOT_FOUND');
      return call.path.endsWith('/exchanges') ? accepted(call) : defaultAnswer(call);
    };
    const { exchange } = await sentExchange();

    await expect(exchange.cancel()).resolves.toBeUndefined();

    expect(cancelPosts()).toHaveLength(1);
  });

  it.each([401, 403, 404])('fails the session when an exchange POST answers %i', async (status) => {
    answerPath('/exchanges', () => json(status, { error: 'no' }));
    const session = await readySession();
    const exchange = session.send('a');

    await expectOutcome(exchange, { state: 'failed', error: 'session_access_revoked' });
    expect(session.state).toBe('failed');
  });

  it.each([
    ['400', () => json(400, { error: 'bad' })],
    ['409 SESSION_NOT_READY', () => refusal(409, 'SESSION_NOT_READY')],
  ])('settles only that exchange publish_failed when its POST answers %s', async (_name, reply) => {
    answerFirst('/exchanges', reply);
    const session = await readySession();
    const rejected = session.send('a');
    const next = session.send('b');
    await flush();

    await expectOutcome(rejected, { state: 'failed', error: 'publish_failed' });
    expect(session.state).toBe('ready');
    expect(exchangePosts().map(exchangeIdOf)).toEqual([rejected.exchangeId, next.exchangeId]);
  });

  it('asks for recovery when a final answer is not followed by its outcome within 750 ms', async () => {
    vi.useFakeTimers();
    await sentExchange();
    await flush();
    expect(exchangePosts()).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(749);
    expect(snapshotReads()).toBe(0);
    await vi.advanceTimersByTimeAsync(1);
    expect(snapshotReads()).toBe(1);
  });

  it('does not recover when the outcome arrives inside the window', async () => {
    vi.useFakeTimers();
    const { exchange } = await sentExchange();

    outcome(exchange.exchangeId);
    await vi.advanceTimersByTimeAsync(2_000);

    expect(snapshotReads()).toBe(0);
  });
});

describe('Closing a managed session', () => {
  it('tries the closed beacon at most three times, then still ends the session', async () => {
    answerPath('/beacons', (call) => {
      if (call.body?.beacon === 'closed') transportError();
      return defaultAnswer(call);
    });
    const { session, exchange } = await sentExchange();

    await session.close();

    expect(beacons().filter((b) => b === 'closed')).toHaveLength(3);
    expect(session.state).toBe('closed');
    await expectOutcome(exchange, { state: 'canceled' });
  });
});

describe('exchange.stream()', () => {
  const started = (exchangeId: string, overrides: Record<string, unknown> = {}) =>
    fromProvider({
      type: 'stream_started',
      exchangeId,
      streamId: 'st1',
      kind: 'foreign_subscribe',
      channel: 'live.eu',
      templateParam: 'show',
      templateValue: 'eu',
      mode: 'access_manager',
      ...overrides,
    });

  const message = (seq: number): Message => ({
    channel: 'live.eu',
    timetoken: String(seq),
    message: { seq },
    messageType: -1,
  });

  /** A stream client whose buffer is the real queue, so iteration through it is the real one. */
  function fakeStream(connected = true) {
    const stop = vi.fn(async () => undefined);
    const queue = new StreamQueue(stop);
    const stream = {
      queue,
      stop,
      subscription: streamSubscription('st1', queue, stop),
      connected,
      end: vi.fn(async (error?: Error) => queue.close(error)),
    };
    startForeignStream.mockReturnValue(stream);
    return stream;
  }

  const lastOptions = () => startForeignStream.mock.calls[0][0];

  const withCredentialsInAnswer = () =>
    answerPath('/exchanges', (call) =>
      json(200, { exchangeId: exchangeIdOf(call), state: 'completed', stream: CREDENTIALS }),
    );

  /** A 202 that carries no credentials, so they must come from the token endpoint. */
  const withoutCredentialsInAnswer = (tokenAnswer: Reply) => {
    answer = (call) => {
      if (call.path.endsWith('/exchanges')) return accepted(call);
      if (call.path.endsWith('/streams/st1/token')) return tokenAnswer(call);
      return defaultAnswer(call);
    };
  };

  const startedAndSettled = (exchangeId: string, overrides?: Record<string, unknown>) => {
    started(exchangeId, overrides);
    outcome(exchangeId);
  };

  /** Sends an exchange and, once its POST went out, answers with `stream_started` and the outcome. */
  async function sendStreaming(session: Session): Promise<Exchange> {
    const exchange = session.send([{ type: 'data', data: { show: 'eu' } }]);
    await vi.waitFor(() => expect(exchangePosts().length).toBeGreaterThan(0));
    startedAndSettled(exchange.exchangeId);
    return exchange;
  }

  async function streamingExchange(session: Session): Promise<Exchange> {
    withCredentialsInAnswer();
    const exchange = await sendStreaming(session);
    await vi.waitFor(() => expect(startForeignStream).toHaveBeenCalled());
    return exchange;
  }

  /** An exchange whose POST went out and whose stream the test starts or settles itself. */
  async function postedExchange(session: Session) {
    const exchange = session.send('input');
    await vi.waitFor(() => expect(exchangePosts()).toHaveLength(1));
    return exchange;
  }

  const collect = async (stream: AsyncIterable<Message>) => {
    const received: Message[] = [];
    for await (const m of stream) received.push(m);
    return received;
  };
  const collectError = (stream: AsyncIterable<Message>) => collect(stream).catch((e: unknown) => e);
  const expectStreamError = (error: unknown, code: string, state = 'failed') => {
    expect(error).toBeInstanceOf(SessionStreamError);
    expect(error).toMatchObject({ code, state });
  };
  const expectSessionFailure = (error: unknown, code: string) => {
    expect(error).toBeInstanceOf(SessionFailedError);
    expect(error).toMatchObject({ code });
  };
  it('starts the subscriber from the answer credentials and the template value before anyone iterates', async () => {
    fakeStream();
    await streamingExchange(await readySession());

    expect(exchangePosts()[0].body).not.toHaveProperty('expect');
    expect(lastOptions()).toMatchObject({
      sessionId: 'ses1',
      credentials: CREDENTIALS,
      templateParam: 'show',
      templateValue: 'eu',
    });
  });

  it('hands over what the subscriber buffered before the first iteration, then ends when the session closes', async () => {
    const stream = fakeStream();
    const session = await readySession();
    const exchange = await streamingExchange(session);
    stream.queue.push(message(1));
    stream.queue.push(message(2));

    const received = collect(exchange.stream());
    await session.close();

    await expect(received).resolves.toEqual([message(1), message(2)]);
    expect(stream.end).toHaveBeenCalledWith(undefined);
  });

  it('can be called before the stream starts', async () => {
    const stream = fakeStream();
    withCredentialsInAnswer();
    const session = await readySession();
    const exchange = session.send('input');
    const received = collect(exchange.stream());
    await vi.waitFor(() => expect(exchangePosts()).toHaveLength(1));

    startedAndSettled(exchange.exchangeId);
    await vi.waitFor(() => expect(startForeignStream).toHaveBeenCalled());
    stream.queue.push(message(1));
    await session.close();

    await expect(received).resolves.toEqual([message(1)]);
  });

  it('stops the stream client, not the session, when the loop is left', async () => {
    const stream = fakeStream();
    const session = await readySession();
    const exchange = await streamingExchange(session);
    stream.queue.push(message(1));

    for await (const _ of exchange.stream()) break;

    expect(stream.stop).toHaveBeenCalledTimes(1);
    expect(session.state).toBe('ready');
  });

  it('stops the stream client when the iterator is returned while a read waits on a quiet stream', async () => {
    const stream = fakeStream();
    const exchange = await streamingExchange(await readySession());
    const iterator = exchange.stream()[Symbol.asyncIterator]();
    void iterator.next();
    await flush();

    void iterator.return?.();

    await vi.waitFor(() => expect(stream.stop).toHaveBeenCalledTimes(1));
  });

  it('stops a started stream client when the iterator is returned before any read', async () => {
    const stream = fakeStream();
    const exchange = await streamingExchange(await readySession());

    await exchange.stream()[Symbol.asyncIterator]().return?.();

    expect(stream.stop).toHaveBeenCalledTimes(1);
  });

  it('starts no stream client for an iterator returned while the first read awaits the stream', async () => {
    fakeStream();
    withCredentialsInAnswer();
    const exchange = (await readySession()).send('input');
    const iterator = exchange.stream()[Symbol.asyncIterator]();
    const first = iterator.next();
    await vi.waitFor(() => expect(exchangePosts()).toHaveLength(1));

    await iterator.return?.();

    await expect(first).resolves.toEqual({ done: true, value: undefined });
    startedAndSettled(exchange.exchangeId);
    await flush();
    expect(startForeignStream).not.toHaveBeenCalled();
  });

  it('throws on a second stream() call for the same exchange', async () => {
    fakeStream();
    const exchange = await streamingExchange(await readySession());
    exchange.stream();

    expect(() => exchange.stream()).toThrow(TypeError);
  });

  it('ends empty on an exchange that completed without a stream, asking no token endpoint', async () => {
    const session = await readySession();
    const exchange = await postedExchange(session);
    outcome(exchange.exchangeId);

    await expect(collect(exchange.stream())).resolves.toEqual([]);

    expect(startForeignStream).not.toHaveBeenCalled();
    expect(callsTo('/token')).toHaveLength(0);
  });

  it('ends empty, within the window the answer needs, when neither it nor stream_started brings a stream', async () => {
    vi.useFakeTimers();
    answerPath('/exchanges', () => new Promise<Response>(() => undefined));
    const session = await readySession();
    const exchange = await postedExchange(session);
    const ended = collect(exchange.stream());
    outcome(exchange.exchangeId);

    await vi.advanceTimersByTimeAsync(750);

    await expect(ended).resolves.toEqual([]);
    expect(startForeignStream).not.toHaveBeenCalled();
  });

  it.each([
    [
      'the reason',
      'failed',
      { error: 'handler_error', reason: 'CONNECTOR_STREAM_ALREADY_OPEN' },
      'CONNECTOR_STREAM_ALREADY_OPEN',
    ],
    ['the error when there is no reason', 'failed', { error: 'handler_error' }, 'handler_error'],
    ['the state when there is neither', 'canceled', {}, 'canceled'],
  ])(
    'throws SessionStreamError with %s when the exchange settles without a stream',
    async (_name, state, fields, code) => {
      const exchange = await postedExchange(await readySession());
      outcome(exchange.exchangeId, state, fields);

      expectStreamError(await collectError(exchange.stream()), code, state);
      expect(startForeignStream).not.toHaveBeenCalled();
    },
  );

  it('throws stream_kind_unsupported for a stream of another kind on a managed session, starting no client', async () => {
    withCredentialsInAnswer();
    const exchange = await postedExchange(await readySession());
    startedAndSettled(exchange.exchangeId, { kind: 'stream' });

    expectStreamError(await collectError(exchange.stream()), 'stream_kind_unsupported');
    expect(startForeignStream).not.toHaveBeenCalled();
  });

  it.each([
    ['of a kind this SDK does not consume', { kind: 'stream' }],
    ['with no kind, as an ordinary session stream is described', { kind: undefined }],
    ['of the kind a Connector starts, which has no token endpoint here', {}],
  ])(
    'throws stream_kind_unsupported for a stream_started %s on a session that is not managed, asking no token endpoint',
    async (_name, overrides) => {
      managed = false;
      const exchange = (await readySession()).send('input');
      startedAndSettled(exchange.exchangeId, overrides);

      expectStreamError(await collectError(exchange.stream()), 'stream_kind_unsupported');
      expect(callsTo('/token')).toHaveLength(0);
      expect(startForeignStream).not.toHaveBeenCalled();
    },
  );

  it('does not ask the token endpoint when the exchange answer carries the credentials, even after stream_started', async () => {
    fakeStream();
    const post = deferredResponse();
    answerPath('/exchanges', () => post.promise);
    const exchange = await postedExchange(await readySession());
    const { exchangeId } = exchange;

    startedAndSettled(exchangeId);
    await flush();
    post.resolve(json(200, { exchangeId, state: 'completed', stream: CREDENTIALS }));
    await vi.waitFor(() => expect(startForeignStream).toHaveBeenCalled());

    expect(callsTo('/token')).toHaveLength(0);
  });

  it('recovers credentials from the token endpoint when the exchange answer carried none', async () => {
    vi.useFakeTimers();
    fakeStream();
    let tokenCalls = 0;
    withoutCredentialsInAnswer(() =>
      ++tokenCalls === 1 ? refusal(404, 'STREAM_NOT_FOUND') : json(200, CREDENTIALS),
    );
    await sendStreaming(await readySession());

    await vi.advanceTimersByTimeAsync(1_750);

    expect(callsTo('/streams/st1/token')).toHaveLength(2);
    expect(lastOptions().credentials).toMatchObject({ streamId: 'st1', token: 'stream-token' });
  });

  it('does not start a subscriber when the session ends right after the outcome', async () => {
    fakeStream();
    withCredentialsInAnswer();
    const session = await readySession();
    const exchange = await postedExchange(session);
    const failure = collectError(exchange.stream());
    // The answer, and with it the credentials, is in before the channel messages arrive.
    await flush();
    await flush();

    startedAndSettled(exchange.exchangeId);
    fromBackend({ type: 'session_failed', error: 'exposure_revoked' });

    expectSessionFailure(await failure, 'exposure_revoked');
    expect(startForeignStream).not.toHaveBeenCalled();
  });

  it('throws the session error when the session fails while the token fetch retries', async () => {
    vi.useFakeTimers();
    withoutCredentialsInAnswer(() => refusal(404, 'STREAM_NOT_FOUND'));
    const exchange = await postedExchange(await readySession());
    const failure = collectError(exchange.stream());
    startedAndSettled(exchange.exchangeId);
    await vi.waitFor(() => expect(callsTo('/streams/st1/token').length).toBeGreaterThan(0));

    fromBackend({ type: 'session_failed', error: 'exposure_revoked' });
    await vi.advanceTimersByTimeAsync(0);

    expectSessionFailure(await failure, 'exposure_revoked');
  });

  it('gives up on a stalled token fetch within its budget, throwing stream_credentials_unavailable', async () => {
    vi.useFakeTimers();
    withoutCredentialsInAnswer(stalledRequest);
    const session = await readySession();
    const exchange = await postedExchange(session);
    const failure = collectError(exchange.stream());
    startedAndSettled(exchange.exchangeId);

    await vi.advanceTimersByTimeAsync(10_000 + 1);

    expectStreamError(await failure, 'stream_credentials_unavailable');
    expect(session.state).toBe('ready');
  });

  it('gives up on a stream token refresh stalled in its auth refresh within its budget', async () => {
    vi.useFakeTimers();
    fakeStream();
    await streamingExchange(await readySession());
    onAuthFailure = () => new Promise(() => {});
    answerPath('/streams/st1/token', () => json(401, { error: 'expired' }));
    let settled = false;
    void lastOptions()
      .refresh()
      .catch(() => undefined)
      .then(() => (settled = true));

    await vi.advanceTimersByTimeAsync(10_000 + 1);

    expect(settled).toBe(true);
  });

  it('still renews the lease when the stream connection flaps between ticks', async () => {
    vi.useFakeTimers();
    fakeStream();
    await streamingExchange(await readySession());
    lastOptions().onConnectionChange(true);

    for (let minute = 0; minute < 11; minute++) {
      await vi.advanceTimersByTimeAsync(20_000);
      lastOptions().onConnectionChange(false);
      await vi.advanceTimersByTimeAsync(10_000);
      lastOptions().onConnectionChange(true);
      await vi.advanceTimersByTimeAsync(30_000);
    }

    expect(beacons().filter((b) => b === 'alive')).toHaveLength(2);
  });

  it('renews the lease with alive beacons only while the stream client is connected', async () => {
    vi.useFakeTimers();
    fakeStream(false);
    const session = await readySession();
    await streamingExchange(session);
    const alive = () => beacons().filter((b) => b === 'alive').length;
    const minutes = (n: number) => vi.advanceTimersByTimeAsync(n * 60_000);

    await minutes(10);
    expect(alive()).toBe(0);

    lastOptions().onConnectionChange(true);
    await minutes(5);
    expect(alive()).toBe(1);

    lastOptions().onConnectionChange(false);
    await minutes(15);
    expect(alive()).toBe(1);

    lastOptions().onConnectionChange(true);
    await minutes(5);
    expect(alive()).toBe(2);

    void session.close();
    await minutes(15);
    expect(alive()).toBe(2);
  });

  it('stops renewing the lease once the stream is stopped', async () => {
    vi.useFakeTimers();
    fakeStream();
    await streamingExchange(await readySession());
    lastOptions().onConnectionChange(true);

    lastOptions().onEnded('stopped');
    await vi.advanceTimersByTimeAsync(15 * 60_000);

    expect(beacons().filter((b) => b === 'alive')).toHaveLength(0);
  });

  it('throws an access error, then closes the session, when the stream loses access', async () => {
    const stream = fakeStream();
    const session = await readySession();
    const exchange = await streamingExchange(session);
    const failure = collectError(exchange.stream());

    lastOptions().onEnded('access_revoked');

    // Ended with the cause before the close, so iteration throws it instead of completing.
    expect(stream.end).toHaveBeenCalledTimes(1);
    expectSessionFailure(await failure, 'session_access_revoked');
    await vi.waitFor(() => expect(session.state).toBe('closed'));
    expect(beacons()).toContain('closed');
  });

  it('fails the stream with the session error when the session fails', async () => {
    const stream = fakeStream();
    const exchange = await streamingExchange(await readySession());
    const failure = collectError(exchange.stream());

    fromBackend({ type: 'session_failed', error: 'exposure_revoked' });

    expectSessionFailure(await failure, 'exposure_revoked');
    expect(stream.end).toHaveBeenCalledTimes(1);
  });
});
