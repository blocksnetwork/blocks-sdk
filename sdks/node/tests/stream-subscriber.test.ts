import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SessionHttpError } from '../src/runtime/sessions/session-http.js';
import { startForeignStream } from '../src/runtime/sessions/stream-subscriber.js';
import type {
  ForeignStream,
  Message,
  StreamCredentials,
} from '../src/runtime/sessions/stream-types.js';

interface FakeListener {
  message?: (event: unknown) => void;
  file?: (event: unknown) => void;
  status?: (event: unknown) => void;
}

const { clients, createFakeClient } = vi.hoisted(() => {
  const clients: ReturnType<typeof createFakeClient>[] = [];
  function createFakeClient(config: Record<string, unknown>) {
    const listeners: FakeListener[] = [];
    const emit = (kind: keyof FakeListener, event: unknown) => {
      for (const l of [...listeners]) l[kind]?.(event);
    };
    const client = {
      config,
      addListener: vi.fn((l: FakeListener) => listeners.push(l)),
      removeListener: vi.fn((l: FakeListener) => {
        const i = listeners.indexOf(l);
        if (i >= 0) listeners.splice(i, 1);
      }),
      setToken: vi.fn(),
      subscribe: vi.fn(),
      reconnect: vi.fn(),
      unsubscribeAll: vi.fn(),
      destroy: vi.fn(),
      status: (category: string) => emit('status', { category }),
      message: (event: Record<string, unknown>) => emit('message', event),
      file: (event: Record<string, unknown>) => emit('file', event),
    };
    clients.push(client);
    return client;
  }
  return { clients, createFakeClient };
});

vi.mock('pubnub', () => ({ default: vi.fn(createFakeClient) }));

const MINUTE = 60_000;

function credentials(overrides: Record<string, unknown> = {}): StreamCredentials {
  return {
    streamId: 'str1',
    subscribeKey: 'sub-foreign',
    origin: null,
    channel: 'live.eu',
    mode: 'access_manager',
    token: 'tok-1',
    ttlMinutes: 10,
    expiresAt: '2030-01-01T00:00:00.000Z',
    ...overrides,
  } as StreamCredentials;
}

const wildcardCredentials = () => credentials({ channel: undefined, wildcard: 'live.*' });

const consentCredentials = () =>
  credentials({ mode: 'consent', token: undefined, ttlMinutes: undefined, expiresAt: undefined });

function start(
  creds: StreamCredentials = credentials(),
  template: { templateParam?: string; templateValue?: string } = {},
) {
  const refresh = vi.fn<() => Promise<StreamCredentials>>();
  refresh.mockResolvedValue(credentials({ token: 'tok-2' }));
  const onConnectionChange = vi.fn();
  const onEnded = vi.fn();
  const options = { sessionId: 'ses1', credentials: creds, ...template, refresh };
  const stream = startForeignStream({ ...options, onConnectionChange, onEnded });
  return { stream, refresh, onConnectionChange, onEnded, client: clients[clients.length - 1]! };
}

async function collect(stream: ForeignStream): Promise<Message[]> {
  const out: Message[] = [];
  for await (const m of stream.subscription) out.push(m);
  return out;
}

/** Ends the stream, then reads what it still buffered. */
async function drain({ stream }: { stream: ForeignStream }): Promise<Message[]> {
  await stream.end();
  return collect(stream);
}

const event = (channel: string, extra: Record<string, unknown> = {}) => ({
  channel,
  subscription: channel,
  timetoken: '17000000000000001',
  publisher: 'pub-1',
  message: { text: 'hi' },
  ...extra,
});

const feed = (client: { message(e: Record<string, unknown>): void }, ...channels: string[]) =>
  channels.forEach((channel) => client.message(event(channel)));

beforeEach(() => {
  clients.length = 0;
});

