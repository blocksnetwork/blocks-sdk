import { describe, expect, it, vi, beforeAll, afterAll } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';

// Mock global fetch so connectAgent resolves quickly in tests.
// The connect response must include controlChannel so the agent instance
// knows which channel to subscribe/setState on.
const TEST_AGENT_ID = 'cccccccc-3333-3333-3333-333333333333';
const originalFetch = globalThis.fetch;
beforeAll(() => {
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({
      accessToken: 'mock-jwt',
      refreshToken: 'mock-refresh',
      expiresIn: 3600,
      agentId: TEST_AGENT_ID,
      controlChannel: `agent.${TEST_AGENT_ID}.control`,
    }),
  }) as unknown as typeof fetch;
});
afterAll(() => {
  globalThis.fetch = originalFetch;
});

// Shared publish mock used by all per-task PubNub instances
// so old tests can inspect publishes from a single source.
let sharedPublish = vi.fn(async () => ({ timetoken: Date.now().toString() }));

// Mock createPubNubClient so per-task clients use shared publish mock
vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => ({
    publish: (...args: unknown[]) => sharedPublish(...args),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    setFilterExpression: vi.fn(),
    setToken: vi.fn(),
    setState: vi.fn(async () => ({})),
    destroy: vi.fn(),
    hereNow: vi.fn(async () => ({ channels: {} })),
  })),
}));

/**
 * Agent Instance tests:
 * - Agent instance no longer uses defaultTaskIndexStore (registry updates via fan-out Function)
 * - Agent instance no longer publishes to obs.{agentName}.log (obs fan-out via Function)
 * - Agent instance publishes ONLY to task channels (u.{ownerId}.{taskId})
 */

const createFakePubNub = () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  // Reset shared mocks for each test
  sharedPublish = vi.fn(async () => ({ timetoken: Date.now().toString() }));
  const pubnub = {
    publish: sharedPublish,
    addMessageAction: vi.fn().mockResolvedValue({}),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    addListener: (l: any) => listeners.push(l),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setFilterExpression: vi.fn(),
    setState: vi.fn().mockResolvedValue({}),
    setToken: vi.fn(),
    destroy: vi.fn(),
    _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any;
  return { pubnub, listeners };
};

