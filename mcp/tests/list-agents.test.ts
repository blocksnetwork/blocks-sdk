import { describe, it, expect } from 'vitest';
import { listAgents } from '../src/tools.js';
import { makeFakeDeps } from './helpers.js';

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
    });

    const res = await listAgents({}, deps);
    const lines = res.content[0].text.split('\n');
    expect(lines).toEqual([
      'Agents (2):',
      'alice | Alice | public | Translate, Summarize',
      'bob | bob | private | ',
    ]);
  });

  it('forwards baseUrl, apiKey, tag, listing, limit to the registry helper', async () => {
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
      limit: 25,
    });
  });

  it('defaults to "public" for missing listing and uses agentName when name is absent', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: {
        agents: [{ agentName: 'naked', tags: [] }],
        totalCount: 1,
      },
    });

    const res = await listAgents({}, deps);
    expect(res.content[0].text).toContain('naked | naked | public |');
  });

  it('uses agents.length as the count when totalCount is omitted', async () => {
    const { deps } = makeFakeDeps({
      listAgentsResult: { agents: [{ agentName: 'a' }, { agentName: 'b' }] },
    });

    const res = await listAgents({}, deps);
    expect(res.content[0].text.split('\n')[0]).toBe('Agents (2):');
  });
});
