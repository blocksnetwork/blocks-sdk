/**
 * Unit tests for `AgentInstance.createStream` after the stream-id-arg
 * removal.
 *
 * Covers:
 * 1. Options-only signature (no positional arg accepted; compile-time
 *    rejection asserted with @ts-expect-error).
 * 2. Runtime guard for JS callers that pass a string / array first arg.
 * 3. Dedicated-affinity resolver produces `{taskId}-{counter}` per task,
 *    counter reset per task id.
 * 4. Shared-affinity resolver uses the card-declared key regardless of
 *    task id.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { makeTestCard, makePipeTestCard } from './helpers/test-card.js';
import type { AgentCard } from '../src/runtime/agent-registry.js';

// --- Mock PubNub client factory ---

function createMockPubNub(overrides: Record<string, unknown> = {}) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const messageListeners: any[] = [];
  let token: string | undefined;

  return {
    addListener: vi.fn((listener) => { messageListeners.push(listener); }),
    removeListener: vi.fn((listener) => {
      const idx = messageListeners.indexOf(listener);
      if (idx >= 0) messageListeners.splice(idx, 1);
    }),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    destroy: vi.fn(),
    setToken: vi.fn((t: string) => { token = t; }),
    setFilterExpression: vi.fn(),
    setState: vi.fn(async () => {}),
    publish: vi.fn(async () => ({ timetoken: '123' })),
    hereNow: vi.fn(async () => ({ channels: {} })),
    getToken: () => token,
    _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
    _simulateMessage(channel: string, message: unknown, userMetadata?: Record<string, unknown>) {
      for (const listener of messageListeners) {
        if (listener?.message) listener.message({ channel, message, userMetadata });
      }
    },
    ...overrides,
  };
}

vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: vi.fn(async () => ({ pamToken: undefined })),
  fetchAgentRegistry: vi.fn(async () => ({})),
  getAgent: vi.fn(async () => null),
  removeAgent: vi.fn(async () => {}),
  fetchAgentsByTag: vi.fn(async () => []),
  fetchAgentsByListing: vi.fn(async () => []),
}));

const allCreatedPubNubs: ReturnType<typeof createMockPubNub>[] = [];

/**
 * The per-task PubNub client performs `stream_setup` publishes that
 * the runtime expects to throw a 403 carrying the setup response.
 * Tests set this interceptor to simulate that handshake and echo back
 * whatever streamId / channel the SDK derived.
 */
let sharedPublishInterceptor: ((args: Record<string, unknown>) => Promise<unknown>) | null = null;

vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => {
    const pn = createMockPubNub({
      publish: vi.fn(async (args: Record<string, unknown>) => {
        if (sharedPublishInterceptor) return sharedPublishInterceptor(args);
        return { timetoken: '123' };
      }),
    });
    allCreatedPubNubs.push(pn);
    return pn;
  }),
}));

vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = await importOriginal() as Record<string, unknown>;
  return {
    ...actual,
    StreamClient: class MockStreamClient {
      private _isActive = true;
      private _channel: string;
      private endCallbacks: Array<() => void> = [];
      constructor(opts: Record<string, unknown>) {
        this._channel = (opts.channel as string) || `stream.${opts.agentName}.${opts.streamId}`;
      }
      get isActive() { return this._isActive; }
      get channel() { return this._channel; }
      get uuid() { return 'mock-stream-uuid'; }
      write = vi.fn();
      end = vi.fn(async () => {
        this._isActive = false;
        for (const cb of this.endCallbacks) cb();
      });
      onEnd(cb: () => void) { this.endCallbacks.push(cb); }
      get inbound(): AsyncIterable<unknown> {
        // eslint-disable-next-line @typescript-eslint/no-this-alias
        const self = this;
        return {
          [Symbol.asyncIterator]() {
            return {
              async next() {
                if (!self._isActive) return { value: undefined, done: true };
                return new Promise(() => {}); // hang
              },
            };
          },
        };
      }
      static fromDescriptor = vi.fn((desc, opts) => new MockStreamClient({ ...desc, ...opts }));
    },
  };
});

// --- Helpers ---

function makePipeStartTask(taskId: string) {
  return {
    type: 'StartTask' as const,
    taskId,
    ownerId: 'alice',
    taskKind: 'pipe' as const,
    hasStream: true,
    writeToken: `wt-${taskId}`,
    duration: 60,
    durationExpiresAtMs: Date.now() + 3600000,
  };
}

