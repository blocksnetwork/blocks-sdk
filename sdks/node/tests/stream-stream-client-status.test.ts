/* eslint-disable @typescript-eslint/no-explicit-any */
/**
 * Tests for StreamClient.onError / status-error surfacing (Fix C, t7c).
 *
 * Classifier + dispatch + forced-termination coverage. Parity with the
 * Python test file `test_stream_stream_client_status.py`.
 *
 * Covers:
 * - `isStreamStatusError` classifier across both `Status` and
 *   `StatusEvent` shapes (v10.2.x PubNub JS SDK).
 * - `isFatalStreamCategory` fatal-allowlist membership.
 * - Dispatch: fatal category fires onError with `fatal: true`, forces
 *   termination, iterator exits cleanly.
 * - Dispatch: benign category (`PNConnectedCategory`) does not fire
 *   onError; stream remains active.
 * - Dispatch: non-fatal error category fires onError with
 *   `fatal: false`; stream remains active.
 * - Robustness: a consumer callback that throws does not break the
 *   forced termination path.
 */

import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import {
  StreamClient,
  _resetUuidCounter,
  isStreamStatusError,
  isFatalStreamCategory,
  FATAL_STREAM_ERROR_CATEGORIES,
  type StreamError,
} from '../src/stream/stream-client.js';

// --- PubNub mock -----------------------------------------------------------
// Mirrors the mock in stream-stream-client.test.ts so the two files
// behave identically with respect to constructor side-effects. Isolated
// from the other file's mock state because vitest scopes module mocks per
// test file.

const mockSetToken = vi.fn();
const mockSetFilterExpression = vi.fn();
const mockAddListener = vi.fn();
const mockRemoveListener = vi.fn();
const mockSubscribe = vi.fn();
const mockUnsubscribe = vi.fn();
const mockUnsubscribeAll = vi.fn();
const mockDestroy = vi.fn();
const mockPublish = vi.fn().mockResolvedValue({ timetoken: '17000000000000000' });
const mockHereNow = vi.fn().mockResolvedValue({ channels: {} });

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation((config: any) => ({
      _config: config,
      setToken: mockSetToken,
      setFilterExpression: mockSetFilterExpression,
      addListener: mockAddListener,
      removeListener: mockRemoveListener,
      subscribe: mockSubscribe,
      unsubscribe: mockUnsubscribe,
      unsubscribeAll: mockUnsubscribeAll,
      destroy: mockDestroy,
      publish: mockPublish,
      hereNow: mockHereNow,
    })),
  };
});

function makeInboundClient(): StreamClient {
  return new StreamClient({
    subscribeKey: 'sub-key',
    publishKey: 'pub-key',
    token: 'test-token',
    agentName: 'test_agent',
    streamId: 'my-stream',
    direction: 'inbound',
  } as any);
}

/** Extract the listener object registered by setupInbound(). */
function getRegisteredListener(): { message?: (e: any) => void; status?: (s: any) => void } {
  const listener = mockAddListener.mock.calls.find(
    (call: any[]) => call[0]?.status && call[0]?.message,
  )?.[0];
  expect(listener).toBeDefined();
  return listener;
}

// --- Tests -----------------------------------------------------------------

