import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import {
  CONSUMER_UUID,
  type FakeRouteClient,
  fakeRouteClient,
  fromConsumer,
  manifest,
  PROVIDER_UUID,
  routeGrant,
  sessionChannel,
} from './helpers/session-provider-fakes.js';
import { makeTestCard } from './helpers/test-card.js';

const AGENT_ID = 'cccccccc-3333-3333-3333-333333333333';
const CONTROL_CHANNEL = `agent.${AGENT_ID}.control`;

const clients: FakeRouteClient[] = [];
vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn((config: { userId?: string }) => {
    const client = fakeRouteClient(config.userId ?? '');
    clients.push(client);
    return client;
  }),
}));

interface Call {
  method: string;
  path: string;
  authorization: string | null;
  body: unknown;
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

function mockBackend(routeResponse: Response) {
  const calls: Call[] = [];
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input));
    const headers = new Headers(init?.headers);
    calls.push({
      method: init?.method ?? 'GET',
      path: url.pathname,
      authorization: headers.get('Authorization'),
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    if (url.pathname === '/api/v1/auth/agent/connect') {
      return json(200, {
        accessToken: 'agent-jwt',
        refreshToken: 'refresh',
        agentId: AGENT_ID,
        controlChannel: CONTROL_CHANNEL,
      });
    }
    if (url.pathname === '/api/v1/provider/routes') return routeResponse.clone();
    return json(200, {});
  }) as unknown as typeof fetch;
  return calls;
}

function controlPubNub() {
  const listeners: Array<{ message?: (event: unknown) => void }> = [];
  const pubnub = {
    publish: vi.fn(async () => ({ timetoken: '1' })),
    addListener: (l: { message?: (event: unknown) => void }) => listeners.push(l),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setFilterExpression: vi.fn(),
    setState: vi.fn(async () => ({})),
    setToken: vi.fn(),
    destroy: vi.fn(),
    _configuration: { keySet: { publishKey: 'pub', subscribeKey: 'sub' } },
  };
  const control = (message: unknown) => listeners[0].message?.({ message, userMetadata: {} });
  return { pubnub, control };
}

describe('startAgentInstance with session handlers', () => {
  const originalFetch = globalThis.fetch;
  let stop: (() => void) | undefined;
  beforeEach(() => {
    clients.length = 0;
    process.env.BLOCKS_API_KEY = 'bk_test';
  });
  afterEach(() => {
    stop?.();
    globalThis.fetch = originalFetch;
  });

  it('opens a route with the agent JWT and serves a session from a control-channel manifest', async () => {
    const calls = mockBackend(json(201, routeGrant()));
    const { pubnub, control } = controlPubNub();
    const handle = await startAgentInstance({
      pubnub: pubnub as never,
      agentName: 'echo_agent',
      card: makeTestCard(),
      baseUrl: 'http://backend.test',
      instanceId: 'AG-echo_agent-1',
      concurrency: 3,
      onExchange: () => ({}),
    });
    stop = handle.stop;

    await vi.waitFor(() => expect(clients.some((c) => c.userId === PROVIDER_UUID)).toBe(true));
    const route = calls.find((c) => c.path === '/api/v1/provider/routes');
    expect(route).toMatchObject({
      method: 'POST',
      authorization: 'Bearer agent-jwt',
      body: { instanceId: 'AG-echo_agent-1', maxSessions: 3 },
    });

    control({ type: 'SessionManifest', ...manifest('s1') });
    const routeClient = clients.find((c) => c.userId === PROVIDER_UUID) as FakeRouteClient;
    routeClient.deliver(sessionChannel('s1'), CONSUMER_UUID, fromConsumer('s1', 1, 'session_open'));

    await vi.waitFor(() => expect(routeClient.ofType('session_ready')).toHaveLength(1));
    expect(calls.some((c) => c.path.startsWith('/api/v1/provider/sessions/'))).toBe(false);
  });

  it('keeps serving tasks when the backend refuses the route with SESSIONS_DISABLED', async () => {
    const calls = mockBackend(json(404, { error: 'off', code: 'SESSIONS_DISABLED' }));
    const { pubnub, control } = controlPubNub();
    const handler = vi.fn(async () => ({}));
    const handle = await startAgentInstance({
      pubnub: pubnub as never,
      agentName: 'echo_agent',
      card: makeTestCard(),
      baseUrl: 'http://backend.test',
      handler,
      onExchange: () => ({}),
    });
    stop = handle.stop;

    await vi.waitFor(() =>
      expect(calls.some((c) => c.path === '/api/v1/provider/routes')).toBe(true),
    );
    control({ type: 'StartTask', taskId: 't1', ownerId: 'user-1', writeToken: 'w' });

    await vi.waitFor(() => expect(handler).toHaveBeenCalledTimes(1));
    expect(clients.some((c) => c.userId === PROVIDER_UUID)).toBe(false);
    expect(calls.filter((c) => c.path === '/api/v1/provider/routes')).toHaveLength(1);
  });

  it('opens no route when stopped before connect resolves', async () => {
    const calls = mockBackend(json(201, routeGrant()));
    const { pubnub } = controlPubNub();
    const handle = await startAgentInstance({
      pubnub: pubnub as never,
      agentName: 'echo_agent',
      card: makeTestCard(),
      baseUrl: 'http://backend.test',
      onExchange: () => ({}),
    });
    handle.stop();

    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(calls.some((c) => c.path === '/api/v1/provider/routes')).toBe(false);
  });
});
