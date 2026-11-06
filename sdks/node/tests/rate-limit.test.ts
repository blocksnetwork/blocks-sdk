import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it, vi } from 'vitest';
import {
  BlocksHttpError,
  RATE_LIMIT_BACKOFF_BASE_MS,
  RATE_LIMIT_BACKOFF_MAX_MS,
  RATE_LIMIT_JITTER_FRACTION,
  RATE_LIMIT_MIN_WAIT_MS,
  RATE_LIMIT_NO_HINT_HOLD_MS,
  RETRY_AFTER_MAX_MS,
  RateLimitHold,
  RateLimitedError,
  connectRetryDelayMs,
  isRateLimited,
  parseRetryAfter,
} from '../src/runtime/rate-limit.js';

// The Python SDK asserts its own constants against the same file.
const SHARED_CONSTANTS = fileURLToPath(new URL('../../../fixtures/rate-limit.json', import.meta.url));

describe('rate-limit constants', () => {
  it('match the values both SDKs share', () => {
    const { about: _about, ...shared } = JSON.parse(readFileSync(SHARED_CONSTANTS, 'utf8'));
    expect({
      retryAfterMaxSeconds: RETRY_AFTER_MAX_MS / 1000,
      noHintHoldSeconds: RATE_LIMIT_NO_HINT_HOLD_MS / 1000,
      backoffBaseSeconds: RATE_LIMIT_BACKOFF_BASE_MS / 1000,
      backoffMaxSeconds: RATE_LIMIT_BACKOFF_MAX_MS / 1000,
      minWaitSeconds: RATE_LIMIT_MIN_WAIT_MS / 1000,
      jitterFraction: RATE_LIMIT_JITTER_FRACTION,
    }).toEqual(shared);
  });
});

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

describe('connectRetryDelayMs', () => {
  it.each([
    ['honors Retry-After', 0, 7_000, 0, 7_000],
    ['floors a zero Retry-After', 3, 0, 0, 1_000],
    ['falls back to 30 s without one', 0, undefined, 0, 30_000],
    ['doubles per consecutive 429', 1, undefined, 0, 60_000],
    ['caps a long run of 429s', 2_000, undefined, 0, 600_000],
    ['adds at most a tenth of the wait as jitter', 0, 3_400_000, 1, 3_740_000],
    ['scales jitter with the wait', 0, undefined, 0.5, 31_500],
  ])('%s', (_name, attempt, retryAfterMs, random, expected) => {
    expect(connectRetryDelayMs(attempt, retryAfterMs, () => random)).toBeCloseTo(expected);
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

describe('isRateLimited', () => {
  it.each([
    ['a RateLimitedError', new RateLimitedError('held', 1_000), true],
    ['an HTTP 429 from a Blocks endpoint', new BlocksHttpError(429, 'slow down'), true],
    // A custom token provider's error is not a Blocks endpoint's, whatever its status.
    ['a token provider error with status 429', Object.assign(new Error('429'), { status: 429 }), false],
    ['a 429 wrapped as a cause', new Error('refresh failed', { cause: new RateLimitedError('held', 1) }), true],
    // A customer token endpoint's failure carries no status: it is a failed refresh, not a wait.
    ['a token endpoint 429', new Error('token endpoint failed: HTTP 429'), false],
    ['an HTTP 503', new BlocksHttpError(503, 'unavailable'), false],
  ])('%s → %s', (_name, err, expected) => {
    expect(isRateLimited(err)).toBe(expected);
  });
});
