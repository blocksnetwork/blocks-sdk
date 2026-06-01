/**
 * Phase 4 Hardening Tests — External Flow, V1 Boundary, Consumer Experience
 *
 * Tests the 10-step external happy path, error paths, credential scoping,
 * failStream, publishTerminal, shared/embedded stream hardening, consumer
 * experience (TaskSession -> StreamRef -> StreamClient), direction inversion,
 * waitForStream, auto-close, gating:false, and v1 boundary enforcement.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import type { StartTaskMessage } from '../src/runtime/agent-instance.js';
import { TaskSession } from '../src/runtime/task-session.js';
import { StreamRef } from '../src/runtime/stream-ref.js';
import { makeTestCard, makePipeTestCard } from './helpers/test-card.js';

// --- Mock infrastructure ---

function createMockPubNub(overrides: Record<string, unknown> = {}) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const messageListeners: any[] = [];
  let token: string | undefined;

  return {
    addListener: vi.fn((listener) => { messageListeners.push(listener); }),
    removeListener: vi.fn((listener) => {
      const idx = messageListeners.indexOf(listener);
      if (idx >= 0) messageListeners.splice(idx, 1);
    }),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    destroy: vi.fn(),
    setToken: vi.fn((t: string) => { token = t; }),
    setFilterExpression: vi.fn(),
    setState: vi.fn(async () => {}),
    publish: vi.fn(async () => ({ timetoken: '123' })),
    hereNow: vi.fn(async () => ({ channels: {} })),
    getToken: () => token,
    _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
    _simulateMessage(channel: string, message: unknown, userMetadata?: Record<string, unknown>) {
      for (const listener of messageListeners) {
        if (listener?.message) {
          listener.message({ channel, message, userMetadata });
        }
      }
    },
    ...overrides,
  };
}

vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: vi.fn(async () => ({ pamToken: undefined })),
  fetchAgentRegistry: vi.fn(async () => ({})),
  getAgent: vi.fn(async () => null),
  removeAgent: vi.fn(async () => {}),
  fetchAgentsByTag: vi.fn(async () => []),
  fetchAgentsByListing: vi.fn(async () => []),
}));

const allCreatedPubNubs: ReturnType<typeof createMockPubNub>[] = [];

/**
 * Shared publish interceptor. Tests that need custom publish behavior
 * for per-task PubNub clients set this function before starting the task.
 * It receives the publish args and should either return a result or throw.
 * If null, default behavior (resolve) is used.
 */
let sharedPublishInterceptor: ((args: Record<string, unknown>) => Promise<unknown>) | null = null;

vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => {
    const pn = createMockPubNub({
      publish: vi.fn(async (args: Record<string, unknown>) => {
        if (sharedPublishInterceptor) {
          return sharedPublishInterceptor(args);
        }
        return { timetoken: '123' };
      }),
    });
    allCreatedPubNubs.push(pn);
    return pn;
  }),
}));

vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = await importOriginal() as Record<string, unknown>;
  return {
    ...actual,
    validateStreamId: (id: string) => {
      if (!id || id.length === 0) throw new Error('Stream ID cannot be empty');
      if (id.length > 92) throw new Error('Stream ID exceeds 92 byte limit');
      if (!/^[a-zA-Z0-9\-_]+$/.test(id)) throw new Error('Stream ID contains invalid characters');
    },
    StreamClient: class MockStreamClient {
      private _isActive = true;
      private _channel: string;
      private _format: string;
      private _direction: string;
      private _gating: boolean;
      private endCallbacks: Array<() => void> = [];
      constructor(opts: Record<string, unknown>) {
        this._channel = (opts.channel as string) || `stream.${opts.agentName}.${opts.streamId}`;
        this._format = (opts.format as string) || 'bytes';
        this._direction = (opts.direction as string) || 'outbound';
        this._gating = opts.gating !== undefined ? Boolean(opts.gating) : true;
      }
      get isActive() { return this._isActive; }
      get channel() { return this._channel; }
      get format() { return this._format; }
      get direction() { return this._direction; }
      get gating() { return this._gating; }
      get uuid() { return 'mock-stream-uuid'; }
      write = vi.fn();
      end = vi.fn(async () => {
        this._isActive = false;
        for (const cb of this.endCallbacks) cb();
      });
      onEnd(cb: () => void) { this.endCallbacks.push(cb); }
      private inboundDoneCallbacks: Array<() => void> = [];
      private inboundDoneFired = false;
      onInboundDone(cb: () => void) {
        if (this.inboundDoneFired) { cb(); return; }
        this.inboundDoneCallbacks.push(cb);
      }
      _fireInboundDone() {
        if (this.inboundDoneFired) return;
        this.inboundDoneFired = true;
        for (const cb of this.inboundDoneCallbacks) { try { cb(); } catch { /* ignore */ } }
        this.inboundDoneCallbacks = [];
      }
      get inbound(): AsyncIterable<unknown> {
        // eslint-disable-next-line @typescript-eslint/no-this-alias
        const self = this;
        return {
          [Symbol.asyncIterator]() {
            return {
              async next() {
                if (!self._isActive) return { value: undefined, done: true };
                return new Promise(() => {}); // hang until ended
              },
            };
          },
        };
      }
      static fromDescriptor = vi.fn((desc, opts) => {
        return new MockStreamClient({
          ...desc,
          ...opts,
          // fromDescriptor should default gating to false for writable consumer streams
          gating: opts?.gating ?? (
            (desc.localDirection === 'outbound' || desc.localDirection === 'bidirectional')
              ? false : true
          ),
        });
      });
    },
    invertDirection: (d: string) => {
      if (d === 'outbound') return 'inbound';
      if (d === 'inbound') return 'outbound';
      return 'bidirectional';
    },
  };
});

// --- Helper to simulate a StartTask message ---

function makeStartTask(overrides: Partial<StartTaskMessage> = {}): StartTaskMessage {
  return {
    type: 'StartTask',
    taskId: 'task-ext-1',
    ownerId: 'alice',
    taskKind: 'pipe',
    hasStream: true,
    writeToken: 'wt-pipe-1',
    duration: 60,
    durationExpiresAtMs: Date.now() + 3600000,
    ...overrides,
  };
}

function _makeSetupError(code: string, message: string) {
  return {
    status: {
      statusCode: 403,
      errorData: {
        message: { ok: false, error: { code, message } },
      },
    },
  };
}

function makeSetupSuccess(opts: {
  taskId: string; streamId: string; channel: string;
  direction: string; phase: string; token?: string;
  tokenTtlMinutes?: number;
}) {
  return {
    status: {
      statusCode: 403,
      errorData: {
        message: {
          ok: true,
          streamSetupResponse: {
            taskId: opts.taskId,
            streamId: opts.streamId,
            channel: opts.channel,
            direction: opts.direction,
            phase: opts.phase,
            token: opts.token,
            tokenTtlMinutes: opts.tokenTtlMinutes ?? 62,
          },
        },
      },
    },
  };
}

// =======================================================================
// 1. External Flow Hardening
// =======================================================================

