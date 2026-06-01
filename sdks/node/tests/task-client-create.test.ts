/**
 * TaskClient.create — billingMode parity test.
 *
 * Cross-SDK parity: this mapping MUST match the Python SDK's
 * `TaskClient.create` billing_mode routing, and the service's
 * `BILLING_MODE_TO_KEYSET` map.
 *
 * - `billingMode === 'free'`  → playground keyset
 * - `billingMode === 'paid'`  → network keyset
 * - missing `billingMode`     → rejected with the exact error message
 *   `"TaskClient.create() requires a billingMode option ('free' or 'paid')"`
 *
 * Mirrors the service's single-source-of-truth policy.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { TaskClient } from '../src/runtime/task-client.js';

// Keep the same static mocking shape the other consumer tests use.
vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation(() => ({
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    destroy: vi.fn(),
    setToken: vi.fn(),
    fetchMessages: vi.fn(),
    time: vi.fn().mockResolvedValue({ timetoken: '17000000000000000' }),
  })),
}));

const FAKE_CDM = {
  playground: { subscribeKey: 'sub-pg', publishKey: 'pub-pg' },
  network: { subscribeKey: 'sub-net', publishKey: 'pub-net' },
  api: { baseUrl: 'https://api.blocks.test' },
};

// Expected parity rows — must agree with:
//   service: `BILLING_MODE_TO_KEYSET` map
//   python:  TaskClient.create mapping in blocks-sdk/sdks/python/blocks_network/task_client.py
const BILLING_MODE_TO_KEYSET_ENV: Record<'free' | 'paid', 'playground' | 'network'> = {
  free: 'playground',
  paid: 'network',
};

describe('TaskClient.create — billingMode parity', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;
  const originalEnv = { ...process.env };

  beforeEach(() => {
    fetchSpy = vi.fn().mockResolvedValue({ ok: true, json: async () => FAKE_CDM });
    globalThis.fetch = fetchSpy;
    delete process.env.BLOCKS_SUBSCRIBE_KEY;
    delete process.env.BLOCKS_PUBLISH_KEY;
    delete process.env.BLOCKS_BACKEND_URL;
    delete process.env.BLOCKS_CDM_URL;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    Object.assign(process.env, originalEnv);
  });

  it('rejects missing billingMode with the exact parity error string', async () => {
    await expect(
      TaskClient.create({} as Parameters<typeof TaskClient.create>[0]),
    ).rejects.toThrow(
      "TaskClient.create() requires a billingMode option ('free' or 'paid')",
    );
  });

  it('rejects invalid billingMode (typo) instead of silently routing to playground', async () => {
    // Regression guard: a truthy-but-invalid string used to slip past the
    // truthy-only check and silently route to the playground keyset (the
    // ternary at the keyset-resolution site treats any non-'paid' value as
    // playground). JS callers and stale TS callers can pass anything truthy;
    // reject early like the constructor does and like the Python SDK does.
    await expect(
      TaskClient.create({ billingMode: 'pad' as unknown as 'free' | 'paid' }),
    ).rejects.toThrow(
      "TaskClient.create() requires a billingMode option ('free' or 'paid')",
    );
    await expect(
      TaskClient.create({ billingMode: 'PAID' as unknown as 'free' | 'paid' }),
    ).rejects.toThrow(
      "TaskClient.create() requires a billingMode option ('free' or 'paid')",
    );
  });

  it('maps billingMode=free → playground keyset (subscribeKey from cdm.playground)', async () => {
    const client = await TaskClient.create({ billingMode: 'free' });
    // Pull the subscribeKey the TaskClient resolved to compare against CDM playground keyset.
    const resolved = (client as unknown as { _subscribeKey: string })._subscribeKey;
    expect(resolved).toBe(FAKE_CDM.playground.subscribeKey);
    expect(BILLING_MODE_TO_KEYSET_ENV.free).toBe('playground');
  });

  it('maps billingMode=paid → network keyset (subscribeKey from cdm.network)', async () => {
    const client = await TaskClient.create({ billingMode: 'paid' });
    const resolved = (client as unknown as { _subscribeKey: string })._subscribeKey;
    expect(resolved).toBe(FAKE_CDM.network.subscribeKey);
    expect(BILLING_MODE_TO_KEYSET_ENV.paid).toBe('network');
  });

  it('BILLING_MODE_TO_KEYSET_ENV parity table has exactly two entries', () => {
    // Parity guard: if this test changes, the backend policy map and
    // Python SDK parity test must change in lockstep.
    const keys = Object.keys(BILLING_MODE_TO_KEYSET_ENV).sort();
    expect(keys).toEqual(['free', 'paid']);
    expect(BILLING_MODE_TO_KEYSET_ENV.free).toBe('playground');
    expect(BILLING_MODE_TO_KEYSET_ENV.paid).toBe('network');
  });

  it('does not call the registry during TaskClient.create (consumer must declare billingMode)', async () => {
    // BMC: TaskClient.create maps billingMode -> keyset directly with no
    // registry GET. The only network call should be the CDM fetch.
    await TaskClient.create({ billingMode: 'paid' });

    // The lone fetch must be the CDM URL, not /api/v1/registry/agents.
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const calls = fetchSpy.mock.calls.map((c) => String(c[0] ?? ''));
    for (const url of calls) {
      expect(url).not.toContain('/api/v1/registry/agents');
    }
  });

  it('direct TaskClient constructor requires billingMode', () => {
    // Parity with TaskClient.create: the underlying constructor MUST
    // also reject missing billingMode so callers cannot bypass the
    // contract by skipping the factory.
    expect(
      () => new TaskClient({
        subscribeKey: 'sub-c-test',
      } as unknown as ConstructorParameters<typeof TaskClient>[0]),
    ).toThrow("TaskClient requires a billingMode option ('free' or 'paid')");
  });

  it('direct TaskClient constructor rejects invalid billingMode value', () => {
    expect(
      () => new TaskClient({
        billingMode: 'network' as unknown as 'free' | 'paid',
        subscribeKey: 'sub-c-test',
      }),
    ).toThrow("TaskClient requires a billingMode option ('free' or 'paid')");
  });
});
