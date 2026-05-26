import { describe, it, expect, vi, beforeEach } from 'vitest';
import { makeTestCard } from './helpers/test-card.js';

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation((config: Record<string, unknown>) => {
      const listeners: Array<Record<string, (...args: unknown[]) => void>> = [];
      return {
        _config: config,
        addListener: vi.fn((l: Record<string, (...args: unknown[]) => void>) => listeners.push(l)),
        removeListener: vi.fn(),
        subscribe: vi.fn(),
        unsubscribe: vi.fn(),
        publish: vi.fn().mockResolvedValue({}),
        setToken: vi.fn(),
        setFilterExpression: vi.fn(),
        setState: vi.fn().mockResolvedValue({}),
        destroy: vi.fn(),
        _listeners: listeners,
        _simulateMessage: (msg: unknown, meta?: unknown) => {
          for (const l of listeners) {
            if (l.message) l.message({ message: msg, userMetadata: meta });
          }
        },
      };
    }),
  };
});

vi.mock('../src/runtime/cdm-config.js', () => ({
  DEFAULT_CDM_URL: 'https://test-cdm.example.com/config.json',
  fetchCdmConfig: vi.fn().mockResolvedValue({
    playground: { publishKey: 'pub-c-pg', subscribeKey: 'sub-c-pg' },
    network: { publishKey: 'pub-c-nw', subscribeKey: 'sub-c-nw' },
    api: { baseUrl: 'http://localhost:3001' },
  }),
}));

// The connect response must include controlChannel so the agent instance
// knows which channel to subscribe on.
const TEST_AGENT_ID_DUAL = 'eeeeeeee-5555-5555-5555-555555555555';
vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: vi.fn().mockResolvedValue({
    pamToken: null,
    agentId: 'eeeeeeee-5555-5555-5555-555555555555',
    controlChannel: 'agent.eeeeeeee-5555-5555-5555-555555555555.control',
  }),
  getAgent: vi.fn().mockResolvedValue({
    agentName: 'test_agent',
    name: 'Test Agent',
    listing: 'public',
    billingMode: 'free',
  }),
}));

import PubNub from 'pubnub';
import { fetchCdmConfig } from '../src/runtime/cdm-config.js';
import { getAgent } from '../src/runtime/agent-registry.js';
import { connectAgent } from '../src/runtime/agent-registry.js';

