/** An embedded stream keeps the keyset of its task's receiving environment. */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { makePipeTestCard } from './helpers/test-card.js';

function keyset(env: string) {
  return { publishKey: `${env}-pub`, subscribeKey: `${env}-sub` };
}

const FAKE_STREAM_CREDENTIAL = ['fake', 'stream', 'credential'].join('-');

interface MockPn {
  config: Record<string, unknown>;
  subscribe: ReturnType<typeof vi.fn>;
  _simulateMessage: (message: unknown, userMetadata?: Record<string, unknown>) => void;
  [k: string]: unknown;
}

const created: MockPn[] = [];
const streamClientArgs: Array<Record<string, unknown>> = [];

vi.mock('../src/runtime/cdm-config.js', () => ({
  DEFAULT_CDM_URL: 'https://test-cdm.example.com/config.json',
  fetchCdmConfig: vi.fn().mockResolvedValue({
    playground: keyset('pg'),
    network: keyset('nw'),
    api: { baseUrl: 'http://localhost:3001' },
  }),
}));

vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: vi.fn().mockResolvedValue({
    pamToken: null,
    agentId: 'agent-1',
    controlChannel: 'agent.agent-1.control',
  }),
  getAgent: vi.fn().mockResolvedValue({
    agentName: 'test_agent',
    name: 'Test Agent',
    listing: 'public',
    billingMode: 'free',
  }),
}));

function createPnStub(config: Record<string, unknown> = {}): MockPn {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  const pn: MockPn = {
    config,
    addListener: vi.fn((l) => listeners.push(l)),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    destroy: vi.fn(),
    setToken: vi.fn(),
    setFilterExpression: vi.fn(),
    setState: vi.fn(async () => {}),
    hereNow: vi.fn(async () => ({ channels: {} })),
    // The runtime's stream_setup handshake answers via a 403 carrying the response.
    publish: vi.fn(async (args: Record<string, unknown>) => {
      if (typeof args.channel === 'string' && args.channel.startsWith('setup.')) {
        const msg = args.message as Record<string, unknown>;
        throw {
          status: {
            statusCode: 403,
            errorData: {
              message: {
                ok: true,
                streamSetupResponse: {
                  taskId: msg.taskId,
                  streamId: msg.streamId,
                  channel: msg.channel,
                  direction: msg.direction,
                  phase: 'embedded',
                  token: FAKE_STREAM_CREDENTIAL,
                  tokenTtlMinutes: 62,
                },
              },
            },
          },
        };
      }
      return { timetoken: '1' };
    }),
    _configuration: { keySet: keyset('injected') },
    _simulateMessage: (message, userMetadata) => {
      for (const l of listeners) l.message?.({ message, userMetadata });
    },
  };
  return pn;
}

vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn((config: Record<string, unknown>) => {
    const pn = createPnStub(config);
    created.push(pn);
    return pn;
  }),
}));

vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = (await importOriginal()) as Record<string, unknown>;
  return {
    ...actual,
    StreamClient: class MockStreamClient {
      isActive = true;
      uuid = 'mock-stream-uuid';
      channel: string;
      constructor(opts: Record<string, unknown>) {
        streamClientArgs.push(opts);
        this.channel = opts.channel as string;
      }
      write = vi.fn();
      end = vi.fn(async () => {});
      onEnd = vi.fn();
    },
  };
});

type Handle = Awaited<ReturnType<typeof import('../src/runtime/agent-instance.js').startAgentInstance>>;

const PG = keyset('pg');
const NW = keyset('nw');

function startTaskMessage(taskId: string) {
  return {
    type: 'StartTask',
    taskId,
    ownerId: 'alice',
    taskKind: 'pipe',
    hasStream: true,
    writeToken: `wt-${taskId}`,
    duration: 60,
    durationExpiresAtMs: Date.now() + 3_600_000,
  };
}

