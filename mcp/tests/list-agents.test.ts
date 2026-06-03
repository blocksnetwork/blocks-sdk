import { describe, it, expect } from 'vitest';
import { listAgents } from '../src/tools.js';
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

describe('list_agents', () => {
  it('renders one row per agent with tag names joined by commas', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: {
        agents: [
          {
            agentName: 'alice',
            name: 'Alice',
            listing: 'public',
            tags: [
              { id: 'translate', name: 'Translate' },
              { id: 'summarize', name: 'Summarize' },
            ],
          },
          { agentName: 'bob', listing: 'private' },
        ],
        totalCount: 2,
      },
      agentStatusResult: allOnline('alice', 'bob'),
    });

    const res = await listAgents({}, deps);
    const lines = res.content[0].text.split('\n');
    expect(lines).toEqual([
      'Agents (2 online of 2 total):',
      'alice | Alice | public | Translate, Summarize',
      'bob | bob | private | ',
    ]);
  });

  it('forwards baseUrl, apiKey, tag, listing, limit (as maxAgents) to the registry helper', async () => {
    const { deps, mocks } = makeFakeDeps({
      apiKey: 'sk-test',
      baseUrl: 'http://api.test',
    });

    await listAgents(
      { tag: 'translate', listing: 'private', limit: 25 },
      deps,
    );

    expect(mocks.listAgents).toHaveBeenCalledWith({
      baseUrl: 'http://api.test',
      apiKey: 'sk-test',
      tag: 'translate',
      listing: 'private',
      maxAgents: 25,
    });
  });

  it('defaults to "public" for missing listing and uses agentName when name is absent', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: {
        agents: [{ agentName: 'naked', tags: [] }],
        totalCount: 1,
      },
      agentStatusResult: allOnline('naked'),
    });

    const res = await listAgents({}, deps);
    expect(res.content[0].text).toContain('naked | naked | public |');
  });

  it('uses the online agent count for the header, falling back to length for total', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: { agents: [{ agentName: 'a' }, { agentName: 'b' }] },
      agentStatusResult: allOnline('a', 'b'),
    });

    const res = await listAgents({}, deps);
    expect(res.content[0].text.split('\n')[0]).toBe('Agents (2 online of 2 total):');
  });

  it('reports the registry total separately from the online count', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: {
        agents: [{ agentName: 'online' }, { agentName: 'offline' }],
        totalCount: 50,
      },
      agentStatusResult: {
        agents: {
          online: { agentName: 'online', instances: [], onlineCount: 1, taskCount: 0 },
          offline: { agentName: 'offline', instances: [], onlineCount: 0, taskCount: 0 },
        },
      },
    });

    const res = await listAgents({}, deps);
    expect(res.content[0].text.split('\n')[0]).toBe('Agents (1 online of 50 total):');
  });

  it('drops offline agents by default, keeping only those with online instances', async () => {
    const { deps, mocks } = makeFakeDeps({
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

    const res = await listAgents({}, deps);
    const lines = res.content[0].text.split('\n');
    expect(lines).toEqual([
      'Agents (1 online of 2 total):',
      'online | Online | public | ',
    ]);
    expect(mocks.fetchAgentStatus).toHaveBeenCalledWith({
      baseUrl: 'http://api.test',
      apiKey: undefined,
      agentNames: ['online', 'offline'],
    });
  });

  it('includes offline agents and skips the status check when includeOffline is true', async () => {
    const { deps, mocks } = makeFakeDeps({
      listAgentsResult: {
        agents: [
          { agentName: 'online' },
          { agentName: 'offline' },
        ],
        totalCount: 2,
      },
    });

    const res = await listAgents({ includeOffline: true }, deps);
    expect(res.content[0].text.split('\n')[0]).toBe('Agents (2 of 2 total):');
    expect(mocks.fetchAgentStatus).not.toHaveBeenCalled();
  });

  it('drops agents whose names the status endpoint cannot query', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: {
        agents: [
          { agentName: 'valid_name' },
          { agentName: 'has-dash' },
        ],
        totalCount: 2,
      },
      agentStatusResult: allOnline('valid_name'),
    });

    const res = await listAgents({}, deps);
    const lines = res.content[0].text.split('\n');
    expect(lines).toEqual([
      'Agents (1 online of 2 total):',
      'valid_name | valid_name | public | ',
    ]);
  });
});
