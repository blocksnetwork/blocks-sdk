/**
 * BLOCKS-373 end-to-end log-hygiene acceptance test.
 *
 * Enforces three properties against startAgentInstance():
 *   1. LOG_LEVEL=info (default) emits NO PubNub-internal vocabulary
 *      during normal startup.
 *   2. LOG_LEVEL=error emits NO output at all during a healthy
 *      startup → stop() lifecycle.
 *   3. BLOCKS_DEBUG_INTERNAL=diagnostics restores the diagnostics surface
 *      (transport_diagnostics_armed visible). LOG_LEVEL=debug alone does not.
 */
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';

const TEST_AGENT_ID = 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee';

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

const makeFakePubNub = () => {
  const listeners: unknown[] = [];
  return {
    setToken: () => {},
    addListener: (l: unknown) => { listeners.push(l); },
    removeListener: (l: unknown) => {
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

const FORBIDDEN_AT_INFO = [
  'PNConnectedCategory',
  'transport_diagnostics_armed',
  'transport_status_transition',
  'transport_alive_snapshot',
  'PubNub connected to',          // legacy human line
  'snapshotIntervalMs',
  'staleThresholdMs',
];

describe('SDK log hygiene — BLOCKS-373 acceptance criteria', () => {
  let logSpy: ReturnType<typeof vi.spyOn>;
  let warnSpy: ReturnType<typeof vi.spyOn>;
  let errorSpy: ReturnType<typeof vi.spyOn>;
  let infoSpy: ReturnType<typeof vi.spyOn>;
  let instance: { stop: () => void } | undefined;

  beforeEach(() => {
    logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
    warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    infoSpy = vi.spyOn(console, 'info').mockImplementation(() => {});
  });

  afterEach(() => {
    if (instance) {
      instance.stop();
      instance = undefined;
    }
    logSpy.mockRestore();
    warnSpy.mockRestore();
    errorSpy.mockRestore();
    infoSpy.mockRestore();
    delete process.env.LOG_LEVEL;
    delete process.env.BLOCKS_DEBUG_INTERNAL;
  });

  const collectAllOutput = () => {
    const all: string[] = [];
    for (const call of logSpy.mock.calls) all.push(JSON.stringify(call));
    for (const call of warnSpy.mock.calls) all.push(JSON.stringify(call));
    for (const call of errorSpy.mock.calls) all.push(JSON.stringify(call));
    for (const call of infoSpy.mock.calls) all.push(JSON.stringify(call));
    return all.join('\n');
  };

  it('AC1: LOG_LEVEL=info emits no PubNub-internal vocabulary at startup', async () => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    process.env.LOG_LEVEL = 'info';

    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'ac1_info',
      pubnub: makeFakePubNub() as never,
      card: makeTestCard({ agentName: 'ac1_info' }),
    });

    const output = collectAllOutput();
    for (const forbidden of FORBIDDEN_AT_INFO) {
      expect(output, `forbidden substring "${forbidden}" appeared at LOG_LEVEL=info`).not.toContain(forbidden);
    }
    // De-brand: no raw PN…Operation strings should leak at any LOG_LEVEL.
    expect(output, 'raw PN…Operation string leaked at LOG_LEVEL=info').not.toMatch(/PN[A-Z]\w+Operation/);
  });

  it('AC2: LOG_LEVEL=error emits no output at all during a healthy startup + stop', async () => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    process.env.LOG_LEVEL = 'error';

    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'ac2_error',
      pubnub: makeFakePubNub() as never,
      card: makeTestCard({ agentName: 'ac2_error' }),
    });
    instance.stop();
    instance = undefined;

    expect(logSpy).not.toHaveBeenCalled();
    expect(warnSpy).not.toHaveBeenCalled();
    expect(errorSpy).not.toHaveBeenCalled();
    expect(infoSpy).not.toHaveBeenCalled();
  });

  it('AC3: BLOCKS_DEBUG_INTERNAL=diagnostics restores diagnostics surface', async () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'diagnostics';
    process.env.LOG_LEVEL = 'info';

    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'ac3_debug',
      pubnub: makeFakePubNub() as never,
      card: makeTestCard({ agentName: 'ac3_debug' }),
    });

    expect(collectAllOutput()).toContain('transport_diagnostics_armed');
  });

  it('AC3b: LOG_LEVEL=debug alone does NOT restore diagnostics surface', async () => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    process.env.LOG_LEVEL = 'debug';

    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'ac3b_debug_level',
      pubnub: makeFakePubNub() as never,
      card: makeTestCard({ agentName: 'ac3b_debug_level' }),
    });

    expect(collectAllOutput()).not.toContain('transport_diagnostics_armed');
  });

  it('AC2 corollary: stop() emits nothing at LOG_LEVEL=error mid-lifecycle', async () => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    process.env.LOG_LEVEL = 'error';

    instance = await startAgentInstance({
      handler: async () => ({}),
      agentName: 'ac2_stop',
      pubnub: makeFakePubNub() as never,
      card: makeTestCard({ agentName: 'ac2_stop' }),
    });

    // Clear startup output, then exercise stop().
    logSpy.mockClear();
    warnSpy.mockClear();
    errorSpy.mockClear();
    infoSpy.mockClear();

    instance.stop();
    instance = undefined;

    expect(logSpy).not.toHaveBeenCalled();
    expect(warnSpy).not.toHaveBeenCalled();
    expect(errorSpy).not.toHaveBeenCalled();
    expect(infoSpy).not.toHaveBeenCalled();
  });
});
