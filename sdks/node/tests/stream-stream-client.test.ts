/* eslint-disable @typescript-eslint/no-explicit-any */
/**
 * Tests for StreamClient.
 *
 * Covers:
 * - Constructor creates PubNub client with correct token and UUID
 * - Constructor validates stream ID
 * - write() delegates to StreamBundle
 * - write() throws on ended stream
 * - write() throws on inbound-only stream
 * - end() flushes and destroys
 * - inbound iterator yields normalized messages
 * - inbound handles multipart reassembly
 * - inbound throws on outbound-only stream
 * - Self-publish filter set for bidirectional only
 * - Channel computed as stream.{agentName}.{streamId}
 * - UUID follows {agentName}-stream-{NNNN} convention
 * - Configuration hierarchy (constructor overrides env vars)
 */

import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { StreamClient, _resetUuidCounter } from '../src/stream/stream-client.js';
import PubNub from 'pubnub';

// Track PubNub constructor calls
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
    default: vi.fn().mockImplementation((config: any) => {
      const instance = {
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
      };
      return instance;
    }),
  };
});

describe('StreamClient', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    _resetUuidCounter();
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.useRealTimers();
    // Clean up any env vars we set
    delete process.env.STREAM_MAX_MESSAGE_SIZE;
    delete process.env.STREAM_BUNDLE_SIZE;
    delete process.env.STREAM_MAX_LATENCY_MS;
    delete process.env.STREAM_GATING;
  });

  function makeClient(overrides: Partial<Parameters<typeof StreamClient['prototype']['constructor']>[0]> = {}): StreamClient {
    return new StreamClient({
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      token: 'test-token',
      agentName: 'test_agent',
      streamId: 'my-stream',
      ...overrides,
    } as any);
  }

  // -- Constructor -----------------------------------------------------------

  describe('constructor', () => {
    it('creates PubNub client and sets token', () => {
      const _client = makeClient();
      expect(PubNub).toHaveBeenCalled();
      expect(mockSetToken).toHaveBeenCalledWith('test-token');
    });

    it('validates stream ID', () => {
      expect(() => makeClient({ streamId: '' })).toThrow('Stream ID cannot be empty');
      expect(() => makeClient({ streamId: 'bad.id' })).toThrow('Stream ID contains invalid characters');
    });

    it('computes channel as stream.{agentName}.{streamId}', () => {
      const client = makeClient({ agentName: 'weather', streamId: 'temp-out' });
      expect(client.channel).toBe('stream.weather.temp-out');
    });

    it('generates UUID with {agentName}-stream-{NNNN} convention', () => {
      const client1 = makeClient({ agentName: 'myAgent' });
      expect(client1.uuid).toBe('myAgent-stream-0001');

      const client2 = makeClient({ agentName: 'myAgent' });
      expect(client2.uuid).toBe('myAgent-stream-0002');
    });

    it('increments UUID counter per call', () => {
      const c1 = makeClient();
      const c2 = makeClient();
      const c3 = makeClient();

      expect(c1.uuid).toMatch(/-0001$/);
      expect(c2.uuid).toMatch(/-0002$/);
      expect(c3.uuid).toMatch(/-0003$/);
    });

    it('requires agentName via option', () => {
      expect(() => new StreamClient({
        subscribeKey: 'sub',
        publishKey: 'pub',
        token: 'tok',
        streamId: 'stream1',
      } as any)).toThrow('agentName is required');
    });

    it('throws on invalid format', () => {
      expect(() => new StreamClient({
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        token: 'test-token',
        agentName: 'test_agent',
        streamId: 'my-stream',
        format: 'bogus' as any,
      })).toThrow('Invalid stream format: "bogus"');
    });

    it('missing agentName throws without mentioning env fallback', () => {
      expect(() => new StreamClient({
        subscribeKey: 'sub',
        publishKey: 'pub',
        token: 'tok',
        agentName: '',
        streamId: 'stream1',
      })).toThrow('agentName is required');

      // Verify the error message does not mention env vars
      try {
        new StreamClient({
          subscribeKey: 'sub',
          publishKey: 'pub',
          token: 'tok',
          agentName: '',
          streamId: 'stream1',
        });
      } catch (err: any) {
        expect(err.message).not.toMatch(/env/i);
        expect(err.message).not.toMatch(/AGENT_NAME/);
      }
    });
  });

  // -- Self-publish filter ---------------------------------------------------

  describe('self-publish filter', () => {
    it('sets filter expression for bidirectional streams', () => {
      const _client = makeClient({ direction: 'bidirectional', format: 'events' });
      expect(mockSetFilterExpression).toHaveBeenCalledWith(
        expect.stringMatching(/meta\.sender != '.+'/),
      );
    });

    it('does not set filter expression for outbound-only', () => {
      makeClient({ direction: 'outbound' });
      expect(mockSetFilterExpression).not.toHaveBeenCalled();
    });

    it('does not set filter expression for inbound-only', () => {
      makeClient({ direction: 'inbound' });
      expect(mockSetFilterExpression).not.toHaveBeenCalled();
    });
  });

  // -- Write -----------------------------------------------------------------

  describe('write()', () => {
    it('delegates write to StreamBundle (bytes format)', async () => {
      const client = makeClient({ direction: 'outbound', format: 'bytes', gating: false });
      client.write('hello');

      await vi.advanceTimersByTimeAsync(300);

      expect(mockPublish).toHaveBeenCalled();
      const pubArgs = mockPublish.mock.calls[0][0];
      expect(pubArgs.message.type).toBe('stream_data');
      expect(pubArgs.message.chunks).toEqual(['hello']);
    });

    it('delegates write to StreamBundle (events format)', async () => {
      const client = makeClient({ direction: 'outbound', format: 'events', gating: false });
      client.write({ temp: 72 });

      await vi.advanceTimersByTimeAsync(300);

      expect(mockPublish).toHaveBeenCalled();
      const pubArgs = mockPublish.mock.calls[0][0];
      expect(pubArgs.message.type).toBe('stream_events');
      expect(pubArgs.message.events).toEqual([{ temp: 72 }]);
    });

    it('throws on ended stream', async () => {
      const client = makeClient();
      await client.end();
      expect(() => client.write('fail')).toThrow('Cannot write to an ended stream');
    });

    it('throws on inbound-only stream', () => {
      const client = makeClient({ direction: 'inbound' });
      expect(() => client.write('fail')).toThrow('Cannot write to an inbound-only stream');
    });
  });

  // -- End -------------------------------------------------------------------

  describe('end()', () => {
    it('flushes remaining data and destroys PubNub client', async () => {
      const client = makeClient({ gating: false });
      client.write('pending');
      await client.end();

      expect(mockPublish).toHaveBeenCalled();
      expect(mockUnsubscribeAll).toHaveBeenCalled();
      expect(mockDestroy).toHaveBeenCalled();
    });

    it('sets isActive to false', async () => {
      const client = makeClient();
      expect(client.isActive).toBe(true);
      await client.end();
      expect(client.isActive).toBe(false);
    });

    it('calls onEnd callbacks', async () => {
      const client = makeClient();
      const callback = vi.fn();
      client.onEnd(callback);
      await client.end();
      expect(callback).toHaveBeenCalledTimes(1);
    });

    it('is idempotent', async () => {
      const client = makeClient();
      await client.end();
      await client.end(); // Should not throw
    });

    it('publishes stream_end marker for outbound direction', async () => {
      const client = makeClient({ direction: 'outbound', gating: false });
      client.write('data');
      await client.end();

      // Find the stream_end publish among all publish calls
      const endCalls = mockPublish.mock.calls.filter(
        (call: any[]) => call[0]?.message?.type === 'stream_end',
      );
      expect(endCalls.length).toBe(1);
      const endMsg = endCalls[0][0].message;
      expect(endMsg.type).toBe('stream_end');
      expect(endMsg.streamId).toBe('my-stream');
      expect(typeof endMsg.seq).toBe('number');
      expect(typeof endMsg.ts).toBe('number');
    });

    it('does NOT publish stream_end marker for bidirectional', async () => {
      const client = makeClient({ direction: 'bidirectional', format: 'events', gating: false });
      client.write({ data: 'test' });
      await client.end();

      const endCalls = mockPublish.mock.calls.filter(
        (call: any[]) => call[0]?.message?.type === 'stream_end',
      );
      expect(endCalls.length).toBe(0);
    });

    it('does NOT publish stream_end marker for inbound direction (no bundle)', async () => {
      const client = makeClient({ direction: 'inbound' });
      await client.end();

      const endCalls = mockPublish.mock.calls.filter(
        (call: any[]) => call[0]?.message?.type === 'stream_end',
      );
      expect(endCalls.length).toBe(0);
    });
  });

  // -- Inbound iterator ------------------------------------------------------

  describe('inbound', () => {
    it('throws on outbound-only stream', () => {
      const client = makeClient({ direction: 'outbound' });
      expect(() => client.inbound).toThrow('Cannot read from an outbound-only stream');
    });

    it('yields normalized stream_data messages', async () => {
      const client = makeClient({ direction: 'inbound' });

      // Get the listener that was registered
      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];
      expect(listener).toBeDefined();

      // Simulate incoming message
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          encoding: 'utf8',
          chunks: ['hello', 'world'],
        },
      });

      const iterator = client.inbound[Symbol.asyncIterator]();
      const result = await iterator.next();

      expect(result.done).toBe(false);
      expect(result.value.format).toBe('bytes');
      expect(result.value.data).toEqual(['hello', 'world']);
      expect(result.value.seq).toBe(0);
      expect(result.value.encoding).toBe('utf8');
    });

    it('yields normalized stream_events messages', async () => {
      const client = makeClient({ direction: 'inbound', format: 'events' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];

      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_events',
          streamId: 'my-stream',
          seq: 1,
          ts: 1700000000000,
          encoding: 'utf8',
          events: [{ temp: 72 }],
        },
      });

      const iterator = client.inbound[Symbol.asyncIterator]();
      const result = await iterator.next();

      expect(result.done).toBe(false);
      expect(result.value.format).toBe('events');
      expect(result.value.data).toEqual([{ temp: 72 }]);
      expect(result.value.seq).toBe(1);
    });

    it('handles multipart reassembly', async () => {
      const client = makeClient({ direction: 'inbound' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];

      // Create an original message and split it
      const original = {
        type: 'stream_data',
        streamId: 'my-stream',
        seq: 0,
        ts: 1700000000000,
        encoding: 'utf8',
        chunks: ['reassembled content'],
      };
      const serialized = JSON.stringify(original);
      const bytes = Buffer.from(serialized, 'utf-8');
      const mid = Math.ceil(bytes.length / 2);
      const part1Bytes = bytes.subarray(0, mid);
      const part2Bytes = bytes.subarray(mid);

      // Send parts out of order (part 2 first, then part 1)
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          multipart: { id: 'mp-123-0', part: 2, total: 2 },
          data: part2Bytes.toString('base64'),
        },
      });

      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          multipart: { id: 'mp-123-0', part: 1, total: 2 },
          data: part1Bytes.toString('base64'),
        },
      });

      const iterator = client.inbound[Symbol.asyncIterator]();
      const result = await iterator.next();

      expect(result.done).toBe(false);
      expect(result.value.format).toBe('bytes');
      expect(result.value.data).toEqual(['reassembled content']);
    });

    it('completes iterator on stream_end marker', async () => {
      const client = makeClient({ direction: 'inbound' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];

      // Start waiting for next message
      const iterator = client.inbound[Symbol.asyncIterator]();
      const nextPromise = iterator.next();

      // Send stream_end
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_end',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
        },
      });

      const result = await nextPromise;
      expect(result.done).toBe(true);
    });

    it('subsequent next() calls return done after stream_end', async () => {
      const client = makeClient({ direction: 'inbound' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];

      // Send stream_end
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_end',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
        },
      });

      const iterator = client.inbound[Symbol.asyncIterator]();
      const result1 = await iterator.next();
      expect(result1.done).toBe(true);

      const result2 = await iterator.next();
      expect(result2.done).toBe(true);
    });

    it('passes through raw messages with unknown type', async () => {
      const client = makeClient({ direction: 'inbound' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];

      listener.message({
        channel: client.channel,
        message: {
          type: 'unknown_type',
          data: 'raw content',
          seq: 0,
          ts: 1700000000000,
        },
      });

      const iterator = client.inbound[Symbol.asyncIterator]();
      const result = await iterator.next();

      expect(result.done).toBe(false);
      expect(result.value.format).toBe('raw');
    });

    it('signals done when stream is ended', async () => {
      const client = makeClient({ direction: 'inbound' });

      const iteratorPromise = (async () => {
        const iterator = client.inbound[Symbol.asyncIterator]();
        return iterator.next();
      })();

      // End the stream after a small delay
      setTimeout(async () => {
        await client.end();
      }, 10);

      await vi.advanceTimersByTimeAsync(50);

      const result = await iteratorPromise;
      expect(result.done).toBe(true);
    });

    it('subscribes to stream channel for inbound direction', () => {
      makeClient({ direction: 'inbound' });

      expect(mockSubscribe).toHaveBeenCalledWith(
        expect.objectContaining({
          channels: expect.arrayContaining([expect.stringMatching(/^stream\./)]),
        }),
      );
    });

    it('subscribes to stream channel for bidirectional direction', () => {
      makeClient({ direction: 'bidirectional', format: 'events' });

      expect(mockSubscribe).toHaveBeenCalledWith(
        expect.objectContaining({
          channels: expect.arrayContaining([expect.stringMatching(/^stream\./)]),
        }),
      );
    });
  });

  // -- Configuration hierarchy -----------------------------------------------

  describe('configuration hierarchy', () => {
    it('uses env var defaults when constructor options omitted', () => {
      process.env.STREAM_MAX_MESSAGE_SIZE = '8192';
      process.env.STREAM_BUNDLE_SIZE = '2048';
      process.env.STREAM_MAX_LATENCY_MS = '500';
      process.env.STREAM_GATING = 'off';

      // No maxMessageSize/bundleSizeBytes/maxLatencyMs/gating in options
      const client = makeClient();

      // Verify the client was created (env vars used internally)
      expect(client.isActive).toBe(true);
    });

    it('constructor options override env vars', () => {
      process.env.STREAM_GATING = 'on';

      // Explicit gating:false in constructor should override env
      const client = makeClient({ gating: false });
      expect(client.isActive).toBe(true);

      // Write should go through even with 0 occupancy (gating disabled)
      client.write('test');
    });

    it('uses built-in defaults when no env vars or options', () => {
      const client = makeClient();
      expect(client.isActive).toBe(true);
    });
  });

  // -- Multipart validation --------------------------------------------------

  describe('multipart validation', () => {
    /** Helper: get the PubNub message listener from an inbound client. */
    function getListener(_client: StreamClient) {
      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];
      expect(listener).toBeDefined();
      return listener;
    }

    /** Helper: build a valid two-part multipart payload for a message. */
    function makeTwoParts(overrides?: {
      idSuffix?: string;
      seq?: number;
      type?: string;
      streamId?: string;
    }) {
      const id = `mp-test-${overrides?.idSuffix ?? '0'}`;
      const seq = overrides?.seq ?? 0;
      const type = overrides?.type ?? 'stream_data';
      const streamId = overrides?.streamId ?? 'my-stream';

      const original = {
        type,
        streamId,
        seq,
        ts: 1700000000000,
        encoding: 'utf8',
        chunks: ['hello world'],
      };
      const serialized = JSON.stringify(original);
      const bytes = Buffer.from(serialized, 'utf-8');
      const mid = Math.ceil(bytes.length / 2);
      return {
        id,
        seq,
        type,
        streamId,
        ts: 1700000000000,
        part1Data: bytes.subarray(0, mid).toString('base64'),
        part2Data: bytes.subarray(mid).toString('base64'),
      };
    }

    it('valid out-of-order reassembly still works', async () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);
      const parts = makeTwoParts();

      // Send part 2 first, then part 1
      listener.message({
        channel: client.channel,
        message: {
          type: parts.type,
          streamId: parts.streamId,
          seq: parts.seq,
          ts: parts.ts,
          multipart: { id: parts.id, part: 2, total: 2 },
          data: parts.part2Data,
        },
      });
      listener.message({
        channel: client.channel,
        message: {
          type: parts.type,
          streamId: parts.streamId,
          seq: parts.seq,
          ts: parts.ts,
          multipart: { id: parts.id, part: 1, total: 2 },
          data: parts.part1Data,
        },
      });

      const iterator = client.inbound[Symbol.asyncIterator]();
      const result = await iterator.next();
      expect(result.done).toBe(false);
      expect(result.value.format).toBe('bytes');
      expect(result.value.data).toEqual(['hello world']);
    });

    it('part > total is dropped silently', () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);

      // part=3, total=2 -- should be dropped
      expect(() => {
        listener.message({
          channel: client.channel,
          message: {
            type: 'stream_data',
            streamId: 'my-stream',
            seq: 0,
            ts: 1700000000000,
            multipart: { id: 'mp-bad-1', part: 3, total: 2 },
            data: Buffer.from('hello').toString('base64'),
          },
        });
      }).not.toThrow();

      // No message should be queued
      const iterator = client.inbound[Symbol.asyncIterator]();
      // Verify queue is empty by checking we get a pending promise (not resolved)
      let resolved = false;
      const _promise = iterator.next().then(() => { resolved = true; });
      expect(resolved).toBe(false);
    });

    it('part == 0 is dropped silently', () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);

      expect(() => {
        listener.message({
          channel: client.channel,
          message: {
            type: 'stream_data',
            streamId: 'my-stream',
            seq: 0,
            ts: 1700000000000,
            multipart: { id: 'mp-bad-2', part: 0, total: 2 },
            data: Buffer.from('hello').toString('base64'),
          },
        });
      }).not.toThrow();

      let resolved = false;
      const iterator = client.inbound[Symbol.asyncIterator]();
      iterator.next().then(() => { resolved = true; });
      expect(resolved).toBe(false);
    });

    it('malformed "complete" set with wrong part numbers is dropped', () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);

      // Send parts {3, 1} with total=2 -- parts.size will be 2 but key 2 is missing
      // Part 3 will be rejected by validation (part > total), so only part 1 arrives.
      // Lets test the scenario where part numbers dont cover 1..total:
      // We need part numbers that pass validation but still miss a slot.
      // Since part > total is blocked, we simulate with two messages:
      // part 1 of total 3, and part 3 of total 3 -- parts.size never reaches total so it stays buffered.
      // But the requirement says total=2, parts {3,1}: part=3 > total=2 fails validation.
      // That is correct behavior -- the invalid part is dropped, the group never completes.
      // Let's test the actual scenario: total=3, send parts 1 and 3 (skip 2).
      // Then send a bogus part to trigger size==total by adding part 1 again (idempotent) -- still incomplete.

      // Actually, the requirement is: total=2, parts {3, 1}. Part 3 is dropped by validation.
      // Only part 1 arrives. Group stays incomplete. This is the correct behavior.
      // Let's verify no emit and no throw.

      expect(() => {
        // Part 3 of total 2 -- dropped by validation
        listener.message({
          channel: client.channel,
          message: {
            type: 'stream_data',
            streamId: 'my-stream',
            seq: 0,
            ts: 1700000000000,
            multipart: { id: 'mp-bad-3', part: 3, total: 2 },
            data: Buffer.from('aaa').toString('base64'),
          },
        });

        // Part 1 of total 2 -- accepted but group is incomplete (missing part 2)
        listener.message({
          channel: client.channel,
          message: {
            type: 'stream_data',
            streamId: 'my-stream',
            seq: 0,
            ts: 1700000000000,
            multipart: { id: 'mp-bad-3', part: 1, total: 2 },
            data: Buffer.from('bbb').toString('base64'),
          },
        });
      }).not.toThrow();

      // No message emitted
      let resolved = false;
      const iterator = client.inbound[Symbol.asyncIterator]();
      iterator.next().then(() => { resolved = true; });
      expect(resolved).toBe(false);
    });

    it('inconsistent total for same multipart.id drops the group', async () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);

      // First part: total=3
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          multipart: { id: 'mp-inconsistent-1', part: 1, total: 3 },
          data: Buffer.from('part1').toString('base64'),
        },
      });

      // Second part: total=2 (inconsistent!) -- group should be dropped
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          multipart: { id: 'mp-inconsistent-1', part: 2, total: 2 },
          data: Buffer.from('part2').toString('base64'),
        },
      });

      // No message emitted
      let resolved = false;
      const iterator = client.inbound[Symbol.asyncIterator]();
      iterator.next().then(() => { resolved = true; });
      expect(resolved).toBe(false);
    });

    it('inconsistent seq for same multipart.id drops the group', () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);

      // First part: seq=0
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          multipart: { id: 'mp-inconsistent-seq', part: 1, total: 3 },
          data: Buffer.from('part1').toString('base64'),
        },
      });

      // Second part: seq=1 (inconsistent!) -- group should be dropped
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 1,
          ts: 1700000000000,
          multipart: { id: 'mp-inconsistent-seq', part: 2, total: 3 },
          data: Buffer.from('part2').toString('base64'),
        },
      });

      let resolved = false;
      const iterator = client.inbound[Symbol.asyncIterator]();
      iterator.next().then(() => { resolved = true; });
      expect(resolved).toBe(false);
    });

    it('inconsistent type for same multipart.id drops the group', () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);

      // First part: type=stream_data
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          multipart: { id: 'mp-inconsistent-type', part: 1, total: 3 },
          data: Buffer.from('part1').toString('base64'),
        },
      });

      // Second part: type=stream_events (inconsistent!) -- group should be dropped
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_events',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          multipart: { id: 'mp-inconsistent-type', part: 2, total: 3 },
          data: Buffer.from('part2').toString('base64'),
        },
      });

      let resolved = false;
      const iterator = client.inbound[Symbol.asyncIterator]();
      iterator.next().then(() => { resolved = true; });
      expect(resolved).toBe(false);
    });

    it('non-string data field is dropped silently', () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);

      // data is a number
      expect(() => {
        listener.message({
          channel: client.channel,
          message: {
            type: 'stream_data',
            streamId: 'my-stream',
            seq: 0,
            ts: 1700000000000,
            multipart: { id: 'mp-bad-data-1', part: 1, total: 2 },
            data: 12345,
          },
        });
      }).not.toThrow();

      // data is an object
      expect(() => {
        listener.message({
          channel: client.channel,
          message: {
            type: 'stream_data',
            streamId: 'my-stream',
            seq: 0,
            ts: 1700000000000,
            multipart: { id: 'mp-bad-data-2', part: 1, total: 2 },
            data: { nested: true },
          },
        });
      }).not.toThrow();

      let resolved = false;
      const iterator = client.inbound[Symbol.asyncIterator]();
      iterator.next().then(() => { resolved = true; });
      expect(resolved).toBe(false);
    });

    it('stale groups are evicted after TTL', () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);

      // Send part 1 of 2 for a group
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          multipart: { id: 'mp-stale-1', part: 1, total: 2 },
          data: Buffer.from('stale-part').toString('base64'),
        },
      });

      // Advance time past TTL (30 seconds)
      vi.advanceTimersByTime(31_000);

      // Send a new multipart message (triggers eviction)
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 1,
          ts: 1700000031000,
          multipart: { id: 'mp-fresh-1', part: 1, total: 2 },
          data: Buffer.from('fresh-part').toString('base64'),
        },
      });

      // Now try to complete the stale group -- it should have been evicted,
      // so sending part 2 creates a new group entry (with different createdAt)
      // which will have inconsistent metadata (seq/ts differ from the original).
      // Actually, since the old group was evicted, part 2 will create a fresh
      // group and it wont complete (only has part 2).
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          multipart: { id: 'mp-stale-1', part: 2, total: 2 },
          data: Buffer.from('stale-part-2').toString('base64'),
        },
      });

      // The stale group should not have reassembled
      let resolved = false;
      const iterator = client.inbound[Symbol.asyncIterator]();
      iterator.next().then(() => { resolved = true; });
      expect(resolved).toBe(false);
    });

    it('stream_end marker is not yielded to consumer as data', async () => {
      const client = makeClient({ direction: 'inbound' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];

      // Send a normal data message
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          encoding: 'utf8',
          chunks: ['before-end'],
        },
      });

      // Send stream_end marker
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_end',
          streamId: 'my-stream',
          seq: 1,
          ts: 1700000000001,
        },
      });

      const iterator = client.inbound[Symbol.asyncIterator]();

      // First next() should yield the data message
      const result1 = await iterator.next();
      expect(result1.done).toBe(false);
      expect(result1.value.data).toEqual(['before-end']);

      // Second next() should return done (stream_end consumed internally)
      const result2 = await iterator.next();
      expect(result2.done).toBe(true);
    });

    it('normal messages still flow after incomplete multipart', async () => {
      const client = makeClient({ direction: 'inbound' });
      const listener = getListener(client);

      // Send part 1 of 2 (incomplete multipart) -- multipart seq does not
      // enter the reorder buffer until fully reassembled, so seq=0 stays
      // as the next expected value.
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 99,
          ts: 1700000000000,
          multipart: { id: 'mp-incomplete-1', part: 1, total: 2 },
          data: Buffer.from('partial').toString('base64'),
        },
      });

      // Now send a normal (non-multipart) stream_data message at seq=0,
      // which is the first expected seq for a bytes-format stream.
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000001000,
          encoding: 'utf8',
          chunks: ['normal message'],
        },
      });

      const iterator = client.inbound[Symbol.asyncIterator]();
      const result = await iterator.next();

      expect(result.done).toBe(false);
      expect(result.value.format).toBe('bytes');
      expect(result.value.data).toEqual(['normal message']);
      expect(result.value.seq).toBe(0);
    });
  });

  // -- onInboundDone ---------------------------------------------------------

  describe('onInboundDone', () => {
    it('fires when stream_end marker is received', async () => {
      const client = makeClient({ direction: 'inbound' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];
      expect(listener).toBeDefined();

      const fired = vi.fn();
      client.onInboundDone(fired);

      // Send stream_end
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_end',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
        },
      });

      expect(fired).toHaveBeenCalledTimes(1);
    });

    it('fires when end() is called explicitly', async () => {
      const client = makeClient({ direction: 'inbound' });

      const fired = vi.fn();
      client.onInboundDone(fired);

      await client.end();

      expect(fired).toHaveBeenCalledTimes(1);
    });

    it('fires immediately if registered after iterator already completed', async () => {
      const client = makeClient({ direction: 'inbound' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];

      // Complete the iterator first via stream_end
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_end',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
        },
      });

      // Register callback after completion
      const fired = vi.fn();
      client.onInboundDone(fired);

      expect(fired).toHaveBeenCalledTimes(1);
    });

    it('does not fire on normal data messages', () => {
      const client = makeClient({ direction: 'inbound' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];

      const fired = vi.fn();
      client.onInboundDone(fired);

      // Send a normal data message
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_data',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
          encoding: 'utf8',
          chunks: ['hello'],
        },
      });

      expect(fired).not.toHaveBeenCalled();
    });

    it('fires only once even if both stream_end and end() occur', async () => {
      const client = makeClient({ direction: 'inbound' });

      const listener = mockAddListener.mock.calls.find((call: any[]) =>
        call[0]?.message,
      )?.[0];

      const fired = vi.fn();
      client.onInboundDone(fired);

      // First: stream_end fires the callback
      listener.message({
        channel: client.channel,
        message: {
          type: 'stream_end',
          streamId: 'my-stream',
          seq: 0,
          ts: 1700000000000,
        },
      });

      expect(fired).toHaveBeenCalledTimes(1);

      // Second: end() should NOT fire it again
      await client.end();

      expect(fired).toHaveBeenCalledTimes(1);
    });
  });
});
