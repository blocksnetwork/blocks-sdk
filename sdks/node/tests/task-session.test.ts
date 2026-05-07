import { describe, it, expect, vi, beforeEach } from 'vitest';
import { TaskSession } from '../src/runtime/task-session.js';

// Minimal PubNub mock for task session tests
function createMockPubNub() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let messageListener: any = null;
  let simCounter = 0;

  return {
    addListener: vi.fn((listener) => { messageListener = listener; }),
    removeListener: vi.fn(() => { messageListener = null; }),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    destroy: vi.fn(),
    // Simulate a message arriving on the task channel. When `timetoken` is
    // omitted an auto-incrementing unique token is supplied so existing tests
    // (which don't care about dedup) still see distinct-looking events and
    // the dedup layer doesn't suppress repeated fixture deliveries.
    _simulateMessage(channel: string, message: unknown, timetoken?: string) {
      if (messageListener?.message) {
        const tt = timetoken ?? `sim-${++simCounter}`;
        messageListener.message({ channel, message, timetoken: tt });
      }
    },
    _getListener() { return messageListener; },
  };
}

// Helper to create a mock StreamClient with controllable lifecycle
function createMockStreamClient() {
  let active = true;
  const endCallbacks: Array<() => void> = [];
  const inboundDoneCallbacks: Array<() => void> = [];
  let inboundDoneFired = false;

  const client = {
    get isActive() { return active; },
    channel: 'stream.echo.s1',
    uuid: 'echo-stream-0001',
    write: vi.fn(),
    end: vi.fn(async () => {
      active = false;
      // end() also fires inboundDone
      if (!inboundDoneFired) {
        inboundDoneFired = true;
        for (const cb of inboundDoneCallbacks) {
          try { cb(); } catch { /* ignore */ }
        }
        inboundDoneCallbacks.length = 0;
      }
      for (const cb of endCallbacks) cb();
      endCallbacks.length = 0;
    }),
    onEnd: vi.fn((cb: () => void) => { endCallbacks.push(cb); }),
    onInboundDone: vi.fn((cb: () => void) => {
      if (inboundDoneFired) {
        cb();
        return;
      }
      inboundDoneCallbacks.push(cb);
    }),
    inbound: { [Symbol.asyncIterator]: () => ({ next: async () => ({ value: undefined, done: true }) }) },
    // Test helper: simulate stream_end arriving (inbound iterator completes)
    _simulateInboundDone() {
      if (inboundDoneFired) return;
      inboundDoneFired = true;
      for (const cb of inboundDoneCallbacks) {
        try { cb(); } catch { /* ignore */ }
      }
      inboundDoneCallbacks.length = 0;
    },
  };
  return client;
}

// Track mock clients created by fromDescriptor
let lastMockClients: ReturnType<typeof createMockStreamClient>[] = [];

// Mock stream SDK to avoid real PubNub connections
vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = await importOriginal() as Record<string, unknown>;
  return {
    ...actual,
    StreamClient: {
      fromDescriptor: vi.fn(() => {
        const client = createMockStreamClient();
        lastMockClients.push(client);
        return client;
      }),
    },
  };
});