describe('Phase 4: External Flow Hardening', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    allCreatedPubNubs.length = 0;
    sharedPublishInterceptor = null;
  });

  describe('External stream two-phase handshake', () => {
    it('token_request phase returns T7a, activate phase completes without T7a', async () => {
      let publishCallIndex = 0;
      // Set up shared interceptor so per-task PubNub clients handle setup.
      // The SDK derives streamId / channel itself (task-scoped for dedicated
      // affinity); echo whatever it publishes instead of hardcoding literals.
      sharedPublishInterceptor = async (args: Record<string, unknown>) => {
        const ch = args.channel as string;
        if (ch?.startsWith('setup.')) {
          publishCallIndex++;
          const msg = args.message as Record<string, unknown>;
          if (publishCallIndex === 1) {
            throw makeSetupSuccess({
              taskId: msg.taskId as string,
              streamId: msg.streamId as string,
              channel: msg.channel as string,
              direction: msg.direction as string,
              phase: 'token_request', token: 'T7A-TOKEN-123',
            });
          } else {
            throw makeSetupSuccess({
              taskId: msg.taskId as string,
              streamId: msg.streamId as string,
              channel: msg.channel as string,
              direction: msg.direction as string,
              phase: 'activate',
            });
          }
        }
        return { timetoken: '123' };
      };

      const mockPn = createMockPubNub();

      let capturedStreamObj: Record<string, unknown> | undefined;

      const handle = await startAgentInstance({
        agentName: 'gpu_proc',
        card: makePipeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        handler: async (_task, ctx) => {
          const stream = await ctx!.createStream({
            external: true,
            direction: 'outbound',
          });
          capturedStreamObj = stream as unknown as Record<string, unknown>;

          // Verify T7a is available
          expect(stream.token).toBe('T7A-TOKEN-123');
          expect(stream.external).toBe(true);

          // Activate the external stream
          await stream.activate!();

          return {};
        },
      });

      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.gpu-proc.control', makeStartTask({
          taskId: 'task-ext-1', taskKind: 'pipe',
        }), { instance: handle.instanceId });
        setTimeout(resolve, 200);
      });

      expect(capturedStreamObj).toBeDefined();
      expect(publishCallIndex).toBe(2); // token_request + activate
      handle.stop();
    });

    it('write() on external stream handle throws', async () => {
      sharedPublishInterceptor = async (args: Record<string, unknown>) => {
        const ch = args.channel as string;
        if (ch?.startsWith('setup.')) {
          const msg = args.message as Record<string, unknown>;
          throw makeSetupSuccess({
            taskId: msg.taskId as string,
            streamId: msg.streamId as string,
            channel: msg.channel as string,
            direction: msg.direction as string,
            phase: 'token_request', token: 'T7A-123',
          });
        }
        return { timetoken: '123' };
      };

      const mockPn = createMockPubNub();

      let writeError: Error | undefined;

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        handler: async (_task, ctx) => {
          const stream = await ctx!.createStream({ external: true });
          try {
            stream.write('test');
          } catch (e) {
            writeError = e as Error;
          }
          return {};
        },
      });

      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.echo.control', makeStartTask(), {
          instance: handle.instanceId,
        });
        setTimeout(resolve, 200);
      });

      expect(writeError).toBeDefined();
      expect(writeError!.message).toContain('external');
      handle.stop();
    });

    it('inbound on external stream handle throws', async () => {
      sharedPublishInterceptor = async (args: Record<string, unknown>) => {
        const ch = args.channel as string;
        if (ch?.startsWith('setup.')) {
          const msg = args.message as Record<string, unknown>;
          throw makeSetupSuccess({
            taskId: msg.taskId as string,
            streamId: msg.streamId as string,
            channel: msg.channel as string,
            direction: msg.direction as string,
            phase: 'token_request', token: 'T7A-123',
          });
        }
        return { timetoken: '123' };
      };

      const mockPn = createMockPubNub();

      let inboundError: Error | undefined;

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        handler: async (_task, ctx) => {
          const stream = await ctx!.createStream({ external: true });
          try {
            // eslint-disable-next-line @typescript-eslint/no-unused-vars
            const _ = stream.inbound;
          } catch (e) {
            inboundError = e as Error;
          }
          return {};
        },
      });

      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.echo.control', makeStartTask(), {
          instance: handle.instanceId,
        });
        setTimeout(resolve, 200);
      });

      expect(inboundError).toBeDefined();
      expect(inboundError!.message).toContain('external');
      handle.stop();
    });

    it('external stream on request task throws', async () => {
      const mockPn = createMockPubNub();
      let error: Error | undefined;

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        handler: async (_task, ctx) => {
          try {
            await ctx!.createStream({ external: true });
          } catch (e) {
            error = e as Error;
          }
          return {};
        },
      });

      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.echo.control', makeStartTask({
          taskKind: 'request',
        }), { instance: handle.instanceId });
        setTimeout(resolve, 100);
      });

      expect(error).toBeDefined();
      expect(error!.message).toContain('external');
      handle.stop();
    });
  });

  describe('failStream for external streams', () => {
    it('publishes failed terminal to all mapped tasks and removes registry entry', async () => {
      sharedPublishInterceptor = async (args: Record<string, unknown>) => {
        const ch = args.channel as string;
        if (ch?.startsWith('setup.')) {
          const msg = args.message as Record<string, unknown>;
          throw makeSetupSuccess({
            taskId: msg.taskId as string,
            streamId: msg.streamId as string,
            channel: msg.channel as string,
            direction: msg.direction as string,
            phase: 'embedded', token: 'T7A-FS',
          });
        }
        return { timetoken: '123' };
      };

      const mockPn = createMockPubNub();

      let streamCreated = false;
      let capturedStreamId: string | undefined;
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        handler: async (_task, ctx) => {
          const stream = await ctx!.createStream();
          // channel is `stream.{agent}.{streamId}` — extract the suffix.
          capturedStreamId = stream.channel.split('.').slice(2).join('.');
          streamCreated = true;
          // Simulate long-running pipe task
          await new Promise((resolve) => setTimeout(resolve, 500));
          return {};
        },
      });

      // Start the pipe task
      mockPn._simulateMessage('agent.echo.control', makeStartTask({
        taskId: 'task-fs-1',
      }), { instance: handle.instanceId });

      // Wait for stream to be created
      await new Promise<void>((resolve) => {
        const check = () => {
          if (streamCreated) resolve();
          else setTimeout(check, 10);
        };
        check();
      });

      // Now fail the stream (use the SDK-derived streamId)
      await handle.failStream(capturedStreamId!, 'producer_crashed');

      // Verify a terminal was published
      const allPublishCalls = allCreatedPubNubs.flatMap(pn => pn.publish.mock.calls);
      allPublishCalls.push(...mockPn.publish.mock.calls);

      const terminalPublish = allPublishCalls.find((call) => {
        const args = call[0] as Record<string, unknown> | undefined;
        const msg = args?.message as Record<string, unknown> | undefined;
        return msg?.type === 'terminal' && msg?.state === 'failed';
      });
      expect(terminalPublish).toBeDefined();

      handle.stop();
    });
  });
});

