import { describe, it, expect, vi } from 'vitest';
import { getAgentStatus } from '../src/tools.js';
import {
  fetchAgentStatus,
  type AgentStatusResponse,
} from '../src/agent-status.js';
import {
  PROTOCOL_VERSION_HEADER,
  CURRENT_PROTOCOL_VERSION,
} from '../src/protocol-headers.js';
import { makeFakeDeps } from './helpers.js';

function mockResponse(body: unknown, ok = true, status = 200): Response {
  return { ok, status, json: async () => body } as Response;
}

describe('get_agent_status (handler)', () => {
  it('forwards baseUrl, apiKey and agentNames to fetchAgentStatus and emits JSON', async () => {
    const result: AgentStatusResponse = {
      agents: {
        alice: {
          agentName: 'alice',
          instances: [],
          onlineCount: 2,
          totalActiveTasks: 1,
          taskCount: 1,
        },
      },
    };
    const { deps, mocks } = makeFakeDeps({
      apiKey: 'bk_test',
      agentStatusResult: result,
    });

    const res = await getAgentStatus({ agentNames: ['alice'] }, deps);

    expect(mocks.fetchAgentStatus).toHaveBeenCalledWith({
      baseUrl: 'http://api.test',
      apiKey: 'bk_test',
      agentNames: ['alice'],
    });
    const parsed = JSON.parse(res.content[0].text);
    expect(parsed.agents.alice.onlineCount).toBe(2);
  });
});

describe('fetchAgentStatus (HTTP helper)', () => {
  it('builds /api/v1/agent-status URL with agentNames csv and protocol header', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(mockResponse({ agents: {} }));

    await fetchAgentStatus({
      baseUrl: 'http://api.test/',
      agentNames: ['alice', 'bob'],
      apiKey: 'bk_test',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const [url, init] = fetchImpl.mock.calls[0];
    const parsed = new URL(String(url));
    expect(parsed.pathname).toBe('/api/v1/agent-status');
    expect(parsed.searchParams.get('agentNames')).toBe('alice,bob');
    const headers = init?.headers as Record<string, string>;
    expect(headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
    expect(headers['Authorization']).toBe('Bearer bk_test');
  });

  it('rejects empty input', async () => {
    await expect(
      fetchAgentStatus({
        baseUrl: 'http://api.test',
        agentNames: [],
      }),
    ).rejects.toThrow(/at least one/);
  });

  it('rejects more than 50 names', async () => {
    const names = Array.from({ length: 51 }, (_, i) => `a${i}`);
    await expect(
      fetchAgentStatus({
        baseUrl: 'http://api.test',
        agentNames: names,
      }),
    ).rejects.toThrow(/at most 50/);
  });

  it('rejects names with invalid characters', async () => {
    await expect(
      fetchAgentStatus({
        baseUrl: 'http://api.test',
        agentNames: ['has-a-hyphen'],
      }),
    ).rejects.toThrow(/Invalid agent name/);
  });

  it('throws on non-OK responses', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(mockResponse({}, false, 500));

    await expect(
      fetchAgentStatus({
        baseUrl: 'http://api.test',
        agentNames: ['alice'],
        fetchImpl: fetchImpl as unknown as typeof fetch,
      }),
    ).rejects.toThrow('HTTP 500');
  });
});
