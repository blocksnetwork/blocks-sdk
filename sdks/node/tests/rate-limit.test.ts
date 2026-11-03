import { describe, expect, it, vi } from 'vitest';
import {
  RETRY_AFTER_MAX_MS,
  RateLimitHold,
  RateLimitedError,
  parseRetryAfter,
  rateLimitBackoffMs,
} from '../src/runtime/rate-limit.js';

describe('parseRetryAfter', () => {
  const now = Date.parse('2026-01-01T00:00:00Z');

  it.each([
    ['delta-seconds', '120', 120_000],
    ['zero seconds', '0', 0],
    ['an HTTP-date in the future', 'Thu, 01 Jan 2026 00:01:30 GMT', 90_000],
    ['an HTTP-date in the past', 'Wed, 31 Dec 2025 23:59:00 GMT', 0],
    ['seconds past the cap', '86400', RETRY_AFTER_MAX_MS],
    ['an HTTP-date past the cap', 'Fri, 02 Jan 2026 00:00:00 GMT', RETRY_AFTER_MAX_MS],
  ])('reads %s', (_name, value, expected) => {
    expect(parseRetryAfter(value, now)).toBe(expected);
  });

  it.each([
    ['a missing header', null],
    ['an empty header', '  '],
    ['garbage', 'soon'],
    ['negative seconds', '-5'],
    ['fractional seconds', '1.5'],
  ])('ignores %s', (_name, value) => {
    expect(parseRetryAfter(value, now)).toBeUndefined();
  });
});

describe('rateLimitBackoffMs', () => {
  it.each([
    ['honors Retry-After', 0, 7_000, 7_000],
    ['floors a zero Retry-After', 3, 0, 1_000],
    ['doubles without one', 1, undefined, 60_000],
    ['caps a long run of 429s', 2_000, undefined, 600_000],
  ])('%s', (_name, attempt, retryAfterMs, expected) => {
    expect(rateLimitBackoffMs(attempt, retryAfterMs)).toBe(expected);
  });
});

describe('RateLimitHold', () => {
  it('keeps the longer of two overlapping holds', () => {
    vi.useFakeTimers({ now: 0 });
    try {
      const hold = new RateLimitHold();
      hold.record('refresh', { headers: new Headers({ 'retry-after': '120' }) });
      hold.record('refresh', { headers: new Headers() });

      vi.setSystemTime(119_000);
      expect(() => hold.check('refresh')).toThrow(RateLimitedError);
      vi.setSystemTime(120_000);
      expect(() => hold.check('refresh')).not.toThrow();
    } finally {
      vi.useRealTimers();
    }
  });
});
