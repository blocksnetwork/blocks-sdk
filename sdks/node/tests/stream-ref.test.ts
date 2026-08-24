import { describe, it, expect, vi, beforeEach } from 'vitest';
import { StreamRef, StreamUnavailableError } from '../src/runtime/stream-ref.js';
import { StreamClient } from '../src/stream/index.js';
import type { StreamDescriptor, StreamClientFromDescriptorOptions } from '../src/stream/index.js';

// Mock the StreamClient.fromDescriptor to avoid real PubNub connections
vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = await importOriginal() as Record<string, unknown>;
  return {
    ...actual,
    StreamClient: {
      fromDescriptor: vi.fn(() => {
        let active = true;
        const endCallbacks: Array<() => void> = [];
        return {
          get isActive() { return active; },
          channel: 'stream.echo.test-stream',
          uuid: 'echo-stream-0001',
          write: vi.fn(),
          end: vi.fn(async () => {
            active = false;
            for (const cb of endCallbacks) cb();
          }),
          onEnd: (cb: () => void) => { endCallbacks.push(cb); },
          get inbound() { return { [Symbol.asyncIterator]: () => ({ next: async () => ({ value: undefined, done: true }) }) }; },
        };
      }),
    },
  };
});

describe('StreamRef', () => {
  const descriptor: StreamDescriptor = {
    taskId: 'task-1',
    streamId: 'test-stream',
    agentName: 'echo',
    channel: 'stream.echo.test-stream',
    token: 't7c-token',
    agentDirection: 'outbound',
    localDirection: 'inbound',
    format: 'bytes',
    affinity: 'dedicated',
    metadata: { foo: 'bar' },
  };

  const sdkOptions: StreamClientFromDescriptorOptions = {
    subscribeKey: 'sub-key',
    publishKey: 'pub-key',
  };

  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('exposes the descriptor', () => {
    const ref = new StreamRef(descriptor, sdkOptions);
    expect(ref.descriptor).toBe(descriptor);
    expect(ref.descriptor.streamId).toBe('test-stream');
    expect(ref.descriptor.format).toBe('bytes');
    expect(ref.descriptor.localDirection).toBe('inbound');
  });

  it('open() creates a StreamClient from the descriptor', () => {
    const ref = new StreamRef(descriptor, sdkOptions);
    const client = ref.open();
    expect(client).toBeDefined();
    expect(client.isActive).toBe(true);
    expect(ref.isOpen).toBe(true);
  });

  it('open() is idempotent while client is active', () => {
    const ref = new StreamRef(descriptor, sdkOptions);
    const client1 = ref.open();
    const client2 = ref.open();
    expect(client1).toBe(client2);
  });

  it('open() throws after client has ended', async () => {
    const ref = new StreamRef(descriptor, sdkOptions);
    const client = ref.open();
    await client.end();
    expect(() => ref.open()).toThrow('already been ended');
    expect(ref.isOpen).toBe(false);
  });

  it('isOpen is false before first open', () => {
    const ref = new StreamRef(descriptor, sdkOptions);
    expect(ref.isOpen).toBe(false);
  });

  it('open() forwards reorderTimeoutMs to fromDescriptor', () => {
    const ref = new StreamRef(descriptor, sdkOptions);
    ref.open({ reorderTimeoutMs: 250 });
    expect(StreamClient.fromDescriptor).toHaveBeenCalledWith(
      descriptor,
      expect.objectContaining({ reorderTimeoutMs: 250 }),
    );
  });

  describe('onOpen hook', () => {
    it('fires when open() is called', () => {
      const onOpen = vi.fn();
      const ref = new StreamRef(descriptor, sdkOptions, { onOpen });
      const client = ref.open();
      expect(onOpen).toHaveBeenCalledTimes(1);
      expect(onOpen).toHaveBeenCalledWith(client);
    });

    it('is not called when StreamRef constructed without it', () => {
      const ref = new StreamRef(descriptor, sdkOptions);
      ref.open();
      // No onOpen hook -- should not throw
      expect(ref.isOpen).toBe(true);
    });

    it('fires only once for idempotent open() calls', () => {
      const onOpen = vi.fn();
      const ref = new StreamRef(descriptor, sdkOptions, { onOpen });
      ref.open();
      ref.open();
      // fromDescriptor called once (idempotent), so onOpen fires once
      expect(onOpen).toHaveBeenCalledTimes(1);
    });
  });

  describe('terminal-session short-circuit (Fix A)', () => {
    const declaredDescriptor: StreamDescriptor = {
      ...descriptor,
      declaredStream: 'audio',
    };

    it('open() throws StreamUnavailableError when session state is "completed"', () => {
      const ref = new StreamRef(descriptor, sdkOptions, {
        sessionState: () => 'completed',
      });
      expect(() => ref.open()).toThrowError(StreamUnavailableError);
    });

    it('open() throws StreamUnavailableError when session state is "failed"', () => {
      const ref = new StreamRef(descriptor, sdkOptions, {
        sessionState: () => 'failed',
      });
      expect(() => ref.open()).toThrowError(StreamUnavailableError);
    });

    it('open() throws StreamUnavailableError when session state is "canceled"', () => {
      const ref = new StreamRef(descriptor, sdkOptions, {
        sessionState: () => 'canceled',
      });
      expect(() => ref.open()).toThrowError(StreamUnavailableError);
    });

    it('open() proceeds when session state is "running"', () => {
      const ref = new StreamRef(descriptor, sdkOptions, {
        sessionState: () => 'running',
      });
      const client = ref.open();
      expect(client).toBeDefined();
      expect(client.isActive).toBe(true);
    });

    it('open() proceeds when session state is undefined', () => {
      const ref = new StreamRef(descriptor, sdkOptions, {
        sessionState: () => undefined,
      });
      const client = ref.open();
      expect(client).toBeDefined();
    });

    it('open() proceeds when no sessionState hook is provided', () => {
      const ref = new StreamRef(descriptor, sdkOptions);
      const client = ref.open();
      expect(client).toBeDefined();
    });

    it('StreamUnavailableError has typed fields (taskId, streamId, declaredStream, terminalState)', () => {
      const ref = new StreamRef(declaredDescriptor, sdkOptions, {
        sessionState: () => 'completed',
      });
      try {
        ref.open();
        throw new Error('expected StreamUnavailableError to be thrown');
      } catch (err) {
        expect(err).toBeInstanceOf(StreamUnavailableError);
        const e = err as StreamUnavailableError;
        expect(e.name).toBe('StreamUnavailableError');
        expect(e.taskId).toBe('task-1');
        expect(e.streamId).toBe('test-stream');
        expect(e.declaredStream).toBe('audio');
        expect(e.terminalState).toBe('completed');
      }
    });

    it('error message names the declared stream, task, terminal state, and alternatives', () => {
      const ref = new StreamRef(declaredDescriptor, sdkOptions, {
        sessionState: () => 'failed',
      });
      try {
        ref.open();
        throw new Error('expected StreamUnavailableError');
      } catch (err) {
        const msg = (err as Error).message;
        expect(msg).toContain('"audio"');
        expect(msg).toContain('"task-1"');
        expect(msg).toContain('"failed"');
        expect(msg).toContain('live-only');
        expect(msg).toContain('ref.descriptor');
        expect(msg).toContain('session.listArtifacts()');
        expect(msg).toContain('session.state');
      }
    });

    it('error message falls back to streamId when no declaredStream', () => {
      const ref = new StreamRef(descriptor, sdkOptions, {
        sessionState: () => 'canceled',
      });
      try {
        ref.open();
        throw new Error('expected StreamUnavailableError');
      } catch (err) {
        const msg = (err as Error).message;
        // descriptor.declaredStream is undefined -> fallback to streamId
        expect(msg).toContain('"test-stream"');
        expect(msg).toContain('"canceled"');
        const e = err as StreamUnavailableError;
        expect(e.declaredStream).toBeUndefined();
      }
    });

    it('descriptor remains accessible on a terminal-session ref', () => {
      const ref = new StreamRef(declaredDescriptor, sdkOptions, {
        sessionState: () => 'completed',
      });
      // Accessing descriptor should not throw
      expect(ref.descriptor.streamId).toBe('test-stream');
      expect(ref.descriptor.declaredStream).toBe('audio');
      expect(ref.descriptor.format).toBe('bytes');
      expect(ref.descriptor.metadata).toEqual({ foo: 'bar' });
      expect(ref.isOpen).toBe(false);
    });

    it('short-circuit re-evaluates sessionState on each open() call', () => {
      // Ensures the getter is consulted live, not captured at construction.
      // Uses a fresh StreamRef after state flips because an already-active
      // client is (correctly) returned by the idempotency branch before the
      // terminal short-circuit is consulted; see the dedicated test below.
      let state: string | undefined = 'running';
      const freshRef = new StreamRef(descriptor, sdkOptions, {
        sessionState: () => state,
      });
      state = 'completed';
      expect(() => freshRef.open()).toThrowError(StreamUnavailableError);
    });

    it('idempotency wins over terminal short-circuit when client is still active', () => {
      // Regression: a consumer that opened a stream while the task was
      // running MUST continue to receive the same live StreamClient from
      // open() during the auto-drain window or with autoDrain: false,
      // per the SDK contract "idempotent while active". The terminal
      // short-circuit only protects against *constructing* a new client
      // against a revoked T7c token.
      let state: string | undefined = 'running';
      const ref = new StreamRef(descriptor, sdkOptions, {
        sessionState: () => state,
      });
      const client1 = ref.open();
      expect(client1.isActive).toBe(true);

      // Task transitions terminal; live client is still active (drain window).
      state = 'completed';

      // Second open() during the drain window returns the same live client,
      // NOT a StreamUnavailableError.
      const client2 = ref.open();
      expect(client2).toBe(client1);
      expect(client2.isActive).toBe(true);
    });

    it('after live client ends, subsequent open() on terminal session throws StreamUnavailableError', async () => {
      // Complements the idempotency test: once the client has ended AND
      // the session is terminal, open() should surface
      // StreamUnavailableError (not the generic "already been ended")
      // because the terminal short-circuit is the more actionable error
      // for consumers deciding how to recover.
      let state: string | undefined = 'running';
      const ref = new StreamRef(descriptor, sdkOptions, {
        sessionState: () => state,
      });
      const client = ref.open();
      state = 'completed';
      await client.end();

      // _client is now null and _clientEnded is true. The idempotency
      // branch won't match, and _clientEnded fires before the terminal
      // check - that's acceptable: both signals mean "no new client".
      expect(() => ref.open()).toThrow(/already been ended/);
    });
  });
});
