/**
 * Tests for PAM token expiry detection in agent instance.
 *
 * Verifies that PNAccessDeniedCategory status events trigger destroy()
 * on the control client and that duplicate events are deduplicated.
 */
import { describe, it, expect, vi, beforeAll, afterAll } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';

// Mock global fetch so connectAgent resolves quickly
const originalFetch = globalThis.fetch;
beforeAll(() => {
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({}),
  }) as unknown as typeof fetch;
});
afterAll(() => {
  globalThis.fetch = originalFetch;
});

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
    hereNow: vi.fn(async () => ({ channels: {} })),
  })),
}));

interface StatusHandler {
  (event: { category: string; statusCode?: number }): void;
}

interface Listener {
  message?: (event: { message: unknown }) => void;
  status?: StatusHandler;
}

const createFakePubNub = () => {
  const listeners: Listener[] = [];
  const pubnub = {
    publish: vi.fn().mockResolvedValue({ timetoken: Date.now().toString() }),
    addMessageAction: vi.fn().mockResolvedValue({}),
    addListener: (l: Listener) => listeners.push(l),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setFilterExpression: vi.fn(),
    setState: vi.fn().mockResolvedValue({}),
    setToken: vi.fn(),
    destroy: vi.fn(),
    _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any;
  return { pubnub, listeners };
};

describe('agent instance PAM token expiry', () => {
  it('calls destroy() on PNAccessDeniedCategory status event', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const result = await startAgentInstance({
      pubnub,
      agentName: 'test_pam',
      card: makeTestCard(),
    });

    // Wait for registration thread to complete
    await new Promise((r) => setTimeout(r, 50));

    expect(listeners.length).toBeGreaterThan(0);
    const listener = listeners[0];
    expect(listener.status).toBeDefined();

    // Simulate PNAccessDeniedCategory
    listener.status!({ category: 'PNAccessDeniedCategory', statusCode: 403 });

    expect(pubnub.destroy).toHaveBeenCalledTimes(1);

    result.stop();
  });

  it('only fires once on duplicate PNAccessDeniedCategory events', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const result = await startAgentInstance({
      pubnub,
      agentName: 'test_pam_dedup',
      card: makeTestCard(),
    });

    await new Promise((r) => setTimeout(r, 50));

    const listener = listeners[0];

    // Fire twice (subscribe + heartbeat both fail)
    listener.status!({ category: 'PNAccessDeniedCategory', statusCode: 403 });
    listener.status!({ category: 'PNAccessDeniedCategory', statusCode: 403 });

    expect(pubnub.destroy).toHaveBeenCalledTimes(1);

    result.stop();
  });

  it('ignores non-access-denied status events', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const result = await startAgentInstance({
      pubnub,
      agentName: 'test_pam_ignore',

    });

    await new Promise((r) => setTimeout(r, 50));

    const listener = listeners[0];

    listener.status!({ category: 'PNConnectedCategory' });
    listener.status!({ category: 'PNReconnectedCategory' });

    expect(pubnub.destroy).not.toHaveBeenCalled();

    result.stop();
  });
});
