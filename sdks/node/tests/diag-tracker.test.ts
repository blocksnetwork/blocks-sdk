import { describe, expect, it } from 'vitest';
import { _isDiagEntryStale } from '../src/runtime/agent-instance';

const STALE_MS = 60_000;

describe('_isDiagEntryStale', () => {
  it('returns false when lastStatusAt is null (never connected)', () => {
    expect(
      _isDiagEntryStale({
        lastStatusAt: null,
        lastCategory: null,
        now: 1_000_000,
        thresholdMs: STALE_MS,
      }),
    ).toBe(false);
  });

  it('returns false when last category is PNConnectedCategory regardless of how long ago', () => {
    expect(
      _isDiagEntryStale({
        lastStatusAt: 0,
        lastCategory: 'PNConnectedCategory',
        now: 10 * 60 * 1000,
        thresholdMs: STALE_MS,
      }),
    ).toBe(false);
  });

  it('returns false within threshold even for non-connected categories', () => {
    expect(
      _isDiagEntryStale({
        lastStatusAt: 1_000_000,
        lastCategory: 'PNNetworkIssuesCategory',
        now: 1_000_000 + 30_000,
        thresholdMs: STALE_MS,
      }),
    ).toBe(false);
  });

  it('returns true when past threshold AND last category is not PNConnectedCategory', () => {
    expect(
      _isDiagEntryStale({
        lastStatusAt: 1_000_000,
        lastCategory: 'PNNetworkIssuesCategory',
        now: 1_000_000 + 90_000,
        thresholdMs: STALE_MS,
      }),
    ).toBe(true);
  });

  it('returns true when past threshold AND last category is null (initial connect never observed)', () => {
    expect(
      _isDiagEntryStale({
        lastStatusAt: 1_000_000,
        lastCategory: null,
        now: 1_000_000 + 90_000,
        thresholdMs: STALE_MS,
      }),
    ).toBe(true);
  });
});
