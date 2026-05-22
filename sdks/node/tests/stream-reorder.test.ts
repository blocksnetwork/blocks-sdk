/* eslint-disable @typescript-eslint/no-explicit-any */
/**
 * Tests for StreamClient reorder buffer.
 *
 * Covers the 11 IMPL-doc scenarios plus post-review additions:
 * 1.  ordered passthrough
 * 2.  out-of-order reorder
 * 3.  adjacent swap
 * 4.  duplicate drop
 * 5.  gap with timeout
 * 6.  stream_end before late data
 * 7.  stream_end with gap + timeout
 * 8.  events format (seq starts at 1)
 * 9.  bytes first arrival out of order
 * 10. tail-gap: stream_end with lost final msgs
 * 11. malformed stream_end (no integer seq) warned and ignored
 * 12. reorderTimeoutMs=0 disables
 */

import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { StreamClient, _resetUuidCounter } from '../src/stream/stream-client.js';

// PubNub mock
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
    default: vi.fn().mockImplementation(() => ({
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

/** Create an inbound StreamClient with configurable options. */
function makeInboundClient(overrides: Record<string, unknown> = {}): StreamClient {
  return new StreamClient({
    subscribeKey: 'sub-key',
    publishKey: 'pub-key',
    token: 'test-token',
    agentName: 'test_agent',
    streamId: 'reorder-stream',
    direction: 'inbound',
    format: 'bytes',
    ...overrides,
  } as any);
}

/** Get the PubNub message listener registered by setupInbound. */
function getListener(): any {
  const call = mockAddListener.mock.calls.find(
    (c: any[]) => c[0]?.message,
  );
  expect(call).toBeDefined();
  return call![0];
}

/** Send a stream_data message to a client through the PubNub listener. */
function sendData(
  listener: any,
  channel: string,
  seq: number,
  chunks: string[] = [`chunk-${seq}`],
): void {
  listener.message({
    channel,
    message: {
      type: 'stream_data',
      streamId: 'reorder-stream',
      seq,
      ts: 1700000000000 + seq,
      encoding: 'utf8',
      chunks,
    },
  });
}

/** Send a stream_events message to a client through the PubNub listener. */
function sendEvent(
  listener: any,
  channel: string,
  seq: number,
  events: unknown[] = [{ idx: seq }],
): void {
  listener.message({
    channel,
    message: {
      type: 'stream_events',
      streamId: 'reorder-stream',
      seq,
      ts: 1700000000000 + seq,
      encoding: 'utf8',
      events,
    },
  });
}

/** Send a stream_end marker through the PubNub listener. */
function sendEnd(listener: any, channel: string, seq: number): void {
  listener.message({
    channel,
    message: {
      type: 'stream_end',
      streamId: 'reorder-stream',
      seq,
      ts: 1700000000000 + seq,
    },
  });
}

/** Collect all yielded messages from the inbound iterator until done. */
async function collectAll(client: StreamClient): Promise<any[]> {
  const results: any[] = [];
  for await (const msg of client.inbound) {
    results.push(msg);
  }
  return results;
}

describe('StreamClient reorder buffer', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    _resetUuidCounter();
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  // 1. ordered passthrough
  it('ordered passthrough: seq 0,1,2 yields in order', async () => {
    const client = makeInboundClient();
    const listener = getListener();

    sendData(listener, client.channel, 0, ['a']);
    sendData(listener, client.channel, 1, ['b']);
    sendData(listener, client.channel, 2, ['c']);
    sendEnd(listener, client.channel, 3);

    const results = await collectAll(client);
    expect(results.map((m: any) => m.seq)).toEqual([0, 1, 2]);
    expect(results.map((m: any) => (m.data as string[])[0])).toEqual(['a', 'b', 'c']);
  });

  // 2. out-of-order reorder
  it('out-of-order reorder: seq 1,0,2 yields 0,1,2', async () => {
    const client = makeInboundClient();
    const listener = getListener();

    sendData(listener, client.channel, 1, ['b']);
    sendData(listener, client.channel, 0, ['a']);
    sendData(listener, client.channel, 2, ['c']);
    sendEnd(listener, client.channel, 3);

    const results = await collectAll(client);
    expect(results.map((m: any) => m.seq)).toEqual([0, 1, 2]);
    expect(results.map((m: any) => (m.data as string[])[0])).toEqual(['a', 'b', 'c']);
  });

  // 3. adjacent swap
  it('adjacent swap: seq 0,2,1,3 yields 0,1,2,3', async () => {
    const client = makeInboundClient();
    const listener = getListener();

    sendData(listener, client.channel, 0, ['a']);
    sendData(listener, client.channel, 2, ['c']);
    sendData(listener, client.channel, 1, ['b']);
    sendData(listener, client.channel, 3, ['d']);
    sendEnd(listener, client.channel, 4);

    const results = await collectAll(client);
    expect(results.map((m: any) => m.seq)).toEqual([0, 1, 2, 3]);
    expect(results.map((m: any) => (m.data as string[])[0])).toEqual(['a', 'b', 'c', 'd']);
  });

  // 4. duplicate drop
  it('duplicate drop: same seq arrives twice, yielded once', async () => {
    const client = makeInboundClient();
    const listener = getListener();

    sendData(listener, client.channel, 0, ['a']);
    sendData(listener, client.channel, 0, ['a-dup']); // duplicate
    sendData(listener, client.channel, 1, ['b']);
    sendEnd(listener, client.channel, 2);

    const results = await collectAll(client);
    expect(results.map((m: any) => m.seq)).toEqual([0, 1]);
    // First arrival wins
    expect(results.map((m: any) => (m.data as string[])[0])).toEqual(['a', 'b']);
  });

  // 5. gap with timeout
  it('gap with timeout: seq 0,2 arrive, 1 never arrives, after timeout yields 2', async () => {
    const client = makeInboundClient();
    const listener = getListener();

    sendData(listener, client.channel, 0, ['a']);
    sendData(listener, client.channel, 2, ['c']);
    // seq 1 never arrives

    // Before timeout: only seq 0 has been yielded
    const iter = client.inbound[Symbol.asyncIterator]();
    const r0 = await iter.next();
    expect(r0.value.seq).toBe(0);

    // Advance past reorder timeout (750ms default)
    await vi.advanceTimersByTimeAsync(800);

    // Now seq 2 should be yielded (seq 1 skipped)
    const r2 = await iter.next();
    expect(r2.value.seq).toBe(2);

    // Send stream_end to complete
    sendEnd(listener, client.channel, 3);
    const rDone = await iter.next();
    expect(rDone.done).toBe(true);
  });

  // 6. stream_end before late data
  it('stream_end before late data: seq 0, end(3), seq 1, seq 2 yields 0,1,2 then completes', async () => {
    const client = makeInboundClient();
    const listener = getListener();

    sendData(listener, client.channel, 0, ['a']);
    sendEnd(listener, client.channel, 3);
    sendData(listener, client.channel, 1, ['b']);
    sendData(listener, client.channel, 2, ['c']);

    const results = await collectAll(client);
    expect(results.map((m: any) => m.seq)).toEqual([0, 1, 2]);
    expect(results.map((m: any) => (m.data as string[])[0])).toEqual(['a', 'b', 'c']);
  });

  // 7. stream_end with gap + timeout
  it('stream_end with gap + timeout: seq 0, end(3), seq 2 arrive, 1 never, after timeout yields 2 and completes', async () => {
    const client = makeInboundClient();
    const listener = getListener();

    sendData(listener, client.channel, 0, ['a']);
    sendEnd(listener, client.channel, 3);
    sendData(listener, client.channel, 2, ['c']);
    // seq 1 never arrives

    const iter = client.inbound[Symbol.asyncIterator]();

    // seq 0 yields immediately
    const r0 = await iter.next();
    expect(r0.value.seq).toBe(0);

    // Advance past reorder timeout
    await vi.advanceTimersByTimeAsync(800);

    // After timeout: seq 1 skipped, seq 2 emitted, then stream completes
    const r2 = await iter.next();
    expect(r2.value.seq).toBe(2);

    const rDone = await iter.next();
    expect(rDone.done).toBe(true);
  });

  // 8. events format (seq starts at 1)
  it('events format: seq 2,1,3 yields 1,2,3 (nextExpectedSeq initialized to 1)', async () => {
    const client = makeInboundClient({ format: 'events' });
    const listener = getListener();

    sendEvent(listener, client.channel, 2, [{ idx: 2 }]);
    sendEvent(listener, client.channel, 1, [{ idx: 1 }]);
    sendEvent(listener, client.channel, 3, [{ idx: 3 }]);
    sendEnd(listener, client.channel, 4);

    const results = await collectAll(client);
    expect(results.map((m: any) => m.seq)).toEqual([1, 2, 3]);
  });

  // 9. bytes first arrival out of order
  it('bytes first arrival out of order: seq 1,0,2 yields 0,1,2 (nextExpectedSeq=0)', async () => {
    const client = makeInboundClient({ format: 'bytes' });
    const listener = getListener();

    sendData(listener, client.channel, 1, ['b']);
    sendData(listener, client.channel, 0, ['a']);
    sendData(listener, client.channel, 2, ['c']);
    sendEnd(listener, client.channel, 3);

    const results = await collectAll(client);
    expect(results.map((m: any) => m.seq)).toEqual([0, 1, 2]);
    expect(results.map((m: any) => (m.data as string[])[0])).toEqual(['a', 'b', 'c']);
  });

  // 10. tail-gap: stream_end with lost final msgs
  it('tail-gap: seq 0, end(3), seq 1 and 2 never arrive, after timeout yields 0 and completes', async () => {
    const client = makeInboundClient();
    const listener = getListener();

    sendData(listener, client.channel, 0, ['a']);
    sendEnd(listener, client.channel, 3);
    // seq 1 and 2 never arrive

    const iter = client.inbound[Symbol.asyncIterator]();

    // seq 0 yields immediately
    const r0 = await iter.next();
    expect(r0.value.seq).toBe(0);

    // Advance past reorder timeout
    await vi.advanceTimersByTimeAsync(800);

    // Stream should complete (tail-gap resolved by timeout)
    const rDone = await iter.next();
    expect(rDone.done).toBe(true);
  });

  // 11. malformed stream_end (no integer seq) warned and ignored
  it('malformed stream_end without integer seq is warned and ignored in reorder mode', async () => {
    const client = makeInboundClient();
    const listener = getListener();

    sendData(listener, client.channel, 0, ['a']);

    // Send malformed stream_end (no seq) — should be warned and ignored
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    listener.message({
      channel: client.channel,
      message: { type: 'stream_end', streamId: 'reorder-stream', ts: 1700000000000 },
    });
    expect(warnSpy).toHaveBeenCalledWith(
      '[StreamClient]',
      expect.objectContaining({
        event: 'stream_client_stream_end_missing_seq',
        level: 'warn',
        message: expect.stringContaining('stream_end missing numeric seq'),
      }),
    );
    warnSpy.mockRestore();

    // Stream should still be open — send more data and a valid stream_end
    sendData(listener, client.channel, 1, ['b']);
    sendEnd(listener, client.channel, 2);

    const results = await collectAll(client);
    expect(results.map((m: any) => m.seq)).toEqual([0, 1]);
  });

  // 12. reorderTimeoutMs=0 disables
  it('reorderTimeoutMs=0: messages arrive out of order, yielded in arrival order, stream_end completes immediately', async () => {
    const client = makeInboundClient({ reorderTimeoutMs: 0 });
    const listener = getListener();

    sendData(listener, client.channel, 1, ['b']);
    sendData(listener, client.channel, 0, ['a']);
    sendData(listener, client.channel, 2, ['c']);
    sendEnd(listener, client.channel, 3);

    const results = await collectAll(client);
    // Arrival order preserved, NOT reordered
    expect(results.map((m: any) => m.seq)).toEqual([1, 0, 2]);
    expect(results.map((m: any) => (m.data as string[])[0])).toEqual(['b', 'a', 'c']);
  });
});
