import { describe, it, expect, vi } from 'vitest';
import {
  composeSearchQuery,
  listAgentsAuthenticated,
  listAllAgentsAuthenticated,
} from '../src/registry-list.js';
import {
  PROTOCOL_VERSION_HEADER,
  CURRENT_PROTOCOL_VERSION,
} from '../src/protocol-headers.js';

function mockResponse(body: unknown, ok = true, status = 200): Response {
  return {
    ok,
    status,
    json: async () => body,
  } as Response;
}

describe('composeSearchQuery', () => {
  it('returns the query unchanged when no provider is given', () => {
    expect(composeSearchQuery('translate', undefined)).toBe('translate');
    expect(composeSearchQuery(undefined, undefined)).toBeUndefined();
  });

  it('emits a quoted provider qualifier when only a provider is given', () => {
    expect(composeSearchQuery(undefined, 'Acme Corp')).toBe('provider:"Acme Corp"');
  });

  it('combines free text and provider with a space (AND)', () => {
    expect(composeSearchQuery('translate', 'Acme')).toBe('translate provider:"Acme"');
  });

  it('trims whitespace around both inputs', () => {
    expect(composeSearchQuery('  translate  ', '  Acme  ')).toBe(
      'translate provider:"Acme"',
    );
  });

  it('treats a blank provider as absent', () => {
    expect(composeSearchQuery('translate', '   ')).toBe('translate');
    expect(composeSearchQuery(undefined, '   ')).toBeUndefined();
  });

  it('strips embedded quotes so the qualifier stays balanced', () => {
    expect(composeSearchQuery(undefined, 'Ac"me')).toBe('provider:"Acme"');
  });
});

describe('listAgentsAuthenticated', () => {
  it('sends Blocks-Protocol-Version header on every request', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [], totalCount: 0 }),
    );

    await listAgentsAuthenticated({
      baseUrl: 'http://api.test',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    expect(fetchImpl).toHaveBeenCalledOnce();
    const [, init] = fetchImpl.mock.calls[0];
    const headers = init?.headers as Record<string, string>;
    expect(headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
    expect(CURRENT_PROTOCOL_VERSION).toBe('2026-05-01');
  });

  it('protocol version header is present even when an API key is supplied', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [], totalCount: 0 }),
    );

    await listAgentsAuthenticated({
      baseUrl: 'http://api.test',
      apiKey: 'secret-key',
      listing: 'private',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const [, init] = fetchImpl.mock.calls[0];
    const headers = init?.headers as Record<string, string>;
    expect(headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
    expect(headers['Authorization']).toBe('Bearer secret-key');
  });

  it('builds the registry URL with include=full and listing/scope params', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [], totalCount: 0 }),
    );

    await listAgentsAuthenticated({
      baseUrl: 'http://api.test/',
      listing: 'private',
      tag: 'translate',
      limit: 50,
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const [url] = fetchImpl.mock.calls[0];
    const parsed = new URL(String(url));
    expect(parsed.pathname).toBe('/api/v1/registry/agents');
    expect(parsed.searchParams.get('include')).toBe('full');
    expect(parsed.searchParams.get('listing')).toBe('private');
    expect(parsed.searchParams.get('scope')).toBe('owned');
    expect(parsed.searchParams.get('tag')).toBe('translate');
    expect(parsed.searchParams.get('limit')).toBe('50');
  });

  it('folds the provider filter into the q param as a quoted qualifier', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [], totalCount: 0 }),
    );

    await listAgentsAuthenticated({
      baseUrl: 'http://api.test',
      q: 'translate',
      provider: 'Acme Corp',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const [url] = fetchImpl.mock.calls[0];
    expect(new URL(String(url)).searchParams.get('q')).toBe(
      'translate provider:"Acme Corp"',
    );
  });

  it('sets q to just the provider qualifier when no free text is given', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [], totalCount: 0 }),
    );

    await listAgentsAuthenticated({
      baseUrl: 'http://api.test',
      provider: 'Acme',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const [url] = fetchImpl.mock.calls[0];
    expect(new URL(String(url)).searchParams.get('q')).toBe('provider:"Acme"');
  });

  it('does not set scope=owned for public listing', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [], totalCount: 0 }),
    );

    await listAgentsAuthenticated({
      baseUrl: 'http://api.test',
      listing: 'public',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const [url] = fetchImpl.mock.calls[0];
    const parsed = new URL(String(url));
    expect(parsed.searchParams.get('listing')).toBe('public');
    expect(parsed.searchParams.get('scope')).toBeNull();
  });

  it('throws on non-OK responses (e.g. 426 Upgrade Required)', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({}, false, 426),
    );

    await expect(
      listAgentsAuthenticated({
        baseUrl: 'http://api.test',
        fetchImpl: fetchImpl as unknown as typeof fetch,
      }),
    ).rejects.toThrow('HTTP 426');
  });

  it('returns parsed JSON body on success', async () => {
    const body = {
      agents: [{ agentName: 'alice', tags: [] }],
      totalCount: 1,
    };
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(mockResponse(body));

    const result = await listAgentsAuthenticated({
      baseUrl: 'http://api.test',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    expect(result).toEqual(body);
  });

  it('forwards the cursor query param when provided', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [], totalCount: 0 }),
    );

    await listAgentsAuthenticated({
      baseUrl: 'http://api.test',
      cursor: 'abc123',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const [url] = fetchImpl.mock.calls[0];
    expect(new URL(String(url)).searchParams.get('cursor')).toBe('abc123');
  });
});

