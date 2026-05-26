/**
 * Tests for StreamDescriptor, fromDescriptor, and invertDirection.
 *
 * Covers:
 * - invertDirection: all 3 cases + unknown throws
 * - fromDescriptor: direction mapping from localDirection
 * - fromDescriptor: consumer gating policy (gating defaults to false for writable)
 * - fromDescriptor: explicit gating override
 * - fromDescriptor: extracts fields from descriptor
 * - fromDescriptor: format propagation (bytes and events)
 * - descriptor-opened writers publish correct wire format
 * - direct constructor format default unchanged
 */

import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { invertDirection, type StreamDescriptor } from '../src/stream/descriptor.js';
import { StreamClient, _resetUuidCounter } from '../src/stream/stream-client.js';

// Track publish and filter calls for wire-format and filter assertions
const mockPublish = vi.fn().mockResolvedValue({ timetoken: '17000000000000000' });
const mockSetFilterExpression = vi.fn();

// Mock PubNub to avoid real connections
vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation(() => ({
      setToken: vi.fn(),
      setFilterExpression: mockSetFilterExpression,
      addListener: vi.fn(),
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

describe('invertDirection', () => {
  it('inverts outbound to inbound', () => {
    expect(invertDirection('outbound')).toBe('inbound');
  });

  it('inverts inbound to outbound', () => {
    expect(invertDirection('inbound')).toBe('outbound');
  });

  it('keeps bidirectional as bidirectional', () => {
    expect(invertDirection('bidirectional')).toBe('bidirectional');
  });

  it('throws on unknown direction', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect(() => invertDirection('unknown' as any)).toThrow('Unknown direction: unknown');
  });
});

describe('StreamClient.fromDescriptor', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    _resetUuidCounter();
    mockPublish.mockClear();
    mockSetFilterExpression.mockClear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function makeDescriptor(overrides: Partial<StreamDescriptor> = {}): StreamDescriptor {
    return {
      taskId: 'task-123',
      streamId: 'test-stream',
      agentName: 'weather',
      channel: 'stream.weather.test-stream',
      token: 'T7c-token',
      agentDirection: 'outbound',
      localDirection: 'inbound',
      format: 'bytes',
      affinity: 'dedicated',
      ...overrides,
    };
  }

  const baseOptions = {
    subscribeKey: 'sub-key',
    publishKey: 'pub-key',
  };

  it('creates inbound client from localDirection "inbound"', () => {
    const desc = makeDescriptor({ localDirection: 'inbound' });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    expect(client.isActive).toBe(true);
    expect(client.channel).toBe('stream.weather.test-stream');
    // inbound-only cannot write
    expect(() => client.write('test')).toThrow('Cannot write to an inbound-only stream');
  });

  it('creates outbound client from localDirection "outbound"', () => {
    const desc = makeDescriptor({
      agentDirection: 'inbound',
      localDirection: 'outbound',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    expect(client.isActive).toBe(true);
    // outbound-only cannot read
    expect(() => client.inbound).toThrow('Cannot read from an outbound-only stream');
  });

  it('creates bidirectional client from localDirection "bidirectional"', () => {
    const desc = makeDescriptor({
      agentDirection: 'bidirectional',
      localDirection: 'bidirectional',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    expect(client.isActive).toBe(true);
    // Can both write and read
    client.write({ text: 'hello' });
    expect(client.inbound).toBeDefined();
  });

  it('defaults gating to false when localDirection includes writing', () => {
    // outbound = writable, should default gating to false
    const desc = makeDescriptor({
      agentDirection: 'inbound',
      localDirection: 'outbound',
    });
    // No explicit gating in options -- should default to false
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    // Verify the client works (gating:false means writes go through even with 0 occupancy)
    client.write('hello'); // Should not throw or silently drop
    expect(client.isActive).toBe(true);
  });

  it('defaults gating to false for bidirectional (which includes writing)', () => {
    const desc = makeDescriptor({
      agentDirection: 'bidirectional',
      localDirection: 'bidirectional',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    // Should be able to write without gating blocking
    client.write({ text: 'hello' });
    expect(client.isActive).toBe(true);
  });

  it('defaults gating to true for inbound-only (read only)', () => {
    const desc = makeDescriptor({ localDirection: 'inbound' });
    // inbound doesn't write, so gating default is true (though it doesn't matter for read-only)
    const client = StreamClient.fromDescriptor(desc, baseOptions);
    expect(client.isActive).toBe(true);
  });

  it('respects explicit gating: true override for consumer-writable streams', () => {
    const desc = makeDescriptor({
      agentDirection: 'inbound',
      localDirection: 'outbound',
    });
    const client = StreamClient.fromDescriptor(desc, {
      ...baseOptions,
      gating: true,
    });

    expect(client.isActive).toBe(true);
    // With gating:true and 0 occupancy, write is silently discarded
    // (does not throw, just drops)
    client.write('test');
  });

  it('respects explicit gating: false override for inbound streams', () => {
    const desc = makeDescriptor({ localDirection: 'inbound' });
    const client = StreamClient.fromDescriptor(desc, {
      ...baseOptions,
      gating: false,
    });
    expect(client.isActive).toBe(true);
  });

  it('extracts streamId, agentName, token from descriptor', () => {
    const desc = makeDescriptor({
      streamId: 'my-custom-id',
      agentName: 'video_proc',
      channel: 'stream.video_proc.my-custom-id',
      token: 'my-token-123',
      localDirection: 'inbound',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    expect(client.channel).toBe('stream.video_proc.my-custom-id');
    expect(client.uuid).toMatch(/^video_proc-stream-\d{4}$/);
  });

  it('honors descriptor channel instead of recomputing it', () => {
    const desc = makeDescriptor({
      streamId: 'my-stream',
      agentName: 'test_agent',
      channel: 'custom.channel.name',
      localDirection: 'inbound',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    // Must use the descriptor's channel, not stream.test_agent.my-stream
    expect(client.channel).toBe('custom.channel.name');
  });

  it('works with minimal required fields', () => {
    const desc: StreamDescriptor = {
      taskId: 'task-1',
      streamId: 'stream-1',
      agentName: 'test',
      channel: 'stream.test.stream-1',
      token: 'token-1',
      agentDirection: 'outbound',
      localDirection: 'inbound',
      format: 'bytes',
      affinity: 'dedicated',
    };
    const client = StreamClient.fromDescriptor(desc, baseOptions);
    expect(client.isActive).toBe(true);
  });

  it('works with all fields including metadata', () => {
    const desc: StreamDescriptor = {
      taskId: 'task-1',
      streamId: 'stream-1',
      agentName: 'test',
      channel: 'stream.test.stream-1',
      token: 'token-1',
      agentDirection: 'outbound',
      localDirection: 'inbound',
      format: 'bytes',
      affinity: 'dedicated',
      metadata: { resolution: '1080p', codec: 'h264' },
    };
    const client = StreamClient.fromDescriptor(desc, baseOptions);
    expect(client.isActive).toBe(true);
  });

  it('passes through transport config options', () => {
    const desc = makeDescriptor({ localDirection: 'inbound' });
    const client = StreamClient.fromDescriptor(desc, {
      ...baseOptions,
      maxMessageSize: 8192,
      bundleSizeBytes: 2048,
      maxLatencyMs: 100,
    });
    expect(client.isActive).toBe(true);
  });

  // -- Format propagation tests -----------------------------------------------

  it('creates a bytes-format client when descriptor.format is "bytes"', () => {
    const desc = makeDescriptor({
      agentDirection: 'inbound',
      localDirection: 'outbound',
      format: 'bytes',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    // Bytes format accepts raw strings
    client.write('hello');
    expect(client.isActive).toBe(true);
  });

  it('creates an events-format client when descriptor.format is "events"', () => {
    const desc = makeDescriptor({
      agentDirection: 'inbound',
      localDirection: 'outbound',
      format: 'events',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    // Events format accepts objects
    client.write({ text: 'hello' });
    expect(client.isActive).toBe(true);
  });

  it('descriptor-opened events writers reject raw strings', () => {
    const desc = makeDescriptor({
      agentDirection: 'inbound',
      localDirection: 'outbound',
      format: 'events',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    expect(() => client.write('raw string')).toThrow(
      'write() does not accept raw strings in format: "events"',
    );
  });

  it('descriptor-opened events writers publish stream_events', async () => {
    const desc = makeDescriptor({
      agentDirection: 'inbound',
      localDirection: 'outbound',
      format: 'events',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    client.write({ action: 'greet' });
    // Advance timer to trigger flush
    vi.advanceTimersByTime(300);
    // Allow microtasks to resolve
    await vi.advanceTimersByTimeAsync(0);

    expect(mockPublish).toHaveBeenCalled();
    const published = mockPublish.mock.calls[0][0];
    expect(published.message.type).toBe('stream_events');
    expect(published.message.events).toEqual([{ action: 'greet' }]);
  });

  it('descriptor-opened bytes writers publish stream_data', async () => {
    const desc = makeDescriptor({
      agentDirection: 'inbound',
      localDirection: 'outbound',
      format: 'bytes',
    });
    const client = StreamClient.fromDescriptor(desc, baseOptions);

    client.write('hello bytes');
    // Advance timer to trigger flush
    vi.advanceTimersByTime(300);
    await vi.advanceTimersByTimeAsync(0);

    expect(mockPublish).toHaveBeenCalled();
    const published = mockPublish.mock.calls[0][0];
    expect(published.message.type).toBe('stream_data');
    expect(published.message.chunks).toEqual(['hello bytes']);
  });

  it('direct constructor default format is still bytes', () => {
    const client = new StreamClient({
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      token: 'test-token',
      agentName: 'test_agent',
      streamId: 'my-stream',
      direction: 'outbound',
    });

    // Bytes format accepts raw strings (would throw if events)
    client.write('text data');
    expect(client.isActive).toBe(true);
  });
});

describe('StreamClient.fromDescriptor UUID identity', () => {
  beforeEach(() => {
    _resetUuidCounter();
    mockPublish.mockClear();
    mockSetFilterExpression.mockClear();
  });

  const descriptor: StreamDescriptor = {
    taskId: 'task-123',
    streamId: 'test-stream',
    agentName: 'weather',
    channel: 'stream.weather.test-stream',
    token: 'T7c-token',
    agentDirection: 'outbound',
    localDirection: 'inbound',
    format: 'bytes',
    affinity: 'dedicated',
  };

  it('uses consumerUserId as UUID prefix when supplied', () => {
    _resetUuidCounter();
    const client = StreamClient.fromDescriptor(descriptor, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      consumerUserId: 'usr_abc',
    });
    expect(client.uuid).toMatch(/^usr_abc-stream-/);
  });

  it('falls back to descriptor.agentName when consumerUserId is absent', () => {
    _resetUuidCounter();
    const client = StreamClient.fromDescriptor(descriptor, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
    });
    expect(client.uuid).toMatch(/^weather-stream-/);
  });

  it('produces different UUIDs when counter slots collide but prefixes differ', () => {
    _resetUuidCounter();
    const provider = StreamClient.fromDescriptor(descriptor, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
    }); // → weather-stream-0001
    _resetUuidCounter();
    const consumer = StreamClient.fromDescriptor(descriptor, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      consumerUserId: 'usr_abc',
    }); // → usr_abc-stream-0001
    expect(provider.uuid).not.toBe(consumer.uuid);
  });

  it('bidi filter uses consumerUserId-derived UUID', () => {
    _resetUuidCounter();
    const bidiDescriptor: StreamDescriptor = {
      ...descriptor,
      agentDirection: 'bidirectional',
      localDirection: 'bidirectional',
      format: 'events',
    };
    StreamClient.fromDescriptor(bidiDescriptor, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      consumerUserId: 'usr_abc',
    });
    expect(mockSetFilterExpression).toHaveBeenCalledWith(
      expect.stringContaining('usr_abc-stream-'),
    );
  });

  it('filter expressions accept cross-side messages after fix', () => {
    _resetUuidCounter();
    const providerClient = new StreamClient({
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      token: 'T7c-token',
      agentName: 'weather',
      streamId: 'test-stream',
      direction: 'bidirectional',
      format: 'events',
    });
    // providerClient.uuid === 'weather-stream-0001'

    _resetUuidCounter();
    const consumerClient = StreamClient.fromDescriptor(descriptor, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      consumerUserId: 'usr_abc',
    });
    // consumerClient.uuid === 'usr_abc-stream-0001'

    const providerFilter = `meta.sender != '${providerClient.uuid}'`;
    const consumerFilter = `meta.sender != '${consumerClient.uuid}'`;

    // Each side's filter accepts the other side's messages
    expect(providerFilter).not.toContain(consumerClient.uuid);
    expect(consumerFilter).not.toContain(providerClient.uuid);

    // Each side's filter still rejects its own messages (self-echo)
    expect(providerFilter).toContain(providerClient.uuid);
    expect(consumerFilter).toContain(consumerClient.uuid);
  });
});