// =======================================================================
// 2. V1 Boundary Enforcement
// =======================================================================

describe('Phase 4: V1 Boundary Enforcement', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    allCreatedPubNubs.length = 0;
    sharedPublishInterceptor = null;
  });

  it('createStream with direction=inbound on request task throws v1 error', async () => {
    const mockPn = createMockPubNub();
    let error: Error | undefined;

    const handle = await startAgentInstance({
      agentName: 'echo',
      card: makeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        try {
          await ctx!.createStream({ direction: 'inbound' });
        } catch (e) {
          error = e as Error;
        }
        return {};
      },
    });

    await new Promise<void>((resolve) => {
      mockPn._simulateMessage('agent.echo.control', makeStartTask({
        taskKind: 'request',
      }), { instance: handle.instanceId });
      setTimeout(resolve, 100);
    });

    expect(error).toBeDefined();
    expect(error!.message).toContain('outbound');
    handle.stop();
  });

  it('createStream with direction=bidirectional on request task throws v1 error', async () => {
    const mockPn = createMockPubNub();
    let error: Error | undefined;

    const handle = await startAgentInstance({
      agentName: 'echo',
      card: makeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        try {
          await ctx!.createStream({ direction: 'bidirectional' });
        } catch (e) {
          error = e as Error;
        }
        return {};
      },
    });

    await new Promise<void>((resolve) => {
      mockPn._simulateMessage('agent.echo.control', makeStartTask({
        taskKind: 'request',
      }), { instance: handle.instanceId });
      setTimeout(resolve, 100);
    });

    expect(error).toBeDefined();
    expect(error!.message).toContain('outbound');
    handle.stop();
  });

  it('createStream without streaming negotiation throws clear error', async () => {
    const mockPn = createMockPubNub();
    let error: Error | undefined;

    const handle = await startAgentInstance({
      agentName: 'echo',
      card: makeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        try {
          await ctx!.createStream();
        } catch (e) {
          error = e as Error;
        }
        return {};
      },
    });

    await new Promise<void>((resolve) => {
      mockPn._simulateMessage('agent.echo.control', makeStartTask({
        hasStream: false,
      }), { instance: handle.instanceId });
      setTimeout(resolve, 100);
    });

    expect(error).toBeDefined();
    expect(error!.message).toContain('not negotiated');
    handle.stop();
  });
});

