// The provider session runtime at its route boundary: HTTP and PubNub are faked.
import { afterEach, describe, expect, it, vi } from 'vitest';
import type PubNub from 'pubnub';
import { DEFAULTS } from '../src/defaults.js';
import { AgentAuth } from '../src/runtime/agent-auth.js';
import { CURRENT_PROTOCOL_VERSION } from '../src/runtime/protocol-version.js';
import { ProviderApiError, ProviderSessionApi } from '../src/runtime/sessions/provider-api.js';
import { CHECKPOINT_FLUSH_MS } from '../src/runtime/sessions/provider-checkpoints.js';
import { SessionProviderRuntime } from '../src/runtime/sessions/provider-route.js';
import {
  MAX_PENDING_EXCHANGES,
  MAX_SETTLED_EXCHANGE_IDS,
  SESSION_OPEN_TIMEOUT_MS,
} from '../src/runtime/sessions/provider-session.js';
import { MAX_SESSION_READY_TIMEOUT_MS } from '../src/runtime/sessions/session.js';
import type { SessionProviderOptions } from '../src/runtime/sessions/provider-types.js';
import {
  BACKEND_UUID,
  CONSUMER_UUID,
  type FakeRouteClient,
  fakeRouteClient,
  fromConsumer,
  manifest,
  ROUTE_CHANNEL,
  routeGrant,
  sessionChannel,
  textParts,
} from './helpers/session-provider-fakes.js';

const SESSION = 's1';
const CHANNEL = sessionChannel(SESSION);
const PROBE_CHANNEL = 'sess.route1.probe';

function deferred<T = void>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

function fakeApi() {
  return {
    registerRoute: vi.fn(async () => routeGrant()),
    refreshRouteToken: vi.fn(),
    getSession: vi.fn(async (sessionId: string) => manifest(sessionId)),
    postCheckpoint: vi.fn(async () => {}),
  };
}

type Handlers = SessionProviderOptions & Required<Pick<SessionProviderOptions, 'onExchange'>>;

const runtimes: SessionProviderRuntime[] = [];
afterEach(() => {
  for (const r of runtimes.splice(0)) r.stop();
  vi.useRealTimers();
});

async function startRuntime(
  handlers: Handlers,
  opts: { maxSessions?: number; api?: ReturnType<typeof fakeApi> } = {},
) {
  const api = opts.api ?? fakeApi();
  const clients: FakeRouteClient[] = [];
  const runtime = new SessionProviderRuntime({
    api: api as unknown as ProviderSessionApi,
    instanceId: 'AG-echo_agent-1',
    maxSessions: opts.maxSessions ?? 4,
    handlers,
    createClient: (userId) => {
      const client = fakeRouteClient(userId);
      clients.push(client);
      return client as unknown as PubNub;
    },
  });
  runtimes.push(runtime);
  await runtime.start();
  const client = () => clients[clients.length - 1];
  const open = (exchange?: { exchangeId: string; requestParts: unknown[] }) =>
    client().deliver(
      CHANNEL,
      CONSUMER_UUID,
      fromConsumer(SESSION, 1, 'session_open', exchange ? { exchange } : {}),
    );
  return { api, runtime, clients, client, open };
}

const exchangeMsg = (seq: number, exchangeId: string, text = exchangeId) =>
  fromConsumer(SESSION, seq, 'exchange', { exchangeId, requestParts: textParts(text) });

const checkpointedEvents = (api: ReturnType<typeof fakeApi>) =>
  (api.postCheckpoint.mock.calls as unknown as [{ events: Record<string, unknown>[] }][]).flatMap(
    ([batch]) => batch.events,
  );

const outcomes = (client: FakeRouteClient) =>
  client.ofType('exchange_outcome').map((m) => [m.exchangeId, m.state, m.error]);

const failing = (client: FakeRouteClient, type: string, times: number) => {
  let left = times;
  const publish = client.publish.getMockImplementation()!;
  client.publish.mockImplementation(async (params) => {
    if (params.message.type === type && left-- > 0) throw new Error('publish 503');
    return publish(params);
  });
};

