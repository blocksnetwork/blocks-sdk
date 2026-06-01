/**
 * Tests for SDK Consumer Simplification Phase B (Fixes 8-12).
 *
 * - Fix 8: Stream convenience APIs (bytes(), events(), readable())
 * - Fix 9: Subscribe grace period for outbound streams
 * - Fix 10: Card affinity enforcement on createStream
 * - Fix 11: getAgentCard() on TaskClient
 * - Fix 12: declaredStream in StreamDescriptor and waitForStream matching
 */

/* eslint-disable @typescript-eslint/no-explicit-any */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { StreamClient, _resetUuidCounter } from '../src/stream/stream-client.js';
import { TaskSession } from '../src/runtime/task-session.js';
import { StreamRef } from '../src/runtime/stream-ref.js';
import type { StreamDescriptor } from '../src/stream/descriptor.js';
import type { AgentCard } from '../src/runtime/agent-registry.js';

// ============================================================================
// Mock PubNub for StreamClient tests
// ============================================================================

const mockPublish = vi.fn().mockResolvedValue({ timetoken: '17000000000000000' });
let capturedMessageListener: any = null;

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation(() => ({
      setToken: vi.fn(),
      setFilterExpression: vi.fn(),
      addListener: vi.fn((listener: any) => { capturedMessageListener = listener; }),
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

// ============================================================================
// Test helpers
// ============================================================================

function makeInboundClient(format: 'bytes' | 'events' = 'bytes'): StreamClient {
  return new StreamClient({
    subscribeKey: 'sub-key',
    publishKey: 'pub-key',
    token: 'test-token',
    agentName: 'test_agent',
    streamId: 'test-stream',
    direction: 'inbound',
    format,
  });
}

function simulateInboundMessage(msg: Record<string, unknown>): void {
  if (capturedMessageListener?.message) {
    capturedMessageListener.message({
      channel: 'stream.test_agent.test-stream',
      message: msg,
    });
  }
}

function createMockPubNub() {
  let messageListener: any = null;
  return {
    addListener: vi.fn((listener: any) => { messageListener = listener; }),
    removeListener: vi.fn(() => { messageListener = null; }),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    destroy: vi.fn(),
    setToken: vi.fn(),
    _simulateMessage(channel: string, message: unknown) {
      if (messageListener?.message) {
        messageListener.message({ channel, message });
      }
    },
  };
}

// ============================================================================
// Fix 8: Stream convenience APIs
// ============================================================================

describe('Fix 8: Stream convenience APIs', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    _resetUuidCounter();
    capturedMessageListener = null;
  });

  describe('bytes()', () => {
    const td = new TextDecoder();

    it('decodes base64-encoded chunks', async () => {
      const client = makeInboundClient('bytes');
      const decoded: Uint8Array[] = [];

      const iter = client.bytes()[Symbol.asyncIterator]();

      // Simulate a stream_data message with base64 chunks
      simulateInboundMessage({
        type: 'stream_data',
        chunks: [Buffer.from('hello').toString('base64')],
        encoding: 'base64',
        seq: 0,
        ts: Date.now(),
      });

      const result = await iter.next();
      expect(result.done).toBe(false);
      expect(result.value).toBeInstanceOf(Uint8Array);
      decoded.push(result.value);
      expect(td.decode(decoded[0])).toBe('hello');

      // End the stream
      simulateInboundMessage({ type: 'stream_end', seq: 1 });
      const endResult = await iter.next();
      expect(endResult.done).toBe(true);
    });

    it('decodes utf-8 chunks', async () => {
      const client = makeInboundClient('bytes');
      const iter = client.bytes()[Symbol.asyncIterator]();

      simulateInboundMessage({
        type: 'stream_data',
        chunks: ['hello world'],
        encoding: 'utf8',
        seq: 0,
        ts: Date.now(),
      });

      const result = await iter.next();
      expect(result.done).toBe(false);
      expect(result.value).toBeInstanceOf(Uint8Array);
      expect(td.decode(result.value)).toBe('hello world');

      simulateInboundMessage({ type: 'stream_end', seq: 1 });
    });

    it('yields one Uint8Array per chunk in a multi-chunk message', async () => {
      const client = makeInboundClient('bytes');
      const iter = client.bytes()[Symbol.asyncIterator]();

      simulateInboundMessage({
        type: 'stream_data',
        chunks: ['aaa', 'bbb'],
        encoding: 'utf8',
        seq: 0,
        ts: Date.now(),
      });

      const r1 = await iter.next();
      expect(r1.value).toBeInstanceOf(Uint8Array);
      expect(td.decode(r1.value)).toBe('aaa');
      const r2 = await iter.next();
      expect(r2.value).toBeInstanceOf(Uint8Array);
      expect(td.decode(r2.value)).toBe('bbb');

      simulateInboundMessage({ type: 'stream_end', seq: 1 });
    });
  });

  describe('events()', () => {
    it('flattens batched events and yields individually', async () => {
      const client = makeInboundClient('events');
      const iter = client.events<{ action: string }>()[Symbol.asyncIterator]();

      simulateInboundMessage({
        type: 'stream_events',
        events: [{ action: 'greet' }, { action: 'wave' }],
        seq: 1,
        ts: Date.now(),
      });

      const r1 = await iter.next();
      expect(r1.done).toBe(false);
      expect(r1.value).toEqual({ action: 'greet' });

      const r2 = await iter.next();
      expect(r2.done).toBe(false);
      expect(r2.value).toEqual({ action: 'wave' });

      simulateInboundMessage({ type: 'stream_end', seq: 2 });
      const endResult = await iter.next();
      expect(endResult.done).toBe(true);
    });

    it('handles single-event messages', async () => {
      const client = makeInboundClient('events');
      const iter = client.events()[Symbol.asyncIterator]();

      simulateInboundMessage({
        type: 'stream_events',
        events: [{ text: 'hello' }],
        seq: 1,
        ts: Date.now(),
      });

      const result = await iter.next();
      expect(result.value).toEqual({ text: 'hello' });

      simulateInboundMessage({ type: 'stream_end', seq: 2 });
    });
  });

  describe('readable()', () => {
    it('returns a Readable that pipes decoded bytes', async () => {
      const client = makeInboundClient('bytes');
      const readable = await client.readable();

      const chunks: Buffer[] = [];
      const readPromise = new Promise<void>((resolve) => {
        readable.on('data', (chunk: Buffer) => {
          chunks.push(chunk);
        });
        readable.on('end', () => {
          resolve();
        });
      });

      // Simulate data
      simulateInboundMessage({
        type: 'stream_data',
        chunks: ['hello'],
        encoding: 'utf8',
        seq: 0,
        ts: Date.now(),
      });

      simulateInboundMessage({
        type: 'stream_data',
        chunks: [' world'],
        encoding: 'utf8',
        seq: 1,
        ts: Date.now(),
      });

      // End the stream
      simulateInboundMessage({ type: 'stream_end', seq: 2 });

      await readPromise;
      const combined = Buffer.concat(chunks).toString('utf-8');
      expect(combined).toBe('hello world');
    });
  });

  describe('inbound still works', () => {
    it('yields raw InboundMessage objects', async () => {
      const client = makeInboundClient('bytes');
      const iter = client.inbound[Symbol.asyncIterator]();

      simulateInboundMessage({
        type: 'stream_data',
        chunks: ['hello'],
        encoding: 'utf8',
        seq: 0,
        ts: 1000,
      });

      const result = await iter.next();
      expect(result.done).toBe(false);
      expect(result.value.data).toEqual(['hello']);
      expect(result.value.format).toBe('bytes');
      expect(result.value.encoding).toBe('utf8');

      simulateInboundMessage({ type: 'stream_end', seq: 1 });
    });
  });
});

