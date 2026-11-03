import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  CHECKPOINT_FLUSH_MS,
  CHECKPOINT_MAX_EVENTS,
  type CheckpointBatch,
  CheckpointLoop,
} from '../src/runtime/sessions/provider-checkpoints.js';
import { ProviderApiError } from '../src/runtime/sessions/provider-api.js';
import { RateLimitedError } from '../src/runtime/rate-limit.js';

const ready = (sessionId: string) => ({ type: 'session_ready' as const, sessionId });

function loop(post: (batch: CheckpointBatch) => Promise<void>) {
  const sent: CheckpointBatch[] = [];
  const checkpoints = new CheckpointLoop({
    routeId: 'route1',
    post: (batch) => {
      sent.push(structuredClone(batch));
      return post(batch);
    },
    openSessions: () => ['s1'],
    onRouteLost: () => {},
  });
  checkpoints.start();
  return { checkpoints, sent };
}

async function tick() {
  await vi.advanceTimersByTimeAsync(CHECKPOINT_FLUSH_MS);
}

describe('checkpoint loop', () => {
  let stop: (() => void) | undefined;
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    stop?.();
    vi.useRealTimers();
  });

  it('sends an empty batch as the heartbeat on every tick', async () => {
    const { checkpoints, sent } = loop(async () => {});
    stop = () => checkpoints.stop();
    await tick();
    await tick();
    expect(sent).toHaveLength(2);
    for (const batch of sent) {
      expect(batch).toMatchObject({
        routeId: 'route1',
        openSessions: ['s1'],
        events: [],
        probeEchoes: [],
      });
    }
    expect(sent[0].idempotencyKey).not.toBe(sent[1].idempotencyKey);
  });

  it('retains a failed batch and replays it, same key, before later batches', async () => {
    const post = vi.fn(async () => {});
    post.mockRejectedValueOnce(new Error('503'));
    const { checkpoints, sent } = loop(post);
    stop = () => checkpoints.stop();

    checkpoints.record(ready('a'));
    await tick();
    checkpoints.record(ready('b'));
    await tick();

    expect(sent.map((b) => b.events.map((e) => e.sessionId))).toEqual([['a'], ['a'], ['b']]);
    expect(sent[1].idempotencyKey).toBe(sent[0].idempotencyKey);
    expect(sent[2].idempotencyKey).not.toBe(sent[0].idempotencyKey);
  });

  it.each([400, 413])('drops a batch refused with %s instead of replaying it', async (status) => {
    const post = vi.fn(async () => {});
    post.mockRejectedValueOnce(new ProviderApiError(status, undefined, 'refused'));
    const { checkpoints, sent } = loop(post);
    stop = () => checkpoints.stop();

    checkpoints.record(ready('a'));
    await tick();
    checkpoints.record(ready('b'));
    await tick();

    expect(sent.map((b) => b.events.map((e) => e.sessionId))).toEqual([['a'], ['b']]);
  });

  it('posts nothing until a rate-limited failure\'s Retry-After has elapsed', async () => {
    const post = vi.fn(async () => {});
    post.mockRejectedValueOnce(new RateLimitedError('Token refresh rate limited (HTTP 429)', 10_000));
    const { checkpoints, sent } = loop(post);
    stop = () => checkpoints.stop();
    await tick();
    checkpoints.record(ready('s1'));
    // The tick that lands exactly on the deadline is the first to post again.
    await vi.advanceTimersByTimeAsync(9_999);
    expect(sent).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(sent).toHaveLength(2);
    expect(sent[1].events).toEqual([ready('s1')]);
  });

  it(`flushes at ${CHECKPOINT_MAX_EVENTS} events without waiting for the tick`, async () => {
    const { checkpoints, sent } = loop(async () => {});
    stop = () => checkpoints.stop();
    for (let i = 0; i < CHECKPOINT_MAX_EVENTS; i++) checkpoints.record(ready(`s${i}`));
    await vi.advanceTimersByTimeAsync(0);
    expect(sent).toHaveLength(1);
    expect(sent[0].events).toHaveLength(CHECKPOINT_MAX_EVENTS);
  });

  it('echoes each probe nonce once in the next batch, and replays it with that batch on failure', async () => {
    const post = vi.fn(async () => {});
    post.mockRejectedValueOnce(new Error('503'));
    const { checkpoints, sent } = loop(post);
    stop = () => checkpoints.stop();

    checkpoints.echoProbe('n-1');
    checkpoints.echoProbe('n-1');
    await tick();
    await tick();
    await tick();

    expect(sent.map((b) => b.probeEchoes)).toEqual([['n-1'], ['n-1'], []]);
    expect(sent[1].idempotencyKey).toBe(sent[0].idempotencyKey);
  });

  it.each([
    { name: 'a non-string', nonce: 42 },
    { name: 'a missing nonce', nonce: undefined },
    { name: 'an empty string', nonce: '' },
    { name: 'a nonce over 256 characters', nonce: 'n'.repeat(257) },
  ])('drops $name rather than echo what the backend would refuse', async ({ nonce }) => {
    const { checkpoints, sent } = loop(async () => {});
    stop = () => checkpoints.stop();
    checkpoints.echoProbe(nonce);
    await tick();
    expect(sent.map((b) => b.probeEchoes)).toEqual([[]]);
  });

  it('echoes a 256-character nonce, and at most 256 nonces in one batch', async () => {
    const { checkpoints, sent } = loop(async () => {});
    stop = () => checkpoints.stop();
    const longest = 'n'.repeat(256);
    checkpoints.echoProbe(longest);
    for (let i = 1; i <= 256; i++) checkpoints.echoProbe(`n${i}`);
    await tick();
    expect(sent[0].probeEchoes).toHaveLength(256);
    expect(sent[0].probeEchoes[0]).toBe(longest);
  });
});