describe('startForeignStream client', () => {
  it('builds a subscribe-only client with a random userId and no presence settings', () => {
    const a = start();
    const b = start();
    for (const { client } of [a, b]) {
      expect(client.config).toMatchObject({
        subscribeKey: 'sub-foreign',
        enableEventEngine: true,
        userId: expect.any(String),
      });
      for (const key of ['publishKey', 'origin', 'presenceTimeout', 'heartbeatInterval']) {
        expect(client.config).not.toHaveProperty(key);
      }
    }
    expect(a.client.config.userId).not.toBe(b.client.config.userId);
  });

  it('passes the origin when the credentials carry one', () => {
    const { client } = start(credentials({ origin: 'ps-eu.example.net' }));
    expect(client.config.origin).toBe('ps-eu.example.net');
  });

  it('applies the token before it subscribes to the channel, without a history cursor or presence', () => {
    const { client } = start();
    expect(client.setToken).toHaveBeenCalledWith('tok-1');
    expect(client.subscribe).toHaveBeenCalledWith({ channels: ['live.eu'] });
    expect(client.setToken.mock.invocationCallOrder[0]).toBeLessThan(
      client.subscribe.mock.invocationCallOrder[0]!,
    );
  });

  it('subscribes to the wildcard of an all-values stream', () => {
    const { client } = start(wildcardCredentials());
    expect(client.subscribe).toHaveBeenCalledWith({ channels: ['live.*'] });
  });
});

describe('startForeignStream token rotation', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('refreshes at 80 % of ttlMinutes and applies the token without resubscribing', async () => {
    const { client, refresh } = start(credentials({ ttlMinutes: 10 }));
    await vi.advanceTimersByTimeAsync(8 * MINUTE - 1);
    expect(refresh).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(client.setToken).toHaveBeenLastCalledWith('tok-2');
    expect(client.subscribe).toHaveBeenCalledTimes(1);
    expect(client.reconnect).not.toHaveBeenCalled();
  });

  it('refreshes once on access_denied, applies the token and reconnects', async () => {
    const { client, refresh, onEnded } = start();
    client.status('PNAccessDeniedCategory');
    client.status('PNAccessDeniedCategory');
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(client.setToken).toHaveBeenLastCalledWith('tok-2');
    expect(client.reconnect).toHaveBeenCalledTimes(1);
    expect(onEnded).not.toHaveBeenCalled();
  });

  it('ends with access_revoked on a second denial before it reconnects', async () => {
    const { client, onEnded } = start();
    client.status('PNAccessDeniedCategory');
    await vi.advanceTimersByTimeAsync(0);
    client.status('PNAccessDeniedCategory');
    expect(onEnded.mock.calls).toEqual([['access_revoked']]);
    expect(client.destroy).toHaveBeenCalledTimes(1);
  });

  it('allows a new denial refresh once the subscription reconnected', async () => {
    const { client, refresh, onEnded } = start();
    client.status('PNAccessDeniedCategory');
    await vi.advanceTimersByTimeAsync(0);
    client.status('PNReconnectedCategory');
    client.status('PNAccessDeniedCategory');
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(onEnded).not.toHaveBeenCalled();
  });

  it('ends with access_revoked when the refresh after a denial is refused', async () => {
    const { client, refresh, onEnded } = start();
    refresh.mockRejectedValue(new SessionHttpError(409, 'EXPOSURE_REVOKED'));
    client.status('PNAccessDeniedCategory');
    await vi.advanceTimersByTimeAsync(0);
    expect(onEnded.mock.calls).toEqual([['access_revoked']]);
    expect(client.reconnect).not.toHaveBeenCalled();
    expect(client.unsubscribeAll).toHaveBeenCalledTimes(1);
  });

  it('ends with access_revoked when the refresh scheduled at 80 % is refused', async () => {
    const { client, refresh, onEnded } = start(credentials({ ttlMinutes: 10 }));
    refresh.mockRejectedValue(new SessionHttpError(409, 'EXPOSURE_REVOKED'));
    await vi.advanceTimersByTimeAsync(8 * MINUTE);
    expect(onEnded.mock.calls).toEqual([['access_revoked']]);
    expect(client.destroy).toHaveBeenCalledTimes(1);
  });

  it('keeps the stream when the refresh scheduled at 80 % fails in transit, and retries it', async () => {
    const { refresh, onEnded } = start(credentials({ ttlMinutes: 10 }));
    refresh.mockRejectedValueOnce(new SessionHttpError(502, 'CONNECTOR_UPSTREAM_FAILED'));
    await vi.advanceTimersByTimeAsync(8 * MINUTE + 10_000);
    expect(refresh).toHaveBeenCalledTimes(2);
    expect(onEnded).not.toHaveBeenCalled();
  });

  it('treats a refresh answer without a token as a failed refresh', async () => {
    const { client, refresh, onEnded } = start();
    refresh.mockResolvedValue(consentCredentials());
    client.status('PNAccessDeniedCategory');
    await vi.advanceTimersByTimeAsync(0);
    expect(onEnded.mock.calls).toEqual([['access_revoked']]);
    expect(client.setToken).toHaveBeenCalledTimes(1);
  });

  it('ends a consent stream with access_revoked on access_denied', () => {
    const { client, refresh, onEnded } = start(consentCredentials());
    client.status('PNAccessDeniedCategory');
    expect(onEnded.mock.calls).toEqual([['access_revoked']]);
    expect(refresh).not.toHaveBeenCalled();
    expect(client.destroy).toHaveBeenCalledTimes(1);
  });

  it('neither sets a token nor rotates in consent mode', async () => {
    const { client, refresh } = start(consentCredentials());
    await vi.advanceTimersByTimeAsync(24 * 60 * MINUTE);
    expect(client.setToken).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
    expect(client.subscribe).toHaveBeenCalledWith({ channels: ['live.eu'] });
  });
});