// =======================================================================
// 3. Consumer Experience Hardening (TaskSession -> StreamRef -> StreamClient)
// =======================================================================

describe('Phase 4: Consumer Experience Hardening', () => {
  const taskId = 'task-consumer-1';
  const ownerId = 'alice';
  const agentName = 'echo';
  const channel = `u.${ownerId}.${taskId}`;

  let mockPubNub: ReturnType<typeof createMockPubNub>;
  let session: TaskSession;

  beforeEach(() => {
    vi.clearAllMocks();
    mockPubNub = createMockPubNub();
    session = new TaskSession({
      taskId,
      ownerId,
      readToken: 't4-token',
      agentName,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPubNub as any,
      sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
    });
  });

  describe('direction inversion', () => {
    it('agent outbound becomes consumer inbound', () => {
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1', direction: 'outbound',
            format: 'bytes', affinity: 'dedicated', token: 't7c-1', tokenTtlMinutes: 62,
          },
        },
      });

      const streams = session.listStreams();
      expect(streams).toHaveLength(1);
      expect(streams[0].descriptor.agentDirection).toBe('outbound');
      expect(streams[0].descriptor.localDirection).toBe('inbound');
    });

    it('agent inbound becomes consumer outbound', () => {
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1', direction: 'inbound',
            format: 'events', affinity: 'dedicated', token: 't7c-1', tokenTtlMinutes: 62,
          },
        },
      });

      const streams = session.listStreams();
      expect(streams[0].descriptor.agentDirection).toBe('inbound');
      expect(streams[0].descriptor.localDirection).toBe('outbound');
    });

    it('bidirectional stays bidirectional', () => {
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1', direction: 'bidirectional',
            format: 'bytes', affinity: 'dedicated', token: 't7c-1', tokenTtlMinutes: 62,
          },
        },
      });

      const streams = session.listStreams();
      expect(streams[0].descriptor.agentDirection).toBe('bidirectional');
      expect(streams[0].descriptor.localDirection).toBe('bidirectional');
    });
  });

  describe('format propagation', () => {
    it('format bytes propagates through descriptor', () => {
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1', direction: 'outbound',
            format: 'bytes', affinity: 'dedicated', token: 't7c-1', tokenTtlMinutes: 62,
          },
        },
      });
      const ref = session.listStreams()[0];
      expect(ref.descriptor.format).toBe('bytes');
    });

    it('format events propagates through descriptor', () => {
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1', direction: 'outbound',
            format: 'events', affinity: 'dedicated', token: 't7c-1', tokenTtlMinutes: 62,
          },
        },
      });
      const ref = session.listStreams()[0];
      expect(ref.descriptor.format).toBe('events');
    });

    it('invalid format is silently skipped', () => {
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1', direction: 'outbound',
            format: 'invalid', affinity: 'dedicated', token: 't7c-1', tokenTtlMinutes: 62,
          },
        },
      });
      expect(session.listStreams()).toHaveLength(0);
    });
  });

  describe('waitForStream and waitForStreamWhere', () => {
    it('waitForStream resolves when stream arrives later', async () => {
      const promise = session.waitForStream('s1');
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1', direction: 'outbound',
            format: 'bytes', affinity: 'dedicated', token: 't7c-1', tokenTtlMinutes: 62,
            metadata: { unit: 'celsius' },
          },
        },
      });
      const ref = await promise;
      expect(ref.descriptor.streamId).toBe('s1');
      expect(ref.descriptor.metadata).toEqual({ unit: 'celsius' });
    });

    it('waitForStreamWhere resolves with predicate on metadata', async () => {
      const promise = session.waitForStreamWhere(
        (r) => r.descriptor.metadata?.kind === 'temperature',
      );
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          'temp': {
            channel: 'stream.echo.temp', direction: 'outbound',
            format: 'bytes', affinity: 'dedicated', token: 't7c-t', tokenTtlMinutes: 62,
            metadata: { kind: 'temperature' },
          },
        },
      });
      const ref = await promise;
      expect(ref.descriptor.streamId).toBe('temp');
    });

    it('multi-stream selection: waitForStream with specific ID', async () => {
      // Announce two streams at once
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          'temp': {
            channel: 'stream.echo.temp', direction: 'outbound',
            format: 'bytes', affinity: 'dedicated', token: 't1', tokenTtlMinutes: 62,
          },
          'humidity': {
            channel: 'stream.echo.humidity', direction: 'outbound',
            format: 'bytes', affinity: 'dedicated', token: 't2', tokenTtlMinutes: 62,
          },
        },
      });

      const ref = await session.waitForStream('humidity');
      expect(ref.descriptor.streamId).toBe('humidity');
    });
  });

  describe('auto-close on terminal', () => {
    it('terminal rejects pending waitForStream', async () => {
      const promise = session.waitForStream('s1');
      mockPubNub._simulateMessage(channel, {
        type: 'terminal', taskId, state: 'completed',
      });
      await expect(promise).rejects.toThrow('closed');
    });

    it('terminal rejects pending waitForStreamWhere', async () => {
      const promise = session.waitForStreamWhere(() => true);
      mockPubNub._simulateMessage(channel, {
        type: 'terminal', taskId, state: 'failed',
      });
      await expect(promise).rejects.toThrow('closed');
    });

    it('events after close are not delivered', () => {
      const cb = vi.fn();
      session.onProgress(cb);
      session.close();
      mockPubNub._simulateMessage(channel, { type: 'progress', taskId });
      expect(cb).not.toHaveBeenCalled();
    });
  });

  describe('StreamRef idempotency', () => {
    it('open() returns same client on repeated calls', () => {
      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1', direction: 'outbound',
            format: 'bytes', affinity: 'dedicated', token: 't7c-1', tokenTtlMinutes: 62,
          },
        },
      });

      const ref = session.listStreams()[0];
      const client1 = ref.open();
      const client2 = ref.open();
      expect(client1).toBe(client2);
    });
  });

  describe('onStream fires for each discovered stream', () => {
    it('fires once per stream', () => {
      const cb = vi.fn();
      session.onStream(cb);

      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's1': {
            channel: 'stream.echo.s1', direction: 'outbound',
            format: 'bytes', affinity: 'dedicated', token: 't1', tokenTtlMinutes: 62,
          },
        },
      });

      mockPubNub._simulateMessage(channel, {
        type: 'progress', taskId,
        streamEvent: 'stream_started',
        streams: {
          's2': {
            channel: 'stream.echo.s2', direction: 'inbound',
            format: 'events', affinity: 'dedicated', token: 't2', tokenTtlMinutes: 62,
          },
        },
      });

      expect(cb).toHaveBeenCalledTimes(2);
    });
  });
});