describe('listAllAgentsAuthenticated', () => {
  function agent(name: string) {
    return { agentName: name, tags: [] };
  }

  it('follows the next cursor across pages and aggregates results', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(
        mockResponse({
          agents: [agent('a'), agent('b')],
          next: 'c1',
          totalCount: 5,
          totalOnlineCount: 3,
        }),
      )
      .mockResolvedValueOnce(
        mockResponse({ agents: [agent('c'), agent('d')], next: 'c2' }),
      )
      .mockResolvedValueOnce(
        mockResponse({ agents: [agent('e')], next: null }),
      );

    const result = await listAllAgentsAuthenticated({
      baseUrl: 'http://api.test',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    expect(result.agents.map((a) => a.agentName)).toEqual(['a', 'b', 'c', 'd', 'e']);
    expect(result.totalCount).toBe(5);
    expect(result.totalOnlineCount).toBe(3);
    expect(fetchImpl).toHaveBeenCalledTimes(3);

    // First request carries no cursor; follow-ups carry the prior page's next.
    expect(new URL(String(fetchImpl.mock.calls[0][0])).searchParams.get('cursor')).toBeNull();
    expect(new URL(String(fetchImpl.mock.calls[1][0])).searchParams.get('cursor')).toBe('c1');
    expect(new URL(String(fetchImpl.mock.calls[2][0])).searchParams.get('cursor')).toBe('c2');
  });

  it('stops after a single page when next is absent', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [agent('a')], totalCount: 1 }),
    );

    const result = await listAllAgentsAuthenticated({
      baseUrl: 'http://api.test',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    expect(fetchImpl).toHaveBeenCalledOnce();
    expect(result.agents.map((a) => a.agentName)).toEqual(['a']);
  });

  it('requests the max page size (limit=100) by default', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [], next: null }),
    );

    await listAllAgentsAuthenticated({
      baseUrl: 'http://api.test',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    expect(new URL(String(fetchImpl.mock.calls[0][0])).searchParams.get('limit')).toBe('100');
  });

  it('caps the total agents and shrinks the final page request', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(
        mockResponse({ agents: [agent('a'), agent('b')], next: 'c1' }),
      )
      .mockResolvedValueOnce(
        mockResponse({ agents: [agent('c'), agent('d')], next: 'c2' }),
      );

    const result = await listAllAgentsAuthenticated({
      baseUrl: 'http://api.test',
      maxAgents: 3,
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    expect(result.agents.map((a) => a.agentName)).toEqual(['a', 'b', 'c']);
    // Two requests only. The cap bounds each page size: first page asks for
    // min(100, 3)=3; after 2 returned, the second asks for the remaining 1.
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(new URL(String(fetchImpl.mock.calls[0][0])).searchParams.get('limit')).toBe('3');
    expect(new URL(String(fetchImpl.mock.calls[1][0])).searchParams.get('limit')).toBe('1');
  });

  it('terminates when a backend returns a stuck non-null cursor', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      mockResponse({ agents: [agent('a')], next: 'always' }),
    );

    const result = await listAllAgentsAuthenticated({
      baseUrl: 'http://api.test',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    // Bounded by the internal page guard rather than looping forever.
    expect(fetchImpl).toHaveBeenCalledTimes(1000);
    expect(result.agents).toHaveLength(1000);
  });
});
