import { describe, it, expect, vi } from 'vitest';
import { TaskClient } from '../src/runtime/task-client.js';

interface MockListener {
  message?: (event: {
    channel: string;
    message: unknown;
    timetoken?: string;
  }) => void;
}

function createMockPubNub() {
  const listeners = new Set<MockListener>();
  return {
    addListener: vi.fn((l: MockListener) => {
      listeners.add(l);
    }),
    removeListener: vi.fn((l: MockListener) => {
      listeners.delete(l);
    }),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    destroy: vi.fn(),
    _emit(channel: string, message: unknown, timetoken = '0') {
      for (const l of listeners) {
        l.message?.({ channel, message, timetoken });
      }
    },
  };
}

describe('TaskClient.subscribeToTask terminal dedup', () => {
  it('onTerminal fires exactly once when two wire terminals arrive on the same channel', () => {
    const onTerminal = vi.fn();
    const pubnub = createMockPubNub();
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pubnub as any,
    });

    const sub = client.subscribeToTask('t1', 'org-1', { onTerminal });

    pubnub._emit('u.org-1.t1', {
      type: 'terminal',
      taskId: 't1',
      state: 'canceled',
    });
    pubnub._emit('u.org-1.t1', {
      type: 'terminal',
      taskId: 't1',
      state: 'canceled',
    });

    expect(onTerminal).toHaveBeenCalledTimes(1);
    sub.unsubscribe();
  });

  it('first-terminal-wins across different states (cancel survives a late completed)', () => {
    const onTerminal = vi.fn();
    const pubnub = createMockPubNub();
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pubnub as any,
    });

    const sub = client.subscribeToTask('t2', 'org-1', { onTerminal });

    pubnub._emit('u.org-1.t2', {
      type: 'terminal',
      taskId: 't2',
      state: 'canceled',
      reason: 'force_canceled',
    });
    pubnub._emit('u.org-1.t2', {
      type: 'terminal',
      taskId: 't2',
      state: 'completed',
    });

    expect(onTerminal).toHaveBeenCalledTimes(1);
    expect(onTerminal).toHaveBeenCalledWith(
      expect.objectContaining({ state: 'canceled', reason: 'force_canceled' }),
    );
    sub.unsubscribe();
  });

  it('subscriptions are isolated: a terminal on subscription A does not silence subscription B', () => {
    const onTerminalA = vi.fn();
    const onTerminalB = vi.fn();
    const pubnub = createMockPubNub();
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pubnub as any,
    });

    const subA = client.subscribeToTask('a', 'org-1', { onTerminal: onTerminalA });
    const subB = client.subscribeToTask('b', 'org-1', { onTerminal: onTerminalB });

    pubnub._emit('u.org-1.a', { type: 'terminal', taskId: 'a', state: 'canceled' });
    pubnub._emit('u.org-1.a', { type: 'terminal', taskId: 'a', state: 'canceled' });
    pubnub._emit('u.org-1.b', { type: 'terminal', taskId: 'b', state: 'completed' });

    expect(onTerminalA).toHaveBeenCalledTimes(1);
    expect(onTerminalB).toHaveBeenCalledTimes(1);

    subA.unsubscribe();
    subB.unsubscribe();
  });
});

describe('TaskClient.subscribeToTask onCancelRequested', () => {
  it('dispatches cancel_requested events to onCancelRequested', () => {
    const onCancelRequested = vi.fn();
    const pubnub = createMockPubNub();
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pubnub as any,
    });

    const sub = client.subscribeToTask('tc1', 'org-1', { onCancelRequested });

    pubnub._emit('u.org-1.tc1', {
      type: 'cancel_requested',
      taskId: 'tc1',
      ts: 1716800000000,
    });

    expect(onCancelRequested).toHaveBeenCalledTimes(1);
    expect(onCancelRequested).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'cancel_requested',
        taskId: 'tc1',
        ts: 1716800000000,
      }),
    );
    sub.unsubscribe();
  });

  it('fires zero-or-once: duplicate cancel_requested wire emissions are suppressed', () => {
    const onCancelRequested = vi.fn();
    const pubnub = createMockPubNub();
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pubnub as any,
    });

    const sub = client.subscribeToTask('tc2', 'org-1', { onCancelRequested });

    pubnub._emit('u.org-1.tc2', {
      type: 'cancel_requested',
      taskId: 'tc2',
      ts: 1716800000000,
    });
    pubnub._emit('u.org-1.tc2', {
      type: 'cancel_requested',
      taskId: 'tc2',
      ts: 1716800001000,
    });

    expect(onCancelRequested).toHaveBeenCalledTimes(1);
    sub.unsubscribe();
  });

  it('cancel_requested is suppressed once a terminal has been delivered', () => {
    const onTerminal = vi.fn();
    const onCancelRequested = vi.fn();
    const pubnub = createMockPubNub();
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pubnub as any,
    });

    const sub = client.subscribeToTask('tc3', 'org-1', {
      onTerminal,
      onCancelRequested,
    });

    pubnub._emit('u.org-1.tc3', {
      type: 'terminal',
      taskId: 'tc3',
      state: 'completed',
    });
    pubnub._emit('u.org-1.tc3', {
      type: 'cancel_requested',
      taskId: 'tc3',
      ts: 1716800000000,
    });

    expect(onTerminal).toHaveBeenCalledTimes(1);
    expect(onCancelRequested).not.toHaveBeenCalled();
    sub.unsubscribe();
  });

  it('catch-all onEvent receives cancel_requested even after typed dispatch is suppressed', () => {
    const onEvent = vi.fn();
    const onCancelRequested = vi.fn();
    const pubnub = createMockPubNub();
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pubnub as any,
    });

    const sub = client.subscribeToTask('tc4', 'org-1', {
      onEvent,
      onCancelRequested,
    });

    pubnub._emit('u.org-1.tc4', {
      type: 'cancel_requested',
      taskId: 'tc4',
      ts: 1,
    });
    pubnub._emit('u.org-1.tc4', {
      type: 'cancel_requested',
      taskId: 'tc4',
      ts: 2,
    });

    expect(onCancelRequested).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledTimes(2);
    sub.unsubscribe();
  });
});
