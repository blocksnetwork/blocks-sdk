/**
 * Tests for SwitchEnvironment handling in AgentInstance.
 *
 * Covers:
 * - Issue #7: pamToken is required; no fallback re-registration
 * - Keyset switches in either direction are allowed when the backend
 *   supplies a pamToken (paid <-> free billingMode flips)
 * - Issue #8: Race condition resolved (subscribe only fires with pamToken)
 * - Issue #10: No sensitive token/key fragments in logs
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { makeTestCard } from './helpers/test-card.js';

vi.mock('pubnub', () => {
  return {
    // eslint-disable-next-line @typescript-eslint/ban-types
    default: vi.fn().mockImplementation((config: Record<string, unknown>) => {
      // eslint-disable-next-line @typescript-eslint/ban-types
      const listeners: Array<Record<string, Function>> = [];
      return {
        _config: config,
        // eslint-disable-next-line @typescript-eslint/ban-types
        addListener: vi.fn((l: Record<string, Function>) => listeners.push(l)),
        removeListener: vi.fn(),
        subscribe: vi.fn(),
        unsubscribe: vi.fn(),
        publish: vi.fn().mockResolvedValue({}),
        setToken: vi.fn(),
        getToken: vi.fn().mockReturnValue(null),
        setFilterExpression: vi.fn(),
        setState: vi.fn().mockResolvedValue({}),
        destroy: vi.fn(),
        token: null,
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
const TEST_AGENT_ID_SW = 'ffffffff-6666-6666-6666-666666666666';
vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: vi.fn().mockResolvedValue({
    pamToken: null,
    agentId: 'ffffffff-6666-6666-6666-666666666666',
    controlChannel: 'agent.ffffffff-6666-6666-6666-666666666666.control',
  }),
  getAgent: vi.fn().mockResolvedValue({
    agentName: 'test_agent',
    name: 'Test Agent',
    listing: 'public',
    billingMode: 'free',
  }),
}));

import PubNub from 'pubnub';
import { getAgent, connectAgent } from '../src/runtime/agent-registry.js';

describe('SwitchEnvironment', () => {
  let errorSpy: ReturnType<typeof vi.spyOn>;
  let warnSpy: ReturnType<typeof vi.spyOn>;
  let logSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getAgent).mockResolvedValue({
      agentName: 'test_agent',
      name: 'Test Agent',
      listing: 'public',
      billingMode: 'free',
    });
    errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
  });

  afterEach(() => {
    errorSpy.mockRestore();
    warnSpy.mockRestore();
    logSpy.mockRestore();
  });

  // --- Issue #7: pamToken required ---

  it('rejects SwitchEnvironment without pamToken and logs error', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    vi.mocked(connectAgent).mockClear();

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const countBefore = pnMock.mock.results.length;
    const controlClient = pnMock.mock.results[countBefore - 1].value;

    // Send SwitchEnvironment without pamToken
    controlClient._simulateMessage({
      type: 'SwitchEnvironment',
      environment: 'network',
    });

    await new Promise((r) => setTimeout(r, 50));

    // No new PubNub client should be created
    expect(pnMock.mock.results.length).toBe(countBefore);

    // No re-registration fallback
    expect(connectAgent).not.toHaveBeenCalled();

    // The old client should NOT be destroyed (switch was rejected)
    expect(controlClient.destroy).not.toHaveBeenCalled();

    // Error logged about missing pamToken
    expect(errorSpy).toHaveBeenCalledWith(
      expect.stringContaining('[AgentInstance]'),
      expect.objectContaining({
        message: expect.stringContaining('pamToken is required'),
      }),
    );

    handle.stop();
  });

  it('accepts SwitchEnvironment with pamToken', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const countBefore = pnMock.mock.results.length;
    const controlClient = pnMock.mock.results[countBefore - 1].value;

    // Wait for async registration to complete (sets controlChannel)
    await vi.waitFor(() => expect(controlClient.subscribe).toHaveBeenCalled());

    controlClient._simulateMessage({
      type: 'SwitchEnvironment',
      environment: 'network',
      pamToken: 'valid-network-token',
    });

    // A new PubNub client should be created for network
    expect(pnMock.mock.results.length).toBeGreaterThan(countBefore);

    // New client should use network keys
    const lastConfig = pnMock.mock.calls[pnMock.mock.calls.length - 1][0] as Record<string, string>;
    expect(lastConfig.subscribeKey).toBe('sub-c-nw');

    // New client should have the pamToken set
    const newClient = pnMock.mock.results[countBefore].value;
    expect(newClient.setToken).toHaveBeenCalledWith('valid-network-token');

    // Subscribe should fire (token is present)
    expect(newClient.subscribe).toHaveBeenCalledWith({
      channels: [`agent.${TEST_AGENT_ID_SW}.control`],
    });

    handle.stop();
  });

  // --- Keyset transitions ---
  // Keyset selection is a function of billingMode (free → playground,
  // paid → network) and is independent of listing. Both directions
  // (free→paid and paid→free) must be honored; the backend gates which
  // transitions actually fire SwitchEnvironment.

  it('allows network->playground transition (paid->free billingMode flip)', async () => {
    // Start as a paid (network) agent; backend later flips billingMode to free
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

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const countBefore = pnMock.mock.results.length;
    const controlClient = pnMock.mock.results[countBefore - 1].value;

    controlClient._simulateMessage({
      type: 'SwitchEnvironment',
      environment: 'playground',
      pamToken: 'playground-token',
    });

    // A new PubNub client should be created for playground
    expect(pnMock.mock.results.length).toBeGreaterThan(countBefore);

    // New client should use playground keys
    const lastConfig = pnMock.mock.calls[pnMock.mock.calls.length - 1][0] as Record<string, string>;
    expect(lastConfig.subscribeKey).toBe('sub-c-pg');

    // New client should have the pamToken set
    const newClient = pnMock.mock.results[countBefore].value;
    expect(newClient.setToken).toHaveBeenCalledWith('playground-token');

    handle.stop();
  });

  it('allows playground->network transition', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const countBefore = pnMock.mock.results.length;
    const controlClient = pnMock.mock.results[countBefore - 1].value;

    controlClient._simulateMessage({
      type: 'SwitchEnvironment',
      environment: 'network',
      pamToken: 'network-token',
    });

    // New client should be created (allowed transition)
    expect(pnMock.mock.results.length).toBeGreaterThan(countBefore);

    handle.stop();
  });

  // --- Issue #8: Race condition verification ---

  it('subscribes only after pamToken is applied (no race)', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const controlClient = pnMock.mock.results[pnMock.mock.results.length - 1].value;

    // Wait for async registration to complete (sets controlChannel)
    await vi.waitFor(() => expect(controlClient.subscribe).toHaveBeenCalled());

    controlClient._simulateMessage({
      type: 'SwitchEnvironment',
      environment: 'network',
      pamToken: 'network-token',
    });

    // Get the new client created during the switch
    const newClient = pnMock.mock.results[pnMock.mock.results.length - 1].value;

    // Verify setToken was called BEFORE subscribe
    const setTokenOrder = newClient.setToken.mock.invocationCallOrder[0];
    const subscribeOrder = newClient.subscribe.mock.invocationCallOrder[0];
    expect(setTokenOrder).toBeLessThan(subscribeOrder);

    handle.stop();
  });

  // --- Issue #10: No sensitive logging ---

  it('does not log token prefixes, key prefixes, or token lengths', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const controlClient = pnMock.mock.results[pnMock.mock.results.length - 1].value;

    // Trigger a SwitchEnvironment to exercise the logging path
    controlClient._simulateMessage({
      type: 'SwitchEnvironment',
      environment: 'network',
      pamToken: 'super-secret-token-that-should-not-appear-in-logs',
    });

    await new Promise((r) => setTimeout(r, 50));

    // Collect all log output
    const allLogs = [
      ...errorSpy.mock.calls.map((c) => JSON.stringify(c)),
      ...warnSpy.mock.calls.map((c) => JSON.stringify(c)),
      ...logSpy.mock.calls.map((c) => JSON.stringify(c)),
    ].join('\n');

    // Token prefix should never appear
    expect(allLogs).not.toContain('super-secret');
    // substring(0, 20) pattern should not appear
    expect(allLogs).not.toMatch(/prefix=/);
    // Token length should not be logged
    expect(allLogs).not.toMatch(/len=\d+/);
    // subscribeKey prefix should not appear
    expect(allLogs).not.toContain('sub-c-nw');
    expect(allLogs).not.toContain('sub-c-pg');

    handle.stop();
  });

  it('logs only presence/absence indicators for pamToken', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const controlClient = pnMock.mock.results[pnMock.mock.results.length - 1].value;

    // Trigger a SwitchEnvironment with token
    controlClient._simulateMessage({
      type: 'SwitchEnvironment',
      environment: 'network',
      pamToken: 'any-token-value',
    });

    await new Promise((r) => setTimeout(r, 50));

    // Verify presence indicator is logged (not the token itself)
    const allLogs = [
      ...logSpy.mock.calls.map((c) => JSON.stringify(c)),
    ].join('\n');

    // Should contain presence indicator
    expect(allLogs).toContain('present');

    // Should NOT contain the actual token value
    expect(allLogs).not.toContain('any-token-value');

    handle.stop();
  });

  it('logs absent indicator when pamToken is missing', async () => {
    const { startAgentInstance } = await import('../src/runtime/agent-instance.js');

    const handle = await startAgentInstance({
      agentName: 'test_agent',
      card: makeTestCard(),
      handler: async () => ({}),
    });

    const pnMock = PubNub as unknown as ReturnType<typeof vi.fn>;
    const controlClient = pnMock.mock.results[pnMock.mock.results.length - 1].value;

    // Trigger without pamToken (will be rejected but should still log absence)
    controlClient._simulateMessage({
      type: 'SwitchEnvironment',
      environment: 'network',
    });

    await new Promise((r) => setTimeout(r, 50));

    // The info log at the start should show absent
    const allLogs = [
      ...logSpy.mock.calls.map((c) => JSON.stringify(c)),
      ...errorSpy.mock.calls.map((c) => JSON.stringify(c)),
    ].join('\n');

    expect(allLogs).toContain('absent');

    handle.stop();
  });
});
