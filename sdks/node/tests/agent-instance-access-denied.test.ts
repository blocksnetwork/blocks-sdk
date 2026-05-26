import { describe, it, expect, vi, beforeAll, afterAll, beforeEach } from 'vitest';
import { createPubNubClient } from '../src/runtime/pubnub-client.js';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';

const TEST_AGENT_ID = 'dddddddd-4444-4444-4444-444444444444';

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

describe('access-denied handler output hygiene', () => {
  it('emits neutral language and no PubNub/PAM vocabulary on PNAccessDeniedCategory', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'access_denied_hygiene',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-access_denied_hygiene-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
    };
    // The main listener (carries pamDeniedHandler/accessDeniedHandler) has BOTH status and message functions.
    // The connectivity listener has only status. Pick the listener with both.
    const mainListener = stubPn.addListener.mock.calls
      .map(([arg]) => arg as { status?: (e: unknown) => void; message?: unknown })
      .find((l) => typeof l?.status === 'function' && typeof l?.message === 'function');
    expect(mainListener).toBeDefined();

    mainListener!.status!({
      category: 'PNAccessDeniedCategory',
      operation: 'PNSubscribeOperation',
      statusCode: 403,
    });

    const output = errSpy.mock.calls
      .flat()
      .map((arg) => (typeof arg === 'string' ? arg : JSON.stringify(arg)))
      .join('\n');

    expect(output).toContain('access denied — destroying control client');
    expect(output).toContain('access token expired or revoked');
    expect(output).toContain('"event":"access_denied_destroy"');
    expect(output).toContain('"event":"access_denied_user_message"');
    expect(output).not.toContain('PAM');
    expect(output).not.toContain('PubNub');
    expect(output).not.toContain('pam_');

    errSpy.mockRestore();
    stop();
  });

  it('emits neutral operation label, no raw PN…Operation string', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'access_denied_op_label',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-access_denied_op_label-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
    };
    const mainListener = stubPn.addListener.mock.calls
      .map(([arg]) => arg as { status?: (e: unknown) => void; message?: unknown })
      .find((l) => typeof l?.status === 'function' && typeof l?.message === 'function');

    mainListener!.status!({
      category: 'PNAccessDeniedCategory',
      operation: 'PNSubscribeOperation',
      statusCode: 403,
    });

    const output = errSpy.mock.calls
      .flat()
      .map((arg) => (typeof arg === 'string' ? arg : JSON.stringify(arg)))
      .join('\n');

    // No raw PN…Operation strings leak.
    expect(output).not.toMatch(/PN[A-Z]\w+Operation/);
    // Neutral label IS present in the structured log entry.
    expect(output).toContain('"operation":"subscribe"');

    errSpy.mockRestore();
    stop();
  });

  it('destroys control client on Event-Engine-wrapped subscribe 403 (PNConnectionErrorCategory + nested PNAccessDeniedCategory)', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'access_denied_wrapped',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-access_denied_wrapped-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
      destroy: ReturnType<typeof vi.fn>;
    };
    const mainListener = stubPn.addListener.mock.calls
      .map(([arg]) => arg as { status?: (e: unknown) => void; message?: unknown })
      .find((l) => typeof l?.status === 'function' && typeof l?.message === 'function');
    expect(mainListener).toBeDefined();

    // Wrapper shape: outer=PNConnectionErrorCategory, error=PNAccessDeniedCategory, no statusCode.
    mainListener!.status!({
      category: 'PNConnectionErrorCategory',
      error: 'PNAccessDeniedCategory',
      operation: 'PNSubscribeOperation',
    });

    const output = errSpy.mock.calls
      .flat()
      .map((arg) => (typeof arg === 'string' ? arg : JSON.stringify(arg)))
      .join('\n');

    expect(output).toContain('access denied — destroying control client');
    expect(output).toContain('"event":"access_denied_destroy"');
    expect(stubPn.destroy).toHaveBeenCalledTimes(1);

    errSpy.mockRestore();
    stop();
  });

  it('destroys control client on a plain 403 statusCode even with an unfamiliar category', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

    const { stop } = await startAgentInstance({
      agentName: 'access_denied_403_only',
      baseUrl: 'http://test-host',
      card: makeTestCard(),
    });
    await new Promise((r) => setTimeout(r, 50));

    const controlCall = mocked.mock.calls.find(([cfg]) =>
      typeof cfg.userId === 'string' && cfg.userId.startsWith('AG-access_denied_403_only-'),
    );
    const stubIndex = mocked.mock.calls.indexOf(controlCall!);
    const stubPn = mocked.mock.results[stubIndex]?.value as {
      addListener: ReturnType<typeof vi.fn>;
      destroy: ReturnType<typeof vi.fn>;
    };
    const mainListener = stubPn.addListener.mock.calls
      .map(([arg]) => arg as { status?: (e: unknown) => void; message?: unknown })
      .find((l) => typeof l?.status === 'function' && typeof l?.message === 'function');

    // Defensive: an unfamiliar future category that still carries a 403.
    mainListener!.status!({
      category: 'PNFutureWeirdCategory',
      operation: 'PNSubscribeOperation',
      statusCode: 403,
    });

    const output = errSpy.mock.calls
      .flat()
      .map((arg) => (typeof arg === 'string' ? arg : JSON.stringify(arg)))
      .join('\n');

    expect(output).toContain('"event":"access_denied_destroy"');
    expect(stubPn.destroy).toHaveBeenCalledTimes(1);

    errSpy.mockRestore();
    stop();
  });
});