describe('Single Active PubNub Instance', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Restore default registry mock (prevents leaking between tests)
    vi.mocked(getAgent).mockResolvedValue({
      agentName: 'test_agent',
      name: 'Test Agent',
      listing: 'public',
      billingMode: 'free',
    });
  });

  it('creates one control client with playground keys by default', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    // Should create control client with playground keys
    const calls = (PubNub as unknown as ReturnType<typeof vi.fn>).mock.calls;
    const firstConfig = calls[0][0] as Record<string, string>;
    expect(firstConfig.subscribeKey).toBe('sub-c-pg');

    handle.stop();
  });

  it('exposes cdmConfig on handle', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    expect(handle.cdmConfig).toBeDefined();
    expect(handle.cdmConfig!.playground.publishKey).toBe('pub-c-pg');
    expect(handle.cdmConfig!.network.subscribeKey).toBe('sub-c-nw');

    handle.stop();
  });

  it('does not have clients property', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    expect((handle as Record<string, unknown>).clients).toBeUndefined();

    handle.stop();
  });

  it('uses playground keys when agent billingMode is free', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const calls = (PubNub as unknown as ReturnType<typeof vi.fn>).mock.calls;
    const firstConfig = calls[0][0] as Record<string, string>;
    expect(firstConfig.subscribeKey).toBe('sub-c-pg');

    handle.stop();
  });

  it('uses network keys when agent billingMode is paid (public listing)', async () => {
    vi.mocked(getAgent).mockResolvedValue({
      agentName: 'test_agent',
      name: 'Test Agent',
      listing: 'public',
      billingMode: 'paid',
    });
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const calls = (PubNub as unknown as ReturnType<typeof vi.fn>).mock.calls;
    const firstConfig = calls[0][0] as Record<string, string>;
    expect(firstConfig.subscribeKey).toBe('sub-c-nw');

    handle.stop();
  });

  it('uses network keys when agent billingMode is paid (private listing)', async () => {
    vi.mocked(getAgent).mockResolvedValue({
      agentName: 'test_agent',
      name: 'Test Agent',
      listing: 'private',
      billingMode: 'paid',
    });
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const calls = (PubNub as unknown as ReturnType<typeof vi.fn>).mock.calls;
    const firstConfig = calls[0][0] as Record<string, string>;
    expect(firstConfig.subscribeKey).toBe('sub-c-nw');

    handle.stop();
  });

  it('throws when billingMode is missing from registry entry', async () => {
    // BMC: registry is authoritative for billingMode. Missing field is a
    // hard error — the agent must re-register with explicit billingMode.
    vi.mocked(getAgent).mockResolvedValue({
      agentName: 'test_agent',
      name: 'Test Agent',
      listing: 'public',
      // billingMode intentionally omitted
    });
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    await expect(
      startAgentInstance({
        agentName: 'test_agent',
        card: makeTestCard(),
        handler: async () => ({}),
      }),
    ).rejects.toThrow(/missing billingMode/);
  });

  it('throws when agent is not found in registry', async () => {
    // BMC: no fallback to playground for unknown agents — the registry must
    // contain the agent before startAgentInstance runs.
    vi.mocked(getAgent).mockResolvedValue(null);
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    await expect(
      startAgentInstance({
        agentName: 'unknown_agent',
        handler: async () => ({}),
      }),
    ).rejects.toThrow(/not found in registry/);
  });

  it('throws when registry API call fails', async () => {
    vi.mocked(getAgent).mockRejectedValue(new Error('Network error'));
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    await expect(
      startAgentInstance({
        agentName: 'test_agent',
        handler: async () => ({}),
      }),
    ).rejects.toThrow('Network error');
  });

  it('throws when CDM fetch fails', async () => {
    vi.mocked(fetchCdmConfig).mockRejectedValueOnce(new Error('CDM config fetch failed: 503 Service Unavailable'));
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    await expect(
      startAgentInstance({
        agentName: 'test_agent',
        handler: async () => ({}),
      }),
    ).rejects.toThrow('CDM config fetch failed: 503 Service Unavailable');

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    expect(pnMock).not.toHaveBeenCalled();
  });

  it('applies pamToken from SwitchEnvironment message to new control client', async () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'diagnostics';
    try {
      const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

      const handle = await startAgentInstance({
        agentName: 'test_agent',
        card: makeTestCard(),
        handler: async () => ({}),
      });

      const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
      const countBeforeSwitch = pnMock.mock.results.length;
      // The control client is the last one created before the switch
      const controlClient = pnMock.mock.results[countBeforeSwitch - 1].value;

      // Wait for async registration to complete (sets controlChannel)
      await vi.waitFor(() => expect(controlClient.subscribe).toHaveBeenCalled());

      // Simulate SwitchEnvironment message with pamToken
      controlClient._simulateMessage({
        type: 'SwitchEnvironment',
        environment: 'network',
        pamToken: 'token-for-network',
      });

      // A new PubNub client should have been created for network
      expect(pnMock.mock.results.length).toBeGreaterThan(countBeforeSwitch);
      const lastConfig = pnMock.mock.calls[pnMock.mock.calls.length - 1][0] as Record<string, string>;
      expect(lastConfig.subscribeKey).toBe('sub-c-nw');

      // Three removeListener calls during switchEnvironment cleanup:
      // 1. the primary control listener (message/status handler)
      // 2. the connectivity listener (always attached, watches PNNetworkDownCategory etc.)
      // 3. the diagnostic listener (from untrackClient — gated behind BLOCKS_DEBUG_INTERNAL,
      //    exercised here because BLOCKS_DEBUG_INTERNAL='diagnostics' is set above).
      expect(controlClient.removeListener).toHaveBeenCalledTimes(3);
      expect(controlClient.unsubscribe).toHaveBeenCalledWith({
        channels: [`agent.${TEST_AGENT_ID_DUAL}.control`],
      });
      expect(controlClient.destroy).toHaveBeenCalledTimes(1);

      // The new client (created during switch) should have setToken called with the provided pamToken
      const newClient = pnMock.mock.results[countBeforeSwitch].value;
      expect(newClient.setToken).toHaveBeenCalledWith('token-for-network');

      handle.stop();
    } finally {
      delete process.env.BLOCKS_DEBUG_INTERNAL;
    }
  });

  it('rejects SwitchEnvironment when pamToken is absent (no fallback re-registration)', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    // Clear mocks so we can track that no re-registration call happens
    vi.mocked(connectAgent).mockClear();

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const countBeforeSwitch = pnMock.mock.results.length;
    const controlClient = pnMock.mock.results[countBeforeSwitch - 1].value;

    // Simulate SwitchEnvironment WITHOUT pamToken — should be rejected
    const errorSpy = vi.spyOn(console, 'error');
    controlClient._simulateMessage({ type: 'SwitchEnvironment', environment: 'network' });

    // Give time for any async operations
    await new Promise((r) => setTimeout(r, 50));

    // No re-registration should have been attempted
    expect(connectAgent).not.toHaveBeenCalled();

    // No new PubNub client should have been created (switch was rejected)
    expect(pnMock.mock.results.length).toBe(countBeforeSwitch);

    // The current control client should NOT have been destroyed
    expect(controlClient.destroy).not.toHaveBeenCalled();

    // Error should have been logged about missing pamToken
    expect(errorSpy).toHaveBeenCalledWith(
      expect.stringContaining('[AgentInstance]'),
      expect.objectContaining({
        message: expect.stringContaining('pamToken is required'),
      }),
    );

    errorSpy.mockRestore();
    handle.stop();
  });

  it('clears latestControlToken seeded by StartTask on environment switch', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const controlClient = pnMock.mock.results[pnMock.mock.results.length - 1].value;

    // Seed latestControlToken via a StartTask message with controlToken
    controlClient._simulateMessage(
      {
        type: 'StartTask',
        taskId: 'seed-task-1',
        requestParts: [{ role: 'user', parts: [{ type: 'text', text: 'hi' }] }],
        controlToken: 'stale-control-token',
      },
      { instance: 'ignored', broadcast: 'true' },
    );

    // Record count right before the switch (StartTask may have created a task PubNub)
    const countBeforeSwitch = pnMock.mock.results.length;

    // Now switch environment with pamToken — the stale latestControlToken must NOT be applied
    controlClient._simulateMessage({
      type: 'SwitchEnvironment',
      environment: 'network',
      pamToken: 'network-token',
    });

    // The new control client is the one created during the switch
    const newClient = pnMock.mock.results[countBeforeSwitch].value;
    const setTokenCalls = newClient.setToken.mock.calls;
    // Should have exactly one setToken call with the message's pamToken, NOT the stale one
    expect(setTokenCalls).toHaveLength(1);
    expect(setTokenCalls[0][0]).toBe('network-token');

    handle.stop();
  });
});
