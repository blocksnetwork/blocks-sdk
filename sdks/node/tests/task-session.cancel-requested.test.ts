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

describe('TaskSession.onCancelRequested', () => {
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

  it('fires onCancelRequested when a cancel_requested event arrives on the wire', () => {
    const cb = vi.fn();
    session.onCancelRequested(cb);

    mockPubNub._simulateMessage(channel, {
      type: 'cancel_requested',
      protocolVersion: '2026-05-27',
      taskId,
      ts: 1716800000000,
    });

    expect(cb).toHaveBeenCalledTimes(1);
    expect(cb).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'cancel_requested',
        taskId,
        ts: 1716800000000,
      }),
    );
  });

  it('multiple subscribers all receive the event', () => {
    const cb1 = vi.fn();
    const cb2 = vi.fn();
    session.onCancelRequested(cb1);
    session.onCancelRequested(cb2);

    mockPubNub._simulateMessage(channel, {
      type: 'cancel_requested',
      protocolVersion: '2026-05-27',
      taskId,
      ts: 1,
    });

    expect(cb1).toHaveBeenCalledTimes(1);
    expect(cb2).toHaveBeenCalledTimes(1);
  });

  it('unsubscribe stops the callback', () => {
    const cb = vi.fn();
    const unsubscribe = session.onCancelRequested(cb);
    unsubscribe();

    mockPubNub._simulateMessage(channel, {
      type: 'cancel_requested',
      protocolVersion: '2026-05-27',
      taskId,
      ts: 1,
    });

    expect(cb).not.toHaveBeenCalled();
  });

  it('does not fire after a terminal has been delivered', () => {
    const cb = vi.fn();
    session.onCancelRequested(cb);

    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'canceled',
    });
    mockPubNub._simulateMessage(channel, {
      type: 'cancel_requested',
      protocolVersion: '2026-05-27',
      taskId,
      ts: 1,
    });

    expect(cb).not.toHaveBeenCalled();
  });

  it('fires zero-or-once: duplicate cancel_requested wire emissions are suppressed', () => {
    const cb = vi.fn();
    session.onCancelRequested(cb);

    mockPubNub._simulateMessage(channel, {
      type: 'cancel_requested',
      protocolVersion: '2026-05-27',
      taskId,
      ts: 1716800000000,
    });
    mockPubNub._simulateMessage(channel, {
      type: 'cancel_requested',
      protocolVersion: '2026-05-27',
      taskId,
      ts: 1716800001000,
    });

    expect(cb).toHaveBeenCalledTimes(1);
    expect(cb).toHaveBeenCalledWith(
      expect.objectContaining({ ts: 1716800000000 }),
    );
  });

  it('replays the first cancel_requested to a callback registered after the event arrived', () => {
    mockPubNub._simulateMessage(channel, {
      type: 'cancel_requested',
      protocolVersion: '2026-05-27',
      taskId,
      ts: 1700000000000,
    });

    const cb = vi.fn();
    session.onCancelRequested(cb);

    expect(cb).toHaveBeenCalledTimes(1);
    expect(cb.mock.calls[0][0]).toMatchObject({
      type: 'cancel_requested',
      taskId,
      ts: 1700000000000,
    });
  });

  it('does not double-fire cancel_requested across registration order', () => {
    const cbEarly = vi.fn();
    session.onCancelRequested(cbEarly);

    mockPubNub._simulateMessage(channel, {
      type: 'cancel_requested',
      protocolVersion: '2026-05-27',
      taskId,
      ts: 1700000000000,
    });

    const cbLate = vi.fn();
    session.onCancelRequested(cbLate);

    expect(cbEarly).toHaveBeenCalledTimes(1);
    expect(cbLate).toHaveBeenCalledTimes(1);
  });

  it('does not replay cancel_requested to a callback registered after a terminal was delivered', () => {
    // Wire order: cancel_requested, then terminal. A consumer that registers
    // onCancelRequested AFTER both must NOT receive a replayed cancel_requested
    // (causality: the task is already terminal).
    mockPubNub._simulateMessage(channel, {
      type: 'cancel_requested',
      protocolVersion: '2026-05-27',
      taskId,
      ts: 1700000000000,
    });
    mockPubNub._simulateMessage(channel, {
      type: 'terminal',
      taskId,
      state: 'canceled',
    });

    const cb = vi.fn();
    session.onCancelRequested(cb);

    expect(cb).not.toHaveBeenCalled();
  });
});
