import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  fetchAgentRegistry,
  fetchAgentsByListing,
  fetchAgentsByTag,
  getAgent,
} from '../src/runtime/agent-registry.js';

// Registry reads are mounted on optional auth, so a credential is optional on
// Blocks Network. A Blocks Enterprise deployment serves agent metadata to
// authenticated callers only, so a caller that cannot send its credential reads
// nothing there even when correctly configured. These assert the credential
// reaches the wire.

const BASE_URL = 'http://test-api.example.com';

function captureFetch(body: unknown) {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => body,
    text: async () => JSON.stringify(body),
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

/** Authorization header from the most recent fetch call, or undefined.
 *
 * `registryFetch` normalises headers into a `Headers` instance before calling
 * fetch, so this must read through `Headers.get()`. Indexing `headers` as a plain
 * object yields `undefined` for every call, which would make the negative
 * assertions below pass for the wrong reason. */
function sentAuth(fetchMock: ReturnType<typeof vi.fn>): string | null {
  const init = fetchMock.mock.calls.at(-1)?.[1] as RequestInit | undefined;
  const headers = init?.headers;
  if (headers instanceof Headers) return headers.get('Authorization');
  if (headers && typeof headers === 'object') {
    return (headers as Record<string, string>).Authorization ?? null;
  }
  return null;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('getAgent — credential forwarding', () => {
  it('sends the credential as a bearer token when given one', async () => {
    const fetchMock = captureFetch({ agent: { agentName: 'a', card: {} } });
    await getAgent('a', { baseUrl: BASE_URL, apiKey: 'cred-123' });
    expect(sentAuth(fetchMock)).toBe('Bearer cred-123');
  });

  it('sends no Authorization header when given none', async () => {
    const fetchMock = captureFetch({ agent: { agentName: 'a', card: {} } });
    await getAgent('a', { baseUrl: BASE_URL });
    expect(sentAuth(fetchMock)).toBeNull();
  });
});

describe('fetchAgentRegistry — credential forwarding', () => {
  it('sends the credential as a bearer token when given one', async () => {
    const fetchMock = captureFetch({ agents: [], next: null, totalCount: 0 });
    await fetchAgentRegistry({ baseUrl: BASE_URL, apiKey: 'cred-456' });
    expect(sentAuth(fetchMock)).toBe('Bearer cred-456');
  });

  it('sends no Authorization header when given none', async () => {
    const fetchMock = captureFetch({ agents: [], next: null, totalCount: 0 });
    await fetchAgentRegistry({ baseUrl: BASE_URL });
    expect(sentAuth(fetchMock)).toBeNull();
  });
});

// Every exported registry read helper, so a newly added one is an obvious gap
// rather than a silent hole: on Enterprise a helper that cannot authenticate
// returns an empty page to a caller that holds a perfectly good credential.
describe('every registry list helper forwards a credential', () => {
  const LIST_BODY = { agents: [], next: null, totalCount: 0 };

  const helpers: Array<[string, (apiKey?: string) => Promise<unknown>]> = [
    ['fetchAgentRegistry', (apiKey) =>
      fetchAgentRegistry({ baseUrl: BASE_URL, apiKey })],
    ['fetchAgentsByTag', (apiKey) =>
      fetchAgentsByTag('t', { baseUrl: BASE_URL, apiKey })],
    ['fetchAgentsByListing', (apiKey) =>
      fetchAgentsByListing('public', { baseUrl: BASE_URL, apiKey })],
  ];

  it.each(helpers)('%s sends the credential', async (_name, call) => {
    const fetchMock = captureFetch(LIST_BODY);
    await call('cred-789');
    expect(sentAuth(fetchMock)).toBe('Bearer cred-789');
  });

  it.each(helpers)('%s sends nothing when given nothing', async (_name, call) => {
    const fetchMock = captureFetch(LIST_BODY);
    await call(undefined);
    expect(sentAuth(fetchMock)).toBeNull();
  });
});
