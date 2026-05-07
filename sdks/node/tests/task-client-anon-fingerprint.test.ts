/**
 * TaskClient anonymous-fingerprint mode tests — IMPL §6 of
 * `dev_docs/initiative/04-30_anon_playground_artifacts`.
 *
 * Anonymous consumer mode:
 * - `TaskClient.create({ anonFingerprint, billingMode: 'free' })` builds a
 *   TaskClient that skips the authProvider path and mints T4 read tokens
 *   via `POST /api/v1/auth/anon-task-read-token` with `{ taskId, fingerprint }`.
 * - Mutually exclusive with `apiKey` / `tokenEndpoint` / `tokenProvider`.
 * - Only valid for `billingMode === 'free'`.
 * - `connect()` on an anon TaskClient must never send an Authorization header
 *   on the read-token request.
 * - A 403 from the anon read-token endpoint surfaces as
 *   `AnonTaskAccessDeniedError` whose `.message` contains the substring
 *   `403` (the Playground frontend relies on a `/\b403\b/` regex match
 *   to fall back to the sanitized-record view).
 * - `sendMessage()` is not supported on anon clients.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  TaskClient,
  AnonTaskAccessDeniedError,
} from '../src/runtime/task-client.js';

// Keep the same static mocking shape the other consumer tests use.
vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation(() => ({
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    destroy: vi.fn(),
    setToken: vi.fn(),
    fetchMessages: vi.fn().mockResolvedValue({ channels: {} }),
    time: vi.fn().mockResolvedValue({ timetoken: '17000000000000000' }),
  })),
}));

vi.mock('../src/runtime/cdm-config.js', () => ({
  fetchCdmConfig: vi.fn().mockResolvedValue({
    playground: { subscribeKey: 'sub-playground', publishKey: 'pub-playground' },
    network: { subscribeKey: 'sub-network', publishKey: 'pub-network' },
    api: { baseUrl: 'http://cdm-backend.example.com' },
  }),
  DEFAULT_CDM_URL: 'https://mock-cdm.example.com/config.json',
}));

vi.mock('../src/env.js', () => ({
  getEnv: vi.fn().mockReturnValue(undefined),
}));

const originalFetch = globalThis.fetch;
let fetchSpy: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchSpy = vi.fn();
  globalThis.fetch = fetchSpy;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
});

// ===========================================================================
// create() surface
// ===========================================================================

describe('TaskClient.create({ anonFingerprint }) — surface validation', () => {
  it('succeeds with billingMode:free and does not invoke an authProvider', async () => {
    // No fetch calls should fire beyond the CDM (and CDM itself is mocked
    // out of fetch). No token endpoint call should happen at create-time.
    const client = await TaskClient.create({
      anonFingerprint: 'fp-abc',
      billingMode: 'free',
    });

    // No authProvider was attached: internal config should expose the
    // anon fingerprint instead.
    const anon = (client as unknown as { _anonFingerprint: string | null })._anonFingerprint;
    expect(anon).toBe('fp-abc');

    // No authProvider path fired: getUserId returns null (ConsumerAuth
    // was never constructed).
    expect(client.getUserId()).toBeNull();

    client.destroy();
  });

  it('throws when billingMode is paid', async () => {
    await expect(
      TaskClient.create({ anonFingerprint: 'fp-abc', billingMode: 'paid' }),
    ).rejects.toThrow(/anonFingerprint.*billingMode.*free/i);
  });

  it('throws when combined with tokenEndpoint (mutual exclusion)', async () => {
    await expect(
      TaskClient.create({
        anonFingerprint: 'fp-abc',
        billingMode: 'free',
        tokenEndpoint: 'http://proxy.example/token',
      }),
    ).rejects.toThrow(/Only one token provider mode/);
  });

  it('throws when combined with apiKey (mutual exclusion)', async () => {
    await expect(
      TaskClient.create({
        anonFingerprint: 'fp-abc',
        billingMode: 'free',
        apiKey: 'bk_test_key',
      }),
    ).rejects.toThrow(/Only one token provider mode/);
  });

  it('throws when combined with tokenProvider (mutual exclusion)', async () => {
    await expect(
      TaskClient.create({
        anonFingerprint: 'fp-abc',
        billingMode: 'free',
        tokenProvider: async () => ({ token: 't', expiresIn: 60 }),
      }),
    ).rejects.toThrow(/Only one token provider mode/);
  });
});

// ===========================================================================
// connect() — anon-mode request shape + 403 mapping
// ===========================================================================

describe('TaskClient.connect() — anon mode', () => {
  it('calls /auth/anon-task-read-token with { taskId, fingerprint } and no Authorization header', async () => {
    const client = await TaskClient.create({
      anonFingerprint: 'fp-owner',
      billingMode: 'free',
    });

    // Mock getTask RPC (anon GetTask on public+free returns sanitized DTO).
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          task: {
            taskId: 'task-anon-1',
            agentName: 'echo_agent',
            state: 'completed',
            owner: 'anonymous-user-id',
          },
        },
      }),
    });

    // Mock anon-task-read-token response
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        pamToken: 'pam-anon-123',
        channel: 'u.anon-org.task-anon-1',
        ttlMinutes: 60,
      }),
    });

    const PubNub = (await import('pubnub')).default;
    const mockPubNub = new PubNub({ subscribeKey: 'sub-playground', userId: 't' });
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (mockPubNub as any).fetchMessages = vi.fn().mockResolvedValue({ channels: {} });
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (client as any).createPerSessionPubNub = () => mockPubNub;

    const session = await client.connect({ taskId: 'task-anon-1' });
    expect(session).toBeDefined();

    // fetchSpy call 0 = getTask RPC, call 1 = anon-task-read-token
    expect(fetchSpy.mock.calls.length).toBeGreaterThanOrEqual(2);
    const tokenCall = fetchSpy.mock.calls[1];
    const [url, init] = tokenCall;
    expect(String(url)).toContain('/api/v1/auth/anon-task-read-token');
    expect(init.method).toBe('POST');

    // Body contains taskId + fingerprint, NOT role:'consumer'.
    const body = JSON.parse(init.body);
    expect(body).toEqual({ taskId: 'task-anon-1', fingerprint: 'fp-owner' });

    // No Authorization header — not even an empty one.
    const headerEntries = Object.keys(init.headers ?? {});
    expect(headerEntries).not.toContain('Authorization');
    expect(headerEntries).not.toContain('authorization');

    session.close();
    client.destroy();
  });

  it('propagates a 403 response as AnonTaskAccessDeniedError whose message contains "403"', async () => {
    const client = await TaskClient.create({
      anonFingerprint: 'fp-wrong',
      billingMode: 'free',
    });

    // getTask succeeds (public+free task)
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          task: {
            taskId: 'task-anon-2',
            agentName: 'echo_agent',
            state: 'running',
            owner: 'anonymous-user-id',
          },
        },
      }),
    });

    // Fingerprint mismatch: endpoint returns 403.
    fetchSpy.mockResolvedValueOnce({
      ok: false,
      status: 403,
      text: async () => 'Not authorized to view this task',
    });

    let thrown: unknown;
    try {
      await client.connect({ taskId: 'task-anon-2' });
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(AnonTaskAccessDeniedError);
    // Frontend depends on `/\b403\b/` regex matching the error message.
    expect((thrown as Error).message).toMatch(/\b403\b/);

    client.destroy();
  });
});

// ===========================================================================
// sendMessage() guard
// ===========================================================================

describe('TaskClient.sendMessage() on anon client', () => {
  it('throws the typed guard error before any RPC call', async () => {
    const client = await TaskClient.create({
      anonFingerprint: 'fp-any',
      billingMode: 'free',
    });

    await expect(
      client.sendMessage({
        agentName: 'echo_agent',
        requestParts: [{ text: 'hi' }],
      }),
    ).rejects.toThrow('anon-mode TaskClient does not support sendMessage()');

    // Confirm no network call fired.
    expect(fetchSpy).not.toHaveBeenCalled();

    client.destroy();
  });
});