describe('session provider runtime', () => {
  it('subscribes once to the route channel as the issued transport UUID', async () => {
    const { client } = await startRuntime({ onExchange: () => ({}) });
    expect(client().userId).toBe('uuid-provider');
    expect(client().setToken).toHaveBeenCalledWith('token-route1');
    expect(client().subscribe).toHaveBeenCalledTimes(1);
    expect(client().subscribe).toHaveBeenCalledWith({ channels: [ROUTE_CHANNEL] });
  });

  it('opens, answers the inline exchange after session_ready, and keeps context across turns', async () => {
    const turns = new Map<string, number>();
    const { client, open } = await startRuntime({
      onSessionOpen: (ctx) => {
        turns.set(ctx.sessionId, 0);
      },
      onExchange: (ctx, exchange) => {
        const turn = (turns.get(ctx.sessionId) ?? 0) + 1;
        turns.set(ctx.sessionId, turn);
        const text = exchange.requestParts.map((p) => p.text).join('');
        return { artifacts: [{ data: `turn ${turn}: ${text}`, mimeType: 'text/plain' }] };
      },
    });

    open({ exchangeId: 'ex1', requestParts: textParts('hi') });
    await vi.waitFor(() => expect(client().ofType('exchange_outcome')).toHaveLength(1));
    client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(2, 'ex2', 'again'));
    await vi.waitFor(() => expect(client().ofType('exchange_outcome')).toHaveLength(2));

    const sent = client().published;
    expect(sent.map((p) => [p.message.type, p.message.seq])).toEqual([
      ['session_ready', 1],
      ['exchange_artifact', 2],
      ['exchange_outcome', 3],
      ['exchange_artifact', 4],
      ['exchange_outcome', 5],
    ]);
    for (const p of sent) {
      expect(p).toMatchObject({
        channel: CHANNEL,
        storeInHistory: true,
        meta: { sessionId: SESSION, protocolVersion: CURRENT_PROTOCOL_VERSION },
        message: { sessionId: SESSION, protocolVersion: CURRENT_PROTOCOL_VERSION },
      });
      expect(typeof p.message.ts).toBe('number');
    }
    const texts = client()
      .ofType('exchange_artifact')
      .map((m) => {
        const ref = m.artifactRef as { kind: string; data: string };
        expect(ref.kind).toBe('inline');
        return Buffer.from(ref.data, 'base64').toString('utf8');
      });
    expect(texts).toEqual(['turn 1: hi', 'turn 2: again']);
    expect(outcomes(client())).toEqual([
      ['ex1', 'completed', undefined],
      ['ex2', 'completed', undefined],
    ]);
  });

  it('fetches the session when no manifest is held, and uses a held manifest without fetching', async () => {
    const { api, runtime, client, open } = await startRuntime({ onExchange: () => ({}) });
    open();
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    expect(api.getSession).toHaveBeenCalledWith(SESSION);

    runtime.acceptManifest({ type: 'SessionManifest', ...manifest('s2') });
    client().deliver(sessionChannel('s2'), CONSUMER_UUID, fromConsumer('s2', 1, 'session_open'));
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(2));
    expect(api.getSession).toHaveBeenCalledTimes(1);
  });

  it('serves a session whose control-channel manifest has no org', async () => {
    let orgId: string | null | undefined;
    const { api, runtime, client, open } = await startRuntime({
      onSessionOpen: (ctx) => {
        orgId = ctx.orgId;
      },
      onExchange: () => ({}),
    });
    runtime.acceptManifest({ type: 'SessionManifest', ...manifest(SESSION), orgId: null });
    open();
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    expect(orgId).toBeNull();
    expect(api.getSession).not.toHaveBeenCalled();
  });

  it('drops session_open silently when the backend does not know the session', async () => {
    const onSessionOpen = vi.fn();
    const { api, client, open } = await startRuntime({ onSessionOpen, onExchange: () => ({}) });
    api.getSession.mockRejectedValueOnce(new ProviderApiError(404, 'SESSION_NOT_FOUND', 'nope'));
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(api.getSession).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(onSessionOpen).not.toHaveBeenCalled();
    expect(client().published).toEqual([]);
  });

  it('runs exchanges one at a time: the next handler starts after the previous outcome', async () => {
    const first = deferred();
    const started: string[] = [];
    const { client, open } = await startRuntime({
      onExchange: async (_ctx, exchange) => {
        started.push(exchange.exchangeId);
        if (exchange.exchangeId === 'ex1') await first.promise;
        return {};
      },
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(started).toEqual(['ex1']));
    client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(2, 'ex2'));
    await new Promise((r) => setTimeout(r, 0));
    expect(started).toEqual(['ex1']);

    first.resolve();
    await vi.waitFor(() => expect(outcomes(client())).toHaveLength(2));
    expect(started).toEqual(['ex1', 'ex2']);
  });

  it('publishes failed/handler_error when the handler throws', async () => {
    const { client, open } = await startRuntime({
      onExchange: () => {
        throw new Error('boom');
      },
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(outcomes(client())).toEqual([['ex1', 'failed', 'handler_error']]));
  });

  it('answers an exchange beyond the pending bound with session_backpressure, without the handler', async () => {
    const hold = deferred();
    const handled: string[] = [];
    const { client, open } = await startRuntime({
      onExchange: async (_ctx, exchange) => {
        handled.push(exchange.exchangeId);
        await hold.promise;
        return {};
      },
    });
    open();
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    for (let i = 1; i <= MAX_PENDING_EXCHANGES + 1; i++) {
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(i + 1, `ex${i}`));
    }
    const overflow = `ex${MAX_PENDING_EXCHANGES + 1}`;
    await vi.waitFor(() =>
      expect(outcomes(client())).toEqual([[overflow, 'failed', 'session_backpressure']]),
    );
    hold.resolve();
    await vi.waitFor(() => expect(outcomes(client())).toHaveLength(MAX_PENDING_EXCHANGES + 1));
    expect(handled).not.toContain(overflow);
  });

  it('fails the session once backpressure refusals outrun their publishes', async () => {
    const outcomeGate = deferred();
    const { client, open } = await startRuntime({ onExchange: () => new Promise(() => {}) });
    open();
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    const publish = client().publish.getMockImplementation()!;
    client().publish.mockImplementation(async (params) => {
      if (params.message.type === 'exchange_outcome') await outcomeGate.promise;
      return publish(params);
    });
    const flood = MAX_PENDING_EXCHANGES * 2 + 50;
    for (let i = 1; i <= flood; i++) {
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(i + 1, `ex${i}`));
    }
    outcomeGate.resolve();

    await vi.waitFor(() =>
      expect(client().ofType('session_failed')).toEqual([
        expect.objectContaining({ error: 'provider_error' }),
      ]),
    );
    const states = outcomes(client()).map(([, state, error]) => `${state}:${error ?? ''}`);
    expect(states.filter((s) => s === 'failed:session_backpressure')).toHaveLength(
      MAX_PENDING_EXCHANGES,
    );
    expect(states.filter((s) => s === 'canceled:')).toHaveLength(MAX_PENDING_EXCHANGES);
    expect(states).toHaveLength(MAX_PENDING_EXCHANGES * 2);
  });

  describe('an exchange re-sent with a new seq', () => {
    it('does not run again while it is running', async () => {
      const hold = deferred();
      const handled: string[] = [];
      const { client, open } = await startRuntime({
        onExchange: async (_ctx, exchange) => {
          handled.push(exchange.exchangeId);
          await hold.promise;
          return {};
        },
      });
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(handled).toEqual(['ex1']));
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(2, 'ex1'));
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(3, 'ex2'));
      hold.resolve();

      await vi.waitFor(() => expect(handled).toEqual(['ex1', 'ex2']));
      await vi.waitFor(() => expect(outcomes(client())).toHaveLength(2));
      expect(outcomes(client()).map(([id]) => id)).toEqual(['ex1', 'ex2']);
    });

    it('does not run again while it is queued', async () => {
      const hold = deferred();
      const handled: string[] = [];
      const { client, open } = await startRuntime({
        onExchange: async (_ctx, exchange) => {
          handled.push(exchange.exchangeId);
          if (exchange.exchangeId === 'ex1') await hold.promise;
          return {};
        },
      });
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(handled).toEqual(['ex1']));
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(2, 'ex2'));
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(3, 'ex2'));
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(4, 'ex3'));
      hold.resolve();

      await vi.waitFor(() => expect(handled).toEqual(['ex1', 'ex2', 'ex3']));
      await vi.waitFor(() => expect(outcomes(client())).toHaveLength(3));
      expect(outcomes(client()).map(([id]) => id)).toEqual(['ex1', 'ex2', 'ex3']);
    });

    it('runs nothing and publishes nothing once it has settled', async () => {
      const handled: string[] = [];
      const { client, open } = await startRuntime({
        onExchange: (_ctx, exchange) => {
          handled.push(exchange.exchangeId);
          return { artifacts: [{ data: 'done', mimeType: 'text/plain' }] };
        },
      });
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(outcomes(client())).toHaveLength(1));
      const published = client().published.length;
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(2, 'ex1'));
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(3, 'ex2'));

      await vi.waitFor(() => expect(outcomes(client())).toHaveLength(2));
      expect(handled).toEqual(['ex1', 'ex2']);
      expect(client().published.slice(published).map((p) => p.message.exchangeId)).toEqual([
        'ex2',
        'ex2',
      ]);
    });

    it('draws one refusal when it is re-sent while that refusal is publishing', async () => {
      const outcomeGate = deferred();
      const { client, open } = await startRuntime({ onExchange: () => new Promise(() => {}) });
      open();
      await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
      for (let i = 1; i <= MAX_PENDING_EXCHANGES; i++) {
        client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(i + 1, `ex${i}`));
      }
      const publish = client().publish.getMockImplementation()!;
      client().publish.mockImplementation(async (params) => {
        if (params.message.type === 'exchange_outcome') await outcomeGate.promise;
        return publish(params);
      });
      const overflow = MAX_PENDING_EXCHANGES + 2;
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(overflow, 'extra'));
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(overflow + 1, 'extra'));
      outcomeGate.resolve();

      await vi.waitFor(() => expect(outcomes(client())).toHaveLength(1));
      await new Promise((r) => setTimeout(r, 0));
      expect(outcomes(client())).toEqual([['extra', 'failed', 'session_backpressure']]);
    });

    it('remembers a bounded number of settled exchanges, forgetting the oldest', async () => {
      const handled: string[] = [];
      const { client, open } = await startRuntime({
        onExchange: (_ctx, exchange) => {
          handled.push(exchange.exchangeId);
          return {};
        },
      });
      open();
      await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
      expect(MAX_SETTLED_EXCHANGE_IDS).toBeGreaterThanOrEqual(256);
      const total = MAX_SETTLED_EXCHANGE_IDS + 1;
      for (let i = 1; i <= total; i++) {
        client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(i + 1, `ex${i}`));
        await vi.waitFor(() => expect(outcomes(client())).toHaveLength(i), { interval: 1 });
      }
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(total + 2, 'ex2'));
      client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(total + 3, 'ex1'));

      await vi.waitFor(() => expect(outcomes(client())).toHaveLength(total + 1));
      expect(handled.slice(total)).toEqual(['ex1']);
    });
  });

  it('settles a cooperatively canceled exchange as canceled', async () => {
    const { client, open } = await startRuntime({
      onExchange: (_ctx, exchange) =>
        new Promise((resolve) => {
          exchange.cancelSignal.addEventListener('abort', () => resolve({}));
        }),
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    client().deliver(
      CHANNEL,
      CONSUMER_UUID,
      fromConsumer(SESSION, 2, 'exchange_cancel', { exchangeId: 'ex1' }),
    );
    await vi.waitFor(() => expect(outcomes(client())).toEqual([['ex1', 'canceled', undefined]]));
  });

  it('on session_close cancels the in-flight exchange, runs onSessionClose, publishes session_closed, and stays closed', async () => {
    const order: string[] = [];
    const { client, open } = await startRuntime({
      onExchange: () => new Promise(() => {}),
      onSessionClose: (_ctx, reason) => {
        order.push(`onSessionClose:${reason}`);
      },
    });
    client().publish.mockImplementation(async (params) => {
      order.push(`${params.message.type}`);
      client().published.push(params);
      return { timetoken: '1' };
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(order).toContain('session_ready'));
    client().deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 2, 'session_close'));
    await vi.waitFor(() => expect(order).toContain('session_closed'));

    expect(order).toEqual([
      'session_ready',
      'exchange_outcome',
      'onSessionClose:consumer_closed',
      'session_closed',
    ]);
    expect(outcomes(client())).toEqual([['ex1', 'canceled', undefined]]);
    expect(client().ofType('session_closed')[0]).toMatchObject({ reason: 'consumer_closed' });

    client().deliver(CHANNEL, CONSUMER_UUID, exchangeMsg(3, 'ex2'));
    client().deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 1, 'session_open'));
    await new Promise((r) => setTimeout(r, 0));
    expect(client().published).toHaveLength(3);
  });

  it.each([
    [
      'throws',
      () => {
        throw new Error('no');
      },
    ],
    [
      'rejects',
      async () => {
        throw new Error('no');
      },
    ],
  ])('fails the session with provider_error when onSessionOpen %s', async (_how, onSessionOpen) => {
    const onExchange = vi.fn(() => ({}));
    const onSessionClose = vi.fn();
    const { client, open } = await startRuntime({
      onSessionOpen,
      onExchange,
      onSessionClose,
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(client().ofType('session_failed')).toHaveLength(1));
    expect(client().ofType('session_failed')[0]).toMatchObject({ error: 'provider_error' });
    expect(client().ofType('session_ready')).toEqual([]);
    // The inline exchange never started, so the consumer settles it failed/provider_error.
    expect(outcomes(client())).toEqual([]);
    expect(onExchange).not.toHaveBeenCalled();
    expect(onSessionClose).not.toHaveBeenCalled();
  });

  it('fails a session beyond maxSessions with provider_at_capacity', async () => {
    const { client } = await startRuntime({ onExchange: () => ({}) }, { maxSessions: 1 });
    for (const id of ['s1', 's2']) {
      client().deliver(sessionChannel(id), CONSUMER_UUID, fromConsumer(id, 1, 'session_open'));
    }
    await vi.waitFor(() => expect(client().ofType('session_failed')).toHaveLength(1));
    expect(client().ofType('session_failed')[0]).toMatchObject({
      sessionId: 's2',
      error: 'provider_at_capacity',
    });
    expect(client().ofType('session_ready').map((m) => m.sessionId)).toEqual(['s1']);
  });

  it('on a backend notice ends the session locally and publishes nothing', async () => {
    const onSessionClose = vi.fn();
    const { client, open } = await startRuntime({
      onExchange: () => new Promise(() => {}),
      onSessionClose,
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    client().deliver(CHANNEL, BACKEND_UUID, {
      ...fromConsumer(SESSION, 0, 'session_closed'),
      reason: 'idle_timeout',
    });
    await vi.waitFor(() => expect(onSessionClose).toHaveBeenCalledWith(expect.anything(), 'idle_timeout'));
    expect(client().published.map((p) => p.message.type)).toEqual(['session_ready']);
  });

  it('echoes a backend route_probe in the next checkpoint, and no one else’s, without opening a session', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    const { api, client } = await startRuntime({ onExchange: () => ({}) });
    const probe = (nonce: string) => ({ type: 'route_probe', protocolVersion: CURRENT_PROTOCOL_VERSION, nonce });
    client().deliver(PROBE_CHANNEL, BACKEND_UUID, probe('n-backend'));
    client().deliver(PROBE_CHANNEL, CONSUMER_UUID, probe('n-consumer'));

    await vi.advanceTimersByTimeAsync(CHECKPOINT_FLUSH_MS);

    const echoes = (api.postCheckpoint.mock.calls as unknown as [{ probeEchoes: string[] }][]).flatMap(
      ([batch]) => batch.probeEchoes,
    );
    expect(echoes).toEqual(['n-backend']);
    expect(api.getSession).not.toHaveBeenCalled();
    expect(client().published).toEqual([]);
  });

  it('checkpoints a session end at once, so its seat frees without waiting for a tick', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    const { api, client, open } = await startRuntime({ onExchange: () => ({}) });
    open();
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    client().deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 2, 'session_close'));

    await vi.waitFor(() =>
      expect(
        (api.postCheckpoint.mock.calls as unknown as [{ events: { type: string }[] }][]).some(
          ([b]) => b.events.some((e) => e.type === 'session_closed'),
        ),
      ).toBe(true),
    );
  });

  it('frees a closing session’s place before its slow onSessionClose returns', async () => {
    const slowClose = deferred();
    const { client } = await startRuntime(
      { onExchange: () => ({}), onSessionClose: () => slowClose.promise },
      { maxSessions: 1 },
    );
    client().deliver(sessionChannel('s1'), CONSUMER_UUID, fromConsumer('s1', 1, 'session_open'));
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    client().deliver(sessionChannel('s1'), CONSUMER_UUID, fromConsumer('s1', 2, 'session_close'));

    client().deliver(sessionChannel('s2'), CONSUMER_UUID, fromConsumer('s2', 1, 'session_open'));

    await vi.waitFor(() =>
      expect(client().ofType('session_ready').map((m) => m.sessionId)).toEqual(['s1', 's2']),
    );
    expect(client().ofType('session_failed')).toEqual([]);
    slowClose.resolve();
  });

  it('still lets a backend notice end a closing session, which then publishes nothing further', async () => {
    const slowClose = deferred();
    const { client, open } = await startRuntime({
      onExchange: () => ({}),
      onSessionClose: () => slowClose.promise,
    });
    open();
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    client().deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 2, 'session_close'));

    client().deliver(CHANNEL, BACKEND_UUID, {
      ...fromConsumer(SESSION, 0, 'session_failed'),
      error: 'route_lost',
    });
    slowClose.resolve();
    await new Promise((r) => setTimeout(r, 20));

    expect(client().ofType('session_closed')).toEqual([]);
  });

  it('drops a publish queued behind a slow one once a backend notice ends the session', async () => {
    const firstArtifact = deferred<{ timetoken: string }>();
    const { client, open } = await startRuntime({
      onExchange: async (_ctx, exchange) => {
        void exchange.publishArtifact('one', { mimeType: 'text/plain' }).catch(() => {});
        void exchange.publishArtifact('two', { mimeType: 'text/plain' }).catch(() => {});
        return new Promise(() => {});
      },
    });
    let held = false;
    const delivered = client().publish.getMockImplementation() as (p: never) => Promise<unknown>;
    client().publish.mockImplementation(async (params) => {
      if (params.message.type === 'exchange_artifact' && !held) {
        held = true;
        client().published.push(params);
        return firstArtifact.promise;
      }
      return delivered(params as never) as Promise<{ timetoken: string }>;
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(client().ofType('exchange_artifact')).toHaveLength(1));

    client().deliver(CHANNEL, BACKEND_UUID, {
      ...fromConsumer(SESSION, 0, 'session_failed'),
      error: 'route_lost',
    });
    firstArtifact.resolve({ timetoken: '1' });
    await new Promise((r) => setTimeout(r, 20));

    expect(client().ofType('exchange_artifact')).toHaveLength(1);
  });

  it('checkpoints lifecycle facts and no artifact data', async () => {
    vi.useFakeTimers({ toFake: ['setInterval'] });
    const { api, client, open } = await startRuntime({
      onExchange: () => ({ artifacts: [{ data: 'small', mimeType: 'text/plain' }] }),
    });
    open({ exchangeId: 'ex1', requestParts: textParts('secret input') });
    await vi.waitFor(() => expect(outcomes(client())).toHaveLength(1));
    client().deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 2, 'session_close'));
    await vi.waitFor(() => expect(client().ofType('session_closed')).toHaveLength(1));

    vi.advanceTimersByTime(CHECKPOINT_FLUSH_MS);
    await vi.waitFor(() => expect(api.postCheckpoint).toHaveBeenCalled());
    // The first call is the heartbeat sent on connect; this one carries the session's facts.
    const batch = (api.postCheckpoint.mock.calls as unknown as [Record<string, unknown>][])
      .map(([b]) => b)
      .find((b) => (b.events as unknown[]).length > 0) as Record<string, unknown>;
    expect(batch).toMatchObject({ routeId: 'route1', openSessions: [], probeEchoes: [] });
    expect(batch.events).toEqual([
      { type: 'session_ready', sessionId: SESSION },
      {
        type: 'exchange_outcome',
        sessionId: SESSION,
        exchangeId: 'ex1',
        state: 'completed',
        artifactRefs: [],
      },
      { type: 'session_closed', sessionId: SESSION, reason: 'consumer_closed' },
    ]);
  });

  it('fails the exchange for an artifact over the inline limit, publishing no artifact', async () => {
    const big = Buffer.alloc(DEFAULTS.inlineLimitBytes + 1, 1);
    const { client, open } = await startRuntime({
      onExchange: () => ({ artifacts: [{ data: big, mimeType: 'application/octet-stream' }] }),
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(outcomes(client())).toEqual([['ex1', 'failed', 'handler_error']]));
    expect(client().ofType('exchange_artifact')).toEqual([]);
  });

  it('defaults a mid-exchange artifact mimeType when the option is passed as undefined', async () => {
    const { client, open } = await startRuntime({
      onExchange: async (_ctx, exchange) => {
        await exchange.publishArtifact('x', { mimeType: undefined });
        return {};
      },
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(outcomes(client())).toHaveLength(1));
    expect(client().ofType('exchange_artifact')[0].artifactRef).toMatchObject({
      mimeType: 'application/octet-stream',
    });
  });

  it('on ROUTE_LOST ends the route sessions locally and registers a new route', async () => {
    vi.useFakeTimers({ toFake: ['setInterval'] });
    const onSessionClose = vi.fn();
    const { api, clients, open } = await startRuntime({
      onExchange: () => new Promise(() => {}),
      onSessionClose,
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(clients[0].ofType('session_ready')).toHaveLength(1));

    api.registerRoute.mockResolvedValueOnce(routeGrant('route2'));
    api.postCheckpoint.mockRejectedValueOnce(new ProviderApiError(409, 'ROUTE_LOST', 'lost'));
    vi.advanceTimersByTime(CHECKPOINT_FLUSH_MS);

    await vi.waitFor(() => expect(clients).toHaveLength(2));
    expect(onSessionClose).toHaveBeenCalledWith(expect.anything(), 'route_lost');
    expect(clients[0].destroy).toHaveBeenCalled();
    expect(clients[1].subscribe).toHaveBeenCalledWith({ channels: ['sess.route2.*'] });
    expect(clients[0].ofType('exchange_outcome')).toEqual([]);
  });

  describe('when the backend refuses the agent credential', () => {
    const BASE_URL = 'https://blocks.test';
    const INSTANCE_ID = 'AG-echo_agent-1';
    const tokens = (n: number) => ({ accessToken: `jwt${n}`, refreshToken: `r${n}`, expiresIn: 900 });

    /** The backend over HTTP: real AgentAuth and ProviderSessionApi run against it. */
    function fakeBackend() {
      const backend = {
        agentDeleted: false,
        staleJwt: '',
        refreshStatus: 0,
        refusesEveryJwt: false,
        issued: 1,
        calls: [] as string[],
      };
      const answer = (status: number, body: unknown) =>
        new Response(JSON.stringify(body), {
          status,
          headers: { 'Content-Type': 'application/json' },
        });
      const handle = (path: string, init: RequestInit): Response => {
        if (path === '/api/v1/auth/agent/connect') {
          return backend.agentDeleted
            ? answer(404, { error: 'Agent not found' })
            : answer(200, tokens(backend.issued));
        }
        if (path === '/api/v1/auth/agent/refresh') {
          if (backend.refreshStatus) return answer(backend.refreshStatus, { error: 'unavailable' });
          return backend.agentDeleted
            ? answer(401, { code: 'REFRESH_TOKEN_INVALID', error: 'invalid' })
            : answer(200, tokens(++backend.issued));
        }
        const jwt = new Headers(init.headers).get('Authorization');
        if (backend.agentDeleted || backend.refusesEveryJwt || jwt === `Bearer ${backend.staleJwt}`) {
          return answer(401, { error: 'Unauthorized' });
        }
        if (path === '/api/v1/provider/routes') return answer(200, routeGrant());
        if (path === `/api/v1/provider/sessions/${SESSION}`) return answer(200, manifest(SESSION));
        if (path === '/api/v1/provider/checkpoints') {
          const { routeId, idempotencyKey } = JSON.parse(String(init.body));
          return answer(200, { routeId, idempotencyKey });
        }
        return answer(404, { error: 'not found' });
      };
      vi.stubGlobal('fetch', async (url: string, init: RequestInit = {}) => {
        const path = new URL(url).pathname;
        backend.calls.push(path);
        return handle(path, init);
      });
      return backend;
    }

    async function startOverHttp() {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
      const backend = fakeBackend();
      const auth = new AgentAuth('api-key', BASE_URL);
      await auth.init({ agentName: 'echo_agent', instanceId: INSTANCE_ID, billingMode: 'free' });
      const onSessionClose = vi.fn();
      const clients: FakeRouteClient[] = [];
      const runtime = new SessionProviderRuntime({
        api: new ProviderSessionApi(BASE_URL, (url, init) => auth.authenticatedFetch(url, init)),
        instanceId: INSTANCE_ID,
        maxSessions: 4,
        handlers: { onExchange: () => new Promise(() => {}), onSessionClose },
        createClient: (userId) => {
          const client = fakeRouteClient(userId);
          clients.push(client);
          return client as unknown as PubNub;
        },
      });
      runtimes.push(runtime);
      await runtime.start();
      clients[0].deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 1, 'session_open'));
      await vi.waitFor(() => expect(clients[0].ofType('session_ready')).toHaveLength(1));
      backend.calls.length = 0;
      return { backend, clients, onSessionClose };
    }

    afterEach(() => {
      vi.unstubAllGlobals();
    });

    it('ends the route once a deleted agent cannot refresh, then re-registers on backoff', async () => {
      const { backend, clients, onSessionClose } = await startOverHttp();
      backend.agentDeleted = true;

      await vi.advanceTimersByTimeAsync(60_000);

      const count = (path: string) => backend.calls.filter((p) => p === path).length;
      expect(count('/api/v1/provider/checkpoints')).toBe(1);
      expect(onSessionClose).toHaveBeenCalledWith(expect.anything(), 'route_lost');
      expect(clients[0].destroy).toHaveBeenCalled();
      // At once, then 1, 2, 4, 8, 16 s later: the registration backoff, not the 2 s tick.
      expect(count('/api/v1/provider/routes')).toBe(6);
      // Each refused call costs a refresh and a connect beside it.
      expect(backend.calls).toHaveLength((1 + 6) * 3);
    });

    it('ends the route when the checkpoint is still refused after a successful refresh', async () => {
      const { backend, clients, onSessionClose } = await startOverHttp();
      backend.refusesEveryJwt = true;

      await vi.advanceTimersByTimeAsync(CHECKPOINT_FLUSH_MS * 5);

      // The first post and its retry after the refresh, then no more.
      expect(backend.calls.filter((p) => p === '/api/v1/provider/checkpoints')).toHaveLength(2);
      expect(onSessionClose).toHaveBeenCalledWith(expect.anything(), 'route_lost');
      expect(clients[0].destroy).toHaveBeenCalled();
    });

    it.each([408, 503])('keeps retrying the route while a refresh answers %s', async (status) => {
      const { backend, clients, onSessionClose } = await startOverHttp();
      backend.staleJwt = `jwt${backend.issued}`;
      backend.refreshStatus = status;

      await vi.advanceTimersByTimeAsync(CHECKPOINT_FLUSH_MS * 3);

      expect(backend.calls.filter((p) => p === '/api/v1/provider/checkpoints')).toHaveLength(3);
      expect(backend.calls).not.toContain('/api/v1/provider/routes');
      expect(onSessionClose).not.toHaveBeenCalled();
      expect(clients).toHaveLength(1);
    });

    it('keeps the route and holds checkpoints while a refresh answers 429', async () => {
      const { backend, clients, onSessionClose } = await startOverHttp();
      backend.staleJwt = `jwt${backend.issued}`;
      backend.refreshStatus = 429;

      await vi.advanceTimersByTimeAsync(CHECKPOINT_FLUSH_MS * 3);

      // The first post meets the 429; the next fails fast inside the hold and starts the
      // loop's own wait, so no third post goes out within the 30 s hold.
      expect(backend.calls.filter((p) => p === '/api/v1/auth/agent/refresh')).toHaveLength(1);
      expect(backend.calls.filter((p) => p === '/api/v1/provider/checkpoints')).toHaveLength(2);
      expect(backend.calls).not.toContain('/api/v1/provider/routes');
      expect(onSessionClose).not.toHaveBeenCalled();
      expect(clients).toHaveLength(1);
    });

    it('keeps the route when a refresh clears the 401', async () => {
      const { backend, clients, onSessionClose } = await startOverHttp();
      backend.staleJwt = `jwt${backend.issued}`;

      await vi.advanceTimersByTimeAsync(CHECKPOINT_FLUSH_MS * 3);

      expect(backend.calls.slice(0, 3)).toEqual([
        '/api/v1/provider/checkpoints',
        '/api/v1/auth/agent/refresh',
        '/api/v1/provider/checkpoints',
      ]);
      expect(backend.calls.filter((p) => p === '/api/v1/provider/checkpoints')).toHaveLength(4);
      expect(backend.calls).not.toContain('/api/v1/provider/routes');
      expect(onSessionClose).not.toHaveBeenCalled();
      expect(clients).toHaveLength(1);
    });
  });

  it.each([
    [0, 1],
    [5000, 256],
    [Number.NaN, 1],
  ])('registers maxSessions %s as %s, the backend range', async (requested, sent) => {
    const { api } = await startRuntime({ onExchange: () => ({}) }, { maxSessions: requested });
    expect(api.registerRoute).toHaveBeenCalledWith('AG-echo_agent-1', sent);
  });

  describe('route readiness', () => {
    const unconnectedRuntime = () => {
      const api = fakeApi();
      const clients: FakeRouteClient[] = [];
      const runtime = new SessionProviderRuntime({
        api: api as unknown as ProviderSessionApi,
        instanceId: 'AG-echo_agent-1',
        maxSessions: 1,
        handlers: { onExchange: () => ({}) },
        createClient: (userId) => {
          const client = fakeRouteClient(userId, { autoConnect: false });
          clients.push(client);
          return client as unknown as PubNub;
        },
      });
      runtimes.push(runtime);
      return { api, clients, runtime };
    };

    it('sends no heartbeat until the route channel is subscribed, then one at once', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
      const { api, clients, runtime } = unconnectedRuntime();
      await runtime.start();

      await vi.advanceTimersByTimeAsync(CHECKPOINT_FLUSH_MS * 3);
      expect(api.postCheckpoint).not.toHaveBeenCalled();

      clients[0].connect();
      await vi.advanceTimersByTimeAsync(0);
      expect(api.postCheckpoint).toHaveBeenCalledTimes(1);
    });

    it('replaces a route that does not subscribe in time, backing off while none does', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
      const { api, clients, runtime } = unconnectedRuntime();
      await runtime.start();

      // Connect timeout (10 s), then the backoff: 1 s, 2 s, 4 s between registrations.
      await vi.advanceTimersByTimeAsync(10_000);
      expect(clients[0].destroy).toHaveBeenCalled();
      expect(api.registerRoute).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(api.registerRoute).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(10_000 + 2_000);
      expect(api.registerRoute).toHaveBeenCalledTimes(3);
      await vi.advanceTimersByTimeAsync(10_000 + 2_000);
      expect(api.registerRoute).toHaveBeenCalledTimes(3);
      expect(api.postCheckpoint).not.toHaveBeenCalled();
    });
  });

  it.each([
    [403, 2],
    [404, 2],
    [429, 1],
  ])('on a route token refresh answered %d, registers a new route %d time(s) in all', async (status, registrations) => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const api = fakeApi();
    api.registerRoute.mockResolvedValueOnce({ ...routeGrant(), ttlMinutes: 1 });
    api.refreshRouteToken.mockRejectedValueOnce(new ProviderApiError(status, undefined, 'refused'));
    await startRuntime({ onExchange: () => ({}) }, { api });

    // 80% of the 1-minute TTL, then the first retry backoff (5 s) for a transient answer.
    await vi.advanceTimersByTimeAsync(48_000 + 5_000);

    expect(api.registerRoute).toHaveBeenCalledTimes(registrations);
    if (registrations === 1) expect(api.refreshRouteToken).toHaveBeenCalledTimes(2);
  });

  it('replaces the route when a token refresh answers ROUTE_LOST', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const api = fakeApi();
    api.registerRoute.mockResolvedValueOnce({ ...routeGrant(), ttlMinutes: 1 });
    api.refreshRouteToken.mockRejectedValueOnce(new ProviderApiError(409, 'ROUTE_LOST', 'lost'));
    const { clients } = await startRuntime({ onExchange: () => ({}) }, { api });

    await vi.advanceTimersByTimeAsync(60_000);
    expect(api.refreshRouteToken).toHaveBeenCalledWith('route1');
    expect(api.registerRoute).toHaveBeenCalledTimes(2);
    expect(clients[0].destroy).toHaveBeenCalled();
    expect(clients).toHaveLength(2);
  });

  describe('on a route token denial', () => {
    it('refreshes once and resumes the same route', async () => {
      const { api, clients, client } = await startRuntime({ onExchange: () => ({}) });
      api.refreshRouteToken.mockResolvedValueOnce({ pamToken: 'fresh', ttlMinutes: 60 });

      client().status('PNAccessDeniedCategory');

      await vi.waitFor(() => expect(client().reconnect).toHaveBeenCalledTimes(1));
      expect(client().setToken).toHaveBeenLastCalledWith('fresh');
      expect(api.registerRoute).toHaveBeenCalledTimes(1);
      expect(clients).toHaveLength(1);
    });

    it('replaces the route when denied again before reconnecting, refreshing only once', async () => {
      const { api, clients } = await startRuntime({ onExchange: () => ({}) });
      api.refreshRouteToken.mockResolvedValue({ pamToken: 'fresh', ttlMinutes: 60 });

      clients[0].status('PNAccessDeniedCategory');
      await vi.waitFor(() => expect(clients[0].reconnect).toHaveBeenCalledTimes(1));
      clients[0].status('PNAccessDeniedCategory');

      await vi.waitFor(() => expect(api.registerRoute).toHaveBeenCalledTimes(2));
      expect(api.refreshRouteToken).toHaveBeenCalledTimes(1);
      expect(clients[0].destroy).toHaveBeenCalled();
    });

    it('refreshes again on a denial after the route reconnected', async () => {
      const { api, clients } = await startRuntime({ onExchange: () => ({}) });
      api.refreshRouteToken.mockResolvedValue({ pamToken: 'fresh', ttlMinutes: 60 });

      clients[0].status('PNAccessDeniedCategory');
      await vi.waitFor(() => expect(clients[0].reconnect).toHaveBeenCalledTimes(1));
      clients[0].status('PNReconnectedCategory');
      clients[0].status('PNAccessDeniedCategory');

      await vi.waitFor(() => expect(clients[0].reconnect).toHaveBeenCalledTimes(2));
      expect(api.registerRoute).toHaveBeenCalledTimes(1);
    });

    it('replaces the route when the refresh fails, ending its sessions with route_lost', async () => {
      const onSessionClose = vi.fn();
      const { api, clients, client, open } = await startRuntime({
        onExchange: () => ({}),
        onSessionClose,
      });
      open();
      await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
      api.refreshRouteToken.mockRejectedValueOnce(new ProviderApiError(403, undefined, 'revoked'));

      clients[0].status('PNAccessDeniedCategory');

      await vi.waitFor(() => expect(api.registerRoute).toHaveBeenCalledTimes(2));
      expect(clients[0].destroy).toHaveBeenCalled();
      expect(onSessionClose).toHaveBeenCalledWith(expect.anything(), 'route_lost');
    });
  });

  describe('when a publish fails', () => {
    const reply = () => ({ artifacts: [{ data: 'hi', mimeType: 'text/plain' }] });

    it('retries once and still delivers', async () => {
      const { client, open } = await startRuntime({ onExchange: reply });
      failing(client(), 'exchange_artifact', 1);
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(outcomes(client())).toEqual([['ex1', 'completed', undefined]]));
      expect(client().ofType('exchange_artifact')).toHaveLength(1);
    });

    it('fails the exchange when its artifact cannot be published', async () => {
      const { client, open } = await startRuntime({ onExchange: reply });
      failing(client(), 'exchange_artifact', 2);
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(outcomes(client())).toEqual([['ex1', 'failed', 'handler_error']]));
    });

    it('fails the session, recording no outcome, when an exchange outcome cannot be published', async () => {
      vi.useFakeTimers({ toFake: ['setInterval'] });
      const { api, client, open } = await startRuntime({ onExchange: () => ({}) });
      failing(client(), 'exchange_outcome', 2);
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(client().ofType('session_failed')).toHaveLength(1));
      expect(client().ofType('session_failed')[0]).toMatchObject({ error: 'provider_error' });

      vi.advanceTimersByTime(CHECKPOINT_FLUSH_MS);
      await vi.waitFor(() =>
        expect(checkpointedEvents(api)).toContainEqual(
          expect.objectContaining({ type: 'session_failed', error: 'provider_error' }),
        ),
      );
      expect(checkpointedEvents(api).map((e) => e.type)).not.toContain('exchange_outcome');
    });

    it('fails the session when session_ready cannot be published', async () => {
      const onExchange = vi.fn(reply);
      const { client, open } = await startRuntime({ onExchange });
      failing(client(), 'session_ready', 2);
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(client().ofType('session_failed')).toHaveLength(1));
      expect(client().ofType('session_failed')[0]).toMatchObject({ error: 'provider_error' });
      expect(onExchange).not.toHaveBeenCalled();
    });
  });

  it('defers a close that arrives during onSessionOpen until it returns', async () => {
    const opening = deferred();
    const order: string[] = [];
    const { client, open } = await startRuntime({
      onSessionOpen: () => opening.promise,
      onExchange: () => ({}),
      onSessionClose: (_ctx, reason) => {
        order.push(`onSessionClose:${reason}`);
      },
    });
    client().publish.mockImplementation(async (params) => {
      order.push(`${params.message.type}`);
      client().published.push(params);
      return { timetoken: '1' };
    });
    open();
    client().deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 2, 'session_close'));
    await new Promise((r) => setTimeout(r, 0));
    expect(order).toEqual([]);

    opening.resolve();
    await vi.waitFor(() => expect(order).toContain('session_closed'));
    expect(order).toEqual(['onSessionClose:consumer_closed', 'session_closed']);
  });

  it('fails a session whose onSessionOpen never returns, freeing its seat, and runs onSessionClose only if it returns', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const opening = deferred();
    const onSessionClose = vi.fn();
    const { client } = await startRuntime(
      {
        onSessionOpen: (ctx) => (ctx.sessionId === 's1' ? opening.promise : undefined),
        onExchange: () => ({}),
        onSessionClose,
      },
      { maxSessions: 1 },
    );
    client().deliver(sessionChannel('s1'), CONSUMER_UUID, fromConsumer('s1', 1, 'session_open'));
    client().deliver(sessionChannel('s1'), CONSUMER_UUID, fromConsumer('s1', 2, 'session_close'));

    await vi.advanceTimersByTimeAsync(SESSION_OPEN_TIMEOUT_MS);
    expect(client().ofType('session_failed')).toEqual([
      expect.objectContaining({ sessionId: 's1', error: 'provider_error' }),
    ]);
    expect(onSessionClose).not.toHaveBeenCalled();
    client().deliver(sessionChannel('s2'), CONSUMER_UUID, fromConsumer('s2', 1, 'session_open'));
    await vi.waitFor(() =>
      expect(client().ofType('session_ready').map((m) => m.sessionId)).toEqual(['s2']),
    );

    opening.resolve();
    await vi.waitFor(() =>
      expect(onSessionClose).toHaveBeenCalledWith(
        expect.objectContaining({ sessionId: 's1' }),
        'provider_error',
      ),
    );
    expect(client().ofType('session_closed')).toEqual([]);
  });

  it('still runs onSessionClose when a backend notice ends a session whose close was deferred', async () => {
    const opening = deferred();
    const onSessionClose = vi.fn();
    const { client, open } = await startRuntime({
      onSessionOpen: () => opening.promise,
      onExchange: () => ({}),
      onSessionClose,
    });
    open();
    client().deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 2, 'session_close'));
    client().deliver(CHANNEL, BACKEND_UUID, {
      ...fromConsumer(SESSION, 0, 'session_failed'),
      error: 'route_lost',
    });

    opening.resolve();

    await vi.waitFor(() => expect(onSessionClose).toHaveBeenCalledWith(expect.anything(), 'route_lost'));
    expect(onSessionClose).toHaveBeenCalledTimes(1);
    expect(client().ofType('session_closed')).toEqual([]);
  });

  it('backs off between failed token refreshes', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const api = fakeApi();
    api.registerRoute.mockResolvedValueOnce({ ...routeGrant(), ttlMinutes: 1 });
    api.refreshRouteToken.mockRejectedValue(new Error('503'));
    await startRuntime({ onExchange: () => ({}) }, { api });

    await vi.advanceTimersByTimeAsync(48_000);
    expect(api.refreshRouteToken).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(api.refreshRouteToken).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(api.refreshRouteToken).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(api.refreshRouteToken).toHaveBeenCalledTimes(3);
  });

  it('waits out a rate-limited token refresh’s Retry-After', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const api = fakeApi();
    api.registerRoute.mockResolvedValueOnce({ ...routeGrant(), ttlMinutes: 1 });
    api.refreshRouteToken
      .mockRejectedValueOnce(new ProviderApiError(429, undefined, 'rate limited', 30_000))
      .mockResolvedValue({ pamToken: 'fresh', ttlMinutes: 60 });
    await startRuntime({ onExchange: () => ({}) }, { api });

    await vi.advanceTimersByTimeAsync(48_000);
    expect(api.refreshRouteToken).toHaveBeenCalledTimes(1);
    // The 5 s backoff alone would retry here.
    await vi.advanceTimersByTimeAsync(29_000);
    expect(api.refreshRouteToken).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(api.refreshRouteToken).toHaveBeenCalledTimes(2);
    expect(api.registerRoute).toHaveBeenCalledTimes(1);
  });

  it('waits out a rate-limited route registration’s Retry-After', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const api = fakeApi();
    api.registerRoute.mockRejectedValueOnce(
      new ProviderApiError(429, undefined, 'rate limited', 20_000),
    );
    await startRuntime({ onExchange: () => ({}) }, { api });

    expect(api.registerRoute).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(19_000);
    expect(api.registerRoute).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(api.registerRoute).toHaveBeenCalledTimes(2);
  });

  describe('a session lookup the backend rate-limits', () => {
    const rateLimited = (retryAfterMs: number) =>
      new ProviderApiError(429, undefined, 'rate limited', retryAfterMs);

    it('waits out each Retry-After without spending the lookup’s retries', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
      const { api, client, open } = await startRuntime({ onExchange: () => ({}) });
      // More rate-limited answers than the lookup has retries.
      for (let i = 0; i < 5; i++) api.getSession.mockRejectedValueOnce(rateLimited(5_000));
      open();
      await vi.advanceTimersByTimeAsync(0);

      for (let attempt = 1; attempt <= 5; attempt++) {
        expect(api.getSession).toHaveBeenCalledTimes(attempt);
        await vi.advanceTimersByTimeAsync(4_999);
        expect(api.getSession).toHaveBeenCalledTimes(attempt);
        await vi.advanceTimersByTimeAsync(1);
      }
      expect(client().ofType('session_ready')).toHaveLength(1);
    });

    it('backs off a 429 without Retry-After without spending the lookup’s retries', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
      const { api, client, open } = await startRuntime({ onExchange: () => ({}) });
      const transient = new ProviderApiError(503, undefined, 'unavailable');
      for (const err of [transient, new ProviderApiError(429, undefined, 'rate limited'), transient, transient]) {
        api.getSession.mockRejectedValueOnce(err);
      }
      open();
      await vi.advanceTimersByTimeAsync(0);

      for (const [attempt, delayMs] of [250, 30_000, 1_000, 3_000].entries()) {
        expect(api.getSession).toHaveBeenCalledTimes(attempt + 1);
        await vi.advanceTimersByTimeAsync(delayMs - 1);
        expect(api.getSession).toHaveBeenCalledTimes(attempt + 1);
        await vi.advanceTimersByTimeAsync(1);
      }
      expect(api.getSession).toHaveBeenCalledTimes(5);
      expect(client().ofType('session_ready')).toHaveLength(1);
    });

    it('leaves no lookup wait pending once the runtime stops', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
      const { api, runtime, open } = await startRuntime({ onExchange: () => ({}) });
      api.getSession.mockRejectedValueOnce(rateLimited(60_000));
      open();
      await vi.advanceTimersByTimeAsync(0);
      expect(api.getSession).toHaveBeenCalledTimes(1);

      await runtime.stop();
      await vi.advanceTimersByTimeAsync(0);
      expect(vi.getTimerCount()).toBe(0);
      expect(api.getSession).toHaveBeenCalledTimes(1);
    });

    it('drops session_open when the wait would outlast the consumer’s wait for session_ready', async () => {
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
      const onSessionOpen = vi.fn();
      const { api, client, open } = await startRuntime({ onSessionOpen, onExchange: () => ({}) });
      api.getSession.mockRejectedValueOnce(rateLimited(MAX_SESSION_READY_TIMEOUT_MS));
      open();

      await vi.advanceTimersByTimeAsync(MAX_SESSION_READY_TIMEOUT_MS * 2);
      expect(api.getSession).toHaveBeenCalledTimes(1);
      expect(onSessionOpen).not.toHaveBeenCalled();
      expect(client().published).toEqual([]);
    });
  });

  it('retries a session lookup that fails transiently', async () => {
    const { api, client, open } = await startRuntime({ onExchange: () => ({}) });
    api.getSession.mockRejectedValueOnce(new ProviderApiError(503, undefined, 'unavailable'));
    open();
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    expect(api.getSession).toHaveBeenCalledTimes(2);
  });

  it('settles canceled when the cancel arrives while an artifact is publishing', async () => {
    const release = deferred();
    const { client, open } = await startRuntime({
      onExchange: () => ({
        artifacts: [
          { data: 'one', mimeType: 'text/plain' },
          { data: 'two', mimeType: 'text/plain' },
        ],
      }),
    });
    const publish = client().publish.getMockImplementation()!;
    client().publish.mockImplementation(async (params) => {
      if (params.message.type === 'exchange_artifact') await release.promise;
      return publish(params);
    });
    open({ exchangeId: 'ex1', requestParts: [] });
    await vi.waitFor(() => expect(client().publish).toHaveBeenCalledTimes(2));
    client().deliver(
      CHANNEL,
      CONSUMER_UUID,
      fromConsumer(SESSION, 2, 'exchange_cancel', { exchangeId: 'ex1' }),
    );
    release.resolve();

    await vi.waitFor(() => expect(outcomes(client())).toEqual([['ex1', 'canceled', undefined]]));
    expect(client().ofType('exchange_artifact')).toHaveLength(1);
  });

  it('admits no session for a session_open from anyone but the manifest consumer', async () => {
    const { client } = await startRuntime({ onExchange: () => ({}) }, { maxSessions: 1 });
    client().deliver(sessionChannel('s1'), 'uuid-intruder', fromConsumer('s1', 1, 'session_open'));
    await new Promise((r) => setTimeout(r, 0));
    client().deliver(sessionChannel('s2'), CONSUMER_UUID, fromConsumer('s2', 1, 'session_open'));
    await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
    expect(client().ofType('session_ready')[0]).toMatchObject({ sessionId: 's2' });
    expect(client().ofType('session_failed')).toEqual([]);
  });

  it.each([0, -1, '1', undefined])(
    'admits no session for a session_open with seq %j, which the session would drop',
    async (seq) => {
      const { api, client } = await startRuntime({ onExchange: () => ({}) }, { maxSessions: 1 });
      client().deliver(sessionChannel('s1'), CONSUMER_UUID, {
        ...fromConsumer('s1', 1, 'session_open'),
        seq,
      });
      await vi.waitFor(() => expect(api.getSession).toHaveBeenCalledWith('s1'));
      await new Promise((r) => setTimeout(r, 0));
      client().deliver(sessionChannel('s2'), CONSUMER_UUID, fromConsumer('s2', 1, 'session_open'));
      await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
      expect(client().ofType('session_ready')[0]).toMatchObject({ sessionId: 's2' });
      expect(client().ofType('session_failed')).toEqual([]);
    },
  );

  describe('when an exchange outcome is not delivered', () => {
    const holdThenFailOutcomes = (client: FakeRouteClient) => {
      const release = deferred();
      const publish = client.publish.getMockImplementation()!;
      client.publish.mockImplementation(async (params) => {
        if (params.message.type !== 'exchange_outcome') return publish(params);
        await release.promise;
        throw new Error('publish 503');
      });
      return release;
    };
    const outcomeAttempts = (client: FakeRouteClient) =>
      client.publish.mock.calls.filter(([p]) => p.message.type === 'exchange_outcome').length;

    it('fails a close that arrives while the outcome is publishing', async () => {
      const { client, open } = await startRuntime({ onExchange: () => ({}) });
      const release = holdThenFailOutcomes(client());
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(outcomeAttempts(client())).toBe(1));
      client().deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 2, 'session_close'));
      release.resolve();

      await vi.waitFor(() => expect(client().ofType('session_failed')).toHaveLength(1));
      expect(client().ofType('session_failed')[0]).toMatchObject({ error: 'provider_error' });
      expect(client().ofType('session_closed')).toEqual([]);
    });

    it('fails a close whose canceled outcome cannot be published', async () => {
      const { client, open } = await startRuntime({ onExchange: () => new Promise(() => {}) });
      failing(client(), 'exchange_outcome', 2);
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(client().ofType('session_ready')).toHaveLength(1));
      client().deliver(CHANNEL, CONSUMER_UUID, fromConsumer(SESSION, 2, 'session_close'));

      await vi.waitFor(() => expect(client().ofType('session_failed')).toHaveLength(1));
      expect(client().ofType('session_failed')[0]).toMatchObject({ error: 'provider_error' });
      expect(client().ofType('session_closed')).toEqual([]);
    });

    it('does not retry the publish once a backend notice ends the session', async () => {
      const reasons: string[] = [];
      const { client, open } = await startRuntime({
        onExchange: () => ({}),
        onSessionClose: (_ctx, reason) => {
          reasons.push(reason);
        },
      });
      const release = holdThenFailOutcomes(client());
      open({ exchangeId: 'ex1', requestParts: [] });
      await vi.waitFor(() => expect(outcomeAttempts(client())).toBe(1));
      client().deliver(CHANNEL, BACKEND_UUID, {
        ...fromConsumer(SESSION, 0, 'session_failed'),
        error: 'route_lost',
      });
      await vi.waitFor(() => expect(reasons).toEqual(['route_lost']));
      release.resolve();
      await new Promise((r) => setTimeout(r, 20));

      expect(outcomeAttempts(client())).toBe(1);
    });
  });
});
