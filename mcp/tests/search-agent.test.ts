import { describe, it, expect } from 'vitest';
import { searchAgents } from '../src/tools.js';
import { makeFakeDeps } from './helpers.js';

// A status response where every named agent has one online instance, so the
// default online-only filter is a no-op for these agents.
function allOnline(...names: string[]) {
  const agents: Record<string, unknown> = {};
  for (const name of names) {
    agents[name] = { agentName: name, instances: [], onlineCount: 1, taskCount: 0 };
  }
  return { agents };
}

describe('search_agent', () => {
  it('forwards the query to the registry helper as `q`', async () => {
    const { deps, mocks } = makeFakeDeps({
      apiKey: 'sk-test',
      baseUrl: 'http://api.test',
    });

    await searchAgents(
      { query: 'translate', tag: 'language', listing: 'private', limit: 25 },
      deps,
    );

    expect(mocks.listAgents).toHaveBeenCalledWith({
      baseUrl: 'http://api.test',
      apiKey: 'sk-test',
      q: 'translate',
      tag: 'language',
      listing: 'private',
      maxAgents: 25,
    });
  });

  it('forwards the provider filter to the registry helper', async () => {
    const { deps, mocks } = makeFakeDeps();

    await searchAgents({ query: 'translate', provider: 'Acme Corp' }, deps);

    expect(mocks.listAgents).toHaveBeenCalledWith(
      expect.objectContaining({ q: 'translate', provider: 'Acme Corp' }),
    );
  });

  it('renders matching agents with the query in the header', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: {
        agents: [
          {
            agentName: 'alice',
            name: 'Alice',
            listing: 'public',
            orgName: 'Hamilton Labs',
            tags: [{ id: 'translate', name: 'Translate' }],
          },
        ],
        totalCount: 1,
      },
      agentStatusResult: allOnline('alice'),
    });

    const res = await searchAgents({ query: 'trans' }, deps);
    const lines = res.content[0].text.split('\n');
    // Row format: agentName | displayName | provider | listing | tags
    expect(lines).toEqual([
      'Agents matching "trans" (1 online of 1 total):',
      'alice | Alice | Hamilton Labs | public | Translate',
    ]);
  });

  it('trims whitespace from the query before sending it', async () => {
    const { deps, mocks } = makeFakeDeps();

    await searchAgents({ query: '  hello  ' }, deps);

    expect(mocks.listAgents).toHaveBeenCalledWith(
      expect.objectContaining({ q: 'hello' }),
    );
  });

  it('rejects a search with no query, provider, or tag without calling the registry', async () => {
    const { deps, mocks } = makeFakeDeps();

    const res = await searchAgents({ query: '   ' }, deps);
    expect(res.isError).toBe(true);
    expect(res.content[0].text).toContain('at least one of');
    expect(mocks.listAgents).not.toHaveBeenCalled();
  });

  it('allows a provider-only search with no query', async () => {
    const { deps, mocks } = makeFakeDeps({
      listAgentsResult: {
        agents: [{ agentName: 'alice', name: 'Alice', listing: 'public' }],
        totalCount: 1,
      },
      agentStatusResult: allOnline('alice'),
    });

    const res = await searchAgents({ provider: 'Hamilton' }, deps);

    expect(res.isError).toBeUndefined();
    expect(mocks.listAgents).toHaveBeenCalledWith(
      expect.objectContaining({ q: undefined, provider: 'Hamilton' }),
    );
    expect(res.content[0].text.split('\n')[0]).toBe(
      'Agents from provider "Hamilton" (1 online of 1 total):',
    );
  });

  it('allows a tag-only search with no query', async () => {
    const { deps, mocks } = makeFakeDeps({
      listAgentsResult: { agents: [], totalCount: 0 },
    });

    const res = await searchAgents({ tag: 'data' }, deps);

    expect(res.isError).toBeUndefined();
    expect(mocks.listAgents).toHaveBeenCalledWith(
      expect.objectContaining({ tag: 'data' }),
    );
    expect(res.content[0].text.split('\n')[0]).toBe(
      'Agents with tag "data" (0 online of 0 total):',
    );
  });

  it('combines query, provider, and tag in the header', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: { agents: [], totalCount: 0 },
    });

    const res = await searchAgents(
      { query: 'trans', provider: 'Hamilton', tag: 'data' },
      deps,
    );
    expect(res.content[0].text.split('\n')[0]).toBe(
      'Agents matching "trans" from provider "Hamilton" with tag "data" (0 online of 0 total):',
    );
  });

  it('drops offline matches by default, keeping only online ones', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: {
        agents: [
          { agentName: 'online', name: 'Online' },
          { agentName: 'offline', name: 'Offline' },
        ],
        totalCount: 2,
      },
      agentStatusResult: {
        agents: {
          online: { agentName: 'online', instances: [], onlineCount: 1, taskCount: 0 },
          offline: { agentName: 'offline', instances: [], onlineCount: 0, taskCount: 0 },
        },
      },
    });

    const res = await searchAgents({ query: 'foo' }, deps);
    const lines = res.content[0].text.split('\n');
    expect(lines).toEqual([
      'Agents matching "foo" (1 online of 2 total):',
      'online | Online |  | public | ',
    ]);
  });

  it('includes offline matches and skips the status check when includeOffline is true', async () => {
    const { deps, mocks } = makeFakeDeps({
      listAgentsResult: {
        agents: [{ agentName: 'online' }, { agentName: 'offline' }],
        totalCount: 2,
      },
    });

    const res = await searchAgents({ query: 'foo', includeOffline: true }, deps);
    expect(res.content[0].text.split('\n')[0]).toBe(
      'Agents matching "foo" (2 of 2 total):',
    );
    expect(mocks.fetchAgentStatus).not.toHaveBeenCalled();
  });
});
