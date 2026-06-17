import { describe, it, expect, afterEach, vi } from 'vitest';
import { isProfilingEnabled, logDispatchTiming } from '../src/runtime/profiling.js';

afterEach(() => {
  delete process.env.BLOCKS_PROFILE;
  vi.restoreAllMocks();
});

describe('isProfilingEnabled', () => {
  it('false when unset', () => {
    expect(isProfilingEnabled()).toBe(false);
  });
  it('true when token present', () => {
    process.env.BLOCKS_PROFILE = 'timing';
    expect(isProfilingEnabled()).toBe(true);
  });
  it('true when token present among others', () => {
    process.env.BLOCKS_PROFILE = 'foo,timing,bar';
    expect(isProfilingEnabled()).toBe(true);
  });
  it('false for unrelated tokens', () => {
    process.env.BLOCKS_PROFILE = 'foo,bar';
    expect(isProfilingEnabled()).toBe(false);
  });
});

describe('logDispatchTiming', () => {
  it('no-op when disabled', () => {
    const spy = vi.spyOn(console, 'log').mockImplementation(() => {});
    logDispatchTiming('t1', { receivedMs: 0, runningMs: 3, handlerMs: 5 });
    expect(spy).not.toHaveBeenCalled();
  });

  it('emits non-negative phase deltas given chronological marks', () => {
    process.env.BLOCKS_PROFILE = 'timing';
    const spy = vi.spyOn(console, 'log').mockImplementation(() => {});
    // Chronological order: received(0) -> running(3) -> handler(5).
    logDispatchTiming('t1', { receivedMs: 0, runningMs: 3, handlerMs: 5 });
    expect(spy).toHaveBeenCalledTimes(1);
    const meta = spy.mock.calls[0][1] as Record<string, number>;
    expect(meta.received_to_running_ms).toBe(3);
    expect(meta.running_to_handler_ms).toBe(2);
    expect(meta.received_to_handler_ms).toBe(5);
  });
});
