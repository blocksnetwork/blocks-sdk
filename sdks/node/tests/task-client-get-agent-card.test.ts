/**
 * `TaskClient.getAgentCard()` — credential forwarding and auth-failure behaviour.
 *
 * `agent-registry-auth.test.ts` covers the standalone registry helpers, which
 * take an explicit `apiKey`. This covers the client method, whose whole job is to
 * derive that credential from the auth provider: prefix handling, lazy provider
 * init, the reactive refresh driven off an empty result, and the two different
 * meanings an empty result can carry.
 *
 * The distinction that needs pinning: on a Blocks Enterprise deployment a
 * *rejected* credential produces the same 404 as a *missing agent*, because the
 * registry read is on optional auth and degrades a bad bearer to anonymous rather
 * than 401ing. So `null` must mean "no such agent" and an auth failure must
 * raise — but only on evidence the provider actually has one, or every ordinary
 * missing-agent lookup through a static provider would throw.
 */

import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AgentAuth } from '../src/runtime/agent-auth.js';
import type { AuthProvider } from '../src/runtime/auth-provider.js';
import { TaskClient } from '../src/runtime/task-client.js';

vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation(() => ({
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setToken: vi.fn(),
  })),
}));

const BASE_URL = 'http://test-api.example.com';

function jsonResponse(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers(),
    json: async () => body,
    text: async () => JSON.stringify(body),
  };
}

const found = () => jsonResponse(200, { agent: { agentName: 'a', card: { name: 'a' } } });
const notFound = () => jsonResponse(404, {});

