/**
 * Cross-SDK parity test for the shared-affinity stream lifecycle work
 * (SHARED_STREAM_LIFECYCLE_IMPL Code Changes §11).
 *
 * Parity claim: every assertion here has a Python mirror in
 * `blocks-sdk/sdks/python/tests/test_shared_stream_parity.py`. Harness
 * specifics differ (vitest mocks vs pytest monkeypatch / capturing
 * fakes), but the behavioral shape is identical.
 *
 * Scenario (two concurrent pipe tasks on a shared-affinity outbound
 * stream):
 *   - Both tasks' handlers receive a StreamObject.
 *   - Each task publishes its OWN stream_setup to its OWN setup channel
 *     (`setup.{orgId}.{taskId}`). Task A publishes `phase: 'embedded'`;
 *     task B publishes `phase: 'activate'`. Distinct setup channels ⇒
 *     distinct T7c KV slots (`streamtoken:{taskId}:{streamId}`) on the
 *     real Function ⇒ distinct per-task T7c tokens.
 *   - Each setup message carries the OWNING task's `durationMinutes`.
 *     On the real Function this becomes a per-task T7c TTL; asserting
 *     the `durationMinutes` on the setup is the correct SDK-side parity
 *     gate.
 *   - `stream.end()` on either task's handler does NOT publish a
 *     `stream_end` marker to the shared channel (affinity gate).
 *   - Consumer late-subscribe: with no cached marker on the shared
 *     channel, a later consumer would not be forced to exit on a stale
 *     marker from any prior task's cleanup.
 *
 * The sibling tests at `shared-up-consumer-writer.test.ts` (§12a) and
 * `shared-stream-late-reader.test.ts` (§12b) cover the consumer-writer
 * and late-reader cases at the StreamClient / descriptor level.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { TaskSession, type TaskEvent } from '../src/runtime/task-session.js';
import { makePipeTestCard } from './helpers/test-card.js';
import type { AgentCard } from '../src/runtime/agent-registry.js';
import type { ArtifactRef } from '../src/runtime/artifacts.js';

// ---------------------------------------------------------------------------
// vi.hoisted — mirrors agent-instance-shared-stream.test.ts so the
// harness stays consistent with the SDK-wide shared-stream test shape.
// ---------------------------------------------------------------------------

interface CapturedPublish {
  channel: string;
  message: Record<string, unknown>;
}

const hoisted = vi.hoisted(() => {
  const publishedSetups: CapturedPublish[] = [];
  // Every publish that is NOT a `setup.*` publish lands here. Lets the
  // test assert "no stream_end on the shared channel".
  const publishedOther: CapturedPublish[] = [];

  const createMockPubNub = (overrides: Record<string, unknown> = {}) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const messageListeners: any[] = [];
    let token: string | undefined;

    const base: Record<string, unknown> = {
      addListener: (listener: unknown) => { messageListeners.push(listener); },
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
          const target = listener as {
            message?: (event: { channel: string; message: unknown; userMetadata?: Record<string, unknown> }) => void;
          };
          if (target.message) {
            target.message({ channel, message, userMetadata });
          }
        }
      },
    };
    return { ...base, ...overrides };
  };

  const createTaskPubNub = () =>
    createMockPubNub({
      publish: async (args: Record<string, unknown>) => {
        const ch = args.channel as string;
        const msg = args.message as Record<string, unknown>;
        if (ch?.startsWith('setup.')) {
          publishedSetups.push({ channel: ch, message: msg });
          // Simulate Function's 403 abort carrying streamSetupResponse.
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
                    token: msg.phase === 'activate' ? undefined : `T7A-${msg.taskId}`,
                    tokenTtlMinutes: 62,
                  },
                },
              },
            },
          };
        }
        publishedOther.push({ channel: ch, message: msg });
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
    public streamId: string;

    constructor(opts: Record<string, unknown>) {
      this.streamId = (opts.streamId as string) ?? '';
      this._channel = (opts.channel as string) || `stream.${opts.agentName}.${opts.streamId}`;
      this._direction = (opts.direction as string) ?? 'outbound';
      this._affinity = (opts.affinity as string) ?? 'dedicated';
      streamClientInstances.push(this);
    }

    get isActive(): boolean { return this._isActive; }
    get channel(): string { return this._channel; }
    get uuid(): string { return 'mock-stream-uuid'; }
    get affinity(): string { return this._affinity; }
    get direction(): string { return this._direction; }
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
              return new Promise(() => {});
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
    publishedOther,
    createMockPubNub,
    createTaskPubNub,
    streamClientInstances,
    MockStreamClient,
  };
});

const publishedSetups = hoisted.publishedSetups;
const publishedOther = hoisted.publishedOther;
const streamClientInstances = hoisted.streamClientInstances;
const createMockPubNub = hoisted.createMockPubNub;

vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: async () => ({ pamToken: undefined }),
  fetchAgentRegistry: async () => ({}),
  getAgent: async () => null,
  removeAgent: async () => {},
  fetchAgentsByTag: async () => [],
  fetchAgentsByListing: async () => [],
}));

vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: () => hoisted.createTaskPubNub(),
}));

vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = await importOriginal() as Record<string, unknown>;
  return {
    ...actual,
    StreamClient: hoisted.MockStreamClient,
  };
});

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makePipeStartTask(
  taskId: string,
  durationMinutes = 60,
): Record<string, unknown> {
  return {
    type: 'StartTask' as const,
    taskId,
    ownerId: 'alice',
    taskKind: 'pipe' as const,
    hasStream: true,
    writeToken: `wt-${taskId}`,
    duration: durationMinutes,
    durationExpiresAtMs: Date.now() + durationMinutes * 60_000,
  };
}

function sharedOutCard(): AgentCard {
  return makePipeTestCard({
    streams: {
      shared_down: {
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

function findSetup(taskId: string, phase: string): CapturedPublish | undefined {
  return publishedSetups.find(
    (s) => s.message.taskId === taskId && s.message.phase === phase,
  );
}

function publishesOnChannel(channel: string): CapturedPublish[] {
  return publishedOther.filter((p) => p.channel === channel);
}

beforeEach(() => {
  vi.clearAllMocks();
  publishedSetups.length = 0;
  publishedOther.length = 0;
  streamClientInstances.length = 0;
});

// ===========================================================================
// §11 Cross-SDK parity: two concurrent pipe tasks on a shared stream
// ===========================================================================

describe('shared-stream parity (§11): two concurrent pipe tasks', () => {
  it('distinct T7c slots per task, no stream_end on shared channel, late reader finds no cached marker', async () => {
    const mockPn = createMockPubNub();
    const streamsReady = new Set<string>();
    let releaseTaskA: (() => void) | null = null;
    const taskACanRelease = new Promise<void>((r) => { releaseTaskA = r; });
    let releaseTaskB: (() => void) | null = null;
    const taskBCanRelease = new Promise<void>((r) => { releaseTaskB = r; });
    const releasedTasks = new Set<string>();

    const handle = await startAgentInstance({
      agentName: 'parity_sh11',
      card: sharedOutCard(),
      concurrency: 3,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: mockPn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (task, ctx) => {
        const stream = await ctx!.createStream({
          declaredStream: 'shared_down',
          subscribeGraceMs: 0,
        });
        streamsReady.add(task.taskId);

        if (task.taskId === 'task-A') {
          await taskACanRelease;
          await stream.end();
          releasedTasks.add('task-A');
        } else {
          await taskBCanRelease;
          await stream.end();
          releasedTasks.add('task-B');
        }
        return {};
      },
    });

    // Task A (durationMinutes: 15) — first acquirer.
    mockPn._simulateMessage('agent.parity_sh11.control', makePipeStartTask('task-A', 15), {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamsReady.has('task-A') ? true : undefined));

    // Task B (durationMinutes: 45) — second acquirer; inherits the shared
    // writer; publishes phase: 'activate' with OWN duration.
    mockPn._simulateMessage('agent.parity_sh11.control', makePipeStartTask('task-B', 45), {
      instance: handle.instanceId,
    });
    await waitFor(() => (streamsReady.has('task-B') ? true : undefined));

    // --- Assertion 1: both handlers discovered the shared stream ---
    expect(streamsReady.has('task-A')).toBe(true);
    expect(streamsReady.has('task-B')).toBe(true);

    // --- Assertion 2: distinct setup channels (distinct T7c KV slots) ---
    const setupA = findSetup('task-A', 'embedded');
    const setupB = findSetup('task-B', 'activate');
    expect(setupA).toBeDefined();
    expect(setupB).toBeDefined();
    // Distinct setup channels == distinct per-task T7c KV slots
    // (streamtoken:{taskId}:{streamId}) on the real Function.
    expect(setupA!.channel).not.toBe(setupB!.channel);
    expect(setupA!.channel).toContain('task-A');
    expect(setupB!.channel).toContain('task-B');
    // Parity-critical: the setup's taskId field is the key into the KV
    // slot. Asserting that the two messages carry DISTINCT taskIds is
    // the SDK-side gate for "distinct T7c per task".
    expect(setupA!.message.taskId).toBe('task-A');
    expect(setupB!.message.taskId).toBe('task-B');
    expect(setupA!.message.taskId).not.toBe(setupB!.message.taskId);

    // --- Assertion 3: each setup carries OWNING task's durationMinutes ---
    expect(setupA!.message.durationMinutes).toBe(15);
    expect(setupB!.message.durationMinutes).toBe(45);

    // --- Assertion 4: both setups carry affinity: 'shared' + taskKind: 'pipe' ---
    expect(setupA!.message.affinity).toBe('shared');
    expect(setupB!.message.affinity).toBe('shared');
    expect(setupA!.message.taskKind).toBe('pipe');
    expect(setupB!.message.taskKind).toBe('pipe');

    // --- Assertion 5: exactly ONE shared StreamClient — writer reused ---
    expect(streamClientInstances).toHaveLength(1);
    const sharedWriter = streamClientInstances[0];
    const sharedChannel = sharedWriter.channel;

    // --- Release task A. Writer stays alive because task B still holds a ref. ---
    releaseTaskA!();
    await waitFor(() => (releasedTasks.has('task-A') ? true : undefined));
    await new Promise((r) => setTimeout(r, 20));

    expect(sharedWriter.isActive).toBe(true);
    expect(sharedWriter.endCount).toBe(0);
    expect(sharedWriter.publishEndMarkerCount).toBe(0);
    // No publish of any kind (including stream_end) hit the shared channel.
    expect(publishesOnChannel(sharedChannel)).toHaveLength(0);

    // --- Release task B. Last ref-holder; registry tears down writer locally. ---
    releaseTaskB!();
    await waitFor(() => (releasedTasks.has('task-B') ? true : undefined));
    await new Promise((r) => setTimeout(r, 30));

    // Task B released: teardown ran (registry called end()), but the
    // affinity gate suppressed publishEndMarker on the shared channel.
    expect(sharedWriter.endCount).toBe(1);
    expect(sharedWriter.publishEndMarkerCount).toBe(0);
    expect(publishesOnChannel(sharedChannel)).toHaveLength(0);

    // --- Assertion 6: no stream_end marker anywhere on the shared channel ---
    // A third consumer subscribing after both tasks released (within PubNub's
    // cache window) would NOT receive a cached stream_end marker because none
    // was ever published. Its iterator therefore cannot be terminated by a
    // stale marker from either task's lifecycle cleanup. This is the late-
    // reader resilience gate encoded at the producer-side surface.
    const streamEndMarkers = publishesOnChannel(sharedChannel).filter(
      (p) => (p.message as Record<string, unknown>)?.type === 'stream_end',
    );
    expect(streamEndMarkers).toHaveLength(0);

    handle.stop();
  });
});

describe('onArtifact history replay', () => {
  it('replays preloaded artifacts in order with minimal synthetic event shape', () => {
    const ref1: ArtifactRef = {
      kind: 'inline',
      mimeType: 'text/plain',
      size: 5,
      data: 'aGVsbG8=',
    };
    const ref2: ArtifactRef = {
      kind: 'file',
      mimeType: 'image/png',
      size: 1000,
      channel: 'u.alice.task-1',
      fileId: 'file-2',
      fileName: 'image.png',
    };
    const pubnub = createMockPubNub();
    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: 't4',
      agentName: 'parity_artifacts',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pubnub as any,
      sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      preloadedArtifacts: [ref1, ref2],
    });
    const events: Array<Record<string, unknown>> = [];

    session.onArtifact((event) => events.push(event));

    expect(events).toHaveLength(2);
    expect(events[0]).toMatchObject({ type: 'artifact', taskId: 'task-1' });
    expect(events[0].artifactRef).toBe(ref1);
    expect(events[0]).not.toHaveProperty('outputId');
    expect(events[0]).not.toHaveProperty('protocolVersion');
    expect(events[1]).toMatchObject({ type: 'artifact', taskId: 'task-1' });
    expect(events[1].artifactRef).toBe(ref2);
    expect(events[1]).not.toHaveProperty('outputId');
    expect(events[1]).not.toHaveProperty('protocolVersion');

    session.close();
  });
});

describe('listEvents history parity', () => {
  it('returns preloaded history events in insertion order', () => {
    const events: TaskEvent[] = [
      { type: 'progress', taskId: 'task-1', message: 'Working' },
      {
        type: 'artifact',
        taskId: 'task-1',
        artifactRef: {
          kind: 'inline',
          mimeType: 'text/plain',
          size: 5,
          data: 'aGVsbG8=',
        },
      },
      { type: 'terminal', taskId: 'task-1', state: 'completed' },
      {
        type: 'progress',
        taskId: 'task-1',
        streamEvent: 'stream_started',
        streams: {
          s1: {
            channel: 'stream.echo.s1',
            direction: 'outbound',
            format: 'bytes',
            affinity: 'dedicated',
            token: 't7c-1',
            tokenTtlMinutes: 62,
          },
        },
      },
    ];
    const pubnub = hoisted.createMockPubNub();
    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: 't4',
      agentName: 'parity_events',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pubnub as any,
      sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
      preloadedEvents: events,
    });

    const listed = session.listEvents();

    expect(listed.map((event) => [event.type, event.streamEvent])).toEqual([
      ['progress', undefined],
      ['artifact', undefined],
      ['terminal', undefined],
      ['progress', 'stream_started'],
    ]);
    expect(listed[0]).toBe(events[0]);
    expect(listed).not.toBe(events);

    session.close();
  });
});
