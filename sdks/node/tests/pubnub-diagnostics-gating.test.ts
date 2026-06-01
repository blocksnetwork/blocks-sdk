/**
 * The connectivity-diagnostics subsystem must be
 * silent by default and must restore its output when
 * BLOCKS_DEBUG_INTERNAL=diagnostics is set. LOG_LEVEL=debug alone does NOT
 * enable diagnostics.
 */
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';

const TEST_AGENT_ID = 'dddddddd-dddd-dddd-dddd-dddddddddddd';

const originalFetch = globalThis.fetch;
beforeAll(() => {
  globalThis.fetch = vi.fn().mockImplementation(async (url: string) => {
    if (url.includes('config.json') || url.includes('/cdm')) {
      return {
        ok: true, status: 200,
        json: async () => ({
          api: { baseUrl: 'http://test-host' },
          playground: { publishKey: 'pub-pg', subscribeKey: 'sub-pg' },
          network: { publishKey: 'pub-net', subscribeKey: 'sub-net' },
        }),
      };
    }
    if (url.includes('/registry/agents?')) {
      const match = url.match(/agentName=([^&]+)/);
      const agentName = match ? decodeURIComponent(match[1]) : 'unknown';
      return {
        ok: true, status: 200,
        json: async () => ({
          agent: { agentName, billingMode: 'free', listing: 'public' },
        }),
      };
    }
    return {
      ok: true, status: 200,
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

interface FakePubNub {
  setToken: (t: string) => void;
  addListener: (l: unknown) => void;
  removeListener: (l: unknown) => void;
  subscribe: (a: unknown) => void;
  unsubscribe: (a: unknown) => void;
  destroy: () => void;
  setFilterExpression?: (e: string) => void;
  getSubscribedChannels?: () => string[];
  publish: (a: unknown) => Promise<unknown>;
  __listeners: unknown[];
}
const makeFakePubNub = (): FakePubNub => {
  const listeners: unknown[] = [];
  return {
    setToken: () => {},
    addListener: (l) => { listeners.push(l); },
    removeListener: (l) => {
      const i = listeners.indexOf(l);
      if (i >= 0) listeners.splice(i, 1);
    },
    subscribe: () => {},
    unsubscribe: () => {},
    destroy: () => {},
    setFilterExpression: () => {},
    getSubscribedChannels: () => [],
    publish: async () => ({}),
    __listeners: listeners,
  };
};

describe('PubNub diagnostics gating', () => {
  let logSpy: ReturnType<typeof vi.spyOn>;
  let warnSpy: ReturnType<typeof vi.spyOn>;
  let errorSpy: ReturnType<typeof vi.spyOn>;
  let instance: { stop: () => void } | undefined;

  beforeEach(() => {
    logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
    warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
  });

  afterEach(async () => {
    if (instance) {
      instance.stop();
      instance = undefined;
    }
    logSpy.mockRestore();
    warnSpy.mockRestore();
    errorSpy.mockRestore();
    delete process.env.LOG_LEVEL;
    delete process.env.BLOCKS_DEBUG_INTERNAL;
  });

  const collectAllOutput = () => {
    const all: string[] = [];
    for (const call of logSpy.mock.calls) all.push(JSON.stringify(call));
    for (const call of warnSpy.mock.calls) all.push(JSON.stringify(call));
    for (const call of errorSpy.mock.calls) all.push(JSON.stringify(call));
    return all.join('\n');
  };

  it('emits NO diagnostics output when BLOCKS_DEBUG_INTERNAL is unset and LOG_LEVEL=info', async () => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    process.env.LOG_LEVEL = 'info';

    const pn = makeFakePubNub();
    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'gating_default',
      pubnub: pn as never,
      card: makeTestCard({ agentName: 'gating_default' }),
    });

    const output = collectAllOutput();
    expect(output).not.toContain('transport_diagnostics_armed');
    expect(output).not.toContain('transport_status_transition');
    expect(output).not.toContain('transport_alive_snapshot');
  });

  it('emits transport_diagnostics_armed at startup when BLOCKS_DEBUG_INTERNAL=diagnostics', async () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'diagnostics';
    process.env.LOG_LEVEL = 'info';

    const pn = makeFakePubNub();
    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'gating_debug_flag',
      pubnub: pn as never,
      card: makeTestCard({ agentName: 'gating_debug_flag' }),
    });

    expect(collectAllOutput()).toContain('transport_diagnostics_armed');
  });

  it('does NOT emit diagnostics when LOG_LEVEL=debug but BLOCKS_DEBUG_INTERNAL is unset', async () => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    process.env.LOG_LEVEL = 'debug';

    const pn = makeFakePubNub();
    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'gating_debug_level',
      pubnub: pn as never,
      card: makeTestCard({ agentName: 'gating_debug_level' }),
    });

    expect(collectAllOutput()).not.toContain('transport_diagnostics_armed');
  });

  it('attaches only main + connectivity listeners (no diag listener) when gated off', async () => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    process.env.LOG_LEVEL = 'info';

    const pn = makeFakePubNub();
    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'gating_listener_count',
      pubnub: pn as never,
      card: makeTestCard({ agentName: 'gating_listener_count' }),
    });

    // Two listeners: the main (status/message) handler and the connectivity
    // listener (always attached, watches PNNetworkDownCategory etc.).
    // No diag listener when BLOCKS_DEBUG_INTERNAL is unset.
    expect(pn.__listeners.length).toBe(2);
  });

  it('attaches main + connectivity + diag listeners when gated on', async () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'diagnostics';
    process.env.LOG_LEVEL = 'info';

    const pn = makeFakePubNub();
    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'gating_listener_count_on',
      pubnub: pn as never,
      card: makeTestCard({ agentName: 'gating_listener_count_on' }),
    });

    // Three listeners: main (status/message) handler + connectivity listener
    // (always attached) + diag listener (from trackClient, gated on).
    expect(pn.__listeners.length).toBe(3);
  });

  it('stop() emits no PubNub vocabulary at default LOG_LEVEL', async () => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    process.env.LOG_LEVEL = 'info';

    const pn = makeFakePubNub();
    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'gating_stop_silent',
      pubnub: pn as never,
      card: makeTestCard({ agentName: 'gating_stop_silent' }),
    });

    // Clear pre-stop output so we only inspect what stop() prints.
    logSpy.mockClear();
    warnSpy.mockClear();
    errorSpy.mockClear();

    instance.stop();
    instance = undefined;

    const output = collectAllOutput();
    expect(output).not.toContain('transport_status_transition');
    expect(output).not.toContain('PNConnectedCategory');
  });
});