describe('startForeignStream message delivery', () => {
  it('derives the templateValue of a wildcard stream from event.channel, not event.subscription', async () => {
    const { stream, client } = start(wildcardCredentials(), { templateParam: 'show', templateValue: 'ignored' });
    client.message({ ...event('live.eu'), subscription: 'live.*' });
    client.message({ ...event('live.us.west'), subscription: 'live.*' });
    const messages = await drain({ stream });
    expect(messages.map((m) => [m.channel, m.templateValue])).toEqual([
      ['live.eu', 'eu'],
      ['live.us.west', 'us.west'],
    ]);
  });

  it('gives a raw wildcard stream no templateValue', async () => {
    const raw = start(wildcardCredentials());
    feed(raw.client, 'live.eu');
    expect(await drain(raw)).toEqual([
      expect.not.objectContaining({ templateValue: expect.anything() }),
    ]);
  });

  it('gives a one-channel stream the templateValue of stream_started, and none under raw access', async () => {
    const withValue = start(credentials(), { templateParam: 'show', templateValue: 'eu' });
    feed(withValue.client, 'live.eu');
    const raw = start(credentials());
    feed(raw.client, 'live.eu');
    expect((await drain(withValue))[0]?.templateValue).toBe('eu');
    expect(await drain(raw)).toEqual([
      expect.not.objectContaining({ templateValue: expect.anything() }),
    ]);
  });

  it('drops presence channels and channels outside the address', async () => {
    const wild = start(wildcardCredentials());
    feed(wild.client, 'live.eu-pnpres', 'other.eu', 'live.', 'live.us');
    const one = start(credentials());
    feed(one.client, 'live.eu-pnpres', 'live.us', 'live.eu');
    expect((await drain(wild)).map((m) => m.channel)).toEqual(['live.us']);
    expect((await drain(one)).map((m) => m.channel)).toEqual(['live.eu']);
  });

  it('maps a message event with messageType -1, its metadata and custom type', async () => {
    const started = start();
    started.client.message(
      event('live.eu', { userMetadata: { lang: 'en' }, customMessageType: 'chat', message: 'hi' }),
    );
    expect(await drain(started)).toEqual([
      {
        channel: 'live.eu',
        timetoken: '17000000000000001',
        publisher: 'pub-1',
        message: 'hi',
        meta: { lang: 'en' },
        messageType: -1,
        customMessageType: 'chat',
      },
    ]);
  });

  it('maps a file event with messageType 4 and the message plus file as its payload', async () => {
    const started = start();
    const file = { id: 'f1', name: 'a.png', url: 'https://files.example/f1' };
    started.client.file(event('live.eu', { message: { caption: 'c' }, file }));
    expect(await drain(started)).toEqual([
      expect.objectContaining({ messageType: 4, message: { message: { caption: 'c' }, file } }),
    ]);
  });
});

