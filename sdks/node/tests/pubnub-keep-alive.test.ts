import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const pubnubMocks = vi.hoisted(() => {
  const configs: Array<Record<string, unknown>> = [];
  const instance = {
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    setToken: vi.fn(),
    setFilterExpression: vi.fn(),
    destroy: vi.fn(),
    downloadFile: vi.fn().mockResolvedValue({ data: new Blob([new Uint8Array([1])]) }),
    time: vi.fn().mockResolvedValue({ timetoken: '17000000000000000' }),
    fetchMessages: vi.fn().mockResolvedValue({ channels: {} }),
  };
  return { configs, instance };
});

vi.mock('pubnub', () => ({
  default: vi.fn((config: Record<string, unknown>) => {
    pubnubMocks.configs.push(config);
    return pubnubMocks.instance;
  }),
}));

import type PubNub from 'pubnub';
import { createPubNubClient } from '../src/runtime/pubnub-client.js';
import { StreamClient } from '../src/stream/stream-client.js';
import { TaskClient } from '../src/runtime/task-client.js';
import { createPreClosedTaskSession, fileRef } from './fixtures/browser-smoke/runtime.js';

const rpcResult = (result: unknown) => ({
  ok: true,
  json: async () => ({ jsonrpc: '2.0', id: 'x', result }),
});

const SEND_MESSAGE_RESULT = {
  taskId: 'task-1',
  idempotent: false,
  queued: false,
  extensions: {
    blocks: {
      streamChannels: { status: 'u.user-1.task-1' },
      readToken: 'T4-read-token',
    },
  },
};

const sendOneMessage = (client: TaskClient) =>
  client.sendMessage({
    agentName: 'agent-b',
    requestParts: [{ type: 'text', text: 'Hello' }],
    ownerId: 'user-1',
  });

describe('every SDK PubNub construction path enables keep-alive', () => {
  const originalFetch = globalThis.fetch;
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    pubnubMocks.configs.length = 0;
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  const expectOnlyKeepAliveClients = () => {
    expect(pubnubMocks.configs.length).toBeGreaterThan(0);
    for (const config of pubnubMocks.configs) {
      expect(config.keepAlive).toBe(true);
      expect(config.keepAliveSettings).toEqual({ timeout: 30_000 });
    }
  };

  it('createPubNubClient', () => {
    createPubNubClient({ publishKey: 'pub', subscribeKey: 'sub' });
    expectOnlyKeepAliveClients();
  });

  it('StreamClient', () => {
    new StreamClient({
      subscribeKey: 'sub',
      publishKey: 'pub',
      token: 'stream-token',
      agentName: 'test_agent',
      streamId: 'my-stream',
    });
    expectOnlyKeepAliveClients();
  });

  it('TaskClient per-session client without a session factory', async () => {
    fetchSpy.mockResolvedValueOnce(rpcResult(SEND_MESSAGE_RESULT));
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub',
      baseUrl: 'http://localhost:3001',
    });
    (await sendOneMessage(client)).close();
    expectOnlyKeepAliveClients();
  });

  it('TaskClient.create session factory', async () => {
    fetchSpy
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          playground: { subscribeKey: 'sub-pg', publishKey: 'pub-pg' },
          network: { subscribeKey: 'sub-net', publishKey: 'pub-net' },
          api: { baseUrl: 'https://api.blocks.test' },
        }),
      })
      .mockResolvedValueOnce(rpcResult(SEND_MESSAGE_RESULT));
    const client = await TaskClient.create({ billingMode: 'free' });
    (await sendOneMessage(client)).close();
    expectOnlyKeepAliveClients();
  });

  it('createPubNubClient honours keepAlive: false for setProxy() callers', () => {
    createPubNubClient({ publishKey: 'pub', subscribeKey: 'sub', keepAlive: false });
    expect(pubnubMocks.configs).toHaveLength(1);
    expect(pubnubMocks.configs[0]?.keepAlive).toBe(false);
  });

  it('TaskClient builds no client of its own when the caller supplies the session client', async () => {
    fetchSpy.mockResolvedValueOnce(rpcResult(SEND_MESSAGE_RESULT));
    const callerClient = pubnubMocks.instance as unknown as PubNub;
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub',
      baseUrl: 'http://localhost:3001',
      createSessionPubNub: () => callerClient,
    });
    (await sendOneMessage(client)).close();
    expect(pubnubMocks.configs).toHaveLength(0);
  });

  it('TaskSession temporary download client', async () => {
    const session = createPreClosedTaskSession();
    await session.downloadArtifact(fileRef());
    session.close();
    expectOnlyKeepAliveClients();
  });
});