/** Stubs fetch with one response per call, last value repeating. */
function stubFetch(...responses: Array<() => unknown>) {
  const fetchMock = vi.fn();
  responses.forEach((r) => fetchMock.mockResolvedValueOnce(r()));
  fetchMock.mockResolvedValue(responses[responses.length - 1]());
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

/** An `AgentAuth` stub. Only `getAccessToken` and `refresh` are reached from this
 *  surface, so the cast is confined here rather than repeated at each call site.
 *  `refresh` defaults to a no-op that leaves the token as-is. */
function fakeAgentAuth(
  token: string | null,
  refresh: () => Promise<void> = async () => {},
): AgentAuth {
  let current = token;
  return {
    getAccessToken: () => current,
    refresh: async () => {
      await refresh();
    },
    // Exposed so a test can rotate what the stub hands out from inside `refresh`.
    _set: (t: string | null) => {
      current = t;
    },
  } as unknown as AgentAuth;
}

function makeClient(authProvider?: AuthProvider) {
  return new TaskClient({
    billingMode: 'free',
    subscribeKey: 'sub-c-test',
    baseUrl: BASE_URL,
    authProvider,
  });
}

/** `registryFetch` always normalises to a `Headers`, so read through `get()`. */
function sentAuth(fetchMock: ReturnType<typeof vi.fn>, call = -1): string | null {
  const init = fetchMock.mock.calls.at(call)?.[1] as RequestInit | undefined;
  const headers = init?.headers;
  return headers instanceof Headers ? headers.get('Authorization') : null;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('getAgentCard — credential forwarding', () => {
  it('forwards the provider credential as a single Bearer token', async () => {
    const fetchMock = stubFetch(found);
    const provider: AuthProvider = {
      getAuthHeader: () => 'Bearer jwt-abc',
      onAuthFailure: async () => false,
    };

    const card = await makeClient(provider).getAgentCard('a');

    // Not `Bearer Bearer jwt-abc`: the method strips the prefix off the header
    // because `getAgent` re-adds it around the raw credential.
    expect(sentAuth(fetchMock)).toBe('Bearer jwt-abc');
    expect(card).toEqual({ name: 'a' });
  });

  it('adds the Bearer prefix when the provider header omits it', async () => {
    const fetchMock = stubFetch(found);
    const provider: AuthProvider = {
      getAuthHeader: () => 'raw-token',
      onAuthFailure: async () => false,
    };

    await makeClient(provider).getAgentCard('a');

    expect(sentAuth(fetchMock)).toBe('Bearer raw-token');
  });

  it('sends no Authorization header when the client has no provider', async () => {
    const fetchMock = stubFetch(found);

    const card = await makeClient().getAgentCard('a');

    expect(sentAuth(fetchMock)).toBeNull();
    expect(card).toEqual({ name: 'a' });
  });

  it('initializes the provider before reading its header', async () => {
    // Agent-side clients had not always initialized the provider by the time a
    // card was requested, so an uninitialized ConsumerAuth read null and the
    // lookup went out anonymous.
    const order: string[] = [];
    stubFetch(found);
    const provider: AuthProvider = {
      getAuthHeader: () => {
        order.push('header');
        return 'Bearer t';
      },
      onAuthFailure: async () => false,
      ensureReady: async () => {
        order.push('ensureReady');
      },
    };

    await makeClient(provider).getAgentCard('a');

    expect(order).toEqual(['ensureReady', 'header']);
  });

  it('forwards an agentAuth access token when there is no authProvider', async () => {
    // `agentAuth` is the other supported way to construct an authenticated
    // client, and the RPC and file-upload paths both honour it. An agent-side
    // client that reads `null` on Enterprise while its other calls succeed is
    // the exact regression this guards.
    const fetchMock = stubFetch(found);

    const card = await new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-c-test',
      baseUrl: BASE_URL,
      agentAuth: fakeAgentAuth('agent-jwt'),
    }).getAgentCard('a');

    expect(sentAuth(fetchMock)).toBe('Bearer agent-jwt');
    expect(card).toEqual({ name: 'a' });
  });

  it('prefers authProvider over agentAuth when both are configured', async () => {
    const fetchMock = stubFetch(found);
    const provider: AuthProvider = {
      getAuthHeader: () => 'Bearer consumer-jwt',
      onAuthFailure: async () => false,
    };

    await new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-c-test',
      baseUrl: BASE_URL,
      authProvider: provider,
      agentAuth: fakeAgentAuth('agent-jwt'),
    }).getAgentCard('a');

    expect(sentAuth(fetchMock)).toBe('Bearer consumer-jwt');
  });

  it('sends no header when agentAuth holds no token yet', async () => {
    const fetchMock = stubFetch(found);

    await new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-c-test',
      baseUrl: BASE_URL,
      agentAuth: fakeAgentAuth(null),
    }).getAgentCard('a');

    expect(sentAuth(fetchMock)).toBeNull();
  });

  it('refreshes a stale agentAuth token off an empty result and retries', async () => {
    // `AgentAuth.refresh()` is driven only by `authenticatedFetch`'s 401 retry,
    // and this read is on optional auth so it never 401s — and there is no
    // proactive scheduler. Without driving refresh here, a stale agent token
    // answers `null` for the client's lifetime.
    const fetchMock = stubFetch(notFound, found);
    let refreshed = false;
    const agentAuth = fakeAgentAuth('stale', async () => {
      refreshed = true;
      (agentAuth as unknown as { _set: (t: string) => void })._set('fresh');
    });

    const card = await new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-c-test',
      baseUrl: BASE_URL,
      agentAuth,
    }).getAgentCard('a');

    expect(refreshed).toBe(true);
    expect(card).toEqual({ name: 'a' });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(sentAuth(fetchMock, 0)).toBe('Bearer stale');
    expect(sentAuth(fetchMock, 1)).toBe('Bearer fresh');
  });

  it('surfaces an agentAuth refresh failure rather than reporting no agent', async () => {
    stubFetch(notFound);
    const fatal = new Error('API key invalid');
    fatal.name = 'AgentAuthFatalError';
    const agentAuth = fakeAgentAuth('stale', async () => {
      throw fatal;
    });

    await expect(
      new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: BASE_URL,
        agentAuth,
      }).getAgentCard('a'),
    ).rejects.toThrow('API key invalid');
  });

  it('returns null for a genuinely absent agent after a clean agentAuth refresh', async () => {
    // Refresh succeeds, the retry is still empty: the agent really is missing.
    const fetchMock = stubFetch(notFound, notFound);
    const agentAuth = fakeAgentAuth('valid');

    await expect(
      new TaskClient({
        billingMode: 'free',
        subscribeKey: 'sub-c-test',
        baseUrl: BASE_URL,
        agentAuth,
      }).getAgentCard('nope'),
    ).resolves.toBeNull();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('reads the header per call so a rotated token is not stale', async () => {
    const fetchMock = stubFetch(found, found);
    let token = 'first';
    const provider: AuthProvider = {
      getAuthHeader: () => `Bearer ${token}`,
      onAuthFailure: async () => false,
    };
    const client = makeClient(provider);

    await client.getAgentCard('a');
    token = 'second';
    await client.getAgentCard('a');

    expect(sentAuth(fetchMock, 0)).toBe('Bearer first');
    expect(sentAuth(fetchMock, 1)).toBe('Bearer second');
  });
});

