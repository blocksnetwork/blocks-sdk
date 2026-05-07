/**
 * Pipe task lifecycle tests — Phase 3 Node SDK changes.
 *
 * Tests:
 * - ExpireTask signals handler to stop, agent publishes terminal after artifact
 * - CancelTask on pipe: agent publishes canceled terminal
 * - TerminateTask on pipe: agent publishes terminal with reason
 * - Pipe handler voluntary return does not publish terminal
 * - isExpired is true after ExpireTask, false after CancelTask
 * - Per-task PubNub client created for pipe publishes
 * - Gated flag set for pipe tasks only
 */
import { describe, expect, it, vi } from 'vitest';
import { startAgentInstance, type TaskContext, type StartTaskMessage } from '../src/runtime/agent-instance.js';
import { makePipeTestCard } from './helpers/test-card.js';

// Track all per-task PubNub clients created during tests
const perTaskClients: ReturnType<typeof createFakePubNub>['pubnub'][] = [];

// Mock createPubNubClient so per-task PubNub clients are testable fakes
vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: () => {
    const fake = createFakePubNubInstance();
    perTaskClients.push(fake);
    return fake;
  },
}));

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const createFakePubNubInstance = () => {
  return {
    publish: vi.fn().mockResolvedValue({ timetoken: Date.now().toString() }),
    addMessageAction: vi.fn().mockResolvedValue({}),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setFilterExpression: vi.fn(),
    setState: vi.fn().mockResolvedValue({}),
    setToken: vi.fn(),
    destroy: vi.fn(),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any;
};

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const createFakePubNub = () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  const pubnub = {
    publish: vi.fn().mockResolvedValue({ timetoken: Date.now().toString() }),
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

/** Find a publish call matching criteria across instance + all per-task clients */
function findPublishCall(
  instancePubnub: ReturnType<typeof createFakePubNub>['pubnub'],
  match: (args: Record<string, unknown>) => boolean,
) {
  // Check instance client
  const instanceMatch = instancePubnub.publish.mock.calls.find((call: unknown[]) => match(call[0] as Record<string, unknown>));
  if (instanceMatch) return instanceMatch;
  // Check per-task clients
  for (const client of perTaskClients) {
    const ptMatch = client.publish.mock.calls.find((call: unknown[]) => match(call[0] as Record<string, unknown>));
    if (ptMatch) return ptMatch;
  }
  return undefined;
}

describe('pipe task lifecycle', () => {

  it('ExpireTask signals handler to stop and agent publishes terminal after artifact', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    // Handler that polls isCancelled/isExpired in a loop
    let capturedCtx: TaskContext | undefined;
    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        capturedCtx = ctx;
        for (let i = 0; i < 50; i++) {
          if (ctx.isCancelled) {
            return {};
          }
          await new Promise((r) => setTimeout(r, 20));
        }
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Start a pipe task
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'expire-task-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
      },
    });

    // Let handler start running
    await new Promise((r) => setTimeout(r, 60));

    // Send ExpireTask
    listener.message({
      message: {
        type: 'ExpireTask',
        taskId: 'expire-task-1',
        reason: 'duration_expired',
      },
    });

    // Wait for handler to finish
    await vi.waitFor(() => expect(capturedCtx).toBeDefined(), { timeout: 2000 });
    await new Promise((r) => setTimeout(r, 100));

    // Agent publishes terminal after cleanup so consumers see artifact before terminal.
    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'expire-task-1' &&
        msg?.type === 'terminal'
      );
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('completed');
    expect(terminalMsg.message.completionReason).toBe('duration_expired');
  });

  it('CancelTask on pipe: agent publishes canceled terminal', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        for (let i = 0; i < 50; i++) {
          if (ctx.isCancelled) {
            return {};
          }
          await new Promise((r) => setTimeout(r, 20));
        }
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Start a pipe task
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'cancel-pipe-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
      },
    });

    // Let handler start
    await new Promise((r) => setTimeout(r, 60));

    // Send CancelTask
    listener.message({
      message: {
        type: 'CancelTask',
        taskId: 'cancel-pipe-1',
      },
    });

    await new Promise((r) => setTimeout(r, 200));

    // Agent publishes terminal after cleanup for correct event ordering.
    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'cancel-pipe-1' &&
        msg?.type === 'terminal'
      );
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('canceled');
  });

  it('pipe handler voluntary return does NOT publish terminal', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    // Handler that returns immediately (voluntary return)
    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Start a pipe task
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'voluntary-return-pipe',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
      },
    });

    // Wait for handler to complete
    await vi.waitFor(() => expect(handler).toHaveBeenCalled());

    // Give time for any async publishes
    await new Promise((r) => setTimeout(r, 50));

    // Verify NO terminal event was published for the pipe task
    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'voluntary-return-pipe' &&
        msg?.type === 'terminal'
      );
    });
    expect(terminalPublish).toBeUndefined();
  });

  it('ExpireTask after handler return publishes terminal via cached credentials', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    // Handler returns voluntarily (pipe task, credentials stay cached)
    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'ext-expire-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
        writeToken: 'test-write-token',
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 50));

    // Handler has returned — send ExpireTask (external stream scenario)
    listener.message({
      message: {
        type: 'ExpireTask',
        taskId: 'ext-expire-1',
        reason: 'duration_expired',
      },
    });

    await new Promise((r) => setTimeout(r, 100));

    // publishTerminalImpl creates an ephemeral client (tracked in perTaskClients)
    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return msg?.taskId === 'ext-expire-1' && msg?.type === 'terminal';
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('completed');
    expect(terminalMsg.message.completionReason).toBe('duration_expired');
  });

  it('CancelTask after handler return publishes terminal via cached credentials', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'ext-cancel-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
        writeToken: 'test-write-token',
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 50));

    listener.message({
      message: { type: 'CancelTask', taskId: 'ext-cancel-1' },
    });

    await new Promise((r) => setTimeout(r, 100));

    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return msg?.taskId === 'ext-cancel-1' && msg?.type === 'terminal';
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('canceled');
  });

  it('TerminateTask after handler return publishes terminal via cached credentials', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'ext-terminate-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
        writeToken: 'test-write-token',
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 50));

    listener.message({
      message: { type: 'TerminateTask', taskId: 'ext-terminate-1' },
    });

    await new Promise((r) => setTimeout(r, 100));

    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return msg?.taskId === 'ext-terminate-1' && msg?.type === 'terminal';
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('canceled');
    expect(terminalMsg.message.reason).toBe('terminated');
  });

  it('local duration timer fires before ExpireTask and publishes terminal', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    let capturedCtx: TaskContext | undefined;
    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        capturedCtx = ctx;
        for (let i = 0; i < 500; i++) {
          if (ctx.isCancelled) return {};
          await new Promise((r) => setTimeout(r, 20));
        }
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // durationExpiresAtMs 1s from now — local timer should fire
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'timer-expire-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 1,
        durationExpiresAtMs: Date.now() + 1000,
        writeToken: 'test-token',
      },
    });

    // Wait for handler to start, timer to fire, and post-handler cleanup to complete
    await new Promise((r) => setTimeout(r, 3000));

    expect(capturedCtx).toBeDefined();

    // Check terminal was published (isExpired may already be cleared by finally block)
    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return msg?.taskId === 'timer-expire-1' && msg?.type === 'terminal';
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('completed');
    expect(terminalMsg.message.completionReason).toBe('duration_expired');
  }, 10000);

  it('local duration timer cancelled on voluntary return (no late expiry)', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // durationExpiresAtMs 300ms from now, but handler returns immediately
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'timer-voluntary-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 1,
        durationExpiresAtMs: Date.now() + 300,
        writeToken: 'test-token',
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    // Wait past the expiresAt deadline
    await new Promise((r) => setTimeout(r, 500));

    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return msg?.taskId === 'timer-voluntary-1' && msg?.type === 'terminal';
    });
    expect(terminalPublish).toBeUndefined();
  });

  it('ExpireTask arrives before local timer — no duplicate terminal', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    let capturedCtx: TaskContext | undefined;
    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        capturedCtx = ctx;
        for (let i = 0; i < 200; i++) {
          if (ctx.isCancelled) return {};
          await new Promise((r) => setTimeout(r, 20));
        }
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // durationExpiresAtMs 10 minutes from now — local timer will NOT fire soon
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'dedup-expire-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 10,
        durationExpiresAtMs: Date.now() + 600000,
        writeToken: 'test-token',
      },
    });

    // Let handler start polling
    await new Promise((r) => setTimeout(r, 60));

    // Send ExpireTask before local timer fires
    listener.message({
      message: {
        type: 'ExpireTask',
        taskId: 'dedup-expire-1',
        reason: 'duration_expired',
      },
    });

    // Wait for handler to exit
    await vi.waitFor(() => expect(capturedCtx?.isExpired).toBe(true), { timeout: 2000 });
    await new Promise((r) => setTimeout(r, 100));

    // Assert terminal is published with correct state
    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return msg?.taskId === 'dedup-expire-1' && msg?.type === 'terminal';
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('completed');
    expect(terminalMsg.message.completionReason).toBe('duration_expired');

    // Wait additional time to ensure no late duplicate from local timer
    await new Promise((r) => setTimeout(r, 200));

    // Count ALL terminal publishes across instance + per-task clients
    let terminalCount = 0;
    const countTerminals = (calls: unknown[][]) => {
      for (const call of calls) {
        const args = call[0] as Record<string, unknown>;
        const msg = args.message as Record<string, unknown> | undefined;
        if (msg?.taskId === 'dedup-expire-1' && msg?.type === 'terminal') {
          terminalCount++;
        }
      }
    };
    countTerminals(pubnub.publish.mock.calls);
    for (const client of perTaskClients) {
      countTerminals(client.publish.mock.calls);
    }
    expect(terminalCount).toBe(1);
  });

  it('CancelTask before local timer fires: terminal is canceled not expired', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        for (let i = 0; i < 200; i++) {
          if (ctx.isCancelled) return {};
          await new Promise((r) => setTimeout(r, 20));
        }
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'timer-cancel-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 10,
        durationExpiresAtMs: Date.now() + 600000,
        writeToken: 'test-token',
      },
    });

    await new Promise((r) => setTimeout(r, 60));

    // Send CancelTask before local timer fires
    listener.message({
      message: { type: 'CancelTask', taskId: 'timer-cancel-1' },
    });

    await new Promise((r) => setTimeout(r, 200));

    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return msg?.taskId === 'timer-cancel-1' && msg?.type === 'terminal';
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('canceled');
  });

  it('request task still auto-completes on handler return', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Start a request task (default taskKind)
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'request-task-1',
        ownerId: 'user1',
      },
    });

    // Wait for terminal/completed event (may be on per-task client)
    await vi.waitFor(() => {
      const completedTerminal = findPublishCall(pubnub, (args) => {
        const msg = args.message as Record<string, unknown> | undefined;
        return (
          msg?.taskId === 'request-task-1' &&
          msg?.type === 'terminal' &&
          msg?.state === 'completed'
        );
      });
      expect(completedTerminal).toBeDefined();
    });
  });

  it('isExpired is true after ExpireTask', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    let capturedIsExpired: boolean | undefined;

    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        for (let i = 0; i < 100; i++) {
          if (ctx.isCancelled) {
            capturedIsExpired = ctx.isExpired;
            return {};
          }
          await new Promise((r) => setTimeout(r, 10));
        }
        return {};
      },
    );

    const instance = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'expire-flag-check',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
      },
    });
    await new Promise((r) => setTimeout(r, 40));

    listener.message({
      message: {
        type: 'ExpireTask',
        taskId: 'expire-flag-check',
      },
    });

    await vi.waitFor(() => {
      expect(capturedIsExpired).toBe(true);
    }, { timeout: 2000 });

    instance.stop();
  });

  it('isExpired is false after CancelTask', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    let capturedIsExpired: boolean | undefined;

    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        for (let i = 0; i < 100; i++) {
          if (ctx.isCancelled) {
            capturedIsExpired = ctx.isExpired;
            return {};
          }
          await new Promise((r) => setTimeout(r, 10));
        }
        return {};
      },
    );

    const instance = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'cancel-flag-check',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
      },
    });
    await new Promise((r) => setTimeout(r, 40));

    listener.message({
      message: {
        type: 'CancelTask',
        taskId: 'cancel-flag-check',
      },
    });

    await vi.waitFor(() => {
      expect(capturedIsExpired).toBe(false);
    }, { timeout: 2000 });

    instance.stop();
  });

  it('rejects pipe StartTask with missing duration (no handler, terminal failed)', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Send pipe StartTask with NO duration or durationExpiresAtMs
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'invalid-pipe-no-dur',
        ownerId: 'user1',
        taskKind: 'pipe',
      },
    });

    await new Promise((r) => setTimeout(r, 100));

    // Handler should NOT have been called
    expect(handler).not.toHaveBeenCalled();

    // Terminal failed should be published on the instance client
    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'invalid-pipe-no-dur' &&
        msg?.type === 'terminal'
      );
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('failed');
    expect(terminalMsg.message.error).toBe('invalid_start_task');
  });

  it('rejects pipe StartTask with duration but missing durationExpiresAtMs', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Send pipe StartTask with duration but no durationExpiresAtMs
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'invalid-pipe-no-exp',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
      },
    });

    await new Promise((r) => setTimeout(r, 100));

    // Handler should NOT have been called
    expect(handler).not.toHaveBeenCalled();

    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'invalid-pipe-no-exp' &&
        msg?.type === 'terminal'
      );
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('failed');
    expect(terminalMsg.message.error).toBe('invalid_start_task');
  });

  it('rejects pipe StartTask with duration 0', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'invalid-pipe-dur-zero',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 0,
        durationExpiresAtMs: Date.now() + 60000,
      },
    });

    await new Promise((r) => setTimeout(r, 100));

    expect(handler).not.toHaveBeenCalled();

    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'invalid-pipe-dur-zero' &&
        msg?.type === 'terminal'
      );
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('failed');
    expect(terminalMsg.message.error).toBe('invalid_start_task');
  });

  it('rejects pipe StartTask with duration exceeding 43200', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'invalid-pipe-dur-big',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 43201,
        durationExpiresAtMs: Date.now() + 60000,
      },
    });

    await new Promise((r) => setTimeout(r, 100));

    expect(handler).not.toHaveBeenCalled();

    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'invalid-pipe-dur-big' &&
        msg?.type === 'terminal'
      );
    });
    expect(terminalPublish).toBeDefined();
    const terminalMsg = (terminalPublish as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.state).toBe('failed');
    expect(terminalMsg.message.error).toBe('invalid_start_task');
  });

  it('accepts pipe StartTask with valid duration and durationExpiresAtMs', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'valid-pipe-start',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
        writeToken: 'test-token',
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled(), { timeout: 2000 });

    // No terminal failed should have been published on the instance client
    const failedPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'valid-pipe-start' &&
        msg?.type === 'terminal' &&
        msg?.state === 'failed'
      );
    });
    expect(failedPublish).toBeUndefined();
  });

  it('request StartTask without duration is accepted normally', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'request-no-dur',
        ownerId: 'user1',
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled(), { timeout: 2000 });
  });

  it('createStream is now async and uses setup handshake', async () => {
    // In Phase 3, createStream is async and performs the stream setup handshake.
    // The gating behavior is determined by the StreamClient from @blocks-network/sdk/stream.
    // This test validates that createStream exists on TaskContext and is a function.
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    let hasCreateStream = false;
    let createStreamIsFunction = false;

    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        hasCreateStream = 'createStream' in ctx;
        createStreamIsFunction = typeof ctx.createStream === 'function';
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 3,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'create-stream-check',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
        hasStream: true,
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    expect(hasCreateStream).toBe(true);
    expect(createStreamIsFunction).toBe(true);
  });
});

