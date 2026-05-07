/**
 * Tests for SDK Consumer Simplification Phase A (Fixes 1-6).
 *
 * - Fix 1: Auto-populate ownerId from authenticated identity
 * - Fix 2: waitForTerminal on TaskSession
 * - Fix 4: Typed event properties (ProgressEvent, ArtifactEvent, TerminalEvent)
 * - Fix 5: saveArtifacts convenience method
 * - Fix 6: Resource management (Symbol.dispose, asyncClose, Symbol.asyncDispose, close stream cleanup)
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { TaskSession } from '../src/runtime/task-session.js';
import type { ProgressEvent, ArtifactEvent, TerminalEvent } from '../src/runtime/task-session.js';
import { TaskClient } from '../src/runtime/task-client.js';

// ============================================================================
// Mocks
// ============================================================================

// Mock PubNub constructor for TaskClient tests
const sessionPubNubInstances: Array<ReturnType<typeof createFakePubNub>> = [];

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation(() => {
      const fake = createFakePubNub();
      sessionPubNubInstances.push(fake);
      return fake.pubnub;
    }),
  };
});

const mockRpcResponse = (result: unknown) => ({
  ok: true,
  json: async () => ({ jsonrpc: '2.0', id: 'x', result }),
});

function createFakePubNub() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  const pubnub = {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    addListener: (l: any) => listeners.push(l),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setToken: vi.fn(),
    destroy: vi.fn(),
    time: vi.fn(async () => ({ timetoken: '17000000000000000' })),
    fetchMessages: vi.fn(async ({ channels }: { channels: string[] }) => ({
      channels: { [channels[0]]: [] },
    })),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any;
  return { pubnub, listeners };
}

function createMockPubNub() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let messageListener: any = null;
  return {
    addListener: vi.fn((listener) => { messageListener = listener; }),
    removeListener: vi.fn(() => { messageListener = null; }),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    destroy: vi.fn(),
    _simulateMessage(channel: string, message: unknown) {
      if (messageListener?.message) {
        messageListener.message({ channel, message });
      }
    },
  };
}

// Mock StreamClient for resource management tests
function createMockStreamClient() {
  let active = true;
  const inboundDoneCallbacks: Array<() => void> = [];
  let inboundDoneFired = false;

  return {
    get isActive() { return active; },
    channel: 'stream.echo.s1',
    uuid: 'echo-stream-0001',
    write: vi.fn(),
    end: vi.fn(async () => {
      active = false;
      if (!inboundDoneFired) {
        inboundDoneFired = true;
        for (const cb of inboundDoneCallbacks) {
          try { cb(); } catch { /* ignore */ }
        }
        inboundDoneCallbacks.length = 0;
      }
    }),
    onEnd: vi.fn(),
    onInboundDone: vi.fn((cb: () => void) => {
      if (inboundDoneFired) {
        cb();
        return;
      }
      inboundDoneCallbacks.push(cb);
    }),
    inbound: { [Symbol.asyncIterator]: () => ({ next: async () => ({ value: undefined, done: true }) }) },
    _simulateInboundDone() {
      if (inboundDoneFired) return;
      inboundDoneFired = true;
      for (const cb of inboundDoneCallbacks) {
        try { cb(); } catch { /* ignore */ }
      }
      inboundDoneCallbacks.length = 0;
    },
  };
}

let lastMockClients: ReturnType<typeof createMockStreamClient>[] = [];

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

// ============================================================================
// Tests
// ============================================================================

