/**
 * Unit tests for the shared-affinity stream lifecycle fixes landed as
 * part of the `shared_stream_lifecycle` sub-initiative.
 *
 * Covers IMPL Code Changes §9 (Node half) — 8 cases:
 *   1. First acquirer shared: `stream_setup` embedded, entry created
 *      with `taskIds: {taskA}`, `affinity: 'shared'`.
 *   2. Second acquirer different task: `stream_setup activate` with
 *      taskB's `durationMinutes`; `taskIds: {taskA, taskB}`.
 *   3. Same task reacquires shared stream: idempotent — same
 *      StreamObject, no publish, `taskIds` unchanged.
 *   4. Dedicated stream second task: no activate publish.
 *   5. Shared affinity on a request task: throws fix-(g) error.
 *   6. Producer shared `StreamClient.end()`: no `stream_end`
 *      published; teardown deferred until last ref-holder releases.
 *   7. Producer dedicated `StreamClient.end()`: `stream_end` still
 *      published (regression guard).
 *   8. Cleanup-boundary cache eviction: explicit `StreamObject.end()`,
 *      `releaseAllForTask(taskId)`, and `failStream(streamId)` all
 *      evict the matching `sharedStreamHandles` entries without
 *      tearing the underlying `StreamClient` down unless this was
 *      the last ref-holder.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { StreamRegistry } from '../src/runtime/stream-registry.js';
import { makeTestCard, makePipeTestCard } from './helpers/test-card.js';
import type { AgentCard } from '../src/runtime/agent-registry.js';

// --- vi.hoisted shared state + factories ---
//
// `vi.mock(...)` factories are hoisted to the top of the file, so they
// cannot reference module-scope `const`/`let` bindings or non-hoisted
// helpers. Everything the mock factories read at eval time lives inside
// `vi.hoisted()`.

interface CapturedSetup {
  channel: string;
  message: Record<string, unknown>;
}

const hoistedTest = vi.hoisted(() => {
  const publishedSetups: CapturedSetup[] = [];

  const createMockPubNub = (overrides: Record<string, unknown> = {}) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const messageListeners: any[] = [];
    let token: string | undefined;

    const base: Record<string, unknown> = {
      addListener: (listener: unknown) => {
        messageListeners.push(listener);
      },
      removeListener: (listener: unknown) => {
        const idx = messageListeners.indexOf(listener);
        if (idx >= 0) messageListeners.splice(idx, 1);
      },
      subscribe: () => {},
      unsubscribe: () => {},
      unsubscribeAll: () => {},
      destroy: () => {},
      setToken: (t: string) => { token = t; },
      setFilterExpression: () => {},
      setState: async () => ({}),
      publish: async () => ({ timetoken: '123' }),
      hereNow: async () => ({ channels: {} }),
      getToken: () => token,
      _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
      _simulateMessage: (channel: string, message: unknown, userMetadata?: Record<string, unknown>) => {
        for (const listener of messageListeners) {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          if ((listener as any)?.message) (listener as any).message({ channel, message, userMetadata });
        }
      },
    };
    return { ...base, ...overrides };
  };

  // Factory that the pubnub-client.js mock uses for every per-task client.
  const createTaskPubNub = () =>
    createMockPubNub({
      publish: async (args: Record<string, unknown>) => {
        const ch = args.channel as string;
        const msg = args.message as Record<string, unknown>;
        if (ch?.startsWith('setup.')) {
          publishedSetups.push({ channel: ch, message: msg });
          // The runtime expects the publish to throw a 403 carrying the
          // streamSetupResponse. Simulate that.
          throw {
            status: {
              statusCode: 403,
              errorData: {
                message: {
                  ok: true,
                  streamSetupResponse: {
                    taskId: msg.taskId as string,
                    streamId: msg.streamId as string,
                    channel: msg.channel as string,
                    direction: msg.direction as string,
                    phase: (msg.phase as string | undefined) ?? 'embedded',
                    token: msg.phase === 'activate' ? undefined : 'T7A-test',
                    tokenTtlMinutes: 62,
                  },
                },
              },
            },
          };
        }
        return { timetoken: '123' };
      },
    });

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const streamClientInstances: any[] = [];

  class MockStreamClient {
    private _isActive = true;
    private _channel: string;
    private _direction: string;
    private _affinity: string;
    private endCallbacks: Array<() => void> = [];
    public endCount = 0;
    public publishEndMarkerCount = 0;

    constructor(opts: Record<string, unknown>) {
      this._channel = (opts.channel as string) || `stream.${opts.agentName}.${opts.streamId}`;
      this._direction = (opts.direction as string) ?? 'outbound';
      this._affinity = (opts.affinity as string) ?? 'dedicated';
      streamClientInstances.push(this);
    }

    get isActive(): boolean { return this._isActive; }
    get channel(): string { return this._channel; }
    get uuid(): string { return 'mock-stream-uuid'; }
    get affinity(): string { return this._affinity; }
    write = (..._args: unknown[]): void => { void _args; };
    end = async (): Promise<void> => {
      this.endCount++;
      if (!this._isActive) return;
      if (this._direction !== 'bidirectional' && this._affinity !== 'shared') {
        this.publishEndMarkerCount++;
      }
      this._isActive = false;
      for (const cb of this.endCallbacks) cb();
    };
    onEnd(cb: () => void) { this.endCallbacks.push(cb); }
    get inbound(): AsyncIterable<unknown> {
      // eslint-disable-next-line @typescript-eslint/no-this-alias
      const self = this;
      return {
        [Symbol.asyncIterator]() {
          return {
            async next() {
              if (!self._isActive) return { value: undefined, done: true };
              return new Promise(() => {}); // hang
            },
          };
        },
      };
    }
    static fromDescriptor = (desc: Record<string, unknown>, opts: Record<string, unknown>) =>
      new MockStreamClient({ ...desc, ...opts });
  }

  return {
    publishedSetups,
    createMockPubNub,
    createTaskPubNub,
    streamClientInstances,
    MockStreamClient,
  };
});

const publishedSetups = hoistedTest.publishedSetups;
const streamClientInstances = hoistedTest.streamClientInstances;
const createMockPubNub = hoistedTest.createMockPubNub;

vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: async () => ({ pamToken: undefined }),
  fetchAgentRegistry: async () => ({}),
  getAgent: async () => null,
  removeAgent: async () => {},
  fetchAgentsByTag: async () => [],
  fetchAgentsByListing: async () => [],
}));

vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: () => hoistedTest.createTaskPubNub(),
}));

vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = await importOriginal() as Record<string, unknown>;
  return {
    ...actual,
    StreamClient: hoistedTest.MockStreamClient,
  };
});

// --- helpers ---

function makePipeStartTask(taskId: string) {
  return {
    type: 'StartTask' as const,
    taskId,
    ownerId: 'alice',
    taskKind: 'pipe' as const,
    hasStream: true,
    writeToken: `wt-${taskId}`,
    duration: 60,
    durationExpiresAtMs: Date.now() + 3600000,
  };
}

function makeRequestStartTask(taskId: string) {
  return {
    type: 'StartTask' as const,
    taskId,
    ownerId: 'alice',
    taskKind: 'request' as const,
    hasStream: true,
    writeToken: `wt-${taskId}`,
  };
}

function sharedStreamCard(): AgentCard {
  return makeTestCard({
    capabilities: { taskKinds: ['pipe', 'request'] },
    streams: {
      quotes: {
        direction: 'outbound',
        format: 'bytes',
        affinity: 'shared',
      },
    },
  });
}

function sharedStreamPipeCard(): AgentCard {
  return makeTestCard({
    capabilities: { taskKinds: ['pipe'] },
    streams: {
      quotes: {
        direction: 'outbound',
        format: 'bytes',
        affinity: 'shared',
      },
    },
  });
}

async function waitFor<T>(check: () => T | undefined, timeoutMs = 3000): Promise<T> {
  const start = Date.now();
  return new Promise((resolve, reject) => {
    const tick = () => {
      const result = check();
      if (result !== undefined) return resolve(result as T);
      if (Date.now() - start > timeoutMs) return reject(new Error('waitFor timeout'));
      setTimeout(tick, 5);
    };
    tick();
  });
}

function findSetup(taskId: string, phase: string): CapturedSetup | undefined {
  return publishedSetups.find(
    (s) => s.message.taskId === taskId && s.message.phase === phase,
  );
}

// --- tests ---

beforeEach(() => {
  vi.clearAllMocks();
  publishedSetups.length = 0;
  streamClientInstances.length = 0;
});

describe('shared-stream lifecycle', () => {
  it('case 1: first acquirer on shared stream publishes embedded setup and records taskIds/affinity', async () => {
    const mockPn = createMockPubNub();
    let streamReady = false;

    const handle = await startAgentInstance({
      agentName: 'sh1',
      card: sharedStreamPipeCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        await ctx!.createStream({
          declaredStream: 'quotes',
          subscribeGraceMs: 0,
        });
        streamReady = true;
        // Hold the handler open so the stream entry doesn't get released.
        await new Promise(() => {});
        return {};
      },
    });

    mockPn._simulateMessage('agent.sh1.control', makePipeStartTask('task-A'), {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamReady ? true : undefined));

    // Embedded setup went out with affinity: shared
    const embedded = findSetup('task-A', 'embedded');
    expect(embedded).toBeDefined();
    expect(embedded!.message.affinity).toBe('shared');
    expect(embedded!.message.taskKind).toBe('pipe');
    // No activate publish on first-acquirer path
    expect(findSetup('task-A', 'activate')).toBeUndefined();

    handle.stop();
  });

  it('case 2: second task on same shared stream publishes activate with that task\'s durationMinutes', async () => {
    const mockPn = createMockPubNub();
    const streamsReady = new Set<string>();

    const handle = await startAgentInstance({
      agentName: 'sh2',
      card: sharedStreamPipeCard(),
      concurrency: 3,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (task, ctx) => {
        await ctx!.createStream({
          declaredStream: 'quotes',
          subscribeGraceMs: 0,
        });
        streamsReady.add(task.taskId);
        await new Promise(() => {}); // hold open
        return {};
      },
    });

    // Task A — first acquirer (fresh writer, embedded setup).
    mockPn._simulateMessage('agent.sh2.control', { ...makePipeStartTask('task-A'), duration: 15 }, {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamsReady.has('task-A') ? true : undefined));

    // Task B — joins the shared writer (activate setup).
    mockPn._simulateMessage('agent.sh2.control', { ...makePipeStartTask('task-B'), duration: 45 }, {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamsReady.has('task-B') ? true : undefined));

    const embeddedA = findSetup('task-A', 'embedded');
    expect(embeddedA).toBeDefined();
    expect(embeddedA!.message.durationMinutes).toBe(15);

    const activateB = findSetup('task-B', 'activate');
    expect(activateB).toBeDefined();
    expect(activateB!.message.affinity).toBe('shared');
    // Critically: activate carries THIS task's own durationMinutes.
    expect(activateB!.message.durationMinutes).toBe(45);
    expect(activateB!.message.taskKind).toBe('pipe');

    // Task B should NOT have published embedded (no fresh writer creation).
    expect(findSetup('task-B', 'embedded')).toBeUndefined();

    handle.stop();
  });

  it('case 3: same task reacquiring shared stream is idempotent (no publish, same StreamObject)', async () => {
    const mockPn = createMockPubNub();
    const handles: Array<{ s1?: unknown; s2?: unknown }> = [];

    const handle = await startAgentInstance({
      agentName: 'sh3',
      card: sharedStreamPipeCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        const s1 = await ctx!.createStream({ declaredStream: 'quotes', subscribeGraceMs: 0 });
        const s2 = await ctx!.createStream({ declaredStream: 'quotes', subscribeGraceMs: 0 });
        handles.push({ s1, s2 });
        await new Promise(() => {});
        return {};
      },
    });

    mockPn._simulateMessage('agent.sh3.control', makePipeStartTask('task-A'), {
      instance: handle.instanceId,
    });
    await waitFor(() => (handles.length === 1 ? true : undefined));

    // Exactly one embedded setup publish (the first call).
    const setups = publishedSetups.filter((s) => s.message.taskId === 'task-A');
    expect(setups).toHaveLength(1);
    expect(setups[0].message.phase).toBe('embedded');

    // Same StreamObject returned for both calls.
    expect(handles[0].s1).toBe(handles[0].s2);

    handle.stop();
  });

  it('case 4: dedicated stream — second task gets its own stream, no activate publish', async () => {
    const mockPn = createMockPubNub();
    const streamsReady = new Set<string>();

    const handle = await startAgentInstance({
      agentName: 'ded1',
      card: makePipeTestCard(), // dedicated-affinity _default stream
      concurrency: 3,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (task, ctx) => {
        await ctx!.createStream({ subscribeGraceMs: 0 });
        streamsReady.add(task.taskId);
        await new Promise(() => {});
        return {};
      },
    });

    mockPn._simulateMessage('agent.ded1.control', makePipeStartTask('task-A'), {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamsReady.has('task-A') ? true : undefined));

    mockPn._simulateMessage('agent.ded1.control', makePipeStartTask('task-B'), {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamsReady.has('task-B') ? true : undefined));

    // Both tasks publish embedded setups (each gets its OWN stream because
    // dedicated streams have per-task streamIds). Neither publishes activate.
    const embeddedA = findSetup('task-A', 'embedded');
    const embeddedB = findSetup('task-B', 'embedded');
    expect(embeddedA).toBeDefined();
    expect(embeddedB).toBeDefined();
    expect(embeddedA!.message.affinity).toBe('dedicated');
    expect(embeddedB!.message.affinity).toBe('dedicated');
    expect(embeddedA!.message.streamId).not.toBe(embeddedB!.message.streamId);

    expect(publishedSetups.filter((s) => s.message.phase === 'activate')).toHaveLength(0);

    // Each task produced its own MockStreamClient instance (dedicated path
    // unchanged).
    expect(streamClientInstances).toHaveLength(2);

    handle.stop();
  });

  it('case 5a: shared-affinity stream + external: true throws the fix-(h) error', async () => {
    // Fix (h): shared affinity + external is a design contradiction
    // (shared = one SDK-managed writer + many refs; external =
    // delegate writer entirely). The SDK rejects the combination
    // before any registry / handshake state is touched, regardless
    // of taskKind. See SDK_CONTRACT §4.4.3.
    const mockPn = createMockPubNub();
    let caught: unknown;

    const handle = await startAgentInstance({
      agentName: 'sh5ext',
      card: sharedStreamPipeCard(), // pipe-only; external+shared is blocked even for pipe
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        try {
          await ctx!.createStream({
            declaredStream: 'quotes',
            external: true,
            subscribeGraceMs: 0,
          });
        } catch (err) {
          caught = err;
        }
        return {};
      },
    });

    mockPn._simulateMessage('agent.sh5ext.control', makePipeStartTask('task-ext-shared'), {
      instance: handle.instanceId,
    });
    await waitFor(() => caught);

    expect(caught).toBeInstanceOf(Error);
    const err = caught as Error;
    expect(err.message).toContain('Shared-affinity external streams are not supported');
    expect(err.message).toContain('quotes');
    expect(err.message).toContain("affinity: 'shared'");
    expect(err.message).toContain('external: true');

    // No wire publish happened — the check fires before performStreamSetup.
    expect(publishedSetups.filter((s) => s.message.taskId === 'task-ext-shared')).toHaveLength(0);

    handle.stop();
  });

  it('case 5: shared-affinity stream on a request task throws the fix-(g) error', async () => {
    const mockPn = createMockPubNub();
    let caught: unknown;

    const handle = await startAgentInstance({
      agentName: 'sh5',
      card: sharedStreamCard(), // card allows request + pipe
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        try {
          await ctx!.createStream({ declaredStream: 'quotes', subscribeGraceMs: 0 });
        } catch (err) {
          caught = err;
        }
        return {};
      },
    });

    mockPn._simulateMessage('agent.sh5.control', makeRequestStartTask('task-R'), {
      instance: handle.instanceId,
    });
    await waitFor(() => caught);

    expect(caught).toBeInstanceOf(Error);
    const err = caught as Error;
    expect(err.message).toContain('Shared-affinity streams are not supported on request tasks');
    expect(err.message).toContain('quotes');
    expect(err.message).toContain("affinity: 'shared'");

    // No wire publish happened — the check fires before performStreamSetup.
    expect(publishedSetups.filter((s) => s.message.taskId === 'task-R')).toHaveLength(0);

    handle.stop();
  });

  it('case 6: producer shared StreamClient.end() does NOT publish stream_end; teardown deferred until last ref-holder', async () => {
    const mockPn = createMockPubNub();
    const streamsReady = new Set<string>();
    let taskAReleasedResolve: (() => void) | null = null;
    const taskAReleased = new Promise<void>((r) => { taskAReleasedResolve = r; });
    let taskBReleasedResolve: (() => void) | null = null;
    const taskBReleased = new Promise<void>((r) => { taskBReleasedResolve = r; });
    // Gate controlled by the test: task-B waits on this AFTER noticing
    // task-A released, so the test can inspect registry/end-count state
    // between the two releases without the JS scheduler collapsing them
    // onto the same microtask sequence.
    let releaseTaskB: (() => void) | null = null;
    const taskBCanRelease = new Promise<void>((r) => { releaseTaskB = r; });

    const handle = await startAgentInstance({
      agentName: 'sh6',
      card: sharedStreamPipeCard(),
      concurrency: 3,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (task, ctx) => {
        const stream = await ctx!.createStream({
          declaredStream: 'quotes',
          subscribeGraceMs: 0,
        });
        streamsReady.add(task.taskId);
        if (task.taskId === 'task-A') {
          await new Promise((r) => setTimeout(r, 20));
          await stream.end();
          taskAReleasedResolve?.();
          await new Promise(() => {}); // hold open; cleanup via stop()
        } else {
          // task-B waits for the TEST's signal before releasing, so the
          // test can inspect state with only task-A released.
          await taskBCanRelease;
          await stream.end();
          taskBReleasedResolve?.();
        }
        return {};
      },
    });

    mockPn._simulateMessage('agent.sh6.control', makePipeStartTask('task-A'), {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamsReady.has('task-A') ? true : undefined));

    mockPn._simulateMessage('agent.sh6.control', makePipeStartTask('task-B'), {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamsReady.has('task-B') ? true : undefined));

    // Exactly one shared StreamClient was created across both tasks.
    expect(streamClientInstances).toHaveLength(1);
    const shared = streamClientInstances[0];

    // task-A releases first: registry refcount decrements but writer stays alive.
    await taskAReleased;
    // Drain any outstanding microtasks created by the async release chain.
    await new Promise((r) => setTimeout(r, 10));
    expect(shared.endCount).toBe(0);
    expect(shared.isActive).toBe(true);

    // Signal task-B to release. task-B is last ref-holder -> registry tears
    // down the writer internally.
    releaseTaskB?.();
    await taskBReleased;
    await new Promise((r) => setTimeout(r, 20));
    expect(shared.endCount).toBe(1);

    // Crucially: no stream_end marker was ever published (shared-affinity).
    expect(shared.publishEndMarkerCount).toBe(0);

    handle.stop();
  });

  it('case 7: producer dedicated StreamClient.end() still publishes stream_end (regression guard)', async () => {
    const mockPn = createMockPubNub();
    let streamReady = false;
    let endedSignal: (() => void) | null = null;
    const ended = new Promise<void>((r) => { endedSignal = r; });

    const handle = await startAgentInstance({
      agentName: 'ded2',
      card: makePipeTestCard(), // dedicated _default stream
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        const stream = await ctx!.createStream({ subscribeGraceMs: 0 });
        streamReady = true;
        await new Promise((r) => setTimeout(r, 10));
        await stream.end();
        endedSignal?.();
        return {};
      },
    });

    mockPn._simulateMessage('agent.ded2.control', makePipeStartTask('task-A'), {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamReady ? true : undefined));
    await ended;
    // Let the async release() drain.
    await new Promise((r) => setTimeout(r, 20));

    expect(streamClientInstances).toHaveLength(1);
    const client = streamClientInstances[0];
    expect(client.endCount).toBe(1);
    // Dedicated + outbound => publishEndMarker fires.
    expect(client.publishEndMarkerCount).toBe(1);

    handle.stop();
  });

  it('case 8: cleanup boundaries (StreamObject.end, releaseAllForTask, failStream) evict sharedStreamHandles correctly', async () => {
    // This test exercises the cache-eviction invariants directly against
    // the registry + the stream-context wrapper without standing up the
    // full agent runtime. That keeps the assertion surface narrow and
    // lets us inspect the shared cache after each cleanup boundary.
    const { createStreamObject } = await import('../src/runtime/stream-context.js');

    const registry = new StreamRegistry();
    // Simulate the agent-instance shared-handle cache + helpers.
    const sharedStreamHandles = new Map<string, Map<string, unknown>>();

    const fakeClient = {
      endCount: 0,
      isActive: true,
      channel: 'stream.acme.quotes',
      end: async () => { fakeClient.endCount++; fakeClient.isActive = false; },
      onEnd: () => {},
      inbound: { [Symbol.asyncIterator]: () => ({ next: async () => ({ value: undefined, done: true }) }) },
      write: () => {},
    };

    // Seed a shared entry with two tasks attached.
    const acqA = registry.acquire('quotes', 'task-A', {
      direction: 'outbound', format: 'bytes', external: false, affinity: 'shared',
    });
    acqA.entry.streamClient = fakeClient as never;
    const acqB = registry.acquire('quotes', 'task-B', {
      direction: 'outbound', format: 'bytes', external: false, affinity: 'shared',
    });
    expect(acqA.isNewForTask).toBe(true);
    expect(acqB.isNewForTask).toBe(true);
    expect(acqA.entry.taskIds.size).toBe(2);

    // Eviction helper shaped like the one inside agent-instance.ts.
    const evictSharedHandle = (sid: string, tid: string) => {
      const per = sharedStreamHandles.get(sid);
      if (!per) return;
      per.delete(tid);
      if (per.size === 0) sharedStreamHandles.delete(sid);
    };
    const evictSharedHandlesForStream = (sid: string) => sharedStreamHandles.delete(sid);
    const evictSharedHandlesForTask = (tid: string) => {
      for (const [sid, per] of sharedStreamHandles) {
        if (per.has(tid)) {
          per.delete(tid);
          if (per.size === 0) sharedStreamHandles.delete(sid);
        }
      }
    };

    // Wire the task-scoped releaseStream hook used by StreamObject.end().
    const releaseStream = async (sid: string, tid: string) => {
      evictSharedHandle(sid, tid);
      const entry = registry.get(sid);
      const remaining = registry.release(sid, tid);
      if (remaining === 0 && entry?.streamClient) {
        try { await entry.streamClient.end(); } catch { /* ignore */ }
      }
    };

    // Populate the cache with wrappers for both tasks.
    const makeHandle = (tid: string) => {
      const so = createStreamObject('quotes', fakeClient as never, tid, { releaseStream });
      const perStream = sharedStreamHandles.get('quotes') ?? new Map();
      perStream.set(tid, so);
      sharedStreamHandles.set('quotes', perStream);
      return so;
    };
    const handleA = makeHandle('task-A');
    makeHandle('task-B'); // populate the cache for task-B; we eviction-check it by key, not by reference

    // --- (a) Explicit StreamObject.end() on task-A evicts just task-A ---
    await handleA.end();
    expect(sharedStreamHandles.get('quotes')?.has('task-A')).toBe(false);
    expect(sharedStreamHandles.get('quotes')?.has('task-B')).toBe(true);
    // task-B still ref-holding the stream: underlying client NOT torn down.
    expect(fakeClient.endCount).toBe(0);
    expect(registry.get('quotes')!.taskIds.size).toBe(1);

    // --- (b) releaseAllForTask(task-B) evicts the remaining entry ---
    //     and, because task-B was the last ref-holder, the registry
    //     destroys the entry — caller must then end() the client (this
    //     is what agent-instance.ts does after releaseAllForTask).
    evictSharedHandlesForTask('task-B');
    const destroyed = registry.releaseAllForTask('task-B');
    expect(destroyed).toHaveLength(1);
    expect(sharedStreamHandles.has('quotes')).toBe(false);
    // Caller performs the teardown on returned destroyed entries.
    for (const e of destroyed) await e.streamClient!.end();
    expect(fakeClient.endCount).toBe(1);

    // --- (c) failStream path: re-seed a shared entry with two tasks
    //     and verify forceRemove + evictSharedHandlesForStream removes
    //     the whole sharedStreamHandles[streamId] map. ---
    fakeClient.endCount = 0;
    fakeClient.isActive = true;
    const acqC = registry.acquire('quotes', 'task-C', {
      direction: 'outbound', format: 'bytes', external: false, affinity: 'shared',
    });
    acqC.entry.streamClient = fakeClient as never;
    registry.acquire('quotes', 'task-D', {
      direction: 'outbound', format: 'bytes', external: false, affinity: 'shared',
    });
    makeHandle('task-C');
    makeHandle('task-D');
    expect(sharedStreamHandles.get('quotes')?.size).toBe(2);

    const removed = registry.forceRemove('quotes');
    evictSharedHandlesForStream('quotes');
    // Cache completely cleared for the failed stream.
    expect(sharedStreamHandles.has('quotes')).toBe(false);
    // Registry entry is gone.
    expect(registry.get('quotes')).toBeUndefined();
    // taskIds preserved on the returned entry so failStream can fan out.
    expect(removed!.taskIds.has('task-C')).toBe(true);
    expect(removed!.taskIds.has('task-D')).toBe(true);
  });

  // Case 9 covers the first-writer setup race flagged in PR#515 review.
  // `performStreamSetup` is async; if Task B's createStream enters
  // between Task A's `registry.acquire` and Task A's
  // `entry.streamClient = client` assignment, Task B takes the
  // `!isNew` branch and — pre-fix — would find `entry.streamClient ==
  // null`, publish `activate`, then either throw "Stream exists but
  // has no client" (Node) or fall through to a duplicate embedded
  // handshake (Python) creating a second writer on the shared
  // channel. The fix installs a `setupPromise` on the registry entry
  // that Task B awaits before consulting `streamClient`.
  //
  // This is a registry-scope test covering the barrier mechanism
  // itself. The end-to-end agent-instance behavior (the !isNew branch
  // awaiting entry.setupPromise before the activate publish) is
  // exercised by the existing case-2 test and by the human-test §6.2
  // concurrent-task scenario.
  it('case 9: setupPromise on the registry entry serializes second-acquirer attach-after-setup (race regression)', async () => {
    const registry = new StreamRegistry();

    const acqA = registry.acquire('quotes', 'task-A', {
      direction: 'outbound', format: 'events', external: false, affinity: 'shared',
    });
    expect(acqA.isNew).toBe(true);
    expect(acqA.entry.streamClient).toBeNull();

    // First acquirer installs a pending setupPromise (mirrors the
    // agent-instance closure's Promise-deferred pattern).
    let resolveA!: () => void;
    acqA.entry.setupPromise = new Promise<void>((res) => { resolveA = res; });
    acqA.entry.setupPromise.catch(() => { /* swallow for no-await path */ });

    // Second acquirer arrives while Task A's setup is still pending.
    const acqB = registry.acquire('quotes', 'task-B', {
      direction: 'outbound', format: 'events', external: false, affinity: 'shared',
    });
    expect(acqB.isNew).toBe(false);
    expect(acqB.isNewForTask).toBe(true);
    // The setupPromise is observable to Task B.
    expect(acqB.entry.setupPromise).toBe(acqA.entry.setupPromise);

    // Task B awaits the promise before touching streamClient.
    let taskBUnblocked = false;
    const taskBFlow = (async () => {
      if (acqB.entry.setupPromise) await acqB.entry.setupPromise;
      taskBUnblocked = true;
      // Post-wait, Task B would consult entry.streamClient.
      return acqB.entry.streamClient;
    })();

    // Drain the microtask queue; Task B should still be blocked.
    await Promise.resolve();
    await Promise.resolve();
    expect(taskBUnblocked).toBe(false);
    expect(acqB.entry.streamClient).toBeNull();

    // Task A finishes setup: installs streamClient, resolves promise.
    acqA.entry.streamClient = { mock: 'first-writer' } as never;
    resolveA();

    // Task B unblocks and sees the installed streamClient.
    const observedClient = await taskBFlow;
    expect(taskBUnblocked).toBe(true);
    expect(observedClient).toEqual({ mock: 'first-writer' });
  });

  // Case 10 covers the concurrent same-task reacquire race flagged in
  // PR#515 review. A handler that does `Promise.all([createStream(...),
  // createStream(...)])` for the same shared declared stream enters
  // createStream twice before the first call's handle has been
  // cached. Pre-fix, the second call landed in the `!isNewForTask`
  // block, saw `entry.streamClient` was null (setup still in flight),
  // fell through to `!isNew`, awaited setupPromise, and then
  // published `phase: 'activate'` for the SAME task — producing a
  // duplicate stream_setup, a second T7c mint, and an extra
  // stream_started event. Violated the fix (e) idempotent contract.
  //
  // The fix:
  //   (1) the first acquirer caches its handle BEFORE resolveSetup()
  //       fires, so anyone waking on setupPromise sees a populated
  //       cache;
  //   (2) the `!isNewForTask` block awaits setupPromise then returns
  //       the cached handle rather than falling through.
  it('case 10: concurrent same-task reacquire during first-acquirer setup returns same handle, no duplicate activate (race regression)', async () => {
    const mockPn = createMockPubNub();
    const handleCollector: Array<{ s1?: unknown; s2?: unknown }> = [];

    const handle = await startAgentInstance({
      agentName: 'sh10',
      card: sharedStreamPipeCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (_task, ctx) => {
        // Two concurrent createStream calls from the SAME task for
        // the SAME shared declared stream. Promise.all forces
        // concurrent entry — the second call lands while the first
        // is still inside performStreamSetup.
        const [s1, s2] = await Promise.all([
          ctx!.createStream({ declaredStream: 'quotes', subscribeGraceMs: 0 }),
          ctx!.createStream({ declaredStream: 'quotes', subscribeGraceMs: 0 }),
        ]);
        handleCollector.push({ s1, s2 });
        await new Promise(() => {}); // hold open
        return {};
      },
    });

    mockPn._simulateMessage('agent.sh10.control', makePipeStartTask('task-A'), {
      instance: handle.instanceId,
    });
    await waitFor(() => (handleCollector.length === 1 ? true : undefined));

    // Both concurrent calls must return the EXACT same StreamObject
    // reference — fix (e) "same handle" contract.
    expect(handleCollector[0].s1).toBe(handleCollector[0].s2);

    // Exactly ONE stream_setup publish for task-A. Pre-fix, the race
    // produced two (one embedded from the first call, one activate
    // from the second).
    const taskASetups = publishedSetups.filter(
      (s) => s.message.taskId === 'task-A',
    );
    expect(taskASetups).toHaveLength(1);
    expect(taskASetups[0].message.phase).toBe('embedded');

    // Exactly one StreamClient constructed (we didn't accidentally
    // build a second writer on the shared channel).
    const clientsForQuotes = streamClientInstances.filter(
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (c: any) => c._channel === 'stream.sh10.quotes',
    );
    expect(clientsForQuotes).toHaveLength(1);

    handle.stop();
  });

  // Case 11 covers the rollback-on-setup-failure regression flagged
  // in PR#515 review. Pre-fix: first-acquirer setup failure (PubNub
  // 5xx, PAM flap, Function deploy race) left a zombie registry entry
  // with `taskIds={failedTaskId}` and a rejected setupPromise. Any
  // subsequent task calling createStream on the same shared streamId
  // would find the existing entry, await the rejected promise, and
  // re-throw the same error — bricking the channel on that agent
  // instance until restart. The fix releases the registry ref inside
  // createStream's setup-fail catch (Layer 1) and also unconditionally
  // in the outer task-error catch (Layer 2, belt-and-suspenders).
  //
  // This test exercises the registry primitive directly: simulate
  // acquire → install setupPromise → reject + release, then verify a
  // fresh acquire from a different task creates a new entry (isNew=true).
  it('case 11: first-acquirer setup failure rolls back the registry entry so later tasks get a fresh entry (bricking regression)', async () => {
    const registry = new StreamRegistry();

    // Task A: first acquirer.
    const acqA = registry.acquire('shared_quotes', 'task-A', {
      direction: 'outbound', format: 'events', external: false, affinity: 'shared',
    });
    expect(acqA.isNew).toBe(true);
    expect(acqA.entry.taskIds.has('task-A')).toBe(true);

    // Install a pending setupPromise, then simulate setup failure.
    let rejectA!: (err: unknown) => void;
    acqA.entry.setupPromise = new Promise<void>((_res, rej) => { rejectA = rej; });
    acqA.entry.setupPromise.catch(() => { /* swallow */ });

    // Setup throws (simulating performStreamSetup failure). Production
    // code path: rejectSetup(err); evictSharedHandle(...); await
    // streamRegistry.release(streamId, taskId); throw err.
    const setupErr = new Error('PubNub 503 during stream_setup handshake');
    rejectA(setupErr);
    await registry.release('shared_quotes', 'task-A');

    // Registry should be clean — no zombie entry.
    expect(registry.get('shared_quotes')).toBeUndefined();

    // Task B on the same shared stream must get a fresh entry
    // (isNew=true), not re-throw Task A's error.
    const acqB = registry.acquire('shared_quotes', 'task-B', {
      direction: 'outbound', format: 'events', external: false, affinity: 'shared',
    });
    expect(acqB.isNew).toBe(true);
    expect(acqB.isNewForTask).toBe(true);
    expect(acqB.entry.taskIds).toEqual(new Set(['task-B']));
    expect(acqB.entry.setupPromise).toBeNull();
    // New entry means Task B's createStream would start a fresh
    // setup handshake rather than inheriting Task A's rejected
    // setupPromise. Verifies bricking fix.
  });
});
