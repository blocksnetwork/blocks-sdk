import { describe, it, expect, vi } from 'vitest';
import { listAgentsAuthenticated } from '../src/registry-list.js';
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
});