// ============================================================================
// Fix 9: Subscribe grace period (tested via agent-instance integration)
// ============================================================================
// Note: The subscribe grace period is tested primarily via the agent-instance
// tests. Here we verify the CreateStreamOptions type accepts the field.

describe('Fix 9: Subscribe grace period', () => {
  it('CreateStreamOptions accepts subscribeGraceMs', () => {
    const opts: import('../src/runtime/agent-instance.js').CreateStreamOptions = {
      subscribeGraceMs: 0,
    };
    expect(opts.subscribeGraceMs).toBe(0);
  });

  it('subscribeGraceMs defaults to undefined', () => {
    const opts: import('../src/runtime/agent-instance.js').CreateStreamOptions = {};
    expect(opts.subscribeGraceMs).toBeUndefined();
  });
});

// ============================================================================
// Fix 10: Card affinity enforcement
// ============================================================================

describe('Fix 10: Card affinity enforcement', () => {
  it('card is required on AgentInstanceOptions (type level)', () => {
    const opts: import('../src/runtime/agent-instance.js').AgentInstanceOptions = {
      agentName: 'test',
      card: {
        identity: {
          agentName: 'test',
          displayName: 'Test',
          description: 'Test',
          version: '1.0.0',
          provider: { organization: 'test-org' },
        },
        capabilities: { taskKinds: ['request'] },
        tags: [],
        streams: { _default: { direction: 'outbound', format: 'bytes' } },
      },
    };
    expect(opts.card).toBeDefined();
    expect(opts.card.streams).toBeDefined();
  });
});

