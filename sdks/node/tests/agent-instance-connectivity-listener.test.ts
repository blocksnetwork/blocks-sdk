import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from 'vitest';
import { createPubNubClient } from '../src/runtime/pubnub-client.js';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';

const TEST_AGENT_ID = 'cccccccc-3333-3333-3333-333333333333';

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
        json: async () => ({ agent: { agentName, billingMode: 'free', listing: 'public' } }),
      };
    }
    return {
      ok: true, status: 200,
      json: async () => ({
        accessToken: 'mock-jwt', refreshToken: 'mock-refresh', expiresIn: 3600,
        agentId: TEST_AGENT_ID,
        controlChannel: `agent.${TEST_AGENT_ID}.control`,
      }),
    };
  }) as unknown as typeof fetch;
});
afterAll(() => { globalThis.fetch = originalFetch; });

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

beforeEach(() => { mocked.mockClear(); });

/**
 * The connectivity listener registered on the control client is the
 * single-status listener (no `message`). It logs `transport_degraded`
 * (warn) for network/timeout/malformed, `transport_restored` (info)
 * for reconnected.
 */
function findConnectivityListener(stubPn: { addListener: ReturnType<typeof vi.fn> }) {
  const calls = stubPn.addListener.mock.calls.map(([arg]) =>
    arg as { status?: (e: unknown) => void; message?: unknown },
  );
  return calls.find((l) => typeof l?.status === 'function' && l?.message === undefined);
}

/**
 * The diagnostic listener (gated by BLOCKS_DEBUG_INTERNAL=diagnostics)
 * is registered via trackClient() AFTER the main message listener and
 * the connectivity listener. Both the main message listener and the
 * diag listener carry status+message handlers, so we can't distinguish
 * by signature — fall back to ORDER. The diag listener is the LAST
 * one with both handlers.
 */
function findDiagListener(stubPn: { addListener: ReturnType<typeof vi.fn> }) {
  const calls = stubPn.addListener.mock.calls.map(([arg]) =>
    arg as { status?: (e: unknown) => void; message?: unknown },
  );
  const both = calls.filter(
    (l) => typeof l?.status === 'function' && typeof l?.message === 'function',
  );
  return both[both.length - 1];
}

describe('connectivity listener — Event Engine wrapper unwrap', () => {
  it('emits transport_degraded warn line for PNConnectionErrorCategory + PNNetworkIssuesCategory', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'connectivity_wrapped_network',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-connectivity_wrapped_network-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
    };
    const conn = findConnectivityListener(stubPn);
    expect(conn).toBeDefined();

    conn!.status!({
      category: 'PNConnectionErrorCategory',
      error: 'PNNetworkIssuesCategory',
    });

    const out = warnSpy.mock.calls
      .flat()
      .map((a) => (typeof a === 'string' ? a : JSON.stringify(a)))
      .join('\n');
    expect(out).toContain('"event":"transport_degraded"');
    expect(out).toContain('"category":"network_issues"');

    warnSpy.mockRestore();
    stop();
  });

  it('emits transport_degraded warn line for PNDisconnectedUnexpectedlyCategory + PNTimeoutCategory', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'connectivity_wrapped_timeout',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-connectivity_wrapped_timeout-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
    };
    const conn = findConnectivityListener(stubPn);

    conn!.status!({
      category: 'PNDisconnectedUnexpectedlyCategory',
      error: 'PNTimeoutCategory',
    });

    const out = warnSpy.mock.calls
      .flat()
      .map((a) => (typeof a === 'string' ? a : JSON.stringify(a)))
      .join('\n');
    expect(out).toContain('"event":"transport_degraded"');
    expect(out).toContain('"category":"timeout"');

    warnSpy.mockRestore();
    stop();
  });

  it('does NOT emit transport_degraded for a wrapper without a recognised leaf', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'connectivity_wrapped_unknown',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-connectivity_wrapped_unknown-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
    };
    const conn = findConnectivityListener(stubPn);

    conn!.status!({
      category: 'PNConnectionErrorCategory',
      error: 'PNFutureLeafCategory',
    });

    const out = warnSpy.mock.calls
      .flat()
      .map((a) => (typeof a === 'string' ? a : JSON.stringify(a)))
      .join('\n');
    // 'other' is not in DEGRADED_TRANSPORT_CATEGORIES → no warn.
    expect(out).not.toContain('"event":"transport_degraded"');

    warnSpy.mockRestore();
    stop();
  });

  it('emits transport_degraded only on the entry into the degraded set (edge-triggered, not on every echo)', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'connectivity_edge_trigger',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-connectivity_edge_trigger-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
    };
    const conn = findConnectivityListener(stubPn);

    // Three consecutive degraded statuses (PubNub fires per failed handshake).
    conn!.status!({ category: 'PNNetworkIssuesCategory' });
    conn!.status!({ category: 'PNTimeoutCategory' });
    conn!.status!({ category: 'PNNetworkIssuesCategory' });

    const degradedCount = warnSpy.mock.calls
      .flat()
      .map((a) => (typeof a === 'string' ? a : JSON.stringify(a)))
      .filter((s) => s.includes('"event":"transport_degraded"'))
      .length;
    expect(degradedCount).toBe(1);

    warnSpy.mockRestore();
    stop();
  });

  it('emits transport_restored only after a degraded transition (and only once per recovery)', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'connectivity_restored_edge',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-connectivity_restored_edge-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
    };
    const conn = findConnectivityListener(stubPn);

    // No prior degraded state: a bare 'reconnected' must NOT emit `transport_restored`.
    conn!.status!({ category: 'PNReconnectedCategory' });
    const noPriorRestored = logSpy.mock.calls
      .flat()
      .map((a) => (typeof a === 'string' ? a : JSON.stringify(a)))
      .filter((s) => s.includes('"event":"transport_restored"'))
      .length;
    expect(noPriorRestored).toBe(0);

    // Now go degraded, then reconnected twice. Restored fires once.
    conn!.status!({ category: 'PNNetworkIssuesCategory' });
    conn!.status!({ category: 'PNReconnectedCategory' });
    conn!.status!({ category: 'PNReconnectedCategory' });

    const restoredCount = logSpy.mock.calls
      .flat()
      .map((a) => (typeof a === 'string' ? a : JSON.stringify(a)))
      .filter((s) => s.includes('"event":"transport_restored"'))
      .length;
    expect(restoredCount).toBe(1);

    warnSpy.mockRestore();
    logSpy.mockRestore();
    stop();
  });
});

