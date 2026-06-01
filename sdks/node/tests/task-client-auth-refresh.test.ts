/**
 * BLOCKS-433: TaskClient must fail fast with AuthRefreshFailedError when the
 * underlying ConsumerAuth is in a known-broken refresh state, instead of
 * waiting for the next 401 round-trip (which Node SDK swallowed silently
 * pre-fix; Python's was strictly worse because logging.warning is silent
 * without a configured handler).
 *
 * Constructs the broken client through the public `TaskClient.create()`
 * factory — assigning the private `_consumerAuth` directly would leave
 * the production wiring (in `create()`) unproven against future refactors.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { TaskClient } from '../src/runtime/task-client.js';
import { AuthRefreshFailedError, type TokenResult } from '../src/runtime/consumer-auth.js';

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

vi.mock('../src/runtime/cdm-config.js', () => ({
  DEFAULT_CDM_URL: 'https://test-cdm.example.com/config.json',
  fetchCdmConfig: vi.fn().mockResolvedValue({
    playground: { publishKey: 'pub-test', subscribeKey: 'sub-test' },
    network: { publishKey: 'pub-test', subscribeKey: 'sub-test' },
    api: { baseUrl: 'http://localhost:3001' },
  }),
}));

function getRecordedError(client: TaskClient): Error | null {
  // ConsumerAuth is wired up internally; reach in via the same
  // authProvider surface the SDK transports use rather than poking
  // at the private `_consumerAuth` field.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const provider = (client as unknown as { config: { authProvider?: any } }).config.authProvider;
  return provider?.getLastAuthError?.() ?? null;
}

describe('BLOCKS-433: TaskClient fails fast when ConsumerAuth has lastAuthError', () => {
  const originalFetch = globalThis.fetch;
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
    vi.useFakeTimers();
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.useRealTimers();
  });

  async function makeBrokenClient(): Promise<{
    client: TaskClient;
    provider: ReturnType<typeof vi.fn<() => Promise<TokenResult>>>;
  }> {
    // Fail every refresh attempt after the initial bootstrap so the
    // proactive refresh cycle exhausts its 3 retries and ConsumerAuth
    // records an AuthRefreshFailedError. After bootstrap, all subsequent
    // calls reject permanently — including the preflight's reactive
    // recovery attempt — so the recorded error stays set and the
    // `TaskClient` method we exercise sees it on the way out.
    let calls = 0;
    const provider = vi.fn<() => Promise<TokenResult>>().mockImplementation(async () => {
      calls += 1;
      if (calls === 1) {
        return { token: 'jwt-init', expiresIn: 100, userId: 'u-1' };
      }
      throw new Error(`refresh fail ${calls}`);
    });

    const client = await TaskClient.create({
      billingMode: 'free',
      tokenProvider: provider,
    });

    // Drive the proactive refresh through its 3 retries.
    await vi.advanceTimersByTimeAsync(80_000);
    await vi.advanceTimersByTimeAsync(5_000);
    await vi.advanceTimersByTimeAsync(10_000);
    return { client, provider };
  }

  it('sendMessage() throws AuthRefreshFailedError before any fetch', async () => {
    const { client } = await makeBrokenClient();
    fetchSpy.mockClear();

    await expect(
      client.sendMessage({ agentName: 'echo', requestParts: [] }),
    ).rejects.toBeInstanceOf(AuthRefreshFailedError);

    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('connect() throws AuthRefreshFailedError before any fetch', async () => {
    const { client } = await makeBrokenClient();
    fetchSpy.mockClear();

    await expect(
      client.connect({ taskId: 'task-1' }),
    ).rejects.toBeInstanceOf(AuthRefreshFailedError);

    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('getTask() throws AuthRefreshFailedError before any fetch', async () => {
    const { client } = await makeBrokenClient();
    fetchSpy.mockClear();

    await expect(client.getTask('task-1')).rejects.toBeInstanceOf(
      AuthRefreshFailedError,
    );

    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('listTasks() throws AuthRefreshFailedError before any fetch', async () => {
    const { client } = await makeBrokenClient();
    fetchSpy.mockClear();

    await expect(client.listTasks()).rejects.toBeInstanceOf(
      AuthRefreshFailedError,
    );

    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('cancelTask() throws AuthRefreshFailedError before any fetch', async () => {
    const { client } = await makeBrokenClient();
    fetchSpy.mockClear();

    await expect(client.cancelTask('task-1')).rejects.toBeInstanceOf(
      AuthRefreshFailedError,
    );

    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it('preflight reactive recovery clears the error and lets the call proceed', async () => {
    // Bootstrap, then 3 rejections to wedge proactive refresh, then a
    // successful recovery on the preflight's reactive attempt. The
    // sendMessage call should clear the error and continue past the
    // preflight (it'll fail later because we don't mock the full RPC
    // surface, but that failure must NOT be AuthRefreshFailedError).
    let calls = 0;
    const provider = vi.fn<() => Promise<TokenResult>>().mockImplementation(async () => {
      calls += 1;
      if (calls === 1) return { token: 'jwt-init', expiresIn: 100, userId: 'u-1' };
      if (calls <= 4) throw new Error(`proactive fail ${calls}`);
      return { token: 'jwt-recovered', expiresIn: 100, userId: 'u-1' };
    });

    const client = await TaskClient.create({
      billingMode: 'free',
      tokenProvider: provider,
    });
    await vi.advanceTimersByTimeAsync(80_000);
    await vi.advanceTimersByTimeAsync(5_000);
    await vi.advanceTimersByTimeAsync(10_000);

    // Confirm we are wedged before the preflight runs.
    expect(getRecordedError(client)).toBeInstanceOf(
      AuthRefreshFailedError,
    );

    // Stub fetch so the post-preflight RPC fails with a non-auth error.
    fetchSpy.mockResolvedValue(
      new Response(JSON.stringify({ jsonrpc: '2.0', id: '1', error: { code: -32000, message: 'rpc disabled in test' } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );

    await expect(
      client.sendMessage({ agentName: 'echo', requestParts: [] }),
    ).rejects.not.toBeInstanceOf(AuthRefreshFailedError);

    // Recovery cleared the recorded error and the new token is live.
    expect(getRecordedError(client)).toBeNull();
  });
});
