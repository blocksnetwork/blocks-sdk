/**
 * Consumer-writer split test for shared_up (shared-stream lifecycle,
 * consumer-writer coverage).
 *
 * On `shared_up` (affinity: 'shared', agentDirection: 'inbound'),
 * consumer direction inverts to 'outbound' — the consumer builds a
 * StreamClient via `fromDescriptor` that is the WRITER on the shared
 * channel. The agent is the reader.
 *
 * Contract: consumer-writer `StreamClient.end()` MUST NOT publish
 * `stream_end` on a shared channel. The affinity gate sits inside
 * `StreamClient.end()` so both the producer-side StreamClient and the
 * consumer-side StreamClient built via `fromDescriptor` inherit the
 * same rule — fix (c) and fix (f) collapsed into a single gate.
 *
 * Assertions mirror Python `tests/test_shared_up_consumer_writer.py`.
 */

/* eslint-disable @typescript-eslint/no-explicit-any */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { StreamClient, _resetUuidCounter } from '../src/stream/stream-client.js';
import type { StreamDescriptor } from '../src/stream/descriptor.js';

// Track every PubNub publish call so we can assert which ones landed
// on the shared channel. Shared `stream_end` would surface here.
const mockPublish = vi.fn().mockResolvedValue({ timetoken: '17000000000000000' });

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation(() => ({
      setToken: vi.fn(),
      setFilterExpression: vi.fn(),
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

beforeEach(() => {
  vi.clearAllMocks();
  _resetUuidCounter();
  mockPublish.mockClear();
});

function makeConsumerWriterDescriptor(
  overrides: Partial<StreamDescriptor> = {},
): StreamDescriptor {
  return {
    taskId: 'task-c1',
    streamId: 'shared_up',
    agentName: 'sharedup_test',
    channel: 'stream.sharedup_test.shared_up',
    token: 'T7c-c1',
    agentDirection: 'inbound',
    localDirection: 'outbound',
    format: 'events',
    affinity: 'shared',
    declaredStream: 'shared_up',
    ...overrides,
  };
}

function endMarkerPublishes(): unknown[] {
  return mockPublish.mock.calls
    .map((c) => c[0]?.message as Record<string, unknown> | undefined)
    .filter((m): m is Record<string, unknown> =>
      !!m && typeof m === 'object' && (m as { type?: string }).type === 'stream_end',
    );
}

describe('shared_up consumer-writer: no stream_end on end()', () => {
  it('two consumer-writers on a shared_up channel end() without publishing stream_end', async () => {
    // Two distinct consumer tasks, both building a writer-side
    // StreamClient from a shared-affinity descriptor.
    const descA = makeConsumerWriterDescriptor({ taskId: 'task-c1', token: 'T7c-c1' });
    const descB = makeConsumerWriterDescriptor({ taskId: 'task-c2', token: 'T7c-c2' });

    const client1 = StreamClient.fromDescriptor(descA, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
    });
    const client2 = StreamClient.fromDescriptor(descB, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
    });

    // Both consumers are writers on the shared channel (outbound local).
    expect(client1.isActive).toBe(true);
    expect(client2.isActive).toBe(true);

    // Each writes one event so the writer has a non-trivial lifecycle
    // before ending.
    client1.write({ from: 'c1', seq: 1 });
    client2.write({ from: 'c2', seq: 1 });

    await client1.end();
    await client2.end();

    // Core assertion: NO `stream_end` marker was published to the
    // shared channel from either consumer-writer's cleanup path.
    expect(endMarkerPublishes()).toHaveLength(0);

    // Both clients became inactive.
    expect(client1.isActive).toBe(false);
    expect(client2.isActive).toBe(false);
  });

  it('regression gate: dedicated consumer-writer still publishes stream_end', async () => {
    // Sanity: the gate is specific to affinity: 'shared'. A dedicated
    // consumer-writer (rare but symmetric) MUST still publish the
    // marker — over-broad suppression would regress the dedicated
    // stream contract (marker emission on per-task cleanup).
    const desc = makeConsumerWriterDescriptor({
      streamId: 'ded_up',
      channel: 'stream.sharedup_test.ded_up',
      declaredStream: 'ded_up',
      affinity: 'dedicated',
    });

    const client = StreamClient.fromDescriptor(desc, {
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
    });
    client.write({ seq: 1 });
    await client.end();

    expect(endMarkerPublishes()).toHaveLength(1);
  });
});