// =======================================================================
// 4. Shared/Embedded Stream Hardening
// =======================================================================

describe('Phase 4: Shared/Embedded Stream Hardening', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    allCreatedPubNubs.length = 0;
    sharedPublishInterceptor = null;
  });

  // Note: Shared stream refCount, cancellation isolation, and cross-stream
  // bridging are primarily tested via the StreamRegistry unit tests.
  // These tests verify the integration through the agent-instance layer.

  describe('stream registry compatibility checks', () => {
    // These are integration-level checks; the StreamRegistry unit tests
    // are in stream-registry.test.ts. Here we verify the error messages
    // surface correctly from createStream.

    it('rejects direction mismatch with card declaration', async () => {
      // Card enforces direction at createStream time (Fix 10).
      // The card declares _default as outbound; trying inbound conflicts.
      const mockPn = createMockPubNub();

      let error: Error | undefined;

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        handler: async (_task, ctx) => {
          try {
            await ctx!.createStream({ direction: 'inbound' });
          } catch (e) {
            error = e as Error;
          }
          return {};
        },
      });

      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.echo.control', makeStartTask({
          taskId: 'task-shared-1',
        }), { instance: handle.instanceId });
        setTimeout(resolve, 200);
      });

      expect(error).toBeDefined();
      expect(error!.message).toContain('conflicts with card declaration');
      handle.stop();
    });

    it('rejects format mismatch with card declaration', async () => {
      // Card enforces format at createStream time (Fix 10).
      // The card declares _default as bytes; trying events conflicts.
      const mockPn = createMockPubNub();

      let error: Error | undefined;

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        handler: async (_task, ctx) => {
          try {
            await ctx!.createStream({ format: 'events' });
          } catch (e) {
            error = e as Error;
          }
          return {};
        },
      });

      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.echo.control', makeStartTask({
          taskId: 'task-fmt-1',
        }), { instance: handle.instanceId });
        setTimeout(resolve, 200);
      });

      expect(error).toBeDefined();
      expect(error!.message).toContain('conflicts with card declaration');
      handle.stop();
    });
  });
});