describe('diag listener — Event Engine wrapper unwrap', () => {
  // diagEnabled is captured ONCE inside startAgentInstance via
  // isDebugSubsystemEnabled('diagnostics'), so the env var must be set
  // BEFORE the agent starts. LOG_LEVEL=debug is also required because
  // the transport_status line is emitted at debug level and is
  // otherwise filtered out (default LOG_LEVEL=info).
  const originalDebugInternal = process.env.BLOCKS_DEBUG_INTERNAL;
  const originalLogLevel = process.env.LOG_LEVEL;

  beforeEach(() => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'diagnostics';
    process.env.LOG_LEVEL = 'debug';
  });

  afterEach(() => {
    if (originalDebugInternal === undefined) delete process.env.BLOCKS_DEBUG_INTERNAL;
    else process.env.BLOCKS_DEBUG_INTERNAL = originalDebugInternal;
    if (originalLogLevel === undefined) delete process.env.LOG_LEVEL;
    else process.env.LOG_LEVEL = originalLogLevel;
  });

  it('unwraps PNConnectionErrorCategory + PNAccessDeniedCategory leaf into transport_status.category=access_denied', async () => {
    // log('debug', ...) routes through console.log (not console.debug);
    // see logger.ts: only error -> console.error and warn -> console.warn,
    // everything else falls through to console.log.
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'diag_wrapped_access_denied',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-diag_wrapped_access_denied-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
    };
    const diag = findDiagListener(stubPn);
    expect(diag).toBeDefined();

    diag!.status!({
      category: 'PNConnectionErrorCategory',
      error: 'PNAccessDeniedCategory',
      operation: 'PNSubscribeOperation',
      statusCode: 403,
    });

    const out = logSpy.mock.calls
      .flat()
      .map((a) => (typeof a === 'string' ? a : JSON.stringify(a)))
      .join('\n');
    // Regression guard: if buildDiagListener regressed to
    // String(e.category ?? '') the line would carry
    // "category":"PNConnectionErrorCategory" (or "other"), poisoning
    // entry.lastCategory used by _isDiagEntryStale.
    expect(out).toContain('"event":"transport_status"');
    expect(out).toContain('"category":"access_denied"');
    expect(out).not.toContain('"category":"other"');
    expect(out).not.toContain('"category":"PNConnectionErrorCategory"');

    logSpy.mockRestore();
    stop();
  });
});
