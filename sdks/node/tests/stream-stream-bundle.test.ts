/* eslint-disable @typescript-eslint/no-explicit-any */
/**
 * Tests for StreamBundle (internal transport engine).
 *
 * Covers:
 * - Bytes format: single chunk, multiple chunks, flush on size, flush on time
 * - Events format: object events, $binary tag, flush thresholds
 * - Events format: raw string write throws error
 * - Binary encoding: Buffer auto base64 in both formats
 * - Multipart: oversized payload splits correctly, part structure, data field base64
 * - Size tracking: Buffer.byteLength, not .length
 * - Presence gating: temporarily disabled (writes always publish)
 * - meta.sender on every publish
 * - Sequence numbering: stream_data starts at 0, stream_events starts at 1
 * - storeInHistory: false on all publishes
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { StreamBundle } from '../src/stream/stream-bundle.js';
import type { StreamBundleConfig } from '../src/stream/types.js';

// ---------------------------------------------------------------------------
// Mock PubNub client
// ---------------------------------------------------------------------------

interface PublishCall {
  channel: string;
  message: unknown;
  meta?: unknown;
  storeInHistory?: boolean;
  sendByPost?: boolean;
}

function createMockPubNub() {
  const calls: PublishCall[] = [];
  const listeners: any[] = [];
  const subscriptions: string[] = [];
  const pubnub = {
    publish: vi.fn((params: any) => {
      calls.push({
        channel: params.channel,
        message: params.message,
        meta: params.meta,
        storeInHistory: params.storeInHistory,
        sendByPost: params.sendByPost,
      });
      return Promise.resolve({ timetoken: '17000000000000000' });
    }),
    addListener: vi.fn((listener: any) => {
      listeners.push(listener);
    }),
    removeListener: vi.fn(),
    subscribe: vi.fn((params: any) => {
      subscriptions.push(...(params.channels || []));
    }),
    unsubscribe: vi.fn(),
    hereNow: vi.fn().mockResolvedValue({ channels: {} }),
  };
  return { pubnub: pubnub as any, calls, listeners, subscriptions };
}

function defaultConfig(overrides: Partial<StreamBundleConfig> = {}): StreamBundleConfig {
  return {
    maxMessageSize: 16384,
    bundleSizeBytes: 4096,
    maxLatencyMs: 250,
    uuid: 'test_agent-stream-0001',
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('StreamBundle', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  // -- Bytes format ----------------------------------------------------------

  describe('bytes format', () => {
    it('accumulates writes without publishing until threshold', () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ bundleSizeBytes: 1024, maxLatencyMs: 5000 }), false);

      sb.write('hello ');
      sb.write('world');

      expect(calls.length).toBe(0);
    });

    it('flushes when buffer exceeds bundleSizeBytes', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ bundleSizeBytes: 256, maxLatencyMs: 60000 }), false);

      sb.write('x'.repeat(300));
      await vi.advanceTimersByTimeAsync(0);

      expect(calls.length).toBe(1);
      const msg = calls[0].message as Record<string, unknown>;
      expect(msg.type).toBe('stream_data');
      expect(msg.streamId).toBe('s1');
      expect(msg.encoding).toBe('utf8');
      expect(Array.isArray(msg.chunks)).toBe(true);
    });

    it('flushes after maxLatencyMs elapsed', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ bundleSizeBytes: 1024 * 1024, maxLatencyMs: 250 }), false);

      sb.write('small');
      expect(calls.length).toBe(0);

      await vi.advanceTimersByTimeAsync(300);

      expect(calls.length).toBe(1);
      const msg = calls[0].message as Record<string, unknown>;
      expect(msg.type).toBe('stream_data');
      expect((msg as any).chunks).toEqual(['small']);
    });

    it('preserves chunk order', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ bundleSizeBytes: 100000, maxLatencyMs: 100 }), false);

      sb.write('first');
      sb.write('second');
      sb.write('third');

      await vi.advanceTimersByTimeAsync(150);

      expect(calls.length).toBe(1);
      expect((calls[0].message as any).chunks).toEqual(['first', 'second', 'third']);
    });

    it('handles binary (Buffer) with base64 encoding', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxLatencyMs: 100 }), false);

      sb.write(Buffer.from([0xFF, 0xFE, 0xFD]));

      await vi.advanceTimersByTimeAsync(150);

      expect(calls.length).toBe(1);
      const msg = calls[0].message as any;
      expect(msg.encoding).toBe('base64');
      expect(msg.chunks[0]).toBe(Buffer.from([0xFF, 0xFE, 0xFD]).toString('base64'));
    });

    it('sets encoding to base64 for entire batch when any chunk is binary', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxLatencyMs: 100 }), false);

      sb.write('text data');
      sb.write(Buffer.from([0x01, 0x02]));

      await vi.advanceTimersByTimeAsync(150);

      expect(calls.length).toBe(1);
      const msg = calls[0].message as any;
      expect(msg.encoding).toBe('base64');
    });
  });

  // -- Events format ---------------------------------------------------------

  describe('events format', () => {
    it('buffers object events and flushes on timer', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'events',
        defaultConfig({ maxLatencyMs: 100 }), false);

      sb.write({ temp: 72 });
      sb.write({ temp: 73 });

      await vi.advanceTimersByTimeAsync(150);

      expect(calls.length).toBe(1);
      const msg = calls[0].message as any;
      expect(msg.type).toBe('stream_events');
      expect(msg.encoding).toBe('utf8');
      expect(msg.events).toEqual([{ temp: 72 }, { temp: 73 }]);
    });

    it('wraps binary as $binary tag', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'events',
        defaultConfig({ maxLatencyMs: 100 }), false);

      sb.write(Buffer.from([0xDE, 0xAD]));

      await vi.advanceTimersByTimeAsync(150);

      expect(calls.length).toBe(1);
      const msg = calls[0].message as any;
      expect(msg.events[0]).toEqual({ $binary: Buffer.from([0xDE, 0xAD]).toString('base64') });
    });

    it('throws on raw string writes', () => {
      const { pubnub } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'events',
        defaultConfig(), false);

      expect(() => sb.write('raw string')).toThrow(
        'write() does not accept raw strings in format: "events"',
      );
    });

    it('encoding is always utf8 for events', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'events',
        defaultConfig({ maxLatencyMs: 100 }), false);

      sb.write(Buffer.from([0xFF])); // binary
      await vi.advanceTimersByTimeAsync(150);

      expect((calls[0].message as any).encoding).toBe('utf8');
    });
  });

  // -- Sequence numbering ----------------------------------------------------

  describe('sequence numbering', () => {
    it('stream_data starts at seq 0', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ bundleSizeBytes: 10, maxLatencyMs: 60000 }), false);

      sb.write('x'.repeat(20));
      await vi.advanceTimersByTimeAsync(0);

      expect(calls.length).toBe(1);
      expect((calls[0].message as any).seq).toBe(0);
    });

    it('stream_events starts at seq 1', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'events',
        defaultConfig({ bundleSizeBytes: 10, maxLatencyMs: 60000 }), false);

      sb.write({ event: 'test' });
      await vi.advanceTimersByTimeAsync(0);

      expect(calls.length).toBe(1);
      expect((calls[0].message as any).seq).toBe(1);
    });

    it('sequence increments per flush, not per write', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ bundleSizeBytes: 256, maxLatencyMs: 60000 }), false);

      // First batch
      sb.write('a'.repeat(300));
      await vi.advanceTimersByTimeAsync(0);

      // Second batch
      sb.write('b'.repeat(300));
      await vi.advanceTimersByTimeAsync(0);

      expect(calls.length).toBe(2);
      expect((calls[0].message as any).seq).toBe(0);
      expect((calls[1].message as any).seq).toBe(1);
    });
  });

  // -- meta.sender -----------------------------------------------------------

  describe('meta.sender', () => {
    it('includes meta.sender on every publish', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxLatencyMs: 100, uuid: 'my-agent-stream-0001' }), false);

      sb.write('hello');
      await vi.advanceTimersByTimeAsync(150);

      expect(calls.length).toBe(1);
      expect(calls[0].meta).toEqual({ sender: 'my-agent-stream-0001', protocolVersion: '2026-05-01' });
    });

    it('includes meta.sender on events format publish', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'events',
        defaultConfig({ maxLatencyMs: 100, uuid: 'ev-agent-stream-0002' }), false);

      sb.write({ data: 'test' });
      await vi.advanceTimersByTimeAsync(150);

      expect(calls.length).toBe(1);
      expect(calls[0].meta).toEqual({ sender: 'ev-agent-stream-0002', protocolVersion: '2026-05-01' });
    });
  });

  // -- storeInHistory --------------------------------------------------------

  describe('storeInHistory', () => {
    it('storeInHistory is false on bytes format publish', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxLatencyMs: 100 }), false);

      sb.write('hello');
      await vi.advanceTimersByTimeAsync(150);

      expect(calls[0].storeInHistory).toBe(false);
    });

    it('storeInHistory is false on events format publish', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'events',
        defaultConfig({ maxLatencyMs: 100 }), false);

      sb.write({ event: 'test' });
      await vi.advanceTimersByTimeAsync(150);

      expect(calls[0].storeInHistory).toBe(false);
    });
  });

  // -- Multipart splitting ---------------------------------------------------

  describe('multipart', () => {
    it('throws when maxMessageSize is less than or equal to ENVELOPE_RESERVE', () => {
      const { pubnub } = createMockPubNub();
      expect(() => new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxMessageSize: 512 }), false))
        .toThrow('maxMessageSize (512) must be greater than ENVELOPE_RESERVE (512)');
      expect(() => new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxMessageSize: 100 }), false))
        .toThrow('must be greater than ENVELOPE_RESERVE');
    });

    it('splits oversized payload into multiple parts', async () => {
      const { pubnub, calls } = createMockPubNub();
      // Use a small maxMessageSize (must be > ENVELOPE_RESERVE=512) to force multipart
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxMessageSize: 600, bundleSizeBytes: 100000, maxLatencyMs: 100 }), false);

      // Write enough data to produce a serialized message > 600 bytes
      sb.write('x'.repeat(1000));
      await vi.advanceTimersByTimeAsync(150);

      // Should have published multiple parts
      expect(calls.length).toBeGreaterThan(1);

      // Each part should have multipart metadata
      for (const call of calls) {
        const msg = call.message as any;
        expect(msg.multipart).toBeDefined();
        expect(msg.multipart.id).toMatch(/^mp-\d+-\d+$/);
        expect(msg.multipart.part).toBeGreaterThanOrEqual(1);
        expect(msg.multipart.total).toBeGreaterThan(1);
        expect(typeof msg.data).toBe('string'); // base64 data field
        expect(msg.seq).toBe(0); // All parts share the same seq
      }

      // Parts should be numbered 1 through total
      const parts = calls.map(c => (c.message as any).multipart.part);
      const total = (calls[0].message as any).multipart.total;
      expect(parts.length).toBe(total);
      for (let i = 1; i <= total; i++) {
        expect(parts).toContain(i);
      }
    });

    it('splits oversized events format payload', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'events',
        defaultConfig({ maxMessageSize: 600, bundleSizeBytes: 100000, maxLatencyMs: 100 }), false);

      // Write large events to exceed maxMessageSize
      sb.write({ data: 'y'.repeat(1000) });
      await vi.advanceTimersByTimeAsync(150);

      expect(calls.length).toBeGreaterThan(1);
      const msg = calls[0].message as any;
      expect(msg.type).toBe('stream_events');
      expect(msg.multipart).toBeDefined();
      // Events format seq starts at 1
      expect(msg.seq).toBe(1);
    });

    it('multipart parts use meta.sender', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({
          maxMessageSize: 600,
          bundleSizeBytes: 100000,
          maxLatencyMs: 100,
          uuid: 'mp-agent-stream-0001',
        }), false);

      sb.write('z'.repeat(1000));
      await vi.advanceTimersByTimeAsync(150);

      for (const call of calls) {
        expect(call.meta).toEqual({ sender: 'mp-agent-stream-0001', protocolVersion: '2026-05-01' });
      }
    });

    it('multipart parts use storeInHistory: false', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxMessageSize: 600, bundleSizeBytes: 100000, maxLatencyMs: 100 }), false);

      sb.write('z'.repeat(1000));
      await vi.advanceTimersByTimeAsync(150);

      for (const call of calls) {
        expect(call.storeInHistory).toBe(false);
      }
    });

    it('multipart part data can be reassembled', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxMessageSize: 600, bundleSizeBytes: 100000, maxLatencyMs: 100 }), false);

      const originalContent = 'hello-world-test-'.repeat(80);
      sb.write(originalContent);
      await vi.advanceTimersByTimeAsync(150);

      // Reassemble
      const parts = calls.map(c => (c.message as any));
      parts.sort((a: any, b: any) => a.multipart.part - b.multipart.part);

      const buffers = parts.map((p: any) => Buffer.from(p.data, 'base64'));
      const reassembled = Buffer.concat(buffers).toString('utf-8');
      const parsed = JSON.parse(reassembled);

      expect(parsed.type).toBe('stream_data');
      expect(parsed.chunks).toEqual([originalContent]);
    });

    // Regression guard: unbounded `Promise.all` on multipart parts
    // saturated DNS / connection pools in the video_stream use case.
    // Publishes must run with a bounded pool (see
    // stream-bundle.ts::DEFAULT_MULTIPART_CONCURRENCY).
    it('caps concurrent multipart publishes at DEFAULT_MULTIPART_CONCURRENCY (4)', async () => {
      let inFlight = 0;
      let maxInFlight = 0;
      const observedParts: number[] = [];

      // Gated publish mock: each call yields twice on the microtask
      // queue so sibling workers have a chance to race. If the SDK
      // failed to bound concurrency, all parts would enter publish()
      // in the same microtask wave and inFlight would spike far past 4.
      const pubnub = {
        publish: vi.fn(async (params: any) => {
          inFlight++;
          maxInFlight = Math.max(maxInFlight, inFlight);
          observedParts.push((params.message as any).multipart.part);
          await Promise.resolve();
          await Promise.resolve();
          inFlight--;
          return { timetoken: '17000000000000000' };
        }),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        subscribe: vi.fn(),
        unsubscribe: vi.fn(),
        hereNow: vi.fn().mockResolvedValue({ channels: {} }),
      } as any;

      const sb = new StreamBundle(
        pubnub,
        'stream.test.s1',
        's1',
        'bytes',
        defaultConfig({
          maxMessageSize: 600,
          bundleSizeBytes: 100000,
          maxLatencyMs: 100,
        }),
        false,
      );

      // 2000-char payload → ~40 multipart parts at the tiny
      // maxMessageSize setting. Well above the cap so the test
      // exercises multiple worker waves.
      sb.write('z'.repeat(2000));
      await vi.advanceTimersByTimeAsync(150);

      expect(observedParts.length).toBeGreaterThan(4);
      expect(maxInFlight).toBeLessThanOrEqual(4);
      // Sanity: we *are* using concurrency, not accidentally serializing
      expect(maxInFlight).toBeGreaterThan(1);
    });
  });

  // -- Presence gating -------------------------------------------------------

  // Presence gating is temporarily disabled (TODO(presence-gating)).
  // These tests verify the disabled state. When re-enabled, restore
  // the original assertions that validate discard-on-zero-occupancy,
  // -pnpres subscription, and hereNow seeding.
  describe('presence gating (temporarily disabled)', () => {
    it('publishes writes even when gated and occupancy is 0 (gating disabled)', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxLatencyMs: 100 }), true);

      sb.write('gated write');
      await vi.advanceTimersByTimeAsync(150);

      // With gating disabled, writes publish regardless of occupancy
      expect(calls.length).toBe(1);
    });

    it('does not subscribe to -pnpres even when gated (gating disabled)', () => {
      const { pubnub, subscriptions } = createMockPubNub();
      new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig(), true);

      expect(subscriptions).not.toContain('stream.test.s1-pnpres');
    });

    it('does not subscribe to -pnpres when not gated', () => {
      const { pubnub, subscriptions } = createMockPubNub();
      new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig(), false);

      expect(subscriptions).not.toContain('stream.test.s1-pnpres');
    });
  });

  // -- Size tracking ---------------------------------------------------------

  describe('size tracking', () => {
    it('measures size in bytes, not characters (multibyte handling)', async () => {
      const { pubnub, calls } = createMockPubNub();
      // emoji is 4 bytes in UTF-8. 512 emojis = 2048 bytes > bundleSizeBytes=1024
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ bundleSizeBytes: 1024, maxLatencyMs: 60000 }), false);

      const emoji = '\u{1F600}'; // 4 bytes
      sb.write(emoji.repeat(512)); // 2048 bytes

      await vi.advanceTimersByTimeAsync(0);

      expect(calls.length).toBe(1);
    });
  });

  // -- End behavior ----------------------------------------------------------

  describe('end()', () => {
    it('flushes remaining data on end', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ maxLatencyMs: 60000 }), false);

      sb.write('buffered');
      await sb.end();

      expect(calls.length).toBe(1);
      expect((calls[0].message as any).chunks).toEqual(['buffered']);
    });

    it('throws on write after end', async () => {
      const { pubnub } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig(), false);

      await sb.end();
      expect(() => sb.write('fail')).toThrow('Cannot write to a closed stream');
    });

    it('end is idempotent', async () => {
      const { pubnub } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig(), false);

      await sb.end();
      await sb.end(); // should not throw
    });

    it('invokes onEnd callback', async () => {
      const { pubnub } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig(), false);

      const onEnd = vi.fn();
      sb.onEnd = onEnd;

      await sb.end();
      expect(onEnd).toHaveBeenCalledTimes(1);
    });

    it('produces zero publishes on end with empty buffer', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig(), false);

      await sb.end();
      expect(calls.length).toBe(0);
    });
  });

  // -- publishEndMarker() ---------------------------------------------------

  describe('publishEndMarker()', () => {
    it('publishes correct wire shape', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ uuid: 'marker-agent-0001' }), false);

      await sb.publishEndMarker();

      expect(calls.length).toBe(1);
      const msg = calls[0].message as Record<string, unknown>;
      expect(msg.type).toBe('stream_end');
      expect(msg.streamId).toBe('s1');
      expect(typeof msg.seq).toBe('number');
      expect(typeof msg.ts).toBe('number');
      // No data, chunks, events, or encoding fields
      expect(msg).not.toHaveProperty('data');
      expect(msg).not.toHaveProperty('chunks');
      expect(msg).not.toHaveProperty('events');
      expect(msg).not.toHaveProperty('encoding');
      // meta.sender and storeInHistory via publishMessage
      expect(calls[0].meta).toEqual({ sender: 'marker-agent-0001', protocolVersion: '2026-05-01' });
      expect(calls[0].storeInHistory).toBe(false);
      // Stream publishes MUST use POST so multipart payloads end up
      // in the request body (gzipped) instead of the URL path. See
      // stream-bundle.ts::publishMessage. Parity with Python SDK.
      expect(calls[0].sendByPost).toBe(true);
    });

    it('increments seq correctly after data publishes', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ bundleSizeBytes: 10, maxLatencyMs: 60000 }), false);

      // Write enough to trigger a flush (seq 0)
      sb.write('x'.repeat(20));
      await vi.advanceTimersByTimeAsync(0);
      expect(calls.length).toBe(1);
      expect((calls[0].message as any).seq).toBe(0);

      // publishEndMarker should use the next seq (1)
      await sb.publishEndMarker();
      expect(calls.length).toBe(2);
      expect((calls[1].message as any).seq).toBe(1);
    });

    it('does not throw on publish failure (retry exhausted)', async () => {
      const { pubnub } = createMockPubNub();
      pubnub.publish.mockRejectedValue(new Error('Network error'));

      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig(), false);

      // publishMessage retries 3 times with 100 + 200 ms backoff.
      // Advance past the total so the final `.catch()` and error log
      // run before the assertion.
      const p = sb.publishEndMarker();
      await vi.advanceTimersByTimeAsync(500);
      await expect(p).resolves.toBeUndefined();

      // Exactly MAX_ATTEMPTS (3) calls — no over-retry, no under-retry.
      expect(pubnub.publish).toHaveBeenCalledTimes(3);
    });

    // Regression guard for the retry/backoff mitigation added to
    // StreamBundle.publishMessage. Transient network/DNS failures
    // (seen on macOS + local DNS proxy setups) should succeed on the
    // second attempt instead of surfacing as lost stream data.
    it('retries transient publish failures and succeeds on a later attempt', async () => {
      const { pubnub, calls } = createMockPubNub();
      const originalPublish = pubnub.publish.getMockImplementation();

      let callCount = 0;
      pubnub.publish.mockImplementation((params: any) => {
        callCount++;
        if (callCount === 1) {
          // Simulate a transient network error on the first attempt.
          return Promise.reject(new Error('Network error'));
        }
        // Delegate to the original mock impl for the second attempt.
        return originalPublish!(params);
      });

      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig(), false);

      const p = sb.publishEndMarker();
      await vi.advanceTimersByTimeAsync(500);
      await p;

      // Two publish() calls: the failed first attempt + the successful retry.
      expect(pubnub.publish).toHaveBeenCalledTimes(2);
      // Only one successful call made it into the recorded calls[] list.
      expect(calls.length).toBe(1);
    });
  });

  // -------------------------------------------------------------------------
  // protocolVersion in message body
  // -------------------------------------------------------------------------

  describe('protocolVersion in message body', () => {
    it('stream_data body includes protocolVersion', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig({ bundleSizeBytes: 256, maxLatencyMs: 60000 }), false);

      sb.write('hello');
      await vi.advanceTimersByTimeAsync(0);

      // Force flush by writing enough data or advancing time
      sb.write('x'.repeat(300));
      await vi.advanceTimersByTimeAsync(0);

      const msg = calls[0].message as Record<string, unknown>;
      expect(msg.protocolVersion).toBe('2026-05-01');
    });

    it('stream_events body includes protocolVersion', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'events',
        defaultConfig({ maxLatencyMs: 100 }), false);

      sb.write({ data: 'test' });
      await vi.advanceTimersByTimeAsync(150);

      const msg = calls[0].message as Record<string, unknown>;
      expect(msg.protocolVersion).toBe('2026-05-01');
    });

    it('stream_end body includes protocolVersion', async () => {
      const { pubnub, calls } = createMockPubNub();
      const sb = new StreamBundle(pubnub, 'stream.test.s1', 's1', 'bytes',
        defaultConfig(), false);

      await sb.publishEndMarker();

      const msg = calls[0].message as Record<string, unknown>;
      expect(msg.type).toBe('stream_end');
      expect(msg.protocolVersion).toBe('2026-05-01');
    });
  });
});