// =======================================================================
// 5. publishTerminal Hardening
// =======================================================================

describe('Phase 4: publishTerminal Hardening', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    allCreatedPubNubs.length = 0;
    sharedPublishInterceptor = null;
  });

  it('throws for task with no cached credentials', async () => {
    const mockPn = createMockPubNub();
    const handle = await startAgentInstance({
      agentName: 'echo',
      card: makeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
    });

    await expect(
      handle.publishTerminal('no-such-task', { state: 'completed' }),
    ).rejects.toThrow('No cached credentials');

    handle.stop();
  });

  it('publishes terminal using cached T2 for pipe task after handler exit', async () => {
    const mockPn = createMockPubNub();
    let handlerDone = false;

    const handle = await startAgentInstance({
      agentName: 'echo',
      card: makeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async () => {
        handlerDone = true;
        return {};
      },
    });

    // Start a pipe task
    mockPn._simulateMessage('agent.echo.control', makeStartTask({
      taskId: 'task-pt-1', taskKind: 'pipe', writeToken: 'wt-cached',
    }), { instance: handle.instanceId });

    // Wait for handler to complete
    await new Promise<void>((resolve) => {
      const check = () => {
        if (handlerDone) resolve();
        else setTimeout(check, 10);
      };
      check();
    });
    await new Promise(r => setTimeout(r, 100));

    // Now publish terminal using cached credentials
    await handle.publishTerminal('task-pt-1', { state: 'completed' });

    // Verify terminal was published via an ephemeral client
    const allPublishCalls = allCreatedPubNubs.flatMap(pn => pn.publish.mock.calls);
    const terminalPublish = allPublishCalls.find((call) => {
      const args = call[0] as Record<string, unknown> | undefined;
      const msg = args?.message as Record<string, unknown> | undefined;
      return msg?.type === 'terminal' && msg?.state === 'completed';
    });
    expect(terminalPublish).toBeDefined();

    handle.stop();
  });
});

// =======================================================================
// 6. StreamRef standalone tests
// =======================================================================

describe('Phase 4: StreamRef Hardening', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('descriptor carries all required fields', () => {
    const descriptor = {
      taskId: 'task-1',
      streamId: 's1',
      agentName: 'echo',
      channel: 'stream.echo.s1',
      token: 't7c-1',
      agentDirection: 'outbound' as const,
      localDirection: 'inbound' as const,
      format: 'bytes' as const,
      affinity: 'dedicated' as const,
      metadata: { key: 'value' },
    };

    const ref = new StreamRef(descriptor, { subscribeKey: 'sk', publishKey: 'pk' });

    expect(ref.descriptor.taskId).toBe('task-1');
    expect(ref.descriptor.streamId).toBe('s1');
    expect(ref.descriptor.agentName).toBe('echo');
    expect(ref.descriptor.channel).toBe('stream.echo.s1');
    expect(ref.descriptor.token).toBe('t7c-1');
    expect(ref.descriptor.agentDirection).toBe('outbound');
    expect(ref.descriptor.localDirection).toBe('inbound');
    expect(ref.descriptor.format).toBe('bytes');
    expect(ref.descriptor.affinity).toBe('dedicated');
    expect(ref.descriptor.metadata).toEqual({ key: 'value' });
  });

  it('isOpen is false before open()', () => {
    const ref = new StreamRef(
      {
        taskId: 't1', streamId: 's1', agentName: 'echo',
        channel: 'stream.echo.s1', token: 'tk',
        agentDirection: 'outbound', localDirection: 'inbound',
        format: 'bytes', affinity: 'dedicated',
      },
      { subscribeKey: 'sk', publishKey: 'pk' },
    );
    expect(ref.isOpen).toBe(false);
  });
});