describe('agent instance harness', () => {
  it('throws error when agentName is not provided', async () => {
    const { pubnub } = createFakePubNub();
    await expect(startAgentInstance({ pubnub } as Record<string, unknown>)).rejects.toThrow(
      'agentName is required: provide opts.agentName',
    );
  });

  it('throws error when agentName contains a dot', async () => {
    const { pubnub } = createFakePubNub();
    await expect(startAgentInstance({ pubnub, agentName: 'acme.echo', card: makeTestCard() })).rejects.toThrow(
      'agentName must contain only alphanumeric characters and underscores (no hyphens)',
    );
  });

  it('subscribes and handles StartTask with explicit agentName and heartbeat', async () => {
    const { pubnub, listeners } = createFakePubNub();
    // NOTE: No longer need to add task to index - agent instance publishes to task channel only
    const { stop } = await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard(), baseUrl: 'http://test' });
    // Subscribe now happens asynchronously after registration attempt (in .then())
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());
    expect(pubnub.subscribe).toHaveBeenCalledWith({
      channels: [`agent.${TEST_AGENT_ID}.control`],
    });
    const listener = listeners[0];
    listener.message({ message: { type: 'StartTask', taskId: 'w1', ownerId: 'user1' } });
    // Wait for fire-and-forget task to complete
    await vi.waitFor(() => expect(pubnub.publish).toHaveBeenCalled());
    stop();
  });

  it('publishes to user-scoped task channel with ownerId from callerClaims', async () => {
    const { pubnub, listeners } = createFakePubNub();
    // NOTE: No longer need to add task to index - agent instance publishes to task channel only
    const { stop } = await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard() });
    const listener = listeners[0];

    // Send task with callerClaims.sub
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'owner-task',
        ownerId: 'alice',
        callerClaims: { sub: 'alice' },
      },
    });

    // Wait for publish to be called
    await vi.waitFor(() => {
      const publishCalls = pubnub.publish.mock.calls;
      // Find a publish to the user-scoped channel
      const taskChannelPublish = publishCalls.find((call: unknown[]) => {
        const args = call[0] as { channel?: string };
        return args.channel === 'u.alice.owner-task';
      });
      expect(taskChannelPublish).toBeDefined();
    });

    stop();
  });

  it('uses anonymous owner when callerClaims.sub is missing', async () => {
    const { pubnub, listeners } = createFakePubNub();
    // NOTE: No longer need to add task to index - agent instance publishes to task channel only
    const { stop } = await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard() });
    const listener = listeners[0];

    // Send task without callerClaims
    listener.message({
      message: { type: 'StartTask', taskId: 'anon-task', ownerId: 'anonymous' },
    });

    // Wait for publish to be called
    await vi.waitFor(() => {
      const publishCalls = pubnub.publish.mock.calls;
      // Find a publish to the anonymous user channel
      const taskChannelPublish = publishCalls.find((call: unknown[]) => {
        const args = call[0] as { channel?: string };
        return args.channel === 'u.anonymous.anon-task';
      });
      expect(taskChannelPublish).toBeDefined();
    });

    stop();
  });

  it('includes agentName in publish meta for subscribe filtering', async () => {
    const { pubnub, listeners } = createFakePubNub();
    // NOTE: No longer need to add task to index - agent instance publishes to task channel only
    const { stop } = await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard() });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'meta-task',
        ownerId: 'bob',
        callerClaims: { sub: 'bob' },
      },
    });

    // Wait for publish and verify meta includes agentName
    await vi.waitFor(() => {
      const publishCalls = pubnub.publish.mock.calls;
      const taskChannelPublish = publishCalls.find((call: unknown[]) => {
        const args = call[0] as { channel?: string; meta?: { agentName?: string; taskId?: string } };
        return (
          args.channel === 'u.bob.meta-task' &&
          args.meta?.agentName === 'acme_echo' &&
          args.meta?.taskId === 'meta-task'
        );
      });
      expect(taskChannelPublish).toBeDefined();
    });

    stop();
  });

  it('uses provided token when set', async () => {
    const { pubnub } = createFakePubNub();
    pubnub.setToken = vi.fn();
    await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard(), token: 'pam-token' });
    expect(pubnub.setToken).toHaveBeenCalledWith('pam-token');
  });

  it('fails fast for large artifact when no baseUrl is configured', async () => {
    const { pubnub, listeners } = createFakePubNub();
    // Large artifacts without baseUrl must throw instead of silently inlining
    const mockHandler = vi
      .fn()
      .mockResolvedValue({
        artifacts: [{ data: Buffer.alloc(64 * 1024), mimeType: 'application/json' }],
      });
    await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard(), handler: mockHandler });
    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'w2',
        ownerId: 'user1',
        callerClaims: { sub: 'user1' },
      },
    });
    // Wait for handler to complete
    await vi.waitFor(() => expect(mockHandler).toHaveBeenCalled());
    // Verify terminal is published as failed (artifact publish throws, handler errors out)
    await vi.waitFor(() => {
      const terminalCall = sharedPublish.mock.calls.find(
        (call: unknown[]) => {
          const msg = (call[0] as { message?: { type?: string; state?: string } })?.message;
          return msg?.type === 'terminal' && msg?.state === 'failed';
        },
      );
      expect(terminalCall).toBeDefined();
    });
    // Verify no artifact event was published (the error prevented it)
    const artifactCall = sharedPublish.mock.calls.find(
      (call: unknown[]) => (call[0] as { message?: { type?: string } })?.message?.type === 'artifact',
    );
    expect(artifactCall).toBeUndefined();
  });

  it('sets subscribe filter expression by default (expectedInstances >= 1)', async () => {
    const { pubnub } = createFakePubNub();
    const { instanceId } = await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard(), baseUrl: 'http://test' });
    // Registration is fire-and-forget — wait for setFilterExpression to be called
    await vi.waitFor(() => {
      expect(pubnub.setFilterExpression).toHaveBeenCalledWith(
        `meta.instance == '${instanceId}' || meta.broadcast == "true"`,
      );
    });
  });

  it('skips subscribe filter expression when expectedInstances is 0 (opt-out)', async () => {
    const { pubnub } = createFakePubNub();
    await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard(), expectedInstances: 0, baseUrl: 'http://test' });
    // Wait for registration to complete, then verify filter was NOT set
    await new Promise((r) => setTimeout(r, 50));
    expect(pubnub.setFilterExpression).not.toHaveBeenCalled();
  });

  it('single-instance: accepts messages with null meta', async () => {
    const { pubnub, listeners } = createFakePubNub();
    await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard() });
    const listener = listeners[0];

    listener.message({
      message: { type: 'StartTask', taskId: 'single-null-task', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const publish = pubnub.publish.mock.calls.find((call: unknown[]) => {
        const args = call[0] as { message?: { taskId?: string } };
        return args.message?.taskId === 'single-null-task';
      });
      expect(publish).toBeDefined();
    });
  });

  it('rejects task when at capacity (NACK)', async () => {
    const { pubnub, listeners } = createFakePubNub();
    // NOTE: No longer need to add task to index - agent instance publishes to task channel only

    // Start agent instance with concurrency: 1 (default)
    const slowHandler = vi
      .fn()
      .mockImplementation(() => new Promise((resolve) => setTimeout(() => resolve({}), 100)));
    await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard(), concurrency: 1, handler: slowHandler });
    const listener = listeners[0];

    // Start first task (will occupy the only thread) - fire-and-forget
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'capacity-task',
        ownerId: 'user1',
        callerClaims: { sub: 'user1' },
      },
    });

    // Small delay to ensure first task is processing
    await new Promise((r) => setTimeout(r, 10));

    // Try to start second task - should be rejected
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'rejected-task',
        ownerId: 'user2',
        callerClaims: { sub: 'user2' },
      },
    });

    // Wait for NACK to be published to the correct user-scoped channel
    await vi.waitFor(() => {
      const failurePublish = pubnub.publish.mock.calls.find((call: unknown[]) => {
        const args = call[0] as {
          channel?: string;
          message?: { taskId?: string; state?: string; error?: string };
        };
        return (
          args.channel === 'u.user2.rejected-task' &&
          args.message?.taskId === 'rejected-task' &&
          args.message?.state === 'failed' &&
          args.message?.error === 'agent_at_capacity'
        );
      });
      expect(failurePublish).toBeDefined();
    });
  });

  it('silently ignores broadcast StartTask when at capacity', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const slowHandler = vi
      .fn()
      .mockImplementation(() => new Promise((resolve) => setTimeout(() => resolve({}), 200)));
    await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard(), concurrency: 1, handler: slowHandler });
    const listener = listeners[0];

    // First task occupies the only thread
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'task-1',
        ownerId: 'user1',
        callerClaims: { sub: 'user1' },
      },
    });

    await new Promise((r) => setTimeout(r, 10));

    // Clear publish calls so we only see what happens next
    pubnub.publish.mockClear();

    // Second task arrives as a broadcast (queued by the framework)
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'task-broadcast',
        ownerId: 'user2',
        callerClaims: { sub: 'user2' },
      },
      userMetadata: { broadcast: 'true' },
    });

    // Give time for any async publish to fire
    await new Promise((r) => setTimeout(r, 50));

    // Verify NO terminal event was published for the broadcast task
    const terminalPublish = pubnub.publish.mock.calls.find((call: unknown[]) => {
      const args = call[0] as {
        channel?: string;
        message?: { taskId?: string; type?: string };
      };
      return args.message?.taskId === 'task-broadcast' && args.message?.type === 'terminal';
    });
    expect(terminalPublish).toBeUndefined();
  });

  it('cooperatively cancels a running task and publishes canceled terminal with artifact', async () => {
    const { pubnub, listeners } = createFakePubNub();

    // Handler that checks isCancelled in a polling loop
    const cancellableHandler = vi.fn().mockImplementation(
      async (_task: unknown, ctx: { isCancelled: boolean }) => {
        for (let i = 0; i < 50; i++) {
          if (ctx.isCancelled) {
            return {
              artifacts: [{
                data: JSON.stringify({ ok: false, cancelled: true, text: 'Cancelled by user' }),
                mimeType: 'application/json',
              }],
            };
          }
          await new Promise((r) => setTimeout(r, 20));
        }
        return { artifacts: [{ data: JSON.stringify({ ok: true, text: 'done' }), mimeType: 'application/json' }] };
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      concurrency: 1,
      handler: cancellableHandler,
    });
    const listener = listeners[0];

    // Start task
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'cancel-task',
        ownerId: 'user1',
        callerClaims: { sub: 'user1' },
      },
    });

    // Let handler start running
    await new Promise((r) => setTimeout(r, 50));

    // Send CancelTask
    listener.message({
      message: {
        type: 'CancelTask',
        taskId: 'cancel-task',
      },
    });

    // Wait for terminal/canceled event
    await vi.waitFor(() => {
      const cancelTerminal = pubnub.publish.mock.calls.find((call: unknown[]) => {
        const args = call[0] as {
          channel?: string;
          message?: { taskId?: string; type?: string; state?: string };
        };
        return (
          args.message?.taskId === 'cancel-task' &&
          args.message?.type === 'terminal' &&
          args.message?.state === 'canceled'
        );
      });
      expect(cancelTerminal).toBeDefined();
    });

    // Verify artifact was published
    const artifactPublish = pubnub.publish.mock.calls.find((call: unknown[]) => {
      const args = call[0] as {
        message?: { taskId?: string; type?: string };
      };
      return (
        args.message?.taskId === 'cancel-task' &&
        args.message?.type === 'artifact'
      );
    });
    expect(artifactPublish).toBeDefined();
  });

  it('returns instanceId from startAgentInstance', async () => {
    const { pubnub } = createFakePubNub();
    const { instanceId, agentName } = await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard() });
    expect(instanceId).toMatch(/^AG-acme_echo-[a-f0-9-]+$/);
    expect(agentName).toBe('acme_echo');
  });

  it('ignores INSTANCE_ID env var and auto-generates instanceId', async () => {
    const saved = process.env.INSTANCE_ID;
    try {
      process.env.INSTANCE_ID = 'AG-leaked-test-id';
      const { pubnub } = createFakePubNub();
      const { instanceId } = await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard() });
      expect(instanceId).not.toBe('AG-leaked-test-id');
      expect(instanceId).toMatch(/^AG-acme_echo-[a-f0-9-]+$/);
    } finally {
      if (saved === undefined) {
        delete process.env.INSTANCE_ID;
      } else {
        process.env.INSTANCE_ID = saved;
      }
    }
  });

  it('processes multiple tasks concurrently when concurrency > 1', async () => {
    const { pubnub, listeners } = createFakePubNub();

    // Track concurrent execution
    let concurrentCount = 0;
    let maxConcurrent = 0;
    const taskCompletionOrder: string[] = [];

    // NOTE: No longer need to add tasks to index - agent instance publishes to task channel only

    // Handler that tracks concurrency
    const mockHandler = vi.fn().mockImplementation(async (task: { taskId: string }) => {
      concurrentCount++;
      maxConcurrent = Math.max(maxConcurrent, concurrentCount);
      // Simulate work with varying durations
      await new Promise((r) => setTimeout(r, 50));
      taskCompletionOrder.push(task.taskId);
      concurrentCount--;
      return {};
    });

    await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard(), handler: mockHandler, concurrency: 3 });
    const listener = listeners[0];

    // Fire all 3 tasks rapidly (fire-and-forget)
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'concurrent-1',
        ownerId: 'user1',
        callerClaims: { sub: 'user1' },
      },
    });
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'concurrent-2',
        ownerId: 'user1',
        callerClaims: { sub: 'user1' },
      },
    });
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'concurrent-3',
        ownerId: 'user1',
        callerClaims: { sub: 'user1' },
      },
    });

    // Wait for all tasks to complete
    await vi.waitFor(
      () => {
        expect(taskCompletionOrder.length).toBe(3);
      },
      { timeout: 1000 },
    );

    // Verify concurrent execution occurred
    expect(maxConcurrent).toBeGreaterThan(1);
    expect(mockHandler).toHaveBeenCalledTimes(3);
  });

  it('does NOT publish to obs.{agentName}.log channel (fan-out via Function)', async () => {
    // Agent instance publish simplification: obs fan-out is now handled by PubNub Function
    // Agent instance should only publish to task channels (u.{ownerId}.{taskId})
    const { pubnub, listeners } = createFakePubNub();
    const { stop } = await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard() });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'obs-task',
        ownerId: 'alice',
        callerClaims: { sub: 'alice' },
      },
    });

    // Wait for task channel publish to complete
    await vi.waitFor(() => {
      const taskCalls = pubnub.publish.mock.calls.filter((call: unknown[]) => {
        const args = call[0] as { channel?: string };
        return args.channel === 'u.alice.obs-task';
      });
      expect(taskCalls.length).toBeGreaterThan(0);
    });

    // Verify NO obs channel publishes
    const obsCalls = pubnub.publish.mock.calls.filter((call: unknown[]) => {
      const args = call[0] as { channel?: string };
      return args.channel?.startsWith('obs.');
    });
    expect(obsCalls.length).toBe(0);

    stop();
  });

  it('prefers direct ownerId field over callerClaims.sub', async () => {
    // Test the new ownerId field (per DECISIONS.md D9)
    const { pubnub, listeners } = createFakePubNub();
    const { stop } = await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard() });
    const listener = listeners[0];

    // Send task with both ownerId and callerClaims.sub - ownerId should take precedence
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'owner-priority-task',
        ownerId: 'direct-owner',
        callerClaims: { sub: 'legacy-owner' },
      },
    });

    // Wait for publish and verify it uses direct ownerId
    await vi.waitFor(() => {
      const taskChannelPublish = pubnub.publish.mock.calls.find((call: unknown[]) => {
        const args = call[0] as { channel?: string };
        return args.channel === 'u.direct-owner.owner-priority-task';
      });
      expect(taskChannelPublish).toBeDefined();
    });

    // Verify it did NOT use the legacy callerClaims.sub
    const legacyPublish = pubnub.publish.mock.calls.find((call: unknown[]) => {
      const args = call[0] as { channel?: string };
      return args.channel === 'u.legacy-owner.owner-priority-task';
    });
    expect(legacyPublish).toBeUndefined();

    stop();
  });

  it('allows unlimited concurrent tasks when concurrency is 0', async () => {
    const { pubnub, listeners } = createFakePubNub();

    // Track concurrent execution
    let concurrentCount = 0;
    let maxConcurrent = 0;

    // Handler that tracks concurrency with a slow task
    const mockHandler = vi.fn().mockImplementation(async () => {
      concurrentCount++;
      maxConcurrent = Math.max(maxConcurrent, concurrentCount);
      // Simulate work
      await new Promise((r) => setTimeout(r, 50));
      concurrentCount--;
      return {};
    });

    // Start agent instance with unlimited capacity (concurrency: 0)
    await startAgentInstance({ pubnub, agentName: 'acme_echo', card: makeTestCard(), handler: mockHandler, concurrency: 0 });
    const listener = listeners[0];

    // Fire 10 tasks rapidly - all should be accepted
    for (let i = 0; i < 10; i++) {
      listener.message({
        message: {
          type: 'StartTask',
          taskId: `unlimited-task-${i}`,
          ownerId: 'user1',
          callerClaims: { sub: 'user1' },
        },
      });
    }

    // Wait for all tasks to complete
    await vi.waitFor(
      () => {
        expect(mockHandler).toHaveBeenCalledTimes(10);
      },
      { timeout: 2000 },
    );

    // Verify concurrent execution occurred (should be more than 1)
    expect(maxConcurrent).toBeGreaterThan(1);

    // Verify NO tasks were rejected (no 'agent_at_capacity' errors published)
    const capacityErrors = pubnub.publish.mock.calls.filter((call: unknown[]) => {
      const args = call[0] as { message?: { error?: string } };
      return args.message?.error === 'agent_at_capacity';
    });
    expect(capacityErrors.length).toBe(0);
  });

  it('reports concurrency: 0 in presence state', async () => {
    const { pubnub } = createFakePubNub();
    const { instanceId } = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      concurrency: 0,
      baseUrl: 'http://test',
    });

    // setState now happens asynchronously after registration attempt (in .then())
    await vi.waitFor(() => expect(pubnub.setState).toHaveBeenCalled());
    expect(pubnub.setState).toHaveBeenCalledWith({
      channels: [`agent.${TEST_AGENT_ID}.control`],
      state: expect.objectContaining({
        instanceId,
        activeTasks: 0,
        concurrency: 0,
        startedAt: expect.any(Number),
      }),
    });
  });
});
