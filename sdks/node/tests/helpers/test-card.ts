/**
 * Minimal valid AgentCard for tests.
 *
 * Provides a card with a single _default stream (outbound, bytes, dedicated).
 * Tests that need specific stream configurations should spread and override.
 *
 * Pass `agentName` to override `identity.agentName` — required when
 * publishing the card to the live registry, since the service enforces
 * `card.identity.agentName === route agentName`.
 */

import type { AgentCard } from '../../src/runtime/agent-registry.js';

export function makeTestCard(
  overrides: Partial<AgentCard> & { agentName?: string } = {},
): AgentCard {
  const { agentName, ...cardOverrides } = overrides;
  const card: AgentCard = {
    identity: {
      agentName: agentName ?? 'test_agent',
      displayName: 'Test Agent',
      description: 'Minimal test agent card',
      version: '1.0.0',
      provider: { organization: 'test-org' },
    },
    capabilities: { taskKinds: ['request'] },
    tags: [{ id: 'main', name: 'Main', description: 'Test tag' }],
    runtime: { handler: 'index.js' },
    streams: {
      _default: { direction: 'outbound', format: 'bytes' },
    },
    ...cardOverrides,
  };
  if (agentName && cardOverrides.identity) {
    card.identity = { ...cardOverrides.identity, agentName };
  }
  return card;
}

/**
 * Card with pipe support and a default outbound bytes stream.
 */
export function makePipeTestCard(
  overrides: Partial<AgentCard> & { agentName?: string } = {},
): AgentCard {
  return makeTestCard({
    capabilities: { taskKinds: ['request', 'pipe'] },
    ...overrides,
  });
}
