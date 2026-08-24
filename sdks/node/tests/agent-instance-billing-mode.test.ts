/**
 * agent-instance bootstrap — billingMode resolution + connect payload.
 *
 * Node-side billing-mode behavior:
 * - Bootstrap MUST call registry GET at boot to learn the agent's own
 *   billingMode. The registry is authoritative; no caller override.
 * - The resolved value is forwarded UNCONDITIONALLY into the connect
 *   payload.
 * - Missing / unknown billingMode is a hard SDK error (production CDM
 *   path; the injected-PubNub test path is a separate code path with its
 *   own coverage in dual-instance.test.ts and below).
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { makeTestCard } from './helpers/test-card.js';

// PubNub mock — shared shape across the agent-instance test suite.
vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation((config: Record<string, unknown>) => {
    const listeners: Array<Record<string, (...args: unknown[]) => void>> = [];
    return {
      _config: config,
      addListener: vi.fn(
        (l: Record<string, (...args: unknown[]) => void>) => listeners.push(l),
      ),
      removeListener: vi.fn(),
      subscribe: vi.fn(),
      unsubscribe: vi.fn(),
      publish: vi.fn().mockResolvedValue({}),
      setToken: vi.fn(),
      getToken: vi.fn().mockReturnValue(null),
      setFilterExpression: vi.fn(),
      setState: vi.fn().mockResolvedValue({}),
      destroy: vi.fn(),
      _listeners: listeners,
    };
  }),
}));

// CDM mock — required so the registry GET path runs (CDM presence is
// the gate; without CDM the test path skips registry GET entirely).
vi.mock('../src/runtime/cdm-config.js', () => ({
  DEFAULT_CDM_URL: 'https://test-cdm.example.com/config.json',
  fetchCdmConfig: vi.fn().mockResolvedValue({
    playground: { publishKey: 'pub-c-pg', subscribeKey: 'sub-c-pg' },
    network: { publishKey: 'pub-c-nw', subscribeKey: 'sub-c-nw' },
    api: { baseUrl: 'http://localhost:3001' },
  }),
}));

// Registry mock — getAgent returns the agent's row (read by bootstrap),
// connectAgent captures the payload built during connect.
// vi.mock() factories are hoisted; literals must be inlined.
vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: vi.fn().mockResolvedValue({
    pamToken: null,
    agentId: 'aaaaaaaa-1111-2222-3333-444444444444',
    controlChannel: 'agent.aaaaaaaa-1111-2222-3333-444444444444.control',
  }),
  getAgent: vi.fn(),
}));

import { getAgent, connectAgent } from '../src/runtime/agent-registry.js';

describe('agent-instance bootstrap (billingMode contract)', () => {
  const ORIGINAL_API_KEY = process.env.BLOCKS_API_KEY;

  beforeEach(() => {
    vi.clearAllMocks();
    // BLOCKS_API_KEY is required by startAgentInstance.
    process.env.BLOCKS_API_KEY = 'bk_test-api-key-123';
  });

  afterEach(() => {
    if (ORIGINAL_API_KEY === undefined) {
      delete process.env.BLOCKS_API_KEY;
    } else {
      process.env.BLOCKS_API_KEY = ORIGINAL_API_KEY;
    }
  });

  it('reads billingMode from the registry and forwards into the connect payload (paid)', async () => {
    vi.mocked(getAgent).mockResolvedValue({
      agentName: 'paid_agent',
      displayName: 'Paid Agent',
      listing: 'public',
      billingMode: 'paid',
    });

    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'paid_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    // Wait for the async connectAgent.then() to fire.
    await vi.waitFor(() => expect(connectAgent).toHaveBeenCalledTimes(1));

    // Registry GET ran first, before connect.
    expect(getAgent).toHaveBeenCalledTimes(1);
    expect(getAgent).toHaveBeenCalledWith(
      'paid_agent',
      expect.objectContaining({ apiKey: 'bk_test-api-key-123' }),
    );

    const [agentName, options] = vi.mocked(connectAgent).mock.calls[0];
    expect(agentName).toBe('paid_agent');
    expect(options.billingMode).toBe('paid');

    handle.stop();
  });

  it('reads billingMode from the registry and forwards into the connect payload (free)', async () => {
    vi.mocked(getAgent).mockResolvedValue({
      agentName: 'free_agent',
      displayName: 'Free Agent',
      listing: 'private',
      billingMode: 'free',
    });

    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'free_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    await vi.waitFor(() => expect(connectAgent).toHaveBeenCalledTimes(1));

    const [, options] = vi.mocked(connectAgent).mock.calls[0];
    expect(options.billingMode).toBe('free');

    handle.stop();
  });

  it('throws when the registry entry is missing billingMode', async () => {
    // BMC: registry is authoritative. A missing billingMode field is a
    // hard error — the agent must re-register so the registry persists
    // an explicit value.
    vi.mocked(getAgent).mockResolvedValue({
      agentName: 'broken_agent',
      displayName: 'Broken Agent',
      listing: 'public',
      // billingMode intentionally omitted
    });

    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    await expect(
      startAgentInstance({
        agentName: 'broken_agent',
        card: makeTestCard(),
        handler: async () => ({}),
      }),
    ).rejects.toThrow(/missing billingMode/);

    // No connect should have been attempted.
    expect(connectAgent).not.toHaveBeenCalled();
  });

  it('throws when the agent is not in the registry at all', async () => {
    vi.mocked(getAgent).mockResolvedValue(null);

    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    await expect(
      startAgentInstance({
        agentName: 'unknown_agent',
        handler: async () => ({}),
      }),
    ).rejects.toThrow(/not found in registry/);

    expect(connectAgent).not.toHaveBeenCalled();
  });

  it('startAgentInstance options accept no `billingMode` field (no caller override)', async () => {
    // Compile-time + runtime guard: AgentInstanceOptions must NOT expose a
    // `billingMode` field. Passing one would be a TS error; at runtime the
    // SDK ignores anything unknown and uses the registry value.
    vi.mocked(getAgent).mockResolvedValue({
      agentName: 'override_agent',
      displayName: 'Override Agent',
      listing: 'public',
      billingMode: 'paid', // registry says paid
    });

    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'override_agent',
      card: makeTestCard(),
      handler: async () => ({}),
      // Caller attempts to inject billingMode='free' via untyped escape hatch.
      // The SDK MUST ignore this and use 'paid' from the registry.
      ...({ billingMode: 'free' } as Record<string, unknown>),
    });

    await vi.waitFor(() => expect(connectAgent).toHaveBeenCalledTimes(1));
    const [, options] = vi.mocked(connectAgent).mock.calls[0];
    expect(options.billingMode).toBe('paid');

    handle.stop();
  });
});
