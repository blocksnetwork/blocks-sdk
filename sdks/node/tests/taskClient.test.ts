import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { StaticAuthProvider } from '../src/runtime/auth-provider.js';
import {
  TaskClient,
} from '../src/runtime/task-client.js';
import { TaskSession } from '../src/runtime/task-session.js';

// Mock PubNub constructor so sendMessage() can create per-session clients
// without a real PubNub dependency.
let lastCreatedSessionPubNub: ReturnType<typeof createFakePubNub> | null = null;
const sessionPubNubInstances: Array<ReturnType<typeof createFakePubNub>> = [];

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation(() => {
      const fake = createFakePubNub();
      lastCreatedSessionPubNub = fake;
      sessionPubNubInstances.push(fake);
      return fake.pubnub;
    }),
  };
});

// Helper to mock fetch with a successful JSON-RPC response
const mockRpcResponse = (result: unknown) => ({
  ok: true,
  json: async () => ({ jsonrpc: '2.0', id: 'x', result }),
});

// Helper to create a fake PubNub client for subscribe tests
const createFakePubNub = (opts?: {
  historyMessages?: Array<{ message: unknown; timetoken: string }>;
  serverTimetoken?: string;
}) => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  const subscribedChannels: string[] = [];
  const unsubscribedChannels: string[] = [];
  const historyMessages = opts?.historyMessages ?? [];
  const serverTimetoken = opts?.serverTimetoken ?? '17000000000000000';

  const pubnub = {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    addListener: (l: any) => listeners.push(l),
    removeListener: vi.fn(),
    subscribe: vi.fn(({ channels }: { channels: string[] }) => {
      subscribedChannels.push(...channels);
    }),
    unsubscribe: vi.fn(({ channels }: { channels: string[] }) => {
      unsubscribedChannels.push(...channels);
    }),
    setToken: vi.fn(),
    destroy: vi.fn(),
    time: vi.fn(async () => ({ timetoken: serverTimetoken })),
    fetchMessages: vi.fn(async ({ channels }: { channels: string[] }) => {
      const ch = channels[0];
      return { channels: { [ch]: historyMessages } };
    }),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any;

  return { pubnub, listeners, subscribedChannels, unsubscribedChannels };
};