describe('isStreamStatusError (classifier)', () => {
  it('returns true when status.error === true (Status shape)', () => {
    expect(isStreamStatusError({ error: true, statusCode: 200, category: 'X' })).toBe(true);
  });

  it('returns true when status.error is a non-empty string (StatusEvent shape)', () => {
    expect(isStreamStatusError({ error: 'PNAccessDeniedCategory', category: 'PNAccessDeniedCategory' })).toBe(true);
  });

  it('returns true when status.error is a truthy category string', () => {
    expect(isStreamStatusError({ error: 'some error message' })).toBe(true);
  });

  it('returns false when status.error is absent and category is benign', () => {
    expect(isStreamStatusError({ category: 'PNConnectedCategory' })).toBe(false);
  });

  it('returns false when status.error === false', () => {
    expect(isStreamStatusError({ error: false, statusCode: 200, category: 'PNConnectedCategory' })).toBe(false);
  });

  it('returns true when statusCode >= 400 and error is falsy', () => {
    expect(isStreamStatusError({ error: false, statusCode: 403, category: 'X' })).toBe(true);
    expect(isStreamStatusError({ statusCode: 500 })).toBe(true);
  });

  it('returns false when statusCode < 400 and error is falsy', () => {
    expect(isStreamStatusError({ error: false, statusCode: 200 })).toBe(false);
    expect(isStreamStatusError({ statusCode: 399 })).toBe(false);
  });

  it('returns true as fatal-category fallback when error/statusCode absent', () => {
    expect(isStreamStatusError({ category: 'PNAccessDeniedCategory' })).toBe(true);
    expect(isStreamStatusError({ category: 'PNBadRequestCategory' })).toBe(true);
  });

  it('returns false for unknown category without error/statusCode', () => {
    expect(isStreamStatusError({ category: 'PNBogusCategory' })).toBe(false);
  });

  it('returns false for empty/null/undefined status', () => {
    expect(isStreamStatusError(null)).toBe(false);
    expect(isStreamStatusError(undefined)).toBe(false);
    expect(isStreamStatusError({})).toBe(false);
  });

  it('returns false when category is not a string', () => {
    expect(isStreamStatusError({ category: 123 as any })).toBe(false);
    expect(isStreamStatusError({ category: null as any })).toBe(false);
  });

  it('handles numeric non-integer statusCode correctly (typeof check)', () => {
    // String "403" must NOT trigger the numeric gate (type mismatch).
    expect(isStreamStatusError({ statusCode: '403' as any, category: 'X' })).toBe(false);
  });

  // Non-fatal transport errors: these MUST surface via onError so consumers
  // can decide how to react, but they MUST NOT trigger force-terminate.
  // Shapes below mirror what pubnub-js 10.2.x emits for transient network
  // and timeout conditions.
  describe('non-fatal transport errors surface via classifier', () => {
    it('PNTimeoutCategory with error=true is detected', () => {
      expect(
        isStreamStatusError({ category: 'PNTimeoutCategory', error: true, statusCode: 0 }),
      ).toBe(true);
    });

    it('PNNetworkIssuesCategory with error=true is detected', () => {
      expect(
        isStreamStatusError({ category: 'PNNetworkIssuesCategory', error: true, statusCode: 0 }),
      ).toBe(true);
    });

    it('PNConnectionErrorCategory with string error is detected', () => {
      expect(
        isStreamStatusError({ category: 'PNConnectionErrorCategory', error: 'PNTimeoutCategory' }),
      ).toBe(true);
    });

    it('PNDisconnectedUnexpectedlyCategory with string error is detected', () => {
      expect(
        isStreamStatusError({ category: 'PNDisconnectedUnexpectedlyCategory', error: 'PNTimeoutCategory' }),
      ).toBe(true);
    });
  });

  // Category-only transport-state announcements: NOT errors. The classifier
  // must NOT report these as errors; the dispatcher therefore will not
  // invoke onError for them.
  describe('transport-state announcements are not errors', () => {
    it('PNNetworkDownCategory is not an error', () => {
      expect(isStreamStatusError({ category: 'PNNetworkDownCategory' })).toBe(false);
    });

    it('PNNetworkUpCategory is not an error', () => {
      expect(isStreamStatusError({ category: 'PNNetworkUpCategory' })).toBe(false);
    });

    it('PNConnectedCategory is not an error', () => {
      expect(isStreamStatusError({ category: 'PNConnectedCategory' })).toBe(false);
    });

    it('PNReconnectedCategory is not an error', () => {
      expect(isStreamStatusError({ category: 'PNReconnectedCategory' })).toBe(false);
    });
  });
});

describe('isFatalStreamCategory', () => {
  it('accepts PNAccessDeniedCategory', () => {
    expect(isFatalStreamCategory('PNAccessDeniedCategory')).toBe(true);
  });

  it('accepts PNBadRequestCategory', () => {
    expect(isFatalStreamCategory('PNBadRequestCategory')).toBe(true);
  });

  it('rejects non-fatal error categories', () => {
    expect(isFatalStreamCategory('PNNetworkIssuesCategory')).toBe(false);
    expect(isFatalStreamCategory('PNTimeoutCategory')).toBe(false);
    expect(isFatalStreamCategory('PNNetworkDownCategory')).toBe(false);
  });

  it('rejects benign categories', () => {
    expect(isFatalStreamCategory('PNConnectedCategory')).toBe(false);
    expect(isFatalStreamCategory('PNReconnectedCategory')).toBe(false);
  });

  it('rejects empty / null / undefined', () => {
    expect(isFatalStreamCategory('')).toBe(false);
    expect(isFatalStreamCategory(null)).toBe(false);
    expect(isFatalStreamCategory(undefined)).toBe(false);
  });

  it('FATAL_STREAM_ERROR_CATEGORIES is exactly the allowlisted set', () => {
    expect(FATAL_STREAM_ERROR_CATEGORIES.size).toBe(2);
    expect(FATAL_STREAM_ERROR_CATEGORIES.has('PNAccessDeniedCategory')).toBe(true);
    expect(FATAL_STREAM_ERROR_CATEGORIES.has('PNBadRequestCategory')).toBe(true);
  });
});

