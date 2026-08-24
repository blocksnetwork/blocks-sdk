/**
 * Late-reader broadcast resilience test for shared streams
 * (shared-stream lifecycle).
 *
 * Scenario:
 *   1. A writer-side StreamClient on a shared-affinity channel writes
 *      some data then calls end().
 *   2. A later consumer-side StreamClient subscribes (built via
 *      `fromDescriptor`) — simulating a late reader joining the
 *      broadcast within PubNub's cache window.
 *   3. Cached `stream_data` replays hit the late reader's inbound
 *      iterator; a cached `stream_end` would terminate the iterator
 *      prematurely.
 *
 * Under the fix, the writer's end() suppresses the `stream_end` marker
 * because affinity is 'shared'. The cache therefore contains only
 * data messages. The late reader's iterator stays alive: it yields the
 * cached data then waits for more (never terminated by a stale
 * marker).
 *
 * Assertions mirror Python `tests/test_shared_stream_late_reader.py`.
 */

/* eslint-disable @typescript-eslint/no-explicit-any */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { StreamClient, _resetUuidCounter } from '../src/stream/stream-client.js';
import type { StreamDescriptor } from '../src/stream/descriptor.js';

// Capture the consumer-side PubNub's message listener so we can replay
// cached messages into it (simulating PubNub's history/cache replay
// during the subscribe-grace window on the late reader's channel).
let capturedListener: any = null;

const mockPublish = vi.fn().mockResolvedValue({ timetoken: '17000000000000000' });

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation(() => ({
      setToken: vi.fn(),
      setFilterExpression: vi.fn(),
      addListener: vi.fn((listener: any) => { capturedListener = listener; }),
      removeListener: vi.fn(),
      subscribe: vi.fn(),
      unsubscribe: vi.fn(),
      unsubscribeAll: vi.fn(),
      destroy: vi.fn(),
      publish: mockPublish,
      hereNow: vi.fn().mockResolvedValue({ channels: {} }),
    })),
  };
});

function simulateCachedMessage(channel: string, msg: Record<string, unknown>): void {
  if (capturedListener?.message) {
    capturedListener.message({ channel, message: msg });
  }
}

function endMarkerPublishes(): unknown[] {
  return mockPublish.mock.calls
    .map((c) => c[0]?.message as Record<string, unknown> | undefined)
    .filter((m): m is Record<string, unknown> =>
      !!m && typeof m === 'object' && (m as { type?: string }).type === 'stream_end',
    );
}

beforeEach(() => {
  vi.clearAllMocks();
  _resetUuidCounter();
  mockPublish.mockClear();
  capturedListener = null;
});

describe('late-reader broadcast resilience', () => {
  it('writer-side end() on shared stream does not publish stream_end, and a late-subscribing reader does not exit on a cached marker', async () => {
    const channel = 'stream.late_reader_test.shared_down';

    // --- Phase 1: shared-affinity writer does its thing and ends. ---
    const writer = new StreamClient({
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      token: 'T7a-writer',
      agentName: 'late_reader_test',
      streamId: 'shared_down',
      channel,
      direction: 'outbound',
      format: 'bytes',
      affinity: 'shared',
    });

    writer.write('chunk-1');
    writer.write('chunk-2');
    await writer.end();

    // Core invariant: no stream_end marker published by the writer.
    // The PubNub cache for the shared channel therefore contains zero
    // cached stream_end markers — the exact condition the late reader
    // relies on to not exit prematurely.
    expect(endMarkerPublishes()).toHaveLength(0);

    // --- Phase 2: late reader subscribes within the cache window. ---
    // Build a consumer-side StreamClient from a descriptor pointing at
    // the same shared channel, mirroring what TaskSession would do on
    // a fresh stream_started event for a later task.
    const desc: StreamDescriptor = {
      taskId: 'task-late-reader',
      streamId: 'shared_down',
      agentName: 'late_reader_test',
      channel,
      token: 'T7c-late-reader',
      agentDirection: 'outbound',
      localDirection: 'inbound',
      format: 'bytes',
      affinity: 'shared',
      declaredStream: 'shared_down',
    };

    const lateReader = StreamClient.fromDescriptor(desc, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
    });

    expect(lateReader.isActive).toBe(true);
    // Affinity is internal to StreamClient (_affinity, private); its
    // externally observable effect is the end-marker suppression gate,
    // which we exercise on the writer side above. No public accessor
    // to assert directly.

    // --- Phase 3: replay what the PubNub cache would have contained ---
    // In the real world, PubNub's subscribe-with-timetoken: 1000 returns
    // cached messages on the shared channel. Under the fix, the cache
    // contains ONLY the writer's stream_data publishes — no terminator.
    //
    // Simulate that by pushing two cached stream_data messages into the
    // consumer's message listener. In the pre-fix world, a cached
    // stream_end from the writer's cleanup would terminate the iterator
    // here; post-fix, the iterator stays live.
    const iter = lateReader.bytes()[Symbol.asyncIterator]();

    simulateCachedMessage(channel, {
      type: 'stream_data',
      streamId: 'shared_down',
      seq: 0,
      ts: Date.now(),
      encoding: 'utf8',
      chunks: ['chunk-1'],
    });
    simulateCachedMessage(channel, {
      type: 'stream_data',
      streamId: 'shared_down',
      seq: 1,
      ts: Date.now(),
      encoding: 'utf8',
      chunks: ['chunk-2'],
    });

    const first = await iter.next();
    expect(first.done).toBe(false);
    expect(first.value).toBeInstanceOf(Uint8Array);

    const second = await iter.next();
    expect(second.done).toBe(false);
    expect(second.value).toBeInstanceOf(Uint8Array);

    // --- Critical assertion: the iterator has NOT terminated on a
    // stale cached marker. Start a new iter.next() and show that
    // after a tick it has NOT resolved with { done: true }. ---
    let iteratorDone = false;
    const pending = iter.next().then((r) => {
      if (r.done) iteratorDone = true;
    });
    await new Promise((r) => setTimeout(r, 50));
    expect(iteratorDone).toBe(false);

    // --- Phase 4: administrative explicit end() on the late reader ---
    // Ending the consumer's own inbound reader does not publish a
    // marker (consumer-side reader is not a writer), and resolves the
    // pending iter.next().
    await lateReader.end();
    await pending;
    expect(iteratorDone).toBe(true);
  });

  it('regression gate: dedicated stream still publishes stream_end on writer end(), terminating a late reader', async () => {
    // Contrast: a dedicated-affinity writer DOES publish the marker;
    // a late reader on that channel WOULD receive a terminator. This
    // test preserves the dedicated-stream contract.
    const channel = 'stream.late_reader_test.ded_down';

    const writer = new StreamClient({
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      token: 'T7a-writer',
      agentName: 'late_reader_test',
      streamId: 'ded_down',
      channel,
      direction: 'outbound',
      format: 'bytes',
      affinity: 'dedicated',
    });

    writer.write('chunk-1');
    await writer.end();

    // Dedicated: marker WAS published.
    expect(endMarkerPublishes()).toHaveLength(1);
  });
});