describe('TaskClient', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
    lastCreatedSessionPubNub = null;
    sessionPubNubInstances.length = 0;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  // ==========================================================================
  // sendMessage
  // ==========================================================================

  describe('sendMessage', () => {
    /** Helper: build a full SendMessage response with extensions.blocks */
    const fullResponse = (overrides?: Record<string, unknown>) => ({
      taskId: 'task-123',
      idempotent: false,
      queued: false,
      extensions: {
        blocks: {
          streamChannels: { status: 'u.user-1.task-123' },
          readToken: 'T4-read-token',
        },
      },
      ...overrides,
    });

    it('calls SendMessage RPC and returns TaskSession with T4 and statusChannel', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [{ type: 'text', text: 'Hello' }],
        ownerId: 'user-1',
      });

      expect(session).toBeInstanceOf(TaskSession);
      expect(session.taskId).toBe('task-123');
      expect(session.idempotent).toBe(false);
      expect(session.queued).toBe(false);

      // T4 readToken extracted from response
      expect(session.readToken).toBe('T4-read-token');

      // statusChannel from response extensions
      expect(session.statusChannel).toBe('u.user-1.task-123');

      // Per-session PubNub created (not the shared one)
      expect(lastCreatedSessionPubNub).not.toBeNull();
      const spn = lastCreatedSessionPubNub!;

      // T4 applied to the session PubNub
      expect(spn.pubnub.setToken).toHaveBeenCalledWith('T4-read-token');

      // Eagerly subscribed to the response-provided channel
      expect(spn.subscribedChannels).toContain('u.user-1.task-123');

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.method).toBe('SendMessage');
      expect(body.params.agentName).toBe('agent-b');
      expect(body.params.ownerId).toBe('user-1');

      session.close();

      // Session-owned PubNub destroyed on close
      expect(spn.pubnub.destroy).toHaveBeenCalled();
    });

    it('falls back to derived channel when extensions.blocks is absent', async () => {
      fetchSpy.mockResolvedValueOnce(
        mockRpcResponse({ taskId: 'task-no-ext', idempotent: false }),
      );

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      expect(session.statusChannel).toBe('u.user-1.task-no-ext');
      expect(session.readToken).toBeNull();

      const spn = lastCreatedSessionPubNub!;
      expect(spn.pubnub.setToken).not.toHaveBeenCalled();
      expect(spn.subscribedChannels).toContain('u.user-1.task-no-ext');

      session.close();
    });

    it('concurrent sessions get independent PubNub clients with different T4 tokens', async () => {
      // Session 1 with token-A
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({
        taskId: 'task-A',
        extensions: { blocks: {
          streamChannels: { status: 'u.user-1.task-A' },
          readToken: 'T4-token-A',
        }},
      })));
      // Session 2 with token-B
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({
        taskId: 'task-B',
        extensions: { blocks: {
          streamChannels: { status: 'u.user-1.task-B' },
          readToken: 'T4-token-B',
        }},
      })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session1 = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });
      const pn1 = sessionPubNubInstances[0];

      const session2 = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });
      const pn2 = sessionPubNubInstances[1];

      // Two distinct PubNub instances
      expect(pn1.pubnub).not.toBe(pn2.pubnub);

      // Each has its own T4 token
      expect(pn1.pubnub.setToken).toHaveBeenCalledWith('T4-token-A');
      expect(pn2.pubnub.setToken).toHaveBeenCalledWith('T4-token-B');

      // Each subscribes to its own channel
      expect(pn1.subscribedChannels).toContain('u.user-1.task-A');
      expect(pn2.subscribedChannels).toContain('u.user-1.task-B');

      // Sessions independent
      expect(session1.readToken).toBe('T4-token-A');
      expect(session2.readToken).toBe('T4-token-B');

      session1.close();
      session2.close();
    });

    it('uses params.ownerId over defaultOwnerId', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({ taskId: 'task-456' })));

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
        defaultOwnerId: 'default-user',
      });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'override-user',
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.ownerId).toBe('override-user');

      session.close();
    });

    it('forwards idempotencyKey on the wire when supplied', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        idempotencyKey: 'my-dedup-key',
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.idempotencyKey).toBe('my-dedup-key');
      // taskId must NOT be in the wire payload
      expect(body.params.taskId).toBeUndefined();

      session.close();
    });

    it('includes billingMode (free) on every SendMessage RPC params object', async () => {
      // BMC §6: billingMode is threaded into every SendMessage call so the
      // backend can compare against the agent's persisted mode.
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
      });
      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.billingMode).toBe('free');

      session.close();
    });

    it('includes billingMode (paid) on every SendMessage RPC params object', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const client = new TaskClient({
        billingMode: 'paid',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
      });
      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.billingMode).toBe('paid');

      session.close();
    });

    it('does not include idempotencyKey on the wire when not supplied', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.idempotencyKey).toBeUndefined();
      expect(body.params.taskId).toBeUndefined();

      session.close();
    });

    it('includes optional pushNotificationConfig and retryPolicy', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        pushNotificationConfig: { url: 'https://example.com/webhook' },
        retryPolicy: { maxRetries: 3, expiresAfterSec: 300 },
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.pushNotificationConfig).toEqual({ url: 'https://example.com/webhook' });
      expect(body.params.retryPolicy).toEqual({ maxRetries: 3, expiresAfterSec: 300 });

      session.close();
    });

    it('terminal idempotent hit creates a pre-closed TaskSession without PubNub allocation', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({
        idempotent: true,
        state: 'completed',
      })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
        idempotencyKey: 'dedup-key',
      });

      expect(session).toBeInstanceOf(TaskSession);
      expect(session.isClosed).toBe(true);
      expect(session.idempotent).toBe(true);
      expect(session.state).toBe('completed');
      expect(session.taskId).toBe('task-123');

      // No per-session PubNub should have been created for a terminal hit
      expect(lastCreatedSessionPubNub).toBeNull();
      expect(sessionPubNubInstances).toHaveLength(0);
    });

    it('pending idempotent hit creates a normal live TaskSession', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({
        idempotent: true,
        state: 'pending',
      })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
        idempotencyKey: 'dedup-key',
      });

      expect(session).toBeInstanceOf(TaskSession);
      expect(session.isClosed).toBe(false);
      expect(session.idempotent).toBe(true);
      expect(session.state).toBeUndefined();

      // Normal session should have subscribed
      const spn = lastCreatedSessionPubNub!;
      expect(spn.pubnub.subscribe).toHaveBeenCalled();

      session.close();
    });

    it('running idempotent hit creates a normal live TaskSession', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({
        idempotent: true,
        state: 'running',
      })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
        idempotencyKey: 'dedup-key',
      });

      expect(session).toBeInstanceOf(TaskSession);
      expect(session.isClosed).toBe(false);
      expect(session.idempotent).toBe(true);
      expect(session.state).toBeUndefined();

      // Normal session should have subscribed
      const spn = lastCreatedSessionPubNub!;
      expect(spn.pubnub.subscribe).toHaveBeenCalled();

      session.close();
    });

    it('failed idempotent hit creates a pre-closed TaskSession', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({
        idempotent: true,
        state: 'failed',
      })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
        idempotencyKey: 'dedup-key',
      });

      expect(session).toBeInstanceOf(TaskSession);
      expect(session.isClosed).toBe(true);
      expect(session.idempotent).toBe(true);
      expect(session.state).toBe('failed');
    });

    it('canceled idempotent hit creates a pre-closed TaskSession', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({
        idempotent: true,
        state: 'canceled',
      })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
        idempotencyKey: 'dedup-key',
      });

      expect(session).toBeInstanceOf(TaskSession);
      expect(session.isClosed).toBe(true);
      expect(session.idempotent).toBe(true);
      expect(session.state).toBe('canceled');
    });

    it('includes extensions.blocks.taskKind and duration for pipe tasks', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({ taskId: 'pipe-task' })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        taskKind: 'pipe',
        duration: 15,
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.extensions).toEqual({
        blocks: {
          taskKind: 'pipe',
          duration: 15,
        },
      });

      session.close();
    });

    it('includes extensions.blocks.taskKind for explicit request tasks', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({ taskId: 'request-task' })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        taskKind: 'request',
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.extensions).toEqual({
        blocks: {
          taskKind: 'request',
        },
      });

      session.close();
    });

    it('rejects pipe tasks without duration before sending RPC', async () => {
      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      await expect(client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        taskKind: 'pipe',
      })).rejects.toThrow('Pipe tasks require a duration between 1 and 43200 minutes');

      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it('rejects pipe task with duration 0 before sending RPC', async () => {
      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      await expect(client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        taskKind: 'pipe',
        duration: 0,
      })).rejects.toThrow('Pipe tasks require a duration between 1 and 43200 minutes');

      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it('rejects pipe task with duration exceeding 43200 before sending RPC', async () => {
      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      await expect(client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        taskKind: 'pipe',
        duration: 43201,
      })).rejects.toThrow('Pipe tasks require a duration between 1 and 43200 minutes');

      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it('accepts pipe task with duration 1 (lower bound)', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({ taskId: 'pipe-min' })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        taskKind: 'pipe',
        duration: 1,
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.extensions.blocks.duration).toBe(1);

      session.close();
    });

    it('accepts pipe task with duration 43200 (upper bound)', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({ taskId: 'pipe-max' })));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        taskKind: 'pipe',
        duration: 43200,
      });

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.params.extensions.blocks.duration).toBe(43200);

      session.close();
    });

    it('rejects request task with duration present before sending RPC', async () => {
      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      await expect(client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        taskKind: 'request',
        duration: 5,
      })).rejects.toThrow('Request tasks must not include a duration');

      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it('rejects duration without pipe taskKind before sending RPC', async () => {
      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      await expect(client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
        duration: 5,
      })).rejects.toThrow('Request tasks must not include a duration');

      expect(fetchSpy).not.toHaveBeenCalled();
    });

    it('includes Authorization header when authProvider is set', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse({ taskId: 'task-789' })));

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
        authProvider: new StaticAuthProvider('jwt-token-123'),
      });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'test-user',
      });

      const [, init] = fetchSpy.mock.calls[0];
      expect(init.headers['Authorization']).toBe('Bearer jwt-token-123');

      session.close();
    });

    it('carries pushConfigId from RPC response', async () => {
      fetchSpy.mockResolvedValueOnce(
        mockRpcResponse(fullResponse({ pushConfigId: 'push-123' })),
      );

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      expect(session.pushConfigId).toBe('push-123');

      session.close();
    });

    it('cancel() calls CancelTask RPC', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });
      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      fetchSpy.mockResolvedValueOnce(mockRpcResponse(undefined));
      await session.cancel();

      const body = JSON.parse(fetchSpy.mock.calls[1][1].body);
      expect(body.method).toBe('CancelTask');
      expect(body.params.taskId).toBe('task-123');

      session.close();
    });

    it('terminate() calls TerminateTask RPC', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });
      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      fetchSpy.mockResolvedValueOnce(mockRpcResponse(undefined));
      await session.terminate();

      const body = JSON.parse(fetchSpy.mock.calls[1][1].body);
      expect(body.method).toBe('TerminateTask');
      expect(body.params.taskId).toBe('task-123');

      session.close();
    });

    // ========================================================================
    // BN-455: subscribe race condition fix — history-based catch-up
    // ========================================================================

    it('fetches history after RPC to catch events from fast handlers (BN-455)', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [{ type: 'text', text: 'Hello' }],
        ownerId: 'user-1',
      });

      const spn = lastCreatedSessionPubNub!;
      expect(spn.pubnub.time).toHaveBeenCalled();
      expect(spn.pubnub.fetchMessages).toHaveBeenCalled();

      session.close();
    });

    it('returns pre-closed session when history shows terminal event (fast handler)', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const terminalHistory = [
        { message: { type: 'terminal', taskId: 'task-123', state: 'completed' }, timetoken: '17000000000000001' },
      ];

      // Override the PubNub mock constructor to return a client with history
      const { default: PubNubMock } = await import('pubnub');
      const fakePnWithHistory = createFakePubNub({ historyMessages: terminalHistory });
      (PubNubMock as ReturnType<typeof vi.fn>).mockImplementationOnce(() => fakePnWithHistory.pubnub);

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      expect(session.state).toBe('completed');
      // Subscribe should NOT have been called — terminal sessions skip subscription
      expect(fakePnWithHistory.pubnub.subscribe).not.toHaveBeenCalled();

      session.close();
    });

    it('subscribes from history high-water mark when events exist but task is not terminal', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const progressHistory = [
        { message: { type: 'progress', taskId: 'task-123', progress: 50 }, timetoken: '17000000000000005' },
      ];

      const { default: PubNubMock } = await import('pubnub');
      const fakePn = createFakePubNub({ historyMessages: progressHistory });
      (PubNubMock as ReturnType<typeof vi.fn>).mockImplementationOnce(() => fakePn.pubnub);

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      // Should subscribe from the history high-water mark timetoken
      expect(fakePn.pubnub.subscribe).toHaveBeenCalledWith({
        channels: ['u.user-1.task-123'],
        timetoken: '17000000000000005',
      });

      session.close();
    });

    it('uses server timetoken as subscribe cursor when history is empty', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const { default: PubNubMock } = await import('pubnub');
      const fakePn = createFakePubNub({ historyMessages: [], serverTimetoken: '17000000099999999' });
      (PubNubMock as ReturnType<typeof vi.fn>).mockImplementationOnce(() => fakePn.pubnub);

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      // Should use server timetoken since history was empty
      expect(fakePn.pubnub.subscribe).toHaveBeenCalledWith({
        channels: ['u.user-1.task-123'],
        timetoken: '17000000099999999',
      });

      session.close();
    });

    it('falls back to basic session if history fetch throws', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const { default: PubNubMock } = await import('pubnub');
      const fakePn = createFakePubNub();
      fakePn.pubnub.time = vi.fn(async () => { throw new Error('time failed'); });
      (PubNubMock as ReturnType<typeof vi.fn>).mockImplementationOnce(() => fakePn.pubnub);

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      expect(session.taskId).toBe('task-123');
      expect(fakePn.pubnub.destroy).not.toHaveBeenCalled();
      session.close();
    });

    it('pre-populates artifacts from history into the session', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse()));

      const historyWithArtifact = [
        {
          message: {
            type: 'artifact',
            taskId: 'task-123',
            artifactRef: { kind: 'inline', mimeType: 'text/plain', data: 'SGVsbG8=' },
          },
          timetoken: '17000000000000002',
        },
        {
          message: { type: 'terminal', taskId: 'task-123', state: 'completed' },
          timetoken: '17000000000000003',
        },
      ];

      const { default: PubNubMock } = await import('pubnub');
      const fakePn = createFakePubNub({ historyMessages: historyWithArtifact });
      (PubNubMock as ReturnType<typeof vi.fn>).mockImplementationOnce(() => fakePn.pubnub);

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      expect(session.state).toBe('completed');
      const artifacts = session.listArtifacts();
      expect(artifacts).toHaveLength(1);
      expect(artifacts[0].kind).toBe('inline');

      session.close();
    });
  });

  // ==========================================================================
  // Task lifecycle methods
  // ==========================================================================

  describe('task lifecycle', () => {
    it('getTask calls GetTask RPC and unwraps { task } envelope', async () => {
      fetchSpy.mockResolvedValueOnce(
        mockRpcResponse({ task: {
          taskId: 'task-1',
          state: 'running',
          owner: 'user-1',
          agentName: 'echo',
          createdTime: '2024-01-01T00:00:00Z',
          updatedTime: '2024-01-02T00:00:00Z',
        } }),
      );

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });
      const result = await client.getTask('task-1');

      expect(result.taskId).toBe('task-1');
      expect(result.state).toBe('running');
      expect(result.owner).toBe('user-1');
      expect(result.agentName).toBe('echo');
      expect(result.createdTime).toBe('2024-01-01T00:00:00Z');
      expect(result.updatedTime).toBe('2024-01-02T00:00:00Z');

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.method).toBe('GetTask');
      expect(body.params.taskId).toBe('task-1');
    });

    it('listTasks calls ListTasks RPC', async () => {
      fetchSpy.mockResolvedValueOnce(
        mockRpcResponse({ tasks: [{ taskId: 'a' }, { taskId: 'b' }], totalCount: 2 }),
      );

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });
      const result = await client.listTasks({ ownerId: 'user-1', limit: 10 });

      expect(result.tasks).toHaveLength(2);
      expect(result.totalCount).toBe(2);

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.method).toBe('ListTasks');
      expect(body.params.ownerId).toBe('user-1');
      expect(body.params.limit).toBe(10);
    });

    it('cancelTask calls CancelTask RPC', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(undefined));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });
      await client.cancelTask('task-1');

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.method).toBe('CancelTask');
      expect(body.params.taskId).toBe('task-1');
    });

    it.each([
      ['pauseTask', 'PauseTask'],
      ['resumeTask', 'ResumeTask'],
      ['retryTask', 'RetryTask'],
      ['terminateTask', 'TerminateTask'],
    ] as const)('%s calls %s RPC', async (method, rpcMethod) => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(undefined));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      await (client as any)[method]('task-1');

      const body = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(body.method).toBe(rpcMethod);
      expect(body.params.taskId).toBe('task-1');
    });
  });

  // ==========================================================================
  // subscribeToTask
  // ==========================================================================

  describe('subscribeToTask', () => {
    it('subscribes to the correct channel', () => {
      const { pubnub, subscribedChannels } = createFakePubNub();

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        pubnub,
      });

      const sub = client.subscribeToTask('task-1', 'owner-1', {});
      expect(subscribedChannels).toContain('u.owner-1.task-1');

      sub.unsubscribe();
    });

    it('throws without pubnub or createPubNub', () => {
      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });
      expect(() => client.subscribeToTask('task-1', 'owner-1', {})).toThrow(
        'TaskClient requires a pubnub instance for subscribe',
      );
    });

    it('dispatches events to typed callbacks', () => {
      const { pubnub, listeners } = createFakePubNub();

      const onProgress = vi.fn();
      const onArtifact = vi.fn();
      const onTerminal = vi.fn();
      const onSystem = vi.fn();
      const onEvent = vi.fn();

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        pubnub,
      });

      client.subscribeToTask('task-1', 'owner-1', {
        onProgress,
        onArtifact,
        onTerminal,
        onSystem,
        onEvent,
      });

      const listener = listeners[listeners.length - 1];
      const channel = 'u.owner-1.task-1';

      // Progress event
      listener.message({ channel, message: { type: 'progress', taskId: 'task-1', progress: 50 } });
      expect(onProgress).toHaveBeenCalledTimes(1);
      expect(onEvent).toHaveBeenCalledTimes(1);

      // Artifact event
      listener.message({ channel, message: { type: 'artifact', taskId: 'task-1' } });
      expect(onArtifact).toHaveBeenCalledTimes(1);
      expect(onEvent).toHaveBeenCalledTimes(2);

      // Terminal event
      listener.message({ channel, message: { type: 'terminal', taskId: 'task-1', state: 'completed' } });
      expect(onTerminal).toHaveBeenCalledTimes(1);
      expect(onEvent).toHaveBeenCalledTimes(3);

      // System event
      listener.message({ channel, message: { type: 'system', taskId: 'task-1', status: 'paused' } });
      expect(onSystem).toHaveBeenCalledTimes(1);
      expect(onEvent).toHaveBeenCalledTimes(4);
    });

    it('ignores messages from other channels', () => {
      const { pubnub, listeners } = createFakePubNub();
      const onProgress = vi.fn();

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        pubnub,
      });

      client.subscribeToTask('task-1', 'owner-1', { onProgress });

      const listener = listeners[listeners.length - 1];
      listener.message({
        channel: 'u.owner-1.other-task',
        message: { type: 'progress', taskId: 'other-task' },
      });

      expect(onProgress).not.toHaveBeenCalled();
    });

    it('ignores messages without type', () => {
      const { pubnub, listeners } = createFakePubNub();
      const onEvent = vi.fn();

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        pubnub,
      });

      client.subscribeToTask('task-1', 'owner-1', { onEvent });

      const listener = listeners[listeners.length - 1];
      listener.message({ channel: 'u.owner-1.task-1', message: { foo: 'bar' } });
      listener.message({ channel: 'u.owner-1.task-1', message: null });

      expect(onEvent).not.toHaveBeenCalled();
    });

    it('unsubscribe cleans up listener and channel', () => {
      const { pubnub, listeners, unsubscribedChannels } = createFakePubNub();

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        pubnub,
      });

      const sub = client.subscribeToTask('task-1', 'owner-1', {});
      sub.unsubscribe();

      expect(pubnub.removeListener).toHaveBeenCalledWith(listeners[listeners.length - 1]);
      expect(unsubscribedChannels).toContain('u.owner-1.task-1');
    });
  });

  // ==========================================================================
  // createPubNub factory (lazy init)
  // ==========================================================================

  describe('createPubNub factory', () => {
    it('lazily creates PubNub on first subscribe call', () => {
      const { pubnub: fakePn, subscribedChannels } = createFakePubNub();
      const factory = vi.fn(() => fakePn);

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        createPubNub: factory,
      });

      // Factory not called yet
      expect(factory).not.toHaveBeenCalled();

      // First subscribe triggers factory
      const sub = client.subscribeToTask('task-1', 'owner-1', {});
      expect(factory).toHaveBeenCalledTimes(1);
      expect(subscribedChannels).toContain('u.owner-1.task-1');

      sub.unsubscribe();
    });

    it('factory is only called once across multiple subscribes', () => {
      const { pubnub: fakePn } = createFakePubNub();
      const factory = vi.fn(() => fakePn);

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        createPubNub: factory,
      });

      const sub1 = client.subscribeToTask('task-1', 'owner-1', {});
      const sub2 = client.subscribeToTask('task-2', 'owner-1', {});

      expect(factory).toHaveBeenCalledTimes(1);

      sub1.unsubscribe();
      sub2.unsubscribe();
    });

    it('direct pubnub takes precedence over createPubNub', () => {
      const { pubnub: directPn, subscribedChannels } = createFakePubNub();
      const { pubnub: factoryPn } = createFakePubNub();
      const factory = vi.fn(() => factoryPn);

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        pubnub: directPn,
        createPubNub: factory,
      });

      const sub = client.subscribeToTask('task-1', 'owner-1', {});

      // Factory should never be called when direct pubnub is provided
      expect(factory).not.toHaveBeenCalled();
      expect(subscribedChannels).toContain('u.owner-1.task-1');

      sub.unsubscribe();
    });

    it('sendMessage uses createSessionPubNub factory when provided', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(
        { taskId: 'task-factory', extensions: { blocks: {
          streamChannels: { status: 'u.user-1.task-factory' },
          readToken: 'T4-f',
        }}},
      ));

      const sessionFake = createFakePubNub();
      const sessionFactory = vi.fn(() => sessionFake.pubnub);

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
        createSessionPubNub: sessionFactory,
      });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      // Session factory IS called
      expect(sessionFactory).toHaveBeenCalledTimes(1);

      // PubNub constructor mock NOT used (session factory took precedence)
      expect(lastCreatedSessionPubNub).toBeNull();

      // Token applied to the factory-created client
      expect(sessionFake.pubnub.setToken).toHaveBeenCalledWith('T4-f');
      expect(sessionFake.subscribedChannels).toContain('u.user-1.task-factory');

      session.close();
      expect(sessionFake.pubnub.destroy).toHaveBeenCalled();
    });

    it('sendMessage does NOT use shared createPubNub for session creation', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(
        { taskId: 'task-sep', extensions: { blocks: {
          streamChannels: { status: 'u.user-1.task-sep' },
          readToken: 'T4-sep',
        }}},
      ));

      const sharedFactory = vi.fn();

      // Only shared factory, no session factory — should fall back to internal constructor
      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
        createPubNub: sharedFactory,
      });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      // Shared factory NOT called by sendMessage
      expect(sharedFactory).not.toHaveBeenCalled();

      // Internal PubNub constructor used instead
      expect(lastCreatedSessionPubNub).not.toBeNull();
      expect(lastCreatedSessionPubNub!.pubnub.setToken).toHaveBeenCalledWith('T4-sep');

      session.close();
    });

    it('createSessionPubNub factory called once per session, not cached', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(
        { taskId: 'task-S1', extensions: { blocks: {
          streamChannels: { status: 'u.user-1.task-S1' },
          readToken: 'T4-S1',
        }}},
      ));
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(
        { taskId: 'task-S2', extensions: { blocks: {
          streamChannels: { status: 'u.user-1.task-S2' },
          readToken: 'T4-S2',
        }}},
      ));

      const fake1 = createFakePubNub();
      const fake2 = createFakePubNub();
      const sessionFactory = vi.fn()
        .mockReturnValueOnce(fake1.pubnub)
        .mockReturnValueOnce(fake2.pubnub);

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
        createSessionPubNub: sessionFactory,
      });

      const session1 = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });
      const session2 = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      expect(sessionFactory).toHaveBeenCalledTimes(2);
      expect(fake1.pubnub).not.toBe(fake2.pubnub);
      expect(fake1.pubnub.setToken).toHaveBeenCalledWith('T4-S1');
      expect(fake2.pubnub.setToken).toHaveBeenCalledWith('T4-S2');

      session1.close();
      session2.close();
    });

    it('sendMessage falls back to PubNub constructor when no session factory provided', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(
        { taskId: 'task-fallback', extensions: { blocks: {
          streamChannels: { status: 'u.user-1.task-fallback' },
          readToken: 'T4-fb',
        }}},
      ));

      const client = new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      expect(lastCreatedSessionPubNub).not.toBeNull();
      expect(lastCreatedSessionPubNub!.pubnub.setToken).toHaveBeenCalledWith('T4-fb');
      expect(lastCreatedSessionPubNub!.subscribedChannels).toContain('u.user-1.task-fallback');

      session.close();
    });

    it('shared TaskClient pubnub is never mutated by sendMessage', async () => {
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(
        { taskId: 'task-shared', extensions: { blocks: {
          streamChannels: { status: 'u.user-1.task-shared' },
          readToken: 'T4-shared',
        }}},
      ));

      const sharedFake = createFakePubNub();
      const sessionFake = createFakePubNub();
      const sessionFactory = vi.fn(() => sessionFake.pubnub);

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
        pubnub: sharedFake.pubnub,
        createSessionPubNub: sessionFactory,
      });

      const session = await client.sendMessage({
        agentName: 'agent-b',
        requestParts: [],
        ownerId: 'user-1',
      });

      // Shared PubNub untouched
      expect(sharedFake.pubnub.setToken).not.toHaveBeenCalled();
      expect(sharedFake.subscribedChannels).not.toContain('u.user-1.task-shared');

      // Session factory client used
      expect(sessionFake.pubnub.setToken).toHaveBeenCalledWith('T4-shared');
      expect(sessionFake.subscribedChannels).toContain('u.user-1.task-shared');

      session.close();
    });
  });
});