function createHarness() {
  const gates = new Map<string, () => void>();
  const done = new Set<string>();
  // Dedicated stream ids are `{taskId}-{n}`.
  const streamClientFor = (taskId: string) =>
    streamClientArgs.find((a) => String(a.streamId).startsWith(`${taskId}-`));
  let handle: Handle | undefined;

  const start = async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');
    handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makePipeTestCard(),
      concurrency: 2,
      handler: async (task, ctx) => {
        await new Promise<void>((resolve) => gates.set(task.taskId, resolve));
        await ctx!.createStream({ subscribeGraceMs: 0 });
        done.add(task.taskId);
        return {};
      },
    });
    const control = created.find((pn) => pn.config.presenceTimeout === 20)!;
    await vi.waitFor(() => expect(control.subscribe).toHaveBeenCalled());
    return control;
  };

  const receive = async (control: MockPn, taskId: string, key: string) => {
    const before = created.length;
    control._simulateMessage(startTaskMessage(taskId), { instance: handle!.instanceId });
    await vi.waitFor(() => {
      const taskClient = created.slice(before).find((pn) => pn.config.subscribeKey === key);
      expect(taskClient).toBeDefined();
      expect(gates.has(taskId)).toBe(true);
    });
  };

  const switchTo = (control: MockPn, environment: 'playground' | 'network') => {
    const before = created.length;
    control._simulateMessage({ type: 'SwitchEnvironment', environment, pamToken: `${environment}-credential` });
    // The new control client replaces the old one and receives all later messages.
    const next = created.slice(before).find((pn) => pn.config.presenceTimeout === 20);
    expect(next).toBeDefined();
    return next!;
  };

  const finish = async (taskId: string) => {
    gates.get(taskId)!();
    await vi.waitFor(() => expect(done.has(taskId)).toBe(true));
  };

  return { start, receive, switchTo, finish, streamClientFor, stop: () => handle?.stop() };
}

describe('embedded stream keyset after SwitchEnvironment', () => {
  let consoleSpies: Array<{ mockRestore: () => void }> = [];

  beforeEach(() => {
    created.length = 0;
    streamClientArgs.length = 0;
    consoleSpies = [
      vi.spyOn(console, 'log').mockImplementation(() => {}),
      vi.spyOn(console, 'warn').mockImplementation(() => {}),
    ];
  });

  afterEach(() => {
    for (const spy of consoleSpies) spy.mockRestore();
  });

  it('builds the StreamClient with the keys the task was received on', async () => {
    const h = createHarness();
    try {
      const control = await h.start();
      await h.receive(control, 'task-pg', PG.subscribeKey);
      h.switchTo(control, 'network');
      await h.finish('task-pg');

      expect(streamClientArgs).toHaveLength(1);
      expect(streamClientArgs[0]).toMatchObject({ ...PG, token: FAKE_STREAM_CREDENTIAL });
    } finally {
      h.stop();
    }
  });

  it('builds a task received after the switch on the network keys', async () => {
    const h = createHarness();
    try {
      const control = await h.start();
      const networkControl = h.switchTo(control, 'network');
      await h.receive(networkControl, 'task-nw', NW.subscribeKey);
      await h.finish('task-nw');

      expect(streamClientArgs).toHaveLength(1);
      expect(streamClientArgs[0]).toMatchObject(NW);
    } finally {
      h.stop();
    }
  });

  it('gives each of two overlapping tasks the keys of its own environment', async () => {
    const h = createHarness();
    try {
      const control = await h.start();
      await h.receive(control, 'task-pg', PG.subscribeKey);
      const networkControl = h.switchTo(control, 'network');
      await h.receive(networkControl, 'task-nw', NW.subscribeKey);
      // Finish the older task last so "most recently received task wins" cannot pass.
      await h.finish('task-nw');
      await h.finish('task-pg');

      expect(streamClientArgs).toHaveLength(2);
      expect(h.streamClientFor('task-pg')).toMatchObject(PG);
      expect(h.streamClientFor('task-nw')).toMatchObject(NW);
    } finally {
      h.stop();
    }
  });

  it('builds the StreamClient on the injected client keys when no CDM config is used', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');
    const injected = createPnStub();
    let finished = false;
    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makePipeTestCard(),
      pubnub: injected as never,
      handler: async (_task, ctx) => {
        await ctx!.createStream({ subscribeGraceMs: 0 });
        finished = true;
        return {};
      },
    });
    try {
      await vi.waitFor(() => expect(injected.subscribe).toHaveBeenCalled());
      injected._simulateMessage(startTaskMessage('task-inj'), { instance: handle.instanceId });
      await vi.waitFor(() => expect(finished).toBe(true));

      expect(streamClientArgs).toHaveLength(1);
      expect(streamClientArgs[0]).toMatchObject({
        ...keyset('injected'),
      });
    } finally {
      handle.stop();
    }
  });
});
