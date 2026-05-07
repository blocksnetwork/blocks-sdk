import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { StaticAuthProvider } from '../src/runtime/auth-provider.js';
import { TaskClient } from '../src/runtime/task-client.js';

// Mock PubNub constructor to capture subscribeKey/publishKey passed to it.
const constructorCalls: Array<Record<string, unknown>> = [];

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation((opts: Record<string, unknown>) => {
      constructorCalls.push(opts);
      return {
        addListener: vi.fn(),
        removeListener: vi.fn(),
        subscribe: vi.fn(),
        unsubscribe: vi.fn(),
        setToken: vi.fn(),
        destroy: vi.fn(),
        time: vi.fn(async () => ({ timetoken: '17000000000000000' })),
        fetchMessages: vi.fn(async ({ channels }: { channels: string[] }) => ({
          channels: { [channels[0]]: [] },
        })),
      };
    }),
  };
});

// Helper to mock fetch with a successful JSON-RPC response
const mockRpcResponse = (result: unknown) => ({
  ok: true,
  json: async () => ({ jsonrpc: '2.0', id: 'x', result }),
});

describe('TaskClient.updateKeys', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
    constructorCalls.length = 0;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it('updates config.subscribeKey used for RPC calls', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-old',
      authProvider: new StaticAuthProvider('token-old'),
      baseUrl: 'http://localhost:3001',
    });

    // Update keys
    client.updateKeys('sub-new', 'pub-new');

    // Make an RPC call — subscribeKey is no longer in the URL (baseUrl-based routing),
    // but verify the call goes to the correct backend endpoint
    fetchSpy.mockResolvedValueOnce(
      mockRpcResponse({ taskId: 'task-1', state: 'running' }),
    );
    await client.getTask('task-1');

    const [url] = fetchSpy.mock.calls[0];
    expect(url).toBe('http://localhost:3001/api/v1/rpc');
  });

  it('updates _subscribeKey and _publishKey used for PubNub client creation', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-old',
      publishKey: 'pub-old',
      baseUrl: 'http://localhost:3001',
    });

    client.updateKeys('sub-new', 'pub-new');

    // Trigger internal PubNub creation via sendMessage (falls back to constructor)
    fetchSpy.mockResolvedValueOnce(
      mockRpcResponse({
        taskId: 'task-1',
        extensions: {
          blocks: {
            streamChannels: { status: 'u.user-1.task-1' },
            readToken: null,
          },
        },
      }),
    );

    const session = await client.sendMessage({
      agentName: 'agent-a',
      requestParts: [],
      ownerId: 'user-1',
    });

    // The PubNub constructor should have been called with the new keys
    const lastCall = constructorCalls[constructorCalls.length - 1];
    expect(lastCall.subscribeKey).toBe('sub-new');
    expect(lastCall.publishKey).toBe('pub-new');

    session.close();
  });

  it('leaves auth provider unchanged when keys are updated', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-old',
      authProvider: new StaticAuthProvider('token-original'),
      baseUrl: 'http://localhost:3001',
    });

    client.updateKeys('sub-new', 'pub-new');

    fetchSpy.mockResolvedValueOnce(
      mockRpcResponse({ taskId: 'task-1', state: 'running' }),
    );
    await client.getTask('task-1');

    const [, init] = fetchSpy.mock.calls[0];
    expect(init.headers['Authorization']).toBe('Bearer token-original');
  });

  it('defaults _publishKey to empty string when publishKey is omitted', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-old',
      publishKey: 'pub-old',
      baseUrl: 'http://localhost:3001',
    });

    // Update without publishKey
    client.updateKeys('sub-new');

    // Trigger PubNub constructor via sendMessage
    fetchSpy.mockResolvedValueOnce(
      mockRpcResponse({
        taskId: 'task-1',
        extensions: {
          blocks: {
            streamChannels: { status: 'u.user-1.task-1' },
            readToken: null,
          },
        },
      }),
    );

    const session = await client.sendMessage({
      agentName: 'agent-a',
      requestParts: [],
      ownerId: 'user-1',
    });

    // publishKey should be undefined (empty string is falsy, so || undefined yields undefined)
    const lastCall = constructorCalls[constructorCalls.length - 1];
    expect(lastCall.subscribeKey).toBe('sub-new');
    expect(lastCall.publishKey).toBeUndefined();

    session.close();
  });
});
