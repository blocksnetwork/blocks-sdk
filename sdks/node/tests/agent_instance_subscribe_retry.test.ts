/**
 * BLOCKS-129 silent-park fix: the long-lived control client must opt
 * into unbounded subscribe retry (subscribeRetryUnbounded:true) so the
 * PubNub Event Engine never exhausts its retry budget and parks in
 * RECEIVE_FAILED. Short-lived per-task / per-stream clients must opt
 * out so a stuck task fails cleanly rather than looping forever.
 */
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach } from 'vitest';
import { createPubNubClient } from '../src/runtime/pubnub-client.js';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';

const TEST_AGENT_ID = 'cccccccc-3333-3333-3333-333333333333';

const originalFetch = globalThis.fetch;
beforeAll(() => {
  globalThis.fetch = vi.fn().mockImplementation(async (url: string) => {
    if (url.includes('config.json') || url.includes('/cdm')) {
      return {
        ok: true,
        status: 200,
        json: async () => ({
          api: { baseUrl: 'http://test-host' },
          playground: { publishKey: 'pub-pg', subscribeKey: 'sub-pg' },
          network: { publishKey: 'pub-net', subscribeKey: 'sub-net' },
        }),
      };
    }
    if (url.includes('/registry/agents?')) {
      // Derive agentName from the query string so multiple test cases
      // can share this fetch mock without each agent's startAgentInstance
      // call mismatching against a hard-coded name.
      const match = url.match(/agentName=([^&]+)/);
      const agentName = match ? decodeURIComponent(match[1]) : 'unknown';
      return {
        ok: true,
        status: 200,
        json: async () => ({
          agent: {
            agentName,
            billingMode: 'free',
            listing: 'public',
          },
        }),
      };
    }
    return {
      ok: true,
      status: 200,
      json: async () => ({
        accessToken: 'mock-jwt',
        refreshToken: 'mock-refresh',
        expiresIn: 3600,
        agentId: TEST_AGENT_ID,
        controlChannel: `agent.${TEST_AGENT_ID}.control`,
      }),
    };
  }) as unknown as typeof fetch;
});
afterAll(() => {
  globalThis.fetch = originalFetch;
});

// Mock createPubNubClient with a spy so we can inspect every call's
// arguments. Returns a minimal PubNub stub.
vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => ({
    publish: vi.fn(async () => ({ timetoken: Date.now().toString() })),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    setFilterExpression: vi.fn(),
    setToken: vi.fn(),
    setState: vi.fn(async () => ({})),
    destroy: vi.fn(),
    reconnect: vi.fn(),
    hereNow: vi.fn(async () => ({ channels: {} })),
  })),
}));

const mocked = vi.mocked(createPubNubClient);

beforeEach(() => {
  mocked.mockClear();
});

describe('subscribe retry budget per client kind', () => {
  it('control client (SDK-constructed) opts into unbounded subscribe retry', async () => {
    // Don't pass opts.pubnub — force the SDK to call createPubNubClient
    // for the control client. baseUrl supplies the registry endpoint
    // resolution that the SDK needs at boot.
    const { stop } = await startAgentInstance({
      agentName: 'subretry_control',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    // Allow the registration .then() to resolve.
    await new Promise((r) => setTimeout(r, 50));

    expect(mocked).toHaveBeenCalled();
    const controlCall = mocked.mock.calls.find(([cfg]) =>
      cfg.userId === `AG-subretry_control-${''}` ||
      // userId is an instanceId starting with "AG-subretry_control-"
      (typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-subretry_control-')),
    );
    expect(controlCall, 'control client createPubNubClient call not found').toBeDefined();
    expect(controlCall![0].subscribeRetryUnbounded).toBe(true);

    stop();
  });

  it('control client wires an onRetry callback (visibility during outage)', async () => {
    const { stop } = await startAgentInstance({
      agentName: 'subretry_visibility',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-subretry_visibility-'),
    );
    expect(controlCall, 'control client createPubNubClient call not found').toBeDefined();
    expect(typeof controlCall![0].onRetry).toBe('function');

    stop();
  });
});