describe('TaskSession', () => {
  const taskId = 'task-1';
  const ownerId = 'alice';
  const agentName = 'echo';
  const channel = `u.${ownerId}.${taskId}`;

  let mockPubNub: ReturnType<typeof createMockPubNub>;
  let session: TaskSession;

  beforeEach(() => {
    vi.clearAllMocks();
    lastMockClients = [];
    mockPubNub = createMockPubNub();
    session = new TaskSession({
      taskId,
      ownerId,
      readToken: 't4-token',
      agentName,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPubNub as any,
      sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      // Keep the pre-Family-F 2s drain window for auto-drain tests so
      // existing `vi.advanceTimersByTime(2000)` assertions stay fast
      // and explicit. The production default is 30000 ms (Family F);
      // see the "configurable drain window" describe block for
      // coverage of the new default and overrides.
      drainWindowMs: 2000,
    });
  });

  it('subscribes to the task channel on creation with cache-replay timetoken', () => {
    // timetoken: 1000 asks PubNub to replay everything still in the channel's
    // in-memory cache (SDK_CONTRACT §10.4.1a).
    expect(mockPubNub.subscribe).toHaveBeenCalledWith({ channels: [channel], timetoken: 1000 });
    expect(mockPubNub.addListener).toHaveBeenCalled();
  });

  it('exposes taskId, ownerId, orgId, readToken, statusChannel', () => {
    expect(session.taskId).toBe(taskId);
    expect(session.ownerId).toBe(ownerId);
    expect(session.orgId).toBe(ownerId); // defaults to ownerId when orgId not provided
    expect(session.readToken).toBe('t4-token');
    expect(session.statusChannel).toBe(channel);
  });

  it('orgId defaults to ownerId when not provided', () => {
    const s = new TaskSession({
      taskId,
      ownerId: 'alice',
      readToken: null,
      agentName,
      pubnub: mockPubNub as any,
      sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
    });
    expect(s.orgId).toBe('alice');
    s.close();
  });

  it('uses explicit orgId when provided', () => {
    const s = new TaskSession({
      taskId,
      ownerId: 'alice',
      orgId: 'acme-corp',
      readToken: null,
      agentName,
      pubnub: mockPubNub as any,
      sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
    });
    expect(s.orgId).toBe('acme-corp');
    expect(s.ownerId).toBe('alice');
    expect(s.statusChannel).toBe('u.acme-corp.task-1');
    s.close();
  });

  describe('event callbacks', () => {
    it('onProgress fires for progress events', () => {
      const cb = vi.fn();
      session.onProgress(cb);
      mockPubNub._simulateMessage(channel, { type: 'progress', taskId, progress: 50 });
      expect(cb).toHaveBeenCalledWith(expect.objectContaining({ type: 'progress', progress: 50 }));
    });

    it('onArtifact fires for artifact events', () => {
      const cb = vi.fn();
      session.onArtifact(cb);
      mockPubNub._simulateMessage(channel, { type: 'artifact', taskId });
      expect(cb).toHaveBeenCalled();
    });

    it('onTerminal fires for terminal events', () => {
      const cb = vi.fn();
      session.onTerminal(cb);
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      expect(cb).toHaveBeenCalledWith(expect.objectContaining({ state: 'completed' }));
    });

    it('onEvent fires for all events', () => {
      const cb = vi.fn();
      session.onEvent(cb);
      mockPubNub._simulateMessage(channel, { type: 'progress', taskId });
      mockPubNub._simulateMessage(channel, { type: 'artifact', taskId });
      expect(cb).toHaveBeenCalledTimes(2);
    });

    it('unsubscribe callback removes the handler', () => {
      const cb = vi.fn();
      const unsub = session.onProgress(cb);
      unsub();
      mockPubNub._simulateMessage(channel, { type: 'progress', taskId });
      expect(cb).not.toHaveBeenCalled();
    });
  });

  describe('stream discovery', () => {
    const streamStartedEvent = {
      type: 'progress',
      taskId,
      streamEvent: 'stream_started',
      streams: {
        's1': {
          channel: 'stream.echo.s1',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-1',
          tokenTtlMinutes: 62,
          metadata: { kind: 'data' },
        },
      },
    };

    it('parses stream_started into StreamRef with correct direction inversion', () => {
      const cb = vi.fn();
      session.onStream(cb);
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      expect(cb).toHaveBeenCalledTimes(1);
      const ref = cb.mock.calls[0][0];
      expect(ref.descriptor.streamId).toBe('s1');
      expect(ref.descriptor.agentDirection).toBe('outbound');
      expect(ref.descriptor.localDirection).toBe('inbound');
      expect(ref.descriptor.format).toBe('bytes');
      expect(ref.descriptor.token).toBe('t7c-1');
      expect(ref.descriptor.metadata).toEqual({ kind: 'data' });
    });

    it('parses format from stream_started correctly', () => {
      const cb = vi.fn();
      session.onStream(cb);
      mockPubNub._simulateMessage(channel, {
        type: 'progress',
        taskId,
        streamEvent: 'stream_started',
        streams: {
          's2': {
            channel: 'stream.echo.s2',
            direction: 'inbound',
            format: 'events',
            affinity: 'dedicated',
            token: 't7c-2',
            tokenTtlMinutes: 62,
          },
        },
      });
      const ref = cb.mock.calls[0][0];
      expect(ref.descriptor.format).toBe('events');
      expect(ref.descriptor.localDirection).toBe('outbound');
    });

    it('listStreams returns discovered streams', () => {
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const streams = session.listStreams();
      expect(streams).toHaveLength(1);
      expect(streams[0].descriptor.streamId).toBe('s1');
    });

    it('waitForStream resolves for known stream', async () => {
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = await session.waitForStream('s1');
      expect(ref.descriptor.streamId).toBe('s1');
    });

    it('waitForStream with no arg resolves for single stream', async () => {
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = await session.waitForStream();
      expect(ref.descriptor.streamId).toBe('s1');
    });

    it('waitForStream with no arg rejects for multiple streams', async () => {
      mockPubNub._simulateMessage(channel, {
        type: 'progress',
        taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': { channel: 'stream.echo.s1', direction: 'outbound', format: 'bytes', affinity: 'dedicated', token: 't1', tokenTtlMinutes: 62 },
          's2': { channel: 'stream.echo.s2', direction: 'outbound', format: 'bytes', affinity: 'dedicated', token: 't2', tokenTtlMinutes: 62 },
        },
      });
      await expect(session.waitForStream()).rejects.toThrow('Multiple streams');
    });

    it('waitForStream resolves when stream arrives later', async () => {
      const promise = session.waitForStream('s1');
      // Stream not yet announced -- publish it now
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = await promise;
      expect(ref.descriptor.streamId).toBe('s1');
    });

    it('waitForStreamWhere resolves with predicate', async () => {
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = await session.waitForStreamWhere(
        (r) => r.descriptor.metadata?.kind === 'data',
      );
      expect(ref.descriptor.streamId).toBe('s1');
    });

    it('onStream fires for already-known streams', () => {
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const cb = vi.fn();
      session.onStream(cb);
      expect(cb).toHaveBeenCalledTimes(1);
    });

    it('does not duplicate streams on repeated stream_started', () => {
      const cb = vi.fn();
      session.onStream(cb);
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      expect(cb).toHaveBeenCalledTimes(1);
    });
  });

  describe('auto-close on terminal', () => {
    it('unsubscribes from task channel on terminal', () => {
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      expect(mockPubNub.removeListener).toHaveBeenCalled();
      expect(mockPubNub.unsubscribe).toHaveBeenCalledWith({ channels: [channel] });
      expect(session.isClosed).toBe(true);
    });

    it('rejects pending waitForStream on terminal', async () => {
      const promise = session.waitForStream('s1');
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      await expect(promise).rejects.toThrow('closed');
    });

    it('rejects pending waitForStreamWhere on terminal', async () => {
      const promise = session.waitForStreamWhere(() => true);
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      await expect(promise).rejects.toThrow('closed');
    });
  });

  describe('close', () => {
    it('is idempotent', () => {
      session.close();
      session.close(); // should not throw
      expect(session.isClosed).toBe(true);
    });

    it('rejects waitForStream after close', async () => {
      session.close();
      await expect(session.waitForStream()).rejects.toThrow('closed');
    });

    it('stops emitting events after close', () => {
      const cb = vi.fn();
      session.onProgress(cb);
      session.close();
      mockPubNub._simulateMessage(channel, { type: 'progress', taskId });
      expect(cb).not.toHaveBeenCalled();
    });
  });

  describe('auto-drain', () => {
    const streamStartedEvent = {
      type: 'progress',
      taskId,
      streamEvent: 'stream_started',
      streams: {
        's1': {
          channel: 'stream.echo.s1',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-1',
          tokenTtlMinutes: 62,
        },
      },
    };

    const twoStreamStartedEvent = {
      type: 'progress',
      taskId,
      streamEvent: 'stream_started',
      streams: {
        's1': {
          channel: 'stream.echo.s1',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-1',
          tokenTtlMinutes: 62,
        },
        's2': {
          channel: 'stream.echo.s2',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-2',
          tokenTtlMinutes: 62,
        },
      },
    };

    it('terminal + stream_end within drain window: session closes immediately', () => {
      vi.useFakeTimers();
      try {
        // Discover and open a stream
        mockPubNub._simulateMessage(channel, streamStartedEvent);
        const ref = session.listStreams()[0];
        ref.open();
        const client = lastMockClients[0];

        // Terminal arrives
        mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
        expect(session.isClosed).toBe(false);

        // stream_end arrives before drain timer
        client._simulateInboundDone();
        expect(session.isClosed).toBe(true);
      } finally {
        vi.useRealTimers();
      }
    });

    it('terminal + no stream_end: drain timer fires, stream ended, session closes', () => {
      vi.useFakeTimers();
      try {
        mockPubNub._simulateMessage(channel, streamStartedEvent);
        const ref = session.listStreams()[0];
        ref.open();
        const client = lastMockClients[0];

        mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
        expect(session.isClosed).toBe(false);
        expect(client.end).not.toHaveBeenCalled();

        // Advance past drain window
        vi.advanceTimersByTime(2000);
        expect(client.end).toHaveBeenCalled();
        expect(session.isClosed).toBe(true);
      } finally {
        vi.useRealTimers();
      }
    });

    it('terminal + multiple streams, partial drain: undrained stream force-ended after 2s', () => {
      vi.useFakeTimers();
      try {
        mockPubNub._simulateMessage(channel, twoStreamStartedEvent);
        const refs = session.listStreams();
        refs[0].open();
        refs[1].open();
        const client1 = lastMockClients[0];
        const client2 = lastMockClients[1];

        mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
        expect(session.isClosed).toBe(false);

        // stream 1 drains naturally
        client1._simulateInboundDone();
        expect(session.isClosed).toBe(false);

        // Drain timer fires, stream 2 force-ended
        vi.advanceTimersByTime(2000);
        expect(client2.end).toHaveBeenCalled();
        expect(session.isClosed).toBe(true);
      } finally {
        vi.useRealTimers();
      }
    });

    it('stream_end before terminal: session stays open, then terminal closes immediately', () => {
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = session.listStreams()[0];
      ref.open();
      const client = lastMockClients[0];

      // stream_end arrives before terminal
      client._simulateInboundDone();
      expect(session.isClosed).toBe(false);

      // Terminal arrives -- no open streams, closes immediately
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      expect(session.isClosed).toBe(true);
    });

    it('session.close() during drain window: timer cancelled, streams ended, immediate close', () => {
      vi.useFakeTimers();
      try {
        mockPubNub._simulateMessage(channel, streamStartedEvent);
        const ref = session.listStreams()[0];
        ref.open();
        const client = lastMockClients[0];

        mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
        expect(session.isClosed).toBe(false);

        // Developer calls close() during drain window
        session.close();
        expect(session.isClosed).toBe(true);

        // close() now ends open stream clients (fire-and-forget)
        expect(client.end).toHaveBeenCalled();

        // Advancing timer should not cause errors (timer was cancelled)
        vi.advanceTimersByTime(2000);
      } finally {
        vi.useRealTimers();
      }
    });

    it('no streams opened + terminal: immediate close (no timer)', () => {
      // This is the fast path -- default session with no streams
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      expect(session.isClosed).toBe(true);
    });

    it('onTerminal callbacks still fire with autoDrain enabled', () => {
      const cb = vi.fn();
      session.onTerminal(cb);
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = session.listStreams()[0];
      ref.open();

      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      expect(cb).toHaveBeenCalledWith(expect.objectContaining({ state: 'completed' }));
    });

    it('stream_end triggers client.end() to fully tear down the stream client', () => {
      // Finding 1: onInboundDone must call client.end() so PubNub is unsubscribed
      // and isActive becomes false — not just mark the iterator done.
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = session.listStreams()[0];
      ref.open();
      const client = lastMockClients[0];

      expect(client.isActive).toBe(true);

      // stream_end arrives (simulated via _simulateInboundDone)
      client._simulateInboundDone();

      // client.end() should have been called by the onInboundDone handler
      expect(client.end).toHaveBeenCalled();
      expect(client.isActive).toBe(false);
    });

    it('close() from inside onTerminal callback ends stream clients and prevents drain timer', () => {
      // If a terminal callback calls close(), startAutoDrain()
      // must not schedule a drain timer on an already-closed session.
      // close() itself ends the open stream clients.
      vi.useFakeTimers();
      try {
        mockPubNub._simulateMessage(channel, streamStartedEvent);
        const ref = session.listStreams()[0];
        ref.open();
        const client = lastMockClients[0];

        // Register a terminal callback that closes the session
        session.onTerminal(() => {
          session.close();
        });

        mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
        expect(session.isClosed).toBe(true);

        // close() ends open stream clients (fire-and-forget)
        expect(client.end).toHaveBeenCalledTimes(1);

        // Advancing the timer should NOT cause additional client.end() calls
        vi.advanceTimersByTime(2000);
        expect(client.end).toHaveBeenCalledTimes(1);
      } finally {
        vi.useRealTimers();
      }
    });

    it('autoDrain: false causes immediate close on terminal, streams ended by close()', () => {
      // Create a session with autoDrain disabled
      const legacySession = new TaskSession({
        taskId,
        ownerId,
        readToken: 't4-token',
        agentName,
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPubNub as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        autoDrain: false,
      });

      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = legacySession.listStreams()[0];
      ref.open();
      const client = lastMockClients[lastMockClients.length - 1];

      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      // Immediate close, no drain timer, but close() ends open stream clients
      expect(legacySession.isClosed).toBe(true);
      expect(client.end).toHaveBeenCalled();
    });
  });

  describe('configurable drain window (Family F)', () => {
    const streamStartedEvent = {
      type: 'progress',
      taskId,
      streamEvent: 'stream_started',
      streams: {
        's1': {
          channel: 'stream.echo.s1',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-1',
          tokenTtlMinutes: 62,
        },
      },
    };

    it('defaults to 30000 ms when drainWindowMs is not provided', () => {
      vi.useFakeTimers();
      try {
        const defaultSession = new TaskSession({
          taskId,
          ownerId,
          readToken: 't4-token',
          agentName,
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          pubnub: mockPubNub as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });

        mockPubNub._simulateMessage(channel, streamStartedEvent);
        const ref = defaultSession.listStreams()[0];
        ref.open();
        const client = lastMockClients[lastMockClients.length - 1];

        mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
        expect(defaultSession.isClosed).toBe(false);

        // Old 2s default would have fired by now; new 30s default must not.
        vi.advanceTimersByTime(2000);
        expect(client.end).not.toHaveBeenCalled();
        expect(defaultSession.isClosed).toBe(false);

        // Advance to just before 30s -- still waiting.
        vi.advanceTimersByTime(27999);
        expect(client.end).not.toHaveBeenCalled();

        // Cross 30s boundary -- now force-ended.
        vi.advanceTimersByTime(1);
        expect(client.end).toHaveBeenCalled();
        expect(defaultSession.isClosed).toBe(true);
      } finally {
        vi.useRealTimers();
      }
    });

    it('honors a custom drainWindowMs override (60000 ms)', () => {
      vi.useFakeTimers();
      try {
        const customSession = new TaskSession({
          taskId,
          ownerId,
          readToken: 't4-token',
          agentName,
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          pubnub: mockPubNub as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
          drainWindowMs: 60000,
        });

        mockPubNub._simulateMessage(channel, streamStartedEvent);
        const ref = customSession.listStreams()[0];
        ref.open();
        const client = lastMockClients[lastMockClients.length - 1];

        mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });

        // 30s default would have fired; 60s override must not.
        vi.advanceTimersByTime(30000);
        expect(client.end).not.toHaveBeenCalled();
        expect(customSession.isClosed).toBe(false);

        vi.advanceTimersByTime(30000);
        expect(client.end).toHaveBeenCalled();
        expect(customSession.isClosed).toBe(true);
      } finally {
        vi.useRealTimers();
      }
    });

    it('already-open stream is not force-ended before drain window expires', () => {
      vi.useFakeTimers();
      try {
        const customSession = new TaskSession({
          taskId,
          ownerId,
          readToken: 't4-token',
          agentName,
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          pubnub: mockPubNub as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
          drainWindowMs: 5000,
        });

        mockPubNub._simulateMessage(channel, streamStartedEvent);
        const ref = customSession.listStreams()[0];
        ref.open();
        const client = lastMockClients[lastMockClients.length - 1];

        mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });

        // At t=4999 the client is still active.
        vi.advanceTimersByTime(4999);
        expect(client.end).not.toHaveBeenCalled();
        expect(client.isActive).toBe(true);

        // At t=5000 the drain window expires and the client is force-ended.
        vi.advanceTimersByTime(1);
        expect(client.end).toHaveBeenCalled();
        expect(client.isActive).toBe(false);
      } finally {
        vi.useRealTimers();
      }
    });
  });

  describe('openAllStreams (Family F)', () => {
    const twoStreamInbound = {
      type: 'progress',
      taskId,
      streamEvent: 'stream_started',
      streams: {
        's1': {
          channel: 'stream.echo.s1',
          direction: 'outbound', // agent outbound -> consumer inbound
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-1',
          tokenTtlMinutes: 62,
        },
        's2': {
          channel: 'stream.echo.s2',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-2',
          tokenTtlMinutes: 62,
        },
      },
    };

    const mixedDirectionStreams = {
      type: 'progress',
      taskId,
      streamEvent: 'stream_started',
      streams: {
        's1': {
          channel: 'stream.echo.s1',
          direction: 'outbound', // inbound for consumer
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-1',
          tokenTtlMinutes: 62,
        },
        's2': {
          channel: 'stream.echo.s2',
          direction: 'inbound', // outbound for consumer -- skipped
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-2',
          tokenTtlMinutes: 62,
        },
        's3': {
          channel: 'stream.echo.s3',
          direction: 'bidirectional',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-3',
          tokenTtlMinutes: 62,
        },
      },
    };

    it('returns clients for every readable stream', () => {
      mockPubNub._simulateMessage(channel, twoStreamInbound);
      const clients = session.openAllStreams();
      expect(clients).toHaveLength(2);
      expect(clients[0]).toBe(lastMockClients[0]);
      expect(clients[1]).toBe(lastMockClients[1]);
    });

    it('skips outbound-only streams and opens inbound + bidirectional', () => {
      mockPubNub._simulateMessage(channel, mixedDirectionStreams);
      const clients = session.openAllStreams();
      // s1 (inbound) + s3 (bidirectional) = 2; s2 (outbound) skipped.
      expect(clients).toHaveLength(2);
    });

    it('preserves insertion order matching listStreams()', () => {
      mockPubNub._simulateMessage(channel, twoStreamInbound);
      const refOrder = session.listStreams();
      const clients = session.openAllStreams();
      expect(clients).toHaveLength(refOrder.length);
      expect(refOrder.map(r => r.descriptor.streamId)).toEqual(['s1', 's2']);
    });

    it('is idempotent: second call returns the same client objects', () => {
      mockPubNub._simulateMessage(channel, twoStreamInbound);
      const first = session.openAllStreams();
      const second = session.openAllStreams();
      expect(second).toHaveLength(first.length);
      expect(second[0]).toBe(first[0]);
      expect(second[1]).toBe(first[1]);
    });

    it('returns empty list when session has no streams', () => {
      const clients = session.openAllStreams();
      expect(clients).toEqual([]);
    });

    it('skips streams that throw on open (silent-skip policy)', () => {
      mockPubNub._simulateMessage(channel, twoStreamInbound);
      const refs = session.listStreams();
      // Force the first ref to throw on open via a one-shot override.
      const origOpen = refs[0].open.bind(refs[0]);
      let firstCalled = false;
      (refs[0] as unknown as { open: (...a: unknown[]) => unknown }).open = (...args) => {
        if (!firstCalled) { firstCalled = true; throw new Error('boom'); }
        return origOpen(...args as []);
      };
      const clients = session.openAllStreams();
      // s1 threw on first call, s2 opened successfully.
      expect(clients).toHaveLength(1);
    });
  });

  describe('unopened terminal-session ref.open() still throws (t7c regression)', () => {
    it('openAllStreams does not bypass the terminal short-circuit for unopened refs', () => {
      const streamStartedEvent = {
        type: 'progress',
        taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1',
            direction: 'outbound',
            format: 'bytes',
            affinity: 'dedicated',
            token: 't7c-1',
            tokenTtlMinutes: 62,
          },
        },
      };

      // Stream discovered while the task is still active.
      mockPubNub._simulateMessage(channel, streamStartedEvent);

      // Terminal arrives without the consumer opening the stream first.
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });

      // openAllStreams is called after terminal: the unopened ref throws
      // StreamUnavailableError inside StreamRef.open, and
      // openAllStreams silently skips it. Result: empty array.
      const clients = session.openAllStreams();
      expect(clients).toEqual([]);
    });
  });

  describe('pre-closed session (terminal idempotent hit)', () => {
    it('terminal idempotent hit creates a pre-closed session with no PubNub allocation', () => {
      const preClosed = new TaskSession({
        taskId: 'task-done',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: null,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        idempotent: true,
        state: 'completed',
        preClosed: true,
        ownsSubscribeClient: false,
      });

      // Should be immediately closed
      expect(preClosed.isClosed).toBe(true);

      // Should expose correct metadata
      expect(preClosed.taskId).toBe('task-done');
      expect(preClosed.idempotent).toBe(true);
      expect(preClosed.state).toBe('completed');

      // close() should be safe (idempotent, no PubNub to clean up)
      preClosed.close();
      expect(preClosed.isClosed).toBe(true);
    });

    it('pre-closed session rejects waitForStream immediately', async () => {
      const preClosed = new TaskSession({
        taskId: 'task-done',
        ownerId: 'alice',
        readToken: null,
        agentName: 'echo',
        pubnub: null,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        preClosed: true,
        state: 'failed',
      });

      await expect(preClosed.waitForStream()).rejects.toThrow('closed');
      await expect(preClosed.waitForStreamWhere(() => true)).rejects.toThrow('closed');
    });

    it('pre-closed session close() is idempotent', () => {
      const preClosed = new TaskSession({
        taskId: 'task-done',
        ownerId: 'alice',
        readToken: null,
        agentName: 'echo',
        pubnub: null,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        preClosed: true,
        state: 'canceled',
      });

      expect(preClosed.isClosed).toBe(true);
      preClosed.close(); // should not throw
      expect(preClosed.isClosed).toBe(true);
    });

    it('pending idempotent hit creates a normal live session', () => {
      const pn = createMockPubNub();
      const liveSession = new TaskSession({
        taskId: 'task-pending',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: pn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        idempotent: true,
        state: 'pending',
        // preClosed NOT set (defaults to false)
      });

      expect(liveSession.isClosed).toBe(false);
      expect(pn.subscribe).toHaveBeenCalled();
      expect(pn.addListener).toHaveBeenCalled();
      expect(liveSession.idempotent).toBe(true);
      expect(liveSession.state).toBe('pending');

      liveSession.close();
    });

    it('running idempotent hit creates a normal live session', () => {
      const pn = createMockPubNub();
      const liveSession = new TaskSession({
        taskId: 'task-running',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: pn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        idempotent: true,
        state: 'running',
      });

      expect(liveSession.isClosed).toBe(false);
      expect(pn.subscribe).toHaveBeenCalled();
      expect(liveSession.idempotent).toBe(true);
      expect(liveSession.state).toBe('running');

      liveSession.close();
    });

    it('pre-closed session does not emit events', () => {
      const preClosed = new TaskSession({
        taskId: 'task-done',
        ownerId: 'alice',
        readToken: null,
        agentName: 'echo',
        pubnub: null,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        preClosed: true,
        state: 'completed',
      });

      const progressCb = vi.fn();
      const terminalCb = vi.fn();
      const eventCb = vi.fn();
      preClosed.onProgress(progressCb);
      preClosed.onTerminal(terminalCb);
      preClosed.onEvent(eventCb);

      // No live events arrive on a pre-closed session (no subscription).
      expect(progressCb).not.toHaveBeenCalled();
      expect(eventCb).not.toHaveBeenCalled();
      // onTerminal fires immediately for already-terminal sessions.
      expect(terminalCb).toHaveBeenCalledWith(expect.objectContaining({
        type: 'terminal',
        taskId: 'task-done',
        state: 'completed',
      }));
    });

    it('pre-closed session has empty stream list', () => {
      const preClosed = new TaskSession({
        taskId: 'task-done',
        ownerId: 'alice',
        readToken: null,
        agentName: 'echo',
        pubnub: null,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        preClosed: true,
        state: 'completed',
      });

      expect(preClosed.listStreams()).toEqual([]);
    });
  });

  describe('onTerminal immediate fire on already-terminal sessions', () => {
    it('fires onTerminal immediately for skipSubscription sessions with terminal state', () => {
      const session = new TaskSession({
        taskId: 'task-term',
        ownerId: 'alice',
        readToken: null,
        agentName: 'echo',
        pubnub: null,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        skipSubscription: true,
        state: 'failed',
      });

      const cb = vi.fn();
      session.onTerminal(cb);
      expect(cb).toHaveBeenCalledTimes(1);
      expect(cb).toHaveBeenCalledWith(expect.objectContaining({
        type: 'terminal',
        taskId: 'task-term',
        state: 'failed',
      }));
    });

    it('does not fire onTerminal immediately for non-terminal sessions', () => {
      const session = new TaskSession({
        taskId: 'task-active',
        ownerId: 'alice',
        readToken: null,
        agentName: 'echo',
        pubnub: createMockPubNub() as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      });

      const cb = vi.fn();
      session.onTerminal(cb);
      expect(cb).not.toHaveBeenCalled();
      session.close();
    });
  });

  describe('pause/resume not present', () => {
    it('does not have pause method', () => {
      // TaskSession should not have pause/resume
      expect((session as Record<string, unknown>).pause).toBeUndefined();
      expect((session as Record<string, unknown>).resume).toBeUndefined();
    });
  });

  describe('terminal-state mutation & StreamRef short-circuit (Fix A)', () => {
    const streamStartedEvent = {
      type: 'progress',
      taskId,
      streamEvent: 'stream_started',
      streams: {
        's1': {
          channel: 'stream.echo.s1',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c-1',
          tokenTtlMinutes: 62,
        },
      },
    };

    it('state is undefined at construction for non-pre-closed live session', () => {
      expect(session.state).toBeUndefined();
    });

    it('assigns session.state from terminal event state', () => {
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      expect(session.state).toBe('completed');
    });

    it('assigns session.state BEFORE firing terminal callbacks', () => {
      // Register a terminal callback that reads session.state at the
      // moment it fires. If state mutation happens after callbacks, the
      // observed value will be undefined; Fix A requires it be 'failed'.
      let observedState: string | undefined;
      session.onTerminal(() => {
        observedState = session.state;
      });
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'failed' });
      expect(observedState).toBe('failed');
    });

    it('consumer onTerminal calling ref.open() throws StreamUnavailableError', async () => {
      // Announce a stream while session is running
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = session.listStreams()[0];

      // Import lazily to avoid circular type issues in test
      const { StreamUnavailableError } = await import('../src/runtime/stream-ref.js');

      let thrown: Error | undefined;
      session.onTerminal(() => {
        try {
          ref.open();
        } catch (err) {
          thrown = err as Error;
        }
      });

      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'canceled' });

      expect(thrown).toBeInstanceOf(StreamUnavailableError);
      const e = thrown as InstanceType<typeof StreamUnavailableError>;
      expect(e.taskId).toBe(taskId);
      expect(e.streamId).toBe('s1');
      expect(e.terminalState).toBe('canceled');
    });

    it('ref.descriptor remains accessible on a ref whose session has gone terminal', () => {
      mockPubNub._simulateMessage(channel, streamStartedEvent);
      const ref = session.listStreams()[0];
      mockPubNub._simulateMessage(channel, { type: 'terminal', taskId, state: 'completed' });
      // Accessing descriptor on a terminal-session ref must not throw
      expect(ref.descriptor.streamId).toBe('s1');
      expect(ref.descriptor.channel).toBe('stream.echo.s1');
      expect(ref.descriptor.token).toBe('t7c-1');
    });

    it('preloaded streams honor sessionState short-circuit via explicit terminal state', async () => {
      // Construct a session with a preloaded stream and state='completed'
      // as `TaskClient.connect()` would do for a terminal connect.
      const { StreamRef } = await import('../src/runtime/stream-ref.js');
      const { StreamUnavailableError } = await import('../src/runtime/stream-ref.js');

      const preloadedDescriptor = {
        taskId,
        streamId: 'preloaded-1',
        agentName,
        channel: 'stream.echo.preloaded-1',
        token: 'expired-t7c',
        agentDirection: 'outbound' as const,
        localDirection: 'inbound' as const,
        format: 'bytes' as const,
        affinity: 'dedicated' as const,
        metadata: { kind: 'data' },
        declaredStream: 'audio',
      };
      const rawRef = new StreamRef(preloadedDescriptor, { subscribeKey: 'sk', publishKey: 'pk' });
      const preloaded = new Map<string, InstanceType<typeof StreamRef>>();
      preloaded.set('preloaded-1', rawRef);

      const pn = createMockPubNub();
      const connectedSession = new TaskSession({
        taskId,
        ownerId,
        readToken: 't4-token',
        agentName,
        pubnub: pn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        skipSubscription: true,
        state: 'completed',
        preloadedStreams: preloaded,
      });

      const ref = connectedSession.listStreams()[0];
      expect(() => ref.open()).toThrowError(StreamUnavailableError);

      // Descriptor still inspectable
      expect(ref.descriptor.declaredStream).toBe('audio');
      expect(ref.descriptor.streamId).toBe('preloaded-1');

      connectedSession.close();
    });
  });

  describe('subscribe cache-replay dedup (Family E)', () => {
    it('drops duplicate artifact events with the same timetoken', () => {
      const cb = vi.fn();
      session.onArtifact(cb);

      const artifactRef = { kind: 'inline' as const, mimeType: 'text/plain', size: 3, data: 'Zm9v' };
      mockPubNub._simulateMessage(
        channel,
        { type: 'artifact', taskId, artifactRef },
        '17000000000000001',
      );
      // Same event, same timetoken (cache replay + live overlap).
      mockPubNub._simulateMessage(
        channel,
        { type: 'artifact', taskId, artifactRef },
        '17000000000000001',
      );

      expect(cb).toHaveBeenCalledTimes(1);
      expect(session.listArtifacts()).toHaveLength(1);
    });

    it('dispatches events with distinct timetokens normally', () => {
      const cb = vi.fn();
      session.onProgress(cb);

      mockPubNub._simulateMessage(
        channel,
        { type: 'progress', taskId, progress: 25 },
        '17000000000000001',
      );
      mockPubNub._simulateMessage(
        channel,
        { type: 'progress', taskId, progress: 50 },
        '17000000000000002',
      );

      expect(cb).toHaveBeenCalledTimes(2);
    });

    it('does not dedup events delivered without a timetoken (defensive)', () => {
      const cb = vi.fn();
      session.onProgress(cb);
      const listener = mockPubNub._getListener();
      // Call listener directly with no timetoken field at all. Dedup is only
      // applied when a timetoken is present; ensures synthetic test fixtures
      // and any pre-existing dispatch sites that don't thread tt are not
      // silently dropped.
      listener.message({ channel, message: { type: 'progress', taskId, progress: 1 } });
      listener.message({ channel, message: { type: 'progress', taskId, progress: 2 } });

      expect(cb).toHaveBeenCalledTimes(2);
    });

    it('dedups terminal events so onTerminal fires once', () => {
      const terminalCb = vi.fn();
      session.onTerminal(terminalCb);

      mockPubNub._simulateMessage(
        channel,
        { type: 'terminal', taskId, state: 'completed' },
        '17000000000000100',
      );
      mockPubNub._simulateMessage(
        channel,
        { type: 'terminal', taskId, state: 'completed' },
        '17000000000000100',
      );

      expect(terminalCb).toHaveBeenCalledTimes(1);
    });

    it('bounds the seen-timetokens set at 200 entries', () => {
      const cb = vi.fn();
      session.onProgress(cb);

      // Fire 201 events with unique timetokens. 201 unique timetokens should
      // all dispatch; the bounded set caps memory by evicting the oldest once
      // the cap is exceeded.
      for (let i = 0; i < 201; i++) {
        mockPubNub._simulateMessage(
          channel,
          { type: 'progress', taskId, progress: i },
          `tt-${i.toString().padStart(4, '0')}`,
        );
      }
      expect(cb).toHaveBeenCalledTimes(201);

      // Re-deliver the OLDEST timetoken (tt-0000). If eviction worked, it is
      // no longer in the seen set and the event dispatches again.
      mockPubNub._simulateMessage(
        channel,
        { type: 'progress', taskId, progress: 0 },
        'tt-0000',
      );
      expect(cb).toHaveBeenCalledTimes(202);

      // Re-deliver a RECENT timetoken (tt-0200). Still in the seen set, so
      // dedup suppresses it.
      mockPubNub._simulateMessage(
        channel,
        { type: 'progress', taskId, progress: 200 },
        'tt-0200',
      );
      expect(cb).toHaveBeenCalledTimes(202);
    });
  });
});