// ============================================================================
// Fix 11: getAgentCard() on TaskClient
// ============================================================================

describe('Fix 11: getAgentCard on TaskClient', () => {
  it('returns card for a known agent', async () => {
    // Mock getAgent to return an agent with a card
    const mockCard: AgentCard = {
      identity: {
        agentName: 'weather',
        displayName: 'Weather Agent',
        description: 'Provides weather data',
        version: '1.0.0',
        provider: { organization: 'test-org' },
      },
      capabilities: { taskKinds: ['request'] },
      tags: [],
      streams: { _default: { direction: 'outbound', format: 'bytes' } },
    };

    // Import and mock the registry module
    const registryMod = await import('../src/runtime/agent-registry.js');
    const getAgentSpy = vi.spyOn(registryMod, 'getAgent').mockResolvedValue({
      agentName: 'weather',
      displayName: 'Weather Agent',
      listing: 'public',
      billingMode: 'free',
      card: mockCard,
    });

    const { TaskClient } = await import('../src/runtime/task-client.js');
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      baseUrl: 'http://test-api',
    });

    const card = await client.getAgentCard('weather');
    expect(card).toBeDefined();
    expect(card!.identity.agentName).toBe('weather');
    expect(card!.streams).toBeDefined();

    expect(getAgentSpy).toHaveBeenCalledWith('weather', { baseUrl: 'http://test-api' });
    getAgentSpy.mockRestore();
  });

  it('returns null for unknown agent', async () => {
    const registryMod = await import('../src/runtime/agent-registry.js');
    const getAgentSpy = vi.spyOn(registryMod, 'getAgent').mockResolvedValue(null);

    const { TaskClient } = await import('../src/runtime/task-client.js');
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      baseUrl: 'http://test-api',
    });

    const card = await client.getAgentCard('nonexistent');
    expect(card).toBeNull();
    getAgentSpy.mockRestore();
  });
});

// ============================================================================
// Fix 12: declaredStream in StreamDescriptor and waitForStream matching
// ============================================================================

