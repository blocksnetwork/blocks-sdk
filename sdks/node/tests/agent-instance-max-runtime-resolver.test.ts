/**
 * Fix B (t7c_token_lifecycle) — `maxRunningTimeSec` resolver and request-task
 * TTL derivation tests.
 *
 * Covers two pure helpers exported from `agent-instance.ts`:
 *   - `resolveMaxRunningTimeSec(opts, card)` — single source of truth for
 *     the instance-scoped max-running-time; logs on divergence.
 *   - `computeStreamDurationMinutes(taskDuration, isPipeTask, effective)` —
 *     derives the `durationMinutes` passed to the `streamSetup` Function for
 *     each task.
 *
 * Integration tests that drive the full `startAgentInstance` flow through
 * `ctx.createStream` and assert the published `stream_setup` payload live
 * under `agent-instance-p3.test.ts` and the live-test suite.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  computeStreamDurationMinutes,
  resolveMaxRunningTimeSec,
} from '../src/runtime/agent-instance.js';

// ---------------------------------------------------------------------------
// resolveMaxRunningTimeSec
// ---------------------------------------------------------------------------

describe('resolveMaxRunningTimeSec', () => {
  let infoSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    infoSpy = vi.spyOn(console, 'info').mockImplementation(() => {});
  });

  afterEach(() => {
    infoSpy.mockRestore();
  });

  it('returns opts value when only opts is set', () => {
    expect(resolveMaxRunningTimeSec(900, undefined)).toBe(900);
    expect(infoSpy).not.toHaveBeenCalled();
  });

  it('returns card value when only card is set', () => {
    expect(resolveMaxRunningTimeSec(undefined, 1800)).toBe(1800);
    expect(infoSpy).not.toHaveBeenCalled();
  });

  it('returns the shared value when both are set and equal, with no log', () => {
    expect(resolveMaxRunningTimeSec(900, 900)).toBe(900);
    expect(infoSpy).not.toHaveBeenCalled();
  });

  it('returns opts and logs once at info level when values disagree', () => {
    expect(resolveMaxRunningTimeSec(900, 1800)).toBe(900);
    expect(infoSpy).toHaveBeenCalledTimes(1);
    const msg = String(infoSpy.mock.calls[0]?.[0] ?? '');
    expect(msg).toContain('opts.maxRunningTimeSec (900)');
    expect(msg).toContain('card.runtime.maxRunningTimeSec (1800)');
  });

  it('returns undefined when neither source is set', () => {
    expect(resolveMaxRunningTimeSec(undefined, undefined)).toBeUndefined();
    expect(infoSpy).not.toHaveBeenCalled();
  });

  it('treats 0 as a set value (falls back on undefined only)', () => {
    // Practically, 0 is an invalid max-running-time, but the resolver is
    // intentionally shape-only: it uses `undefined` as the sentinel, so a
    // caller-provided 0 is returned as-is rather than silently coerced.
    expect(resolveMaxRunningTimeSec(0, 1800)).toBe(0);
    expect(infoSpy).toHaveBeenCalledTimes(1);
  });
});

// ---------------------------------------------------------------------------
// computeStreamDurationMinutes
// ---------------------------------------------------------------------------

describe('computeStreamDurationMinutes (request-task TTL matrix)', () => {
  it('task.duration wins for request tasks', () => {
    expect(computeStreamDurationMinutes(45, false, 1800)).toBe(45);
  });

  it('task.duration wins for pipe tasks', () => {
    expect(computeStreamDurationMinutes(120, true, 1800)).toBe(120);
  });

  it('pipe task with no duration falls back to 60 (unchanged default)', () => {
    expect(computeStreamDurationMinutes(undefined, true, undefined)).toBe(60);
    expect(computeStreamDurationMinutes(undefined, true, 1800)).toBe(60);
  });

  it('request task with effectiveMaxRunningTimeSec=1800 → 30 minutes', () => {
    expect(computeStreamDurationMinutes(undefined, false, 1800)).toBe(30);
  });

  it('request task with no effective value → 60 (3600s default)', () => {
    expect(computeStreamDurationMinutes(undefined, false, undefined)).toBe(60);
  });

  it('request task with effectiveMaxRunningTimeSec=30s → 1 minute (ceil)', () => {
    expect(computeStreamDurationMinutes(undefined, false, 30)).toBe(1);
  });

  it('request task with effectiveMaxRunningTimeSec=59s → 1 minute (ceil rounding)', () => {
    expect(computeStreamDurationMinutes(undefined, false, 59)).toBe(1);
  });

  it('request task with effectiveMaxRunningTimeSec=61s → 2 minutes (ceil over boundary)', () => {
    expect(computeStreamDurationMinutes(undefined, false, 61)).toBe(2);
  });

  it('request task with effectiveMaxRunningTimeSec=3600s → 60 minutes', () => {
    expect(computeStreamDurationMinutes(undefined, false, 3600)).toBe(60);
  });
});
