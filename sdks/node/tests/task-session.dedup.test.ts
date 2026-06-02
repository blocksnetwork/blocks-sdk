import { describe, it, expect, vi, beforeEach } from 'vitest';
import { TaskSession } from '../src/runtime/task-session.js';

interface TaskMessageListener {
  message?: (event: {
    channel: string;
    message: unknown;
    timetoken?: string;
  }) => void;
}

function createMockPubNub() {
  let messageListener: TaskMessageListener | null = null;
  let simCounter = 0;

  return {
    addListener: vi.fn((listener: TaskMessageListener) => {
      messageListener = listener;
    }),
    removeListener: vi.fn(() => {
      messageListener = null;
    }),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    destroy: vi.fn(),
    _simulateMessage(channel: string, message: unknown, timetoken?: string) {
      if (messageListener?.message) {
        const tt = timetoken ?? `sim-${++simCounter}`;
        messageListener.message({ channel, message, timetoken: tt });
      }
    },
  };
}

type MockPubNub = ReturnType<typeof createMockPubNub>;

function asTaskSessionPubNub(
  pubnub: MockPubNub,
): NonNullable<ConstructorParameters<typeof TaskSession>[0]['pubnub']> {
  return pubnub as unknown as NonNullable<
    ConstructorParameters<typeof TaskSession>[0]['pubnub']
  >;
}

vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = (await importOriginal()) as Record<string, unknown>;
  return {
    ...actual,
    StreamClient: {
      fromDescriptor: vi.fn(() => ({
        isActive: false,
        end: vi.fn(async () => {}),
        onEnd: vi.fn(),
        onInboundDone: vi.fn(),
        inbound: {
          [Symbol.asyncIterator]: () => ({
            next: async () => ({ value: undefined, done: true }),
          }),
        },
      })),
    },
  };
});

describe('TaskSession terminal-event dedup (BLOCKS-370 R7)', () => {
  const taskId = 'task-1';
  const ownerId = 'alice';
  const agentName = 'echo';
  const channel = `u.${ownerId}.${taskId}`;

  let mockPubNub: ReturnType<typeof createMockPubNub>;
  let session: TaskSession;

  beforeEach(() => {
    vi.clearAllMocks();
    mockPubNub = createMockPubNub();
    session = new TaskSession({
      taskId,
      ownerId,
      readToken: 't4-token',
      agentName,
      pubnub: asTaskSessionPubNub(mockPubNub),
      sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      drainWindowMs: 2000,
    });
  });

  it('onTerminal fires exactly once when two wire terminals arrive', () => {
    const cb = vi.fn();
    session.onTerminal(cb);

    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'canceled',
    });
    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'canceled',
    });

    expect(cb).toHaveBeenCalledTimes(1);
  });

  it('first-terminal-wins: a later "completed" does not override the first "canceled"', () => {
    const cb = vi.fn();
    session.onTerminal(cb);

    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'canceled',
      reason: 'force_canceled',
    });
    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'completed',
    });

    expect(cb).toHaveBeenCalledTimes(1);
    expect(cb).toHaveBeenCalledWith(
      expect.objectContaining({ state: 'canceled', reason: 'force_canceled' }),
    );
  });

  it('waitForTerminal resolves once even when two wire terminals arrive', async () => {
    const promise = session.waitForTerminal();

    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'canceled',
    });
    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'canceled',
    });

    const evt = await promise;
    expect(evt.state).toBe('canceled');
  });

  it('waitForTerminal returns the first delivered terminal when called after delivery', async () => {
    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'canceled',
    });
    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'completed',
    });

    const evt = await session.waitForTerminal();
    expect(evt.state).toBe('canceled');
  });

  it('onTerminal registered after the wire terminal arrived fires once with the first event', () => {
    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'canceled',
    });

    const cb = vi.fn();
    session.onTerminal(cb);

    expect(cb).toHaveBeenCalledTimes(1);
    expect(cb).toHaveBeenCalledWith(
      expect.objectContaining({ state: 'canceled' }),
    );

    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'completed',
    });
    expect(cb).toHaveBeenCalledTimes(1);
  });
});
