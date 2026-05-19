import { describe, it, expect } from 'vitest';
import { getAgentCard } from '../src/tools.js';
import { makeFakeDeps } from './helpers.js';

describe('get_agent_card', () => {
  it('returns the agent card JSON when found', async () => {
    const card = {
      io: { inputs: [{ id: 'prompt', contentType: 'text/plain' }] },
    };
    const { deps } = makeFakeDeps({
      agentEntry: { agentName: 'alice', card },
    });

    const res = await getAgentCard({ agentName: 'alice' }, deps);

    expect(res.isError).toBeUndefined();
    expect(JSON.parse(res.content[0].text)).toEqual(card);
  });

  it('returns isError=true with a friendly message when not found', async () => {
    const { deps } = makeFakeDeps({ agentEntry: null });

    const res = await getAgentCard({ agentName: 'ghost' }, deps);

    expect(res.isError).toBe(true);
    expect(res.content[0].text).toBe('Agent "ghost" not found.');
  });

  it('falls back to the bare entry JSON when the card field is missing', async () => {
    const { deps } = makeFakeDeps({
      agentEntry: { agentName: 'cardless', billingMode: 'free' },
    });

    const res = await getAgentCard({ agentName: 'cardless' }, deps);
    const parsed = JSON.parse(res.content[0].text);
    expect(parsed).toMatchObject({ agentName: 'cardless', billingMode: 'free' });
  });

  it('passes baseUrl + apiKey through to getAgentByName', async () => {
    const { deps, mocks } = makeFakeDeps({
      apiKey: 'sk-test',
      baseUrl: 'http://api.test',
      agentEntry: { agentName: 'alice' },
    });

    await getAgentCard({ agentName: 'alice' }, deps);

    expect(mocks.getAgentByName).toHaveBeenCalledWith('alice', {
      baseUrl: 'http://api.test',
      apiKey: 'sk-test',
    });
  });
});