function makeSetupSuccess(opts: {
  taskId: string; streamId: string; channel: string;
  direction: string; phase?: string; token?: string;
  tokenTtlMinutes?: number;
}) {
  return {
    status: {
      statusCode: 403,
      errorData: {
        message: {
          ok: true,
          streamSetupResponse: {
            taskId: opts.taskId,
            streamId: opts.streamId,
            channel: opts.channel,
            direction: opts.direction,
            phase: opts.phase ?? 'embedded',
            token: opts.token,
            tokenTtlMinutes: opts.tokenTtlMinutes ?? 62,
          },
        },
      },
    },
  };
}

/**
 * Drop-in interceptor that echoes the SDK-derived streamId / channel
 * from the stream_setup message. Good enough for any test that only
 * cares about the channel the SDK produced, not the setup phase.
 */
async function echoSetupInterceptor(args: Record<string, unknown>): Promise<unknown> {
  const ch = args.channel as string;
  if (ch?.startsWith('setup.')) {
    const msg = args.message as Record<string, unknown>;
    throw makeSetupSuccess({
      taskId: msg.taskId as string,
      streamId: msg.streamId as string,
      channel: msg.channel as string,
      direction: msg.direction as string,
      phase: 'embedded',
      token: 'T7A-test',
    });
  }
  return { timetoken: '123' };
}

function streamIdSuffix(channel: string): string {
  // channel = "stream.{agentName}.{streamId}"
  return channel.split('.').slice(2).join('.');
}

async function waitFor<T>(check: () => T | undefined, timeoutMs = 3000): Promise<T> {
  const start = Date.now();
  return new Promise((resolve, reject) => {
    const tick = () => {
      const result = check();
      if (result !== undefined) return resolve(result as T);
      if (Date.now() - start > timeoutMs) return reject(new Error('waitFor timeout'));
      setTimeout(tick, 5);
    };
    tick();
  });
}

// --- Tests ---

describe('createStream options-only signature', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    allCreatedPubNubs.length = 0;
    sharedPublishInterceptor = echoSetupInterceptor;
  });

  it('accepts zero args and resolves against a single-stream card', async () => {
    const mockPn = createMockPubNub();
    let channel: string | undefined;

    const handle = await startAgentInstance({
      agentName: 'uno',
      card: makePipeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        const stream = await ctx!.createStream({ subscribeGraceMs: 0 });
        channel = stream.channel;
        return {};
      },
    });

    mockPn._simulateMessage('agent.uno.control', makePipeStartTask('t-a'), {
      instance: handle.instanceId,
    });
    await waitFor(() => channel);

    expect(channel).toBe('stream.uno.t-a-1');
    handle.stop();
  });

  it('accepts options-only arg and propagates format', async () => {
    const mockPn = createMockPubNub();
    const card: AgentCard = makeTestCard({
      capabilities: { taskKinds: ['request', 'pipe'] },
      streams: {
        _default: { direction: 'outbound', format: 'bytes' },
      },
    });

    let channel: string | undefined;
    const handle = await startAgentInstance({
      agentName: 'opts',
      card,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        const stream = await ctx!.createStream({
          format: 'bytes',
          declaredStream: '_default',
          subscribeGraceMs: 0,
        });
        channel = stream.channel;
        return {};
      },
    });

    mockPn._simulateMessage('agent.opts.control', makePipeStartTask('t-opts'), {
      instance: handle.instanceId,
    });
    await waitFor(() => channel);

    expect(channel).toBe('stream.opts.t-opts-1');
    handle.stop();
  });

  // Compile-time rejection: if these lines compile, the signature regressed.
  // Runtime no-op; tsc validates the @ts-expect-error directives.
  it('rejects a positional-string first arg at compile time', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any, @typescript-eslint/no-unused-vars
    const assertCompileError = (ctx: any) => {
      // @ts-expect-error positional streamId arg is removed from the signature
      ctx.createStream('literal', {});
      // @ts-expect-error options-only: no second arg
      ctx.createStream({}, {});
      // @ts-expect-error options-only: no string first arg
      ctx.createStream('literal');
    };
    expect(typeof assertCompileError).toBe('function');
  });
});

describe('createStream runtime guard (JS callers)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    allCreatedPubNubs.length = 0;
    sharedPublishInterceptor = echoSetupInterceptor;
  });

  it('throws TypeError when first arg is a string (simulated JS caller)', async () => {
    const mockPn = createMockPubNub();
    let err: unknown;

    const handle = await startAgentInstance({
      agentName: 'jsguard',
      card: makePipeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        try {
          // JS callers bypass TS; simulate the old positional form.
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          await (ctx!.createStream as any)('literal');
        } catch (e) {
          err = e;
        }
        return {};
      },
    });

    mockPn._simulateMessage('agent.jsguard.control', makePipeStartTask('t-js1'), {
      instance: handle.instanceId,
    });
    await waitFor(() => err);

    expect(err).toBeInstanceOf(TypeError);
    expect((err as Error).message).toContain('declaredStream');
    handle.stop();
  });

  it('throws TypeError when first arg is an array', async () => {
    const mockPn = createMockPubNub();
    let err: unknown;

    const handle = await startAgentInstance({
      agentName: 'jsguard2',
      card: makePipeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        try {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          await (ctx!.createStream as any)(['not', 'an', 'object']);
        } catch (e) {
          err = e;
        }
        return {};
      },
    });

    mockPn._simulateMessage('agent.jsguard2.control', makePipeStartTask('t-js2'), {
      instance: handle.instanceId,
    });
    await waitFor(() => err);

    expect(err).toBeInstanceOf(TypeError);
    handle.stop();
  });
});

