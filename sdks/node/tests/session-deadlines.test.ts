import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { withinDeadline } from '../src/runtime/sessions/deadlines.js';

describe('withinDeadline', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('resolves with the work and never calls onExpired when the work settles first', async () => {
    const onExpired = vi.fn();
    await expect(withinDeadline(Promise.resolve('done'), 100, 'work', onExpired)).resolves.toBe('done');
    await vi.advanceTimersByTimeAsync(1_000);
    expect(onExpired).not.toHaveBeenCalled();
  });

  it('passes on the work\'s own rejection', async () => {
    const failure = new Error('boom');
    await expect(withinDeadline(Promise.reject(failure), 100, 'work')).rejects.toBe(failure);
  });

  it('rejects once the deadline passes, handing onExpired the same error', async () => {
    const onExpired = vi.fn();
    const racing = withinDeadline(new Promise(() => {}), 100, 'history', onExpired);
    const rejected = expect(racing).rejects.toThrow('history did not answer within 100 ms');
    await vi.advanceTimersByTimeAsync(100);
    await rejected;
    expect(onExpired).toHaveBeenCalledWith(expect.objectContaining({ message: 'history did not answer within 100 ms' }));
  });
});