describe('StreamClient status dispatch', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    _resetUuidCounter();
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('registers a status handler on setupInbound() for inbound streams', () => {
    makeInboundClient();
    const listener = getRegisteredListener();
    expect(typeof listener.message).toBe('function');
    expect(typeof listener.status).toBe('function');
  });

  it('fatal category (PNAccessDeniedCategory) fires onError with fatal:true and forces termination', async () => {
    const client = makeInboundClient();
    const listener = getRegisteredListener();

    const received: StreamError[] = [];
    client.onError((e) => received.push(e));

    const inboundDone = vi.fn();
    client.onInboundDone(inboundDone);

    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});

    listener.status!({
      category: 'PNAccessDeniedCategory',
      error: true,
      statusCode: 403,
      errorData: { message: 'PAM revoked' },
      operation: 'PNSubscribeOperation',
    });

    // end() is async; give the microtask queue a chance to flush.
    await Promise.resolve();
    await Promise.resolve();

    expect(received).toHaveLength(1);
    expect(received[0].category).toBe('PNAccessDeniedCategory');
    expect(received[0].fatal).toBe(true);
    expect(received[0].channel).toBe(client.channel);
    expect(received[0].error).toEqual({ message: 'PAM revoked' });
    expect(typeof received[0].timestamp).toBe('number');

    expect(client.isActive).toBe(false);
    expect(inboundDone).toHaveBeenCalledTimes(1);
    expect(warnSpy).toHaveBeenCalled();

    // Iterator must exit cleanly, not hang.
    const iter = client.inbound[Symbol.asyncIterator]();
    const result = await iter.next();
    expect(result.done).toBe(true);
  });

  it('fatal category force-terminates cleanly even when bundle.end()/publishEndMarker() throw (write-capable stream)', async () => {
    // Regression: on fatal PAM revocation, bundle.end() and
    // publishEndMarker() use the dead token and throw. If end() lets
    // that throw propagate, the teardown below (iterator signal,
    // listener removal, destroy) never runs and the consumer's
    // for-await iterator hangs. This test simulates that by swapping
    // the bundle with one whose end/publishEndMarker reject.
    const client = new StreamClient({
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      token: 'revoked-token',
      agentName: 'test_agent',
      streamId: 'my-bidi-stream',
      direction: 'bidirectional',
    } as any);

    // Replace the real bundle with one that rejects on end() — the exact
    // failure mode on a revoked T7c.
    (client as any).bundle = {
      end: vi.fn().mockRejectedValue(new Error('PAM denied: token revoked')),
      publishEndMarker: vi.fn().mockRejectedValue(new Error('PAM denied: token revoked')),
      write: vi.fn(),
    };

    const listener = getRegisteredListener();

    const received: StreamError[] = [];
    client.onError((e) => received.push(e));

    const inboundDone = vi.fn();
    client.onInboundDone(inboundDone);

    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});

    listener.status!({
      category: 'PNAccessDeniedCategory',
      error: true,
      statusCode: 403,
    });

    // Give end()'s awaited catches time to settle.
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    // onError still fires with fatal:true.
    expect(received).toHaveLength(1);
    expect(received[0].fatal).toBe(true);

    // Despite bundle failures, teardown completed:
    expect(client.isActive).toBe(false);
    expect(inboundDone).toHaveBeenCalledTimes(1);

    // Iterator exits cleanly instead of hanging.
    const iter = client.inbound[Symbol.asyncIterator]();
    const result = await iter.next();
    expect(result.done).toBe(true);

    // And the bundle failure was logged as a warning.
    expect(warnSpy).toHaveBeenCalled();
  });

  it('fatal category (PNBadRequestCategory) also forces termination', async () => {
    const client = makeInboundClient();
    const listener = getRegisteredListener();

    const received: StreamError[] = [];
    client.onError((e) => received.push(e));

    vi.spyOn(console, 'warn').mockImplementation(() => {});

    listener.status!({
      category: 'PNBadRequestCategory',
      error: true,
      statusCode: 400,
    });

    await Promise.resolve();
    await Promise.resolve();

    expect(received).toHaveLength(1);
    expect(received[0].fatal).toBe(true);
    expect(client.isActive).toBe(false);
  });

  it('benign category (PNConnectedCategory) does not fire onError, stream stays active', () => {
    const client = makeInboundClient();
    const listener = getRegisteredListener();

    const received: StreamError[] = [];
    client.onError((e) => received.push(e));

    listener.status!({
      category: 'PNConnectedCategory',
      error: false,
      statusCode: 200,
    });

    expect(received).toHaveLength(0);
    expect(client.isActive).toBe(true);
  });

  it('non-fatal error category fires onError with fatal:false, stream stays active', () => {
    const client = makeInboundClient();
    const listener = getRegisteredListener();

    const received: StreamError[] = [];
    client.onError((e) => received.push(e));

    vi.spyOn(console, 'warn').mockImplementation(() => {});

    listener.status!({
      category: 'PNNetworkIssuesCategory',
      error: true,
      statusCode: 0,
    });

    expect(received).toHaveLength(1);
    expect(received[0].category).toBe('PNNetworkIssuesCategory');
    expect(received[0].fatal).toBe(false);
    expect(client.isActive).toBe(true);
  });

  it('consumer callback that throws does not break forced termination', async () => {
    const client = makeInboundClient();
    const listener = getRegisteredListener();

    client.onError(() => {
      throw new Error('consumer handler boom');
    });

    const received: StreamError[] = [];
    client.onError((e) => received.push(e));

    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    vi.spyOn(console, 'warn').mockImplementation(() => {});

    listener.status!({
      category: 'PNAccessDeniedCategory',
      error: true,
      statusCode: 403,
    });

    await Promise.resolve();
    await Promise.resolve();

    // First callback threw; second one still fired.
    expect(received).toHaveLength(1);
    expect(received[0].fatal).toBe(true);

    // console.error captured the consumer exception.
    expect(errSpy).toHaveBeenCalled();

    // Forced termination still happened.
    expect(client.isActive).toBe(false);

    // Iterator exits cleanly.
    const iter = client.inbound[Symbol.asyncIterator]();
    const result = await iter.next();
    expect(result.done).toBe(true);
  });

  it('multiple onError callbacks all fire in registration order', () => {
    const client = makeInboundClient();
    const listener = getRegisteredListener();

    const order: string[] = [];
    client.onError(() => order.push('first'));
    client.onError(() => order.push('second'));
    client.onError(() => order.push('third'));

    vi.spyOn(console, 'warn').mockImplementation(() => {});

    listener.status!({
      category: 'PNNetworkIssuesCategory',
      error: true,
    });

    expect(order).toEqual(['first', 'second', 'third']);
  });

  it('status handler tolerates malformed status (no throw)', () => {
    const client = makeInboundClient();
    const listener = getRegisteredListener();

    const received: StreamError[] = [];
    client.onError((e) => received.push(e));

    // None of these should throw or fire onError.
    expect(() => listener.status!(null)).not.toThrow();
    expect(() => listener.status!(undefined)).not.toThrow();
    expect(() => listener.status!({})).not.toThrow();
    expect(() => listener.status!({ category: 123 })).not.toThrow();

    expect(received).toHaveLength(0);
    expect(client.isActive).toBe(true);
  });

  it('StreamError object carries channel and timestamp', () => {
    const client = makeInboundClient();
    const listener = getRegisteredListener();

    const received: StreamError[] = [];
    client.onError((e) => received.push(e));

    vi.spyOn(console, 'warn').mockImplementation(() => {});

    const before = Date.now();
    listener.status!({
      category: 'PNNetworkIssuesCategory',
      error: true,
    });
    const after = Date.now();

    expect(received).toHaveLength(1);
    expect(received[0].channel).toBe(client.channel);
    expect(received[0].timestamp).toBeGreaterThanOrEqual(before);
    expect(received[0].timestamp).toBeLessThanOrEqual(after);
  });
});