describe('per-task PubNub client for pipe tasks', () => {

  it('per-task client always created for pipe tasks', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockImplementation(
      async () => {
        await new Promise((r) => setTimeout(r, 50));
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Start a pipe task WITHOUT writeToken
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'pipe-no-token',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
      },
    });

    await new Promise((r) => setTimeout(r, 20));

    // Per-task client should still be created (no PAM/non-PAM code split)
    expect(perTaskClients.length).toBeGreaterThanOrEqual(1);
    // setToken should NOT be called since no writeToken provided
    const lastClient = perTaskClients[perTaskClients.length - 1];
    expect(lastClient.setToken).not.toHaveBeenCalled();
  });

  it('does not apply pipe writeToken to instance PubNub (uses per-task client)', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    // Use a slow handler so we can inspect state while task is still starting
    const handler = vi.fn().mockImplementation(
      async () => {
        await new Promise((r) => setTimeout(r, 50));
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Record setToken calls before sending the pipe task
    pubnub.setToken.mockClear();

    // Start a pipe task WITH writeToken — should NOT apply to instance pubnub
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'pipe-with-token',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
        writeToken: 'per-task-write-token-abc',
      },
    });

    // Wait briefly for the handleControlMessage to process synchronously
    await new Promise((r) => setTimeout(r, 20));

    // The instance pubnub.setToken should NOT be called with the pipe writeToken.
    // Pipe tasks create a per-task PubNub client for token isolation.
    const instanceSetTokenCalls = pubnub.setToken.mock.calls.filter(
      (call: unknown[]) => call[0] === 'per-task-write-token-abc',
    );
    expect(instanceSetTokenCalls.length).toBe(0);

    // The per-task client should have the token set
    const lastClient = perTaskClients[perTaskClients.length - 1];
    expect(lastClient.setToken).toHaveBeenCalledWith('per-task-write-token-abc');
  });

  it('request tasks use per-task PubNub client for writeToken (Phase 3 three-tier model)', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    pubnub.setToken.mockClear();

    // Start a request task WITH writeToken
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'request-with-token',
        ownerId: 'user1',
        writeToken: 'request-write-token-xyz',
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 50));

    // In Phase 3 three-tier model, request tasks also use per-task PubNub clients.
    // The instance pubnub should NOT have setToken called with the request writeToken.
    const instanceSetTokenCalls = pubnub.setToken.mock.calls.filter(
      (call: unknown[]) => call[0] === 'request-write-token-xyz',
    );
    expect(instanceSetTokenCalls.length).toBe(0);

    // A per-task client should have the token
    const clientWithToken = perTaskClients.find((c) =>
      c.setToken.mock.calls.some((call: unknown[]) => call[0] === 'request-write-token-xyz'),
    );
    expect(clientWithToken).toBeDefined();
  });

  it('concurrent pipe tasks use separate per-task clients with different writeTokens', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    // Slow handler so both tasks are in flight at the same time
    const handler = vi.fn().mockImplementation(
      async () => {
        await new Promise((r) => setTimeout(r, 100));
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 2,
      handler,
    });
    const listener = listeners[0];

    // Clear instance setToken tracking
    pubnub.setToken.mockClear();

    // Send two pipe tasks simultaneously with different writeTokens
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'concurrent-pipe-A',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
        writeToken: 'token-A',
      },
    });
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'concurrent-pipe-B',
        ownerId: 'user2',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
        writeToken: 'token-B',
      },
    });

    // Wait for both handlers to complete
    await vi.waitFor(() => {
      expect(handler).toHaveBeenCalledTimes(2);
    }, { timeout: 2000 });
    await new Promise((r) => setTimeout(r, 150));

    // At least 2 per-task clients were created
    expect(perTaskClients.length).toBeGreaterThanOrEqual(2);

    // One per-task client had setToken called with token-A
    const clientWithTokenA = perTaskClients.find((c) =>
      c.setToken.mock.calls.some((call: unknown[]) => call[0] === 'token-A'),
    );
    expect(clientWithTokenA).toBeDefined();

    // A different per-task client had setToken called with token-B
    const clientWithTokenB = perTaskClients.find((c) =>
      c.setToken.mock.calls.some((call: unknown[]) => call[0] === 'token-B'),
    );
    expect(clientWithTokenB).toBeDefined();

    // They must be different clients (token isolation)
    expect(clientWithTokenA).not.toBe(clientWithTokenB);

    // The instance pubnub's setToken was NOT called with either token
    const instanceTokenACalls = pubnub.setToken.mock.calls.filter(
      (call: unknown[]) => call[0] === 'token-A',
    );
    const instanceTokenBCalls = pubnub.setToken.mock.calls.filter(
      (call: unknown[]) => call[0] === 'token-B',
    );
    expect(instanceTokenACalls.length).toBe(0);
    expect(instanceTokenBCalls.length).toBe(0);
  });

  it('ExpireTask handler only aborts controller for in-flight tasks', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Send ExpireTask for a non-existent task — should log but not crash
    listener.message({
      message: {
        type: 'ExpireTask',
        taskId: 'non-existent-task',
      },
    });

    // Give time for the handler to process
    await new Promise((r) => setTimeout(r, 30));

    // No terminal event for non-existent task (no crash)
    const terminalPublish = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'non-existent-task' &&
        msg?.type === 'terminal'
      );
    });
    expect(terminalPublish).toBeUndefined();
  });

  it('TerminateTask on pipe: agent publishes terminal with reason', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        for (let i = 0; i < 50; i++) {
          if (ctx.isCancelled) return {};
          await new Promise((r) => setTimeout(r, 20));
        }
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'terminate-test-1',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
      },
    });

    await new Promise((r) => setTimeout(r, 60));

    listener.message({
      message: { type: 'TerminateTask', taskId: 'terminate-test-1' },
    });

    await new Promise((r) => setTimeout(r, 200));

    // Agent publishes terminal after cleanup for correct event ordering.
    const terminal = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return (
        msg?.taskId === 'terminate-test-1' &&
        msg?.type === 'terminal'
      );
    });
    expect(terminal).toBeDefined();
    const terminalMsg = (terminal as unknown[])[0] as Record<string, Record<string, unknown>>;
    expect(terminalMsg.message.reason).toBe('terminated');
  });

  it('TerminateTask for non-in-flight task is silently ignored', async () => {
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    listener.message({
      message: { type: 'TerminateTask', taskId: 'never-started' },
    });

    await new Promise((r) => setTimeout(r, 30));

    const terminal = findPublishCall(pubnub, (args) => {
      const msg = args.message as Record<string, unknown> | undefined;
      return msg?.taskId === 'never-started' && msg?.type === 'terminal';
    });
    expect(terminal).toBeUndefined();
  });

  it('CancelTask arriving before onStart completes still aborts the controller', async () => {
    // P1 bug fix: AbortController must be registered before onStart is awaited,
    // so CancelTask arriving during onStart initialization still finds it.
    perTaskClients.length = 0;
    const { pubnub, listeners } = createFakePubNub();

    let capturedIsCancelled: boolean | undefined;

    const handler = vi.fn().mockImplementation(
      async (_task: StartTaskMessage, ctx: TaskContext) => {
        // Simulate a slow handler that polls isCancelled
        for (let i = 0; i < 100; i++) {
          if (ctx.isCancelled) {
            capturedIsCancelled = true;
            return {};
          }
          await new Promise((r) => setTimeout(r, 10));
        }
        capturedIsCancelled = false;
        return {};
      },
    );

    await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makePipeTestCard(),
      concurrency: 1,
      handler,
    });
    const listener = listeners[0];

    // Start a pipe task
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'early-cancel-race',
        ownerId: 'user1',
        taskKind: 'pipe',
        duration: 60,
        durationExpiresAtMs: Date.now() + 3600000,
      },
    });

    // Immediately send CancelTask (before onStart has had time to run much)
    // The controller should already be registered so this should work.
    await new Promise((r) => setTimeout(r, 5));
    listener.message({
      message: {
        type: 'CancelTask',
        taskId: 'early-cancel-race',
      },
    });

    // Wait for the handler to notice the cancellation
    await vi.waitFor(() => {
      expect(capturedIsCancelled).toBe(true);
    }, { timeout: 3000 });
  });
});