describe('startForeignStream connection', () => {
  it('reports each connect and disconnect transition once and exposes the state', () => {
    const { stream, client, onConnectionChange } = start();
    expect(stream.connected).toBe(false);
    client.status('PNConnectedCategory');
    client.status('PNConnectedCategory');
    expect(stream.connected).toBe(true);
    client.status('PNNetworkDownCategory');
    client.status('PNTimeoutCategory');
    expect(stream.connected).toBe(false);
    client.status('PNReconnectedCategory');
    client.status('PNAccessDeniedCategory');
    expect(onConnectionChange.mock.calls).toEqual([[true], [false], [true], [false]]);
  });
});

describe('startForeignStream ending', () => {
  it('stop() tears the client down, completes the iteration and reports stopped once', async () => {
    const { stream, client, onEnded } = start();
    const done = collect(stream);
    await stream.subscription.stop();
    await stream.subscription.stop();
    expect(await done).toEqual([]);
    expect(onEnded.mock.calls).toEqual([['stopped']]);
    expect(client.removeListener).toHaveBeenCalledTimes(1);
    expect(client.unsubscribeAll).toHaveBeenCalledTimes(1);
    expect(client.destroy).toHaveBeenCalledTimes(1);
  });

  it('breaking out of the iteration stops the stream', async () => {
    const { stream, client, onEnded } = start();
    feed(client, 'live.eu', 'live.eu');
    for await (const _ of stream.subscription) break;
    expect(onEnded.mock.calls).toEqual([['stopped']]);
    expect(client.destroy).toHaveBeenCalledTimes(1);
  });

  it('end() completes a waiting iteration after the buffered messages, without reporting an end', async () => {
    const { stream, client, onEnded } = start();
    client.message(event('live.eu', { message: 'a' }));
    const done = collect(stream);
    await stream.end();
    await stream.end();
    expect((await done).map((m) => m.message)).toEqual(['a']);
    expect(onEnded).not.toHaveBeenCalled();
    expect(client.destroy).toHaveBeenCalledTimes(1);
    await stream.subscription.stop();
    expect(onEnded).not.toHaveBeenCalled();
  });

  it('end(error) makes a waiting iteration throw that error', async () => {
    const { stream } = start();
    const failure = new Error('exposure_revoked');
    const done = collect(stream);
    await stream.end(failure);
    await expect(done).rejects.toBe(failure);
  });

  it('refuses a second iteration instead of leaving its first waiter hanging', async () => {
    const { stream } = start();
    const first = collect(stream);
    expect(() => stream.subscription[Symbol.asyncIterator]()).toThrow(TypeError);
    await stream.end();
    await expect(first).resolves.toEqual([]);
  });

  it('wakes every pending next() on end', async () => {
    const { stream } = start();
    const iterator = stream.subscription[Symbol.asyncIterator]();
    const pending = [iterator.next(), iterator.next()];
    await stream.end();
    await expect(Promise.all(pending)).resolves.toEqual(
      Array(2).fill({ done: true, value: undefined }),
    );
  });

  it('ignores events that arrive after it ended', async () => {
    const started = start();
    await started.stream.end();
    feed(started.client, 'live.eu');
    expect(await collect(started.stream)).toEqual([]);
  });

  it('keeps at most 1000 undelivered messages and drops the oldest', async () => {
    const started = start();
    for (let i = 0; i < 1005; i++) started.client.message(event('live.eu', { message: i }));
    const messages = await drain(started);
    expect(messages.map((m) => m.message)).toEqual(Array.from({ length: 1000 }, (_, i) => i + 5));
  });
});