describe('createStream dedicated-affinity resolver', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    allCreatedPubNubs.length = 0;
    sharedPublishInterceptor = echoSetupInterceptor;
  });

  it('generates `{taskId}-1`, `{taskId}-2` within the same task', async () => {
    const mockPn = createMockPubNub();
    const channels: string[] = [];
    let done = false;

    const handle = await startAgentInstance({
      agentName: 'ded',
      card: makePipeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        const s1 = await ctx!.createStream({ subscribeGraceMs: 0 });
        const s2 = await ctx!.createStream({ subscribeGraceMs: 0 });
        channels.push(s1.channel, s2.channel);
        done = true;
        return {};
      },
    });

    mockPn._simulateMessage('agent.ded.control', makePipeStartTask('task-x'), {
      instance: handle.instanceId,
    });
    await waitFor(() => (done ? true : undefined));

    expect(channels).toHaveLength(2);
    expect(streamIdSuffix(channels[0])).toBe('task-x-1');
    expect(streamIdSuffix(channels[1])).toBe('task-x-2');
    expect(channels[0]).toMatch(/^stream\.\w+\.task-x-\d+$/);
    handle.stop();
  });

  it('resets the counter per taskId (fresh counter for each task)', async () => {
    const mockPn = createMockPubNub();
    const perTaskChannels: Record<string, string[]> = {};

    const handle = await startAgentInstance({
      agentName: 'dedfresh',
      card: makePipeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      concurrency: 3,
      handler: async (task, ctx) => {
        const s1 = await ctx!.createStream({ subscribeGraceMs: 0 });
        const s2 = await ctx!.createStream({ subscribeGraceMs: 0 });
        perTaskChannels[task.taskId] = [s1.channel, s2.channel];
        return {};
      },
    });

    // Task A
    mockPn._simulateMessage('agent.dedfresh.control', makePipeStartTask('task-A'), {
      instance: handle.instanceId,
    });
    await waitFor(() => perTaskChannels['task-A']);

    // Task B
    mockPn._simulateMessage('agent.dedfresh.control', makePipeStartTask('task-B'), {
      instance: handle.instanceId,
    });
    await waitFor(() => perTaskChannels['task-B']);

    expect(perTaskChannels['task-A']).toEqual([
      'stream.dedfresh.task-A-1',
      'stream.dedfresh.task-A-2',
    ]);
    expect(perTaskChannels['task-B']).toEqual([
      'stream.dedfresh.task-B-1',
      'stream.dedfresh.task-B-2',
    ]);
    handle.stop();
  });
});

describe('createStream shared-affinity resolver', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    allCreatedPubNubs.length = 0;
    sharedPublishInterceptor = echoSetupInterceptor;
  });

  it('uses the declared key as streamId regardless of taskId', async () => {
    const mockPn = createMockPubNub();
    const sharedCard: AgentCard = makeTestCard({
      capabilities: { taskKinds: ['request', 'pipe'] },
      streams: {
        chat: {
          direction: 'bidirectional',
          format: 'events',
          affinity: 'shared',
        },
      },
    });

    const channelsByTask: Record<string, string> = {};
    const handle = await startAgentInstance({
      agentName: 'sharedagent',
      card: sharedCard,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      concurrency: 3,
      handler: async (task, ctx) => {
        const s = await ctx!.createStream({
          declaredStream: 'chat',
          direction: 'bidirectional',
          format: 'events',
          subscribeGraceMs: 0,
        });
        channelsByTask[task.taskId] = s.channel;
        return {};
      },
    });

    mockPn._simulateMessage('agent.sharedagent.control', makePipeStartTask('task-1'), {
      instance: handle.instanceId,
    });
    await waitFor(() => channelsByTask['task-1']);

    mockPn._simulateMessage('agent.sharedagent.control', makePipeStartTask('task-2'), {
      instance: handle.instanceId,
    });
    await waitFor(() => channelsByTask['task-2']);

    expect(channelsByTask['task-1']).toBe('stream.sharedagent.chat');
    expect(channelsByTask['task-2']).toBe('stream.sharedagent.chat');
    handle.stop();
  });
});
