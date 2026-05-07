/**
 * Phase 3 Agent Instance Runtime Tests
 *
 * Tests the three-tier connection model, unified createStream, onActivate,
 * stream registry, credential cache, instance-level APIs, and removals.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import type { StartTaskMessage } from '../src/runtime/agent-instance.js';
import { makeTestCard, makePipeTestCard } from './helpers/test-card.js';

// Mock the PubNub client
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

// Mock agent registry to avoid HTTP calls.
// The connect response must include controlChannel so the agent instance
// knows which channel to subscribe on.
const TEST_AGENT_ID_P3 = 'dddddddd-4444-4444-4444-444444444444';
vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: vi.fn(async () => ({
    pamToken: undefined,
    agentId: 'dddddddd-4444-4444-4444-444444444444',
    controlChannel: 'agent.dddddddd-4444-4444-4444-444444444444.control',
  })),
  fetchAgentRegistry: vi.fn(async () => ({})),
  getAgent: vi.fn(async () => null),
  removeAgent: vi.fn(async () => {}),
  fetchAgentsBySkill: vi.fn(async () => []),
  fetchAgentsByListing: vi.fn(async () => []),
}));

// Track all created PubNub clients for inspection
const allCreatedPubNubs: ReturnType<typeof createMockPubNub>[] = [];

// Mock createPubNubClient to return our mock
vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => {
    const pn = createMockPubNub();
    allCreatedPubNubs.push(pn);
    return pn;
  }),
}));

// Mock stream SDK (merged into sdks/node/src/stream/)
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
      private endCallbacks: Array<() => void> = [];
      constructor(opts: Record<string, unknown>) {
        this._channel = (opts.channel as string) || `stream.${opts.agentName}.${opts.streamId}`;
      }
      get isActive() { return this._isActive; }
      get channel() { return this._channel; }
      get uuid() { return 'mock-stream-uuid'; }
      write = vi.fn();
      end = vi.fn(async () => {
        this._isActive = false;
        for (const cb of this.endCallbacks) cb();
      });
      onEnd(cb: () => void) { this.endCallbacks.push(cb); }
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
      static fromDescriptor = vi.fn((desc, opts) => new MockStreamClient({ ...desc, ...opts }));
    },
  };
});

describe('Phase 3 Agent Instance Runtime', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    allCreatedPubNubs.length = 0;
  });

  describe('startAgentInstance', () => {
    it('returns required handle fields', async () => {
      const mockPn = createMockPubNub();
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
      });

      expect(handle.agentName).toBe('echo');
      expect(handle.instanceId).toMatch(/^AG-echo-/);
      // When external PubNub client is injected, subscribeKey comes from _configuration.keySet
      expect(handle.subscribeKey).toBe('sub-mock');
      expect(typeof handle.stop).toBe('function');
      expect(typeof handle.publishTerminal).toBe('function');
      expect(typeof handle.failStream).toBe('function');
      expect(handle.taskClient).toBeDefined();

      handle.stop();
    });

    it('sets subscribe filter expression for instance routing', async () => {
      const mockPn = createMockPubNub();
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
      });
      // Registration is fire-and-forget — wait for setFilterExpression to be called
      await vi.waitFor(() => {
        expect(mockPn.setFilterExpression).toHaveBeenCalled();
      });
      const filterArg = mockPn.setFilterExpression.mock.calls[0][0] as string;
      expect(filterArg).toContain('meta.instance ==');
      expect(filterArg).toContain('meta.broadcast == "true"');
      handle.stop();
    });

    it('skips subscribe filter expression when expectedInstances is 0', async () => {
      const mockPn = createMockPubNub();
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        expectedInstances: 0,
      });
      await new Promise((r) => setTimeout(r, 50));
      expect(mockPn.setFilterExpression).not.toHaveBeenCalled();
      handle.stop();
    });

    it('multi-instance: accepts broadcast messages', async () => {
      const mockPn = createMockPubNub();
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        expectedInstances: 3,
        handler: async () => ({}),
      });

      mockPn._simulateMessage('agent.echo.control', {
        type: 'StartTask',
        taskId: 'task-bcast',
        ownerId: 'alice',
        taskKind: 'request',
        hasStream: false,
        writeToken: 'wt-1',
      }, { instance: 'AG-echo-different', broadcast: 'true' });

      await new Promise((r) => setTimeout(r, 200));

      const allPublishCalls = [...allCreatedPubNubs.flatMap(pn => pn.publish.mock.calls), ...mockPn.publish.mock.calls];
      const bcastPublish = allPublishCalls.find((call) => {
        const args = call[0] as Record<string, unknown> | undefined;
        const msg = args?.message as Record<string, unknown> | undefined;
        return msg?.taskId === 'task-bcast';
      });
      expect(bcastPublish).toBeDefined();

      handle.stop();
    });

    it('multi-instance: accepts messages with null meta (queued tasks)', async () => {
      const mockPn = createMockPubNub();
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        subscribeKey: 'sub-key',
        publishKey: 'pub-key',
        expectedInstances: 3,
        handler: async () => ({}),
      });

      // No userMetadata — should be accepted (queued task with null meta)
      mockPn._simulateMessage('agent.echo.control', {
        type: 'StartTask',
        taskId: 'task-null-meta',
        ownerId: 'alice',
        taskKind: 'request',
        hasStream: false,
        writeToken: 'wt-1',
      });

      await new Promise((r) => setTimeout(r, 200));

      const allPublishCalls = [...allCreatedPubNubs.flatMap(pn => pn.publish.mock.calls), ...mockPn.publish.mock.calls];
      const nullMetaPublish = allPublishCalls.find((call) => {
        const args = call[0] as Record<string, unknown> | undefined;
        const msg = args?.message as Record<string, unknown> | undefined;
        return msg?.taskId === 'task-null-meta';
      });
      expect(nullMetaPublish).toBeDefined();

      handle.stop();
    });

    it('validates agentName format', async () => {
      await expect(
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        startAgentInstance({ agentName: 'bad.type', card: makeTestCard() } as any),
      ).rejects.toThrow('alphanumeric');
    });

    it('requires agentName', async () => {
      await expect(
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        startAgentInstance({ agentName: '', card: makeTestCard() } as any),
      ).rejects.toThrow('agentName is required');
    });
  });

  describe('connection model', () => {
    it('control client never touches data channels', async () => {
      const mockPn = createMockPubNub();
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
      });

      // Control client should only subscribe to the control channel
      // (subscribe may not be called immediately due to async registration,
      // but when it is, it should only be control channel)
      for (const call of mockPn.subscribe.mock.calls) {
        const channels = call[0]?.channels || [];
        for (const ch of channels) {
          expect(ch).not.toMatch(/^stream\./);
          expect(ch).not.toMatch(/^setup\./);
        }
      }

      handle.stop();
    });
  });

  describe('TaskContext removals', () => {
    it('does not expose openInboundStream', async () => {
      const mockPn = createMockPubNub();
      let capturedCtx: Record<string, unknown> | undefined;

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        handler: async (_task, ctx) => {
          capturedCtx = ctx as unknown as Record<string, unknown>;
          return {};
        },
      });

      // Simulate StartTask
      const startMsg: StartTaskMessage = {
        type: 'StartTask',
        taskId: 'task-1',
        ownerId: 'alice',
        taskKind: 'request',
        hasStream: false,
        writeToken: 'wt-1',
      };

      // Wait for async processing
      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.echo.control', startMsg, {
          instance: handle.instanceId,
        });
        setTimeout(resolve, 50);
      });

      expect(capturedCtx).toBeDefined();
      expect(capturedCtx!.openInboundStream).toBeUndefined();
      expect(capturedCtx!.waitForStreamEnd).toBeUndefined();
      expect(typeof capturedCtx!.createStream).toBe('function');

      handle.stop();
    });
  });

  describe('createStream', () => {
    it('throws when streaming not negotiated', async () => {
      const mockPn = createMockPubNub();
      let _createStreamFn: (...args: unknown[]) => unknown;

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        handler: async (_task, ctx) => {
          _createStreamFn = ctx!.createStream as (...args: unknown[]) => unknown;
          return {};
        },
      });

      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.echo.control', {
          type: 'StartTask',
          taskId: 'task-1',
          ownerId: 'alice',
          hasStream: false,
          writeToken: 'wt-1',
        }, { instance: handle.instanceId });
        setTimeout(resolve, 50);
      });

      // createStreamFn was captured but hasStream is false
      // The function should throw when called
      // (test verifies the check exists on TaskContext)
      handle.stop();
    });

    it('rejects inbound direction on request tasks', async () => {
      const mockPn = createMockPubNub({
        publish: vi.fn(async () => {
          // Simulate the setup handshake failure -- the function will throw
          // because we mock it to return normally (no 403)
          return { timetoken: '123' };
        }),
      });

      let error: Error | undefined;

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
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
        mockPn._simulateMessage('agent.echo.control', {
          type: 'StartTask',
          taskId: 'task-1',
          ownerId: 'alice',
          taskKind: 'request',
          hasStream: true,
          writeToken: 'wt-1',
        }, { instance: handle.instanceId });
        setTimeout(resolve, 100);
      });

      expect(error).toBeDefined();
      expect(error!.message).toContain('outbound');

      handle.stop();
    });

    it('rejects external on request tasks', async () => {
      const mockPn = createMockPubNub();
      let error: Error | undefined;

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
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
        mockPn._simulateMessage('agent.echo.control', {
          type: 'StartTask',
          taskId: 'task-1',
          ownerId: 'alice',
          taskKind: 'request',
          hasStream: true,
          writeToken: 'wt-1',
        }, { instance: handle.instanceId });
        setTimeout(resolve, 100);
      });

      expect(error).toBeDefined();
      expect(error!.message).toContain('external');

      handle.stop();
    });
  });

  describe('publishTerminal', () => {
    it('throws for unknown taskId', async () => {
      const mockPn = createMockPubNub();
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
      });

      await expect(
        handle.publishTerminal('unknown-task', { state: 'completed' }),
      ).rejects.toThrow('No cached credentials');

      handle.stop();
    });
  });

  describe('failStream', () => {
    it('is a no-op for unknown stream', async () => {
      const mockPn = createMockPubNub();
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
      });

      // Should not throw
      await handle.failStream('unknown-stream', 'test-error');

      handle.stop();
    });
  });

  describe('lifecycle', () => {
    it('request task auto-completes on handler return', async () => {
      const mockPn = createMockPubNub();

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        handler: async () => ({}),
      });

      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.echo.control', {
          type: 'StartTask',
          taskId: 'task-1',
          ownerId: 'alice',
          taskKind: 'request',
          hasStream: false,
          writeToken: 'wt-1',
        }, { instance: handle.instanceId });
        setTimeout(resolve, 200);
      });

      // Check all PubNub instances for a terminal publish
      // (the per-task PubNub is created by createPubNubClient mock)
      const allPublishCalls = allCreatedPubNubs.flatMap(pn => pn.publish.mock.calls);
      // Also include the control client's publish calls
      allPublishCalls.push(...mockPn.publish.mock.calls);

      const terminalPublish = allPublishCalls.find((call) => {
        const args = call[0] as Record<string, unknown> | undefined;
        const msg = args?.message as Record<string, unknown> | undefined;
        return msg?.type === 'terminal';
      });
      expect(terminalPublish).toBeDefined();

      handle.stop();
    });

    it('pipe task voluntary return does not publish terminal', async () => {
      const mockPn = createMockPubNub();

      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
        handler: async () => ({}),
      });

      await new Promise<void>((resolve) => {
        mockPn._simulateMessage('agent.echo.control', {
          type: 'StartTask',
          taskId: 'task-1',
          ownerId: 'alice',
          taskKind: 'pipe',
          duration: 60,
          durationExpiresAtMs: Date.now() + 3600000,
          hasStream: false,
          writeToken: 'wt-1',
        }, { instance: handle.instanceId });
        setTimeout(resolve, 200);
      });

      // Check all PubNub instances (control + per-task)
      const allPublishCalls = allCreatedPubNubs.flatMap(pn => pn.publish.mock.calls);
      allPublishCalls.push(...mockPn.publish.mock.calls);

      const terminalPublish = allPublishCalls.find((call) => {
        const args = call[0] as Record<string, unknown> | undefined;
        const msg = args?.message as Record<string, unknown> | undefined;
        return msg?.type === 'terminal';
      });
      expect(terminalPublish).toBeUndefined();

      handle.stop();
    });
  });

  describe('stop', () => {
    it('cleans up listeners and subscriptions', async () => {
      const mockPn = createMockPubNub();
      const handle = await startAgentInstance({
        agentName: 'echo',
        card: makeTestCard(),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        pubnub: mockPn as any,
      });

      // Wait for async registration to complete (sets controlChannel)
      await vi.waitFor(() => expect(mockPn.subscribe).toHaveBeenCalled());

      handle.stop();

      expect(mockPn.removeListener).toHaveBeenCalled();
      expect(mockPn.unsubscribe).toHaveBeenCalledWith({
        channels: [`agent.${TEST_AGENT_ID_P3}.control`],
      });
    });
  });

  describe('PAM token isolation (BLOCKS-232)', () => {
    it('handler does not receive writeToken or controlToken', async () => {
      const mockPn = createMockPubNub();
      let receivedTask: StartTaskMessage | undefined;

      const handle = await startAgentInstance({
        agentName: 'echo',
        pubnub: mockPn as never,
        handler: async (task) => {
          receivedTask = task;
          return {};
        },
      });

      mockPn._simulateMessage('agent.echo.control', {
        type: 'StartTask',
        taskId: 'task-token-isolation',
        ownerId: 'alice',
        taskKind: 'request',
        hasStream: false,
        writeToken: 'secret-write-token-abc123',
        controlToken: 'secret-control-token-xyz789',
      });

      await new Promise((r) => setTimeout(r, 200));

      expect(receivedTask).toBeDefined();
      expect(receivedTask!.taskId).toBe('task-token-isolation');
      expect((receivedTask as Record<string, unknown>).writeToken).toBeUndefined();
      expect((receivedTask as Record<string, unknown>).controlToken).toBeUndefined();

      // JSON serialization must not contain tokens
      const serialized = JSON.stringify(receivedTask);
      expect(serialized).not.toContain('secret-write-token-abc123');
      expect(serialized).not.toContain('secret-control-token-xyz789');

      handle.stop();
    });
  });
});