describe('getAgentCard — auth failure vs missing agent', () => {
  it('raises a recorded auth error before issuing any request', async () => {
    const fetchMock = stubFetch(found);
    const recorded = new Error('refresh permanently failed');
    recorded.name = 'AuthRefreshFailedError';
    const provider: AuthProvider = {
      getAuthHeader: () => 'Bearer stale',
      onAuthFailure: async () => false,
      getLastAuthError: () => recorded,
    };

    await expect(makeClient(provider).getAgentCard('a')).rejects.toThrow(
      'refresh permanently failed',
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('drives one reactive refresh off an empty result and retries', async () => {
    // Nothing 401s — optional auth degrades a stale bearer to anonymous — so the
    // empty result is the only signal available to trigger the refresh.
    const fetchMock = stubFetch(notFound, found);
    let token = 'stale';
    const provider: AuthProvider = {
      getAuthHeader: () => `Bearer ${token}`,
      onAuthFailure: async () => {
        token = 'fresh';
        return true;
      },
    };

    const card = await makeClient(provider).getAgentCard('a');

    expect(card).toEqual({ name: 'a' });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(sentAuth(fetchMock, 0)).toBe('Bearer stale');
    expect(sentAuth(fetchMock, 1)).toBe('Bearer fresh');
  });

  it('raises when the refusal is unrecoverable and the provider recorded why', async () => {
    const failure = new Error('refresh rejected by backend');
    failure.name = 'AuthRefreshFailedError';
    let recorded: Error | null = null;
    const provider: AuthProvider = {
      getAuthHeader: () => 'Bearer stale',
      onAuthFailure: async () => {
        recorded = failure;
        return false;
      },
      getLastAuthError: () => recorded,
    };
    stubFetch(notFound);

    await expect(makeClient(provider).getAgentCard('a')).rejects.toThrow(
      'refresh rejected by backend',
    );
  });

  it('returns null for a provider that simply cannot refresh', async () => {
    // A static-token provider always answers `onAuthFailure()` false. That means
    // "no refresh possible", not "the credential was rejected" — so a genuinely
    // absent agent must stay null here rather than throwing an auth error.
    const fetchMock = stubFetch(notFound);
    const provider: AuthProvider = {
      getAuthHeader: () => 'Bearer static',
      onAuthFailure: async () => false,
      getLastAuthError: () => null,
    };

    await expect(makeClient(provider).getAgentCard('nope')).resolves.toBeNull();
    // One GET, not two: a provider that cannot refresh gets no retry, so the
    // extra lookup is spent only when a refresh actually succeeded.
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('returns null without a retry when the client has no provider', async () => {
    const fetchMock = stubFetch(notFound);

    await expect(makeClient().getAgentCard('nope')).resolves.toBeNull();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