describe('Consumer Simplification Phase A', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
    sessionPubNubInstances.length = 0;
    lastMockClients = [];
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  // ==========================================================================
  // Fix 1: Auto-populate ownerId
  // ==========================================================================

  describe('Fix 1: ownerId auto-populate', () => {
    it('sendMessage uses defaultOwnerId when ownerId not provided', async () => {
      const fullResponse = {
        taskId: 'task-auto',
        idempotent: false,
        extensions: {
          blocks: {
            streamChannels: { status: 'u.default-user.task-auto' },
            readToken: 'T4-read',
          },
        },
      };
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse));

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
        defaultOwnerId: 'default-user',
      });

      const session = await client.sendMessage({
        agentName: 'echo',
        requestParts: [{ partId: 'text', text: 'Hello' }],
        // No ownerId provided
      });

      expect(session).toBeInstanceOf(TaskSession);
      expect(session.ownerId).toBe('default-user');

      // Verify the RPC call included the defaultOwnerId
      const rpcBody = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(rpcBody.params.ownerId).toBe('default-user');

      session.close();
    });

    it('explicit ownerId overrides defaultOwnerId', async () => {
      const fullResponse = {
        taskId: 'task-override',
        idempotent: false,
        extensions: {
          blocks: {
            streamChannels: { status: 'u.explicit.task-override' },
            readToken: 'T4-read',
          },
        },
      };
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse));

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
        defaultOwnerId: 'default-user',
      });

      const session = await client.sendMessage({
        agentName: 'echo',
        requestParts: [{ partId: 'text', text: 'Hello' }],
        ownerId: 'explicit-user',
      });

      expect(session.ownerId).toBe('explicit-user');

      const rpcBody = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(rpcBody.params.ownerId).toBe('explicit-user');

      session.close();
    });

    it('falls back to empty string when no ownerId and no defaultOwnerId', async () => {
      const fullResponse = {
        taskId: 'task-empty',
        idempotent: false,
        extensions: {
          blocks: {
            streamChannels: { status: 'u..task-empty' },
            readToken: 'T4-read',
          },
        },
      };
      fetchSpy.mockResolvedValueOnce(mockRpcResponse(fullResponse));

      const client = new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: 'http://localhost:3001',
      });

      const session = await client.sendMessage({
        agentName: 'echo',
        requestParts: [{ partId: 'text', text: 'Hello' }],
      });

      const rpcBody = JSON.parse(fetchSpy.mock.calls[0][1].body);
      expect(rpcBody.params.ownerId).toBe('');

      session.close();
    });
  });

  // ==========================================================================
  // Fix 2: waitForTerminal
  // ==========================================================================

  describe('Fix 2: waitForTerminal', () => {
    it('resolves immediately for pre-closed terminal sessions', async () => {
      const session = new TaskSession({
        taskId: 'task-done',
        ownerId: 'alice',
        readToken: null,
        agentName: 'echo',
        pubnub: null,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        preClosed: true,
        state: 'completed',
      });

      const event = await session.waitForTerminal();
      expect(event.type).toBe('terminal');
      expect(event.taskId).toBe('task-done');
      expect(event.state).toBe('completed');
    });

    it('resolves immediately for terminal connect() sessions', async () => {
      const mockPn = createMockPubNub();
      const session = new TaskSession({
        taskId: 'task-terminal-connect',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: mockPn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        skipSubscription: true,
        state: 'failed',
      });

      const event = await session.waitForTerminal();
      expect(event.type).toBe('terminal');
      expect(event.state).toBe('failed');

      session.close();
    });

    it('resolves when terminal event arrives', async () => {
      const mockPn = createMockPubNub();
      const session = new TaskSession({
        taskId: 'task-live',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: mockPn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      });

      const channel = `u.alice.task-live`;
      const promise = session.waitForTerminal();

      // Simulate terminal event
      mockPn._simulateMessage(channel, {
        type: 'terminal',
        taskId: 'task-live',
        state: 'completed',
        reason: 'done',
      });

      const event = await promise;
      expect(event.type).toBe('terminal');
      expect(event.state).toBe('completed');
      expect(event.reason).toBe('done');
    });

    it('rejects on timeout', async () => {
      vi.useFakeTimers();
      try {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-timeout',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });

        const promise = session.waitForTerminal(5000);

        vi.advanceTimersByTime(5000);

        await expect(promise).rejects.toThrow('waitForTerminal timed out after 5000ms');

        session.close();
      } finally {
        vi.useRealTimers();
      }
    });

    it('resolves immediately for all terminal states', async () => {
      for (const state of ['completed', 'failed', 'canceled']) {
        const session = new TaskSession({
          taskId: `task-${state}`,
          ownerId: 'alice',
          readToken: null,
          agentName: 'echo',
          pubnub: null,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
          preClosed: true,
          state,
        });

        const event = await session.waitForTerminal();
        expect(event.state).toBe(state);
      }
    });

    it('does not resolve immediately for non-terminal state', async () => {
      vi.useFakeTimers();
      try {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-running',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
          state: 'running',
        });

        let resolved = false;
        session.waitForTerminal(1000).then(() => { resolved = true; }).catch(() => {});

        // Should not have resolved yet
        await vi.advanceTimersByTimeAsync(100);
        expect(resolved).toBe(false);

        session.close();
      } finally {
        vi.useRealTimers();
      }
    });

    it('rejects when session is closed (terminalWaiters bug fix)', async () => {
      const mockPn = createMockPubNub();
      const session = new TaskSession({
        taskId: 'task-close-reject',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: mockPn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      });

      const promise = session.waitForTerminal();

      // close() should reject the pending waitForTerminal promise
      session.close();

      await expect(promise).rejects.toThrow('TaskSession closed');
    });

    it('rejects when session is asyncClosed (terminalWaiters bug fix)', async () => {
      const mockPn = createMockPubNub();
      const session = new TaskSession({
        taskId: 'task-async-close-reject',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: mockPn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      });

      const promise = session.waitForTerminal();

      // asyncClose() delegates to close() which should reject the waiter
      await session.asyncClose();

      await expect(promise).rejects.toThrow('TaskSession closed');
    });

    it('timeout still works after terminalWaiters fix', async () => {
      vi.useFakeTimers();
      try {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-timeout-fix',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });

        const promise = session.waitForTerminal(3000);

        vi.advanceTimersByTime(3000);

        await expect(promise).rejects.toThrow('waitForTerminal timed out after 3000ms');

        session.close();
      } finally {
        vi.useRealTimers();
      }
    });
  });

  // ==========================================================================
  // Fix 4: Typed event properties
  // ==========================================================================

  describe('Fix 4: Typed events', () => {
    it('onProgress callback receives ProgressEvent type', () => {
      const mockPn = createMockPubNub();
      const session = new TaskSession({
        taskId: 'task-typed',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: mockPn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      });
      const channel = 'u.alice.task-typed';

      let received: ProgressEvent | undefined;
      session.onProgress((event) => {
        received = event;
      });

      mockPn._simulateMessage(channel, {
        type: 'progress',
        taskId: 'task-typed',
        message: 'Processing...',
        progress: 0.5,
      });

      expect(received).toBeDefined();
      expect(received!.type).toBe('progress');
      expect(received!.message).toBe('Processing...');
      expect(received!.progress).toBe(0.5);

      session.close();
    });

    it('onArtifact callback receives ArtifactEvent type', () => {
      const mockPn = createMockPubNub();
      const session = new TaskSession({
        taskId: 'task-typed',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: mockPn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      });
      const channel = 'u.alice.task-typed';

      let received: ArtifactEvent | undefined;
      session.onArtifact((event) => {
        received = event;
      });

      mockPn._simulateMessage(channel, {
        type: 'artifact',
        taskId: 'task-typed',
        artifactRef: { kind: 'inline', mimeType: 'text/plain', size: 5, data: 'aGVsbG8=' },
        outputId: 'result',
      });

      expect(received).toBeDefined();
      expect(received!.type).toBe('artifact');
      expect(received!.artifactRef.kind).toBe('inline');
      expect(received!.outputId).toBe('result');

      session.close();
    });

    it('onTerminal callback receives TerminalEvent type', () => {
      const mockPn = createMockPubNub();
      const session = new TaskSession({
        taskId: 'task-typed',
        ownerId: 'alice',
        readToken: 't4-token',
        agentName: 'echo',
        pubnub: mockPn as any,
        sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      });
      const channel = 'u.alice.task-typed';

      let received: TerminalEvent | undefined;
      session.onTerminal((event) => {
        received = event;
      });

      mockPn._simulateMessage(channel, {
        type: 'terminal',
        taskId: 'task-typed',
        state: 'failed',
        reason: 'timeout',
        error: 'Task exceeded deadline',
      });

      expect(received).toBeDefined();
      expect(received!.type).toBe('terminal');
      expect(received!.state).toBe('failed');
      expect(received!.reason).toBe('timeout');
      expect(received!.error).toBe('Task exceeded deadline');
    });
  });

  // ==========================================================================
  // Fix 5: saveArtifacts
  // ==========================================================================

  describe('Fix 5: saveArtifacts', () => {
    it('downloads and saves artifacts to directory', async () => {
      const { mkdirSync, rmSync, existsSync, readFileSync } = await import('node:fs');
      const { join } = await import('node:path');
      const { tmpdir } = await import('node:os');

      const tmpDir = join(tmpdir(), `save-artifacts-test-${Date.now()}`);

      try {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-artifacts',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });
        const channel = 'u.alice.task-artifacts';

        // Simulate artifact events
        mockPn._simulateMessage(channel, {
          type: 'artifact',
          taskId: 'task-artifacts',
          artifactRef: {
            kind: 'inline',
            mimeType: 'text/plain',
            size: 5,
            data: Buffer.from('hello').toString('base64'),
            fileName: 'greeting.txt',
          },
        });

        // Mock downloadArtifact on the session
        const origDownload = session.downloadArtifact.bind(session);
        session.downloadArtifact = vi.fn().mockResolvedValue({
          data: new Uint8Array(Buffer.from('hello')),
          mimeType: 'text/plain',
          fileName: 'greeting.txt',
        });

        const paths = await session.saveArtifacts(tmpDir);

        expect(paths).toHaveLength(1);
        expect(paths[0]).toBe(join(tmpDir, 'greeting.txt'));
        expect(existsSync(paths[0])).toBe(true);
        expect(readFileSync(paths[0], 'utf-8')).toBe('hello');

        session.close();
      } finally {
        rmSync(tmpDir, { recursive: true, force: true });
      }
    });

    it('uses artifact-N fallback name when fileName is missing', async () => {
      const { rmSync, readFileSync } = await import('node:fs');
      const { join } = await import('node:path');
      const { tmpdir } = await import('node:os');

      const tmpDir = join(tmpdir(), `save-artifacts-fallback-${Date.now()}`);

      try {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-fallback',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });
        const channel = 'u.alice.task-fallback';

        mockPn._simulateMessage(channel, {
          type: 'artifact',
          taskId: 'task-fallback',
          artifactRef: {
            kind: 'inline',
            mimeType: 'application/octet-stream',
            size: 3,
            data: Buffer.from('abc').toString('base64'),
          },
        });

        session.downloadArtifact = vi.fn().mockResolvedValue({
          data: new Uint8Array(Buffer.from('abc')),
          mimeType: 'application/octet-stream',
          // No fileName
        });

        const paths = await session.saveArtifacts(tmpDir);

        expect(paths).toHaveLength(1);
        expect(paths[0]).toBe(join(tmpDir, 'artifact-0'));
        expect(readFileSync(paths[0], 'utf-8')).toBe('abc');

        session.close();
      } finally {
        rmSync(tmpDir, { recursive: true, force: true });
      }
    });

    it('returns empty array when no artifacts', async () => {
      const { rmSync } = await import('node:fs');
      const { join } = await import('node:path');
      const { tmpdir } = await import('node:os');

      const tmpDir = join(tmpdir(), `save-artifacts-empty-${Date.now()}`);

      try {
        const session = new TaskSession({
          taskId: 'task-no-artifacts',
          ownerId: 'alice',
          readToken: null,
          agentName: 'echo',
          pubnub: null,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
          preClosed: true,
          state: 'completed',
        });

        const paths = await session.saveArtifacts(tmpDir);
        expect(paths).toEqual([]);
      } finally {
        rmSync(tmpDir, { recursive: true, force: true });
      }
    });
  });

  // ==========================================================================
  // Fix 6: Resource management
  // ==========================================================================

  describe('Fix 6: Resource management', () => {
    describe('TaskClient Symbol.dispose', () => {
      it('has Symbol.dispose that calls destroy()', () => {
        const client = new TaskClient({
          billingMode: 'free',
          subscribeKey: 'sub-c-test',
        });

        expect(typeof client[Symbol.dispose]).toBe('function');

        // Calling dispose should not throw
        client[Symbol.dispose]();
      });

      it('Symbol.dispose is idempotent', () => {
        const client = new TaskClient({
          billingMode: 'free',
          subscribeKey: 'sub-c-test',
        });

        client[Symbol.dispose]();
        client[Symbol.dispose](); // Should not throw
      });
    });

    describe('TaskSession asyncClose', () => {
      it('awaits stream client end() calls before closing', async () => {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-async-close',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });
        const channel = 'u.alice.task-async-close';

        // Discover and open a stream
        mockPn._simulateMessage(channel, {
          type: 'progress',
          taskId: 'task-async-close',
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
        });
        const ref = session.listStreams()[0];
        ref.open();
        const client = lastMockClients[lastMockClients.length - 1];

        await session.asyncClose();

        expect(client.end).toHaveBeenCalled();
        expect(session.isClosed).toBe(true);
      });

      it('is idempotent', async () => {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-async-close-idem',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });

        await session.asyncClose();
        await session.asyncClose(); // Should not throw
        expect(session.isClosed).toBe(true);
      });
    });

    describe('TaskSession Symbol.asyncDispose', () => {
      it('has Symbol.asyncDispose that calls asyncClose()', async () => {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-dispose',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });

        expect(typeof session[Symbol.asyncDispose]).toBe('function');

        await session[Symbol.asyncDispose]();
        expect(session.isClosed).toBe(true);
      });
    });

    describe('close() ends open stream clients', () => {
      it('ends all active stream clients on close()', () => {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-close-streams',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });
        const channel = 'u.alice.task-close-streams';

        // Discover and open a stream
        mockPn._simulateMessage(channel, {
          type: 'progress',
          taskId: 'task-close-streams',
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
        });
        const ref = session.listStreams()[0];
        ref.open();
        const client = lastMockClients[lastMockClients.length - 1];

        expect(client.isActive).toBe(true);

        session.close();

        expect(client.end).toHaveBeenCalled();
        expect(session.isClosed).toBe(true);
      });

      it('skips already-inactive stream clients on close()', () => {
        const mockPn = createMockPubNub();
        const session = new TaskSession({
          taskId: 'task-close-inactive',
          ownerId: 'alice',
          readToken: 't4-token',
          agentName: 'echo',
          pubnub: mockPn as any,
          sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
        });
        const channel = 'u.alice.task-close-inactive';

        mockPn._simulateMessage(channel, {
          type: 'progress',
          taskId: 'task-close-inactive',
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
        });
        const ref = session.listStreams()[0];
        ref.open();
        const client = lastMockClients[lastMockClients.length - 1];

        // Simulate stream ending naturally before close
        client._simulateInboundDone();
        expect(client.isActive).toBe(false);

        // The stream client's end() was called by onInboundDone handler,
        // but it's now inactive
        const endCallCount = client.end.mock.calls.length;

        session.close();

        // close() should not call end() again since client is inactive
        expect(client.end.mock.calls.length).toBe(endCallCount);
      });
    });
  });
});
