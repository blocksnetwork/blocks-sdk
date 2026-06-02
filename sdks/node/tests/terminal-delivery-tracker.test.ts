import { describe, expect, it, vi } from 'vitest';
import { TerminalDeliveryTracker } from '../src/runtime/terminal-delivery-tracker.js';
import type { TerminalEvent } from '../src/runtime/task-session.js';

const evt = (state: 'completed' | 'failed' | 'canceled'): TerminalEvent => ({
  type: 'terminal',
  taskId: 't1',
  state,
});

describe('TerminalDeliveryTracker', () => {
  it('peek() returns null and isDelivered=false on a fresh tracker', () => {
    const t = new TerminalDeliveryTracker();
    expect(t.peek()).toBeNull();
    expect(t.isDelivered).toBe(false);
  });

  it('first tryDeliver invokes the callback and returns true', () => {
    const t = new TerminalDeliveryTracker();
    const cb = vi.fn();
    const result = t.tryDeliver(evt('canceled'), cb);
    expect(result).toBe(true);
    expect(cb).toHaveBeenCalledTimes(1);
    expect(cb).toHaveBeenCalledWith(evt('canceled'));
    expect(t.isDelivered).toBe(true);
  });

  it('subsequent tryDeliver does not invoke the callback and returns false', () => {
    const t = new TerminalDeliveryTracker();
    const cb1 = vi.fn();
    const cb2 = vi.fn();
    t.tryDeliver(evt('canceled'), cb1);
    const result = t.tryDeliver(evt('completed'), cb2);
    expect(result).toBe(false);
    expect(cb2).not.toHaveBeenCalled();
  });

  it('peek() after first delivery returns the first event (not the second)', () => {
    const t = new TerminalDeliveryTracker();
    t.tryDeliver(evt('canceled'), () => {});
    t.tryDeliver(evt('completed'), () => {});
    expect(t.peek()).toEqual(evt('canceled'));
  });

  it('marks delivered before invoking callback (re-entrant safety)', () => {
    const t = new TerminalDeliveryTracker();
    let observedDuringCallback = false;
    t.tryDeliver(evt('canceled'), () => {
      observedDuringCallback = t.isDelivered;
    });
    expect(observedDuringCallback).toBe(true);
  });
});