describe('Fix 12: declaredStream in StreamDescriptor', () => {
  it('StreamDescriptor interface accepts declaredStream field', () => {
    const desc: StreamDescriptor = {
      taskId: 'task-1',
      streamId: 'runtime-id-123',
      agentName: 'weather',
      channel: 'stream.weather.runtime-id-123',
      token: 'T7c-token',
      agentDirection: 'outbound',
      localDirection: 'inbound',
      format: 'bytes',
      affinity: 'dedicated',
      declaredStream: 'out',
    };
    expect(desc.declaredStream).toBe('out');
  });

  it('declaredStream is populated from stream_started event', () => {
    const mockPn = createMockPubNub();
    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: null,
      agentName: 'weather',
      pubnub: mockPn as any,
      ownsSubscribeClient: false,
      sdkOptions: { subscribeKey: 'sub-key' },
    });

    // Simulate stream_started event with top-level declaredStream
    mockPn._simulateMessage(session.statusChannel, {
      type: 'progress',
      taskId: 'task-1',
      streamEvent: 'stream_started',
      declaredStream: 'out',
      streams: {
        'runtime-id-456': {
          channel: 'stream.weather.runtime-id-456',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 'T7c-test',
          tokenTtlMinutes: 62,
        },
      },
    });

    const streams = session.listStreams();
    expect(streams).toHaveLength(1);
    expect(streams[0].descriptor.declaredStream).toBe('out');
    expect(streams[0].descriptor.streamId).toBe('runtime-id-456');

    session.close();
  });

  it('waitForStream matches by declared stream key', async () => {
    const mockPn = createMockPubNub();
    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: null,
      agentName: 'weather',
      pubnub: mockPn as any,
      ownsSubscribeClient: false,
      sdkOptions: { subscribeKey: 'sub-key' },
    });

    // Set up waiter for 'out' (declared stream key)
    const streamPromise = session.waitForStream('out');

    // Simulate stream_started with runtime ID different from declared key
    mockPn._simulateMessage(session.statusChannel, {
      type: 'progress',
      taskId: 'task-1',
      streamEvent: 'stream_started',
      declaredStream: 'out',
      streams: {
        'runtime-generated-789': {
          channel: 'stream.weather.runtime-generated-789',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 'T7c-test',
          tokenTtlMinutes: 62,
        },
      },
    });

    const ref = await streamPromise;
    expect(ref).toBeDefined();
    expect(ref.descriptor.streamId).toBe('runtime-generated-789');
    expect(ref.descriptor.declaredStream).toBe('out');

    session.close();
  });

  it('waitForStream still matches by runtime stream ID', async () => {
    const mockPn = createMockPubNub();
    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: null,
      agentName: 'weather',
      pubnub: mockPn as any,
      ownsSubscribeClient: false,
      sdkOptions: { subscribeKey: 'sub-key' },
    });

    const streamPromise = session.waitForStream('runtime-id-100');

    mockPn._simulateMessage(session.statusChannel, {
      type: 'progress',
      taskId: 'task-1',
      streamEvent: 'stream_started',
      streams: {
        'runtime-id-100': {
          channel: 'stream.weather.runtime-id-100',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 'T7c-test',
          tokenTtlMinutes: 62,
        },
      },
    });

    const ref = await streamPromise;
    expect(ref).toBeDefined();
    expect(ref.descriptor.streamId).toBe('runtime-id-100');

    session.close();
  });

  it('waitForStream checks already-known streams by declaredStream', () => {
    const mockPn = createMockPubNub();
    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: null,
      agentName: 'weather',
      pubnub: mockPn as any,
      ownsSubscribeClient: false,
      sdkOptions: { subscribeKey: 'sub-key' },
    });

    // First, add a stream with a declared key
    mockPn._simulateMessage(session.statusChannel, {
      type: 'progress',
      taskId: 'task-1',
      streamEvent: 'stream_started',
      declaredStream: 'my_stream',
      streams: {
        'rt-id-200': {
          channel: 'stream.weather.rt-id-200',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 'T7c-test',
          tokenTtlMinutes: 62,
        },
      },
    });

    // Now waitForStream by declared key should resolve immediately
    const result = session.waitForStream('my_stream');
    return expect(result).resolves.toEqual(
      expect.objectContaining({
        descriptor: expect.objectContaining({
          streamId: 'rt-id-200',
          declaredStream: 'my_stream',
        }),
      }),
    );
  });
});
