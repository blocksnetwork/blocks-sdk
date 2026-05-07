/**
 * Agent instance protocol versioning tests.
 *
 * Covers:
 * - Unsupported version handling (targeted -> fail, broadcast -> ignore)
 * - protocolVersion on all outbound task event payloads and meta
 * - Presence-state includes preferredProtocolVersion and protocolVersions
 */
import { describe, it, expect, vi, beforeAll, afterAll } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { CURRENT_PROTOCOL_VERSION, SUPPORTED_PROTOCOL_VERSIONS } from '../src/runtime/protocol-version.js';
import { makeTestCard } from './helpers/test-card.js';

// Mock global fetch so connectAgent resolves quickly in tests.
// The connect response must include controlChannel so the agent instance
// knows which channel to subscribe/setState on.
const TEST_AGENT_ID = 'bbbbbbbb-2222-2222-2222-222222222222';
const originalFetch = globalThis.fetch;
beforeAll(() => {
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({
      accessToken: 'mock-jwt',
      refreshToken: 'mock-refresh',
      expiresIn: 3600,
      agentId: TEST_AGENT_ID,
      controlChannel: `agent.${TEST_AGENT_ID}.control`,
    }),
  }) as unknown as typeof fetch;
});
afterAll(() => {
  globalThis.fetch = originalFetch;
});

// Shared publish mock
let sharedPublish = vi.fn(async () => ({ timetoken: Date.now().toString() }));

vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => ({
    publish: (...args: unknown[]) => sharedPublish(...args),
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

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const createFakePubNub = (): { pubnub: any; listeners: any[] } => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  sharedPublish = vi.fn(async () => ({ timetoken: Date.now().toString() }));
  const pubnub = {
    publish: sharedPublish,
    addMessageAction: vi.fn().mockResolvedValue({}),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    addListener: (l: any) => listeners.push(l),
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

// ============================================================================
// Unsupported protocol version handling
// ============================================================================

describe('unsupported protocol version handling', () => {
  it('targeted StartTask with unsupported protocolVersion publishes terminal failed', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const { stop } = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      handler: async () => ({}),
      baseUrl: 'http://test',
    });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    // Targeted (non-broadcast) StartTask with unsupported version
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'unsupported-v-task',
        ownerId: 'user1',
        protocolVersion: '1999-01-01',
      },
      userMetadata: { instance: 'AG-acme_echo-123' },
    });

    await vi.waitFor(() => {
      const publishCalls = pubnub.publish.mock.calls;
      const terminalCall = publishCalls.find((call: unknown[]) => {
        const args = call[0] as { message?: { type?: string; error?: string } };
        return (
          args.message?.type === 'terminal' &&
          args.message?.error === 'unsupported_protocol_version'
        );
      });
      expect(terminalCall).toBeDefined();
    });

    stop();
  });

  it('broadcast StartTask with unsupported protocolVersion is silently ignored', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});
    const { stop } = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      handler,
      baseUrl: 'http://test',
    });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    // Broadcast StartTask with unsupported version
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'broadcast-unsupported',
        ownerId: 'user1',
        protocolVersion: '1999-01-01',
      },
      userMetadata: { broadcast: 'true' },
    });

    // Wait a bit and verify the handler was never called
    await new Promise(r => setTimeout(r, 100));
    expect(handler).not.toHaveBeenCalled();

    // Also verify no terminal was published (silently ignored)
    const publishCalls = pubnub.publish.mock.calls;
    const terminalCalls = publishCalls.filter((call: unknown[]) => {
      const args = call[0] as { message?: { type?: string; taskId?: string } };
      return args.message?.type === 'terminal' && args.message?.taskId === 'broadcast-unsupported';
    });
    expect(terminalCalls).toHaveLength(0);

    stop();
  });

  it('StartTask with supported protocolVersion proceeds normally', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});
    const { stop } = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      handler,
      baseUrl: 'http://test',
    });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'supported-v-task',
        ownerId: 'user1',
        protocolVersion: CURRENT_PROTOCOL_VERSION,
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    stop();
  });

  it('StartTask without protocolVersion proceeds normally', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn().mockResolvedValue({});
    const { stop } = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      handler,
      baseUrl: 'http://test',
    });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'no-version-task',
        ownerId: 'user1',
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    stop();
  });
});

// ============================================================================
// Outbound task events include protocolVersion
// ============================================================================

describe('outbound task events include protocolVersion', () => {
  it('progress event body and meta include protocolVersion', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const { stop } = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      handler: async () => ({}),
      baseUrl: 'http://test',
    });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'pv-task', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const publishCalls = pubnub.publish.mock.calls;
      const progressCall = publishCalls.find((call: unknown[]) => {
        const args = call[0] as { message?: { type?: string } };
        return args.message?.type === 'progress';
      });
      expect(progressCall).toBeDefined();
    });

    const publishCalls = pubnub.publish.mock.calls;
    const progressCall = publishCalls.find((call: unknown[]) => {
      const args = call[0] as { message?: { type?: string } };
      return args.message?.type === 'progress';
    });
    const args = progressCall![0] as {
      message?: { protocolVersion?: string };
      meta?: { protocolVersion?: string };
    };
    expect(args.message?.protocolVersion).toBe(CURRENT_PROTOCOL_VERSION);
    expect(args.meta?.protocolVersion).toBe(CURRENT_PROTOCOL_VERSION);

    stop();
  });

  it('terminal event body and meta include protocolVersion', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const { stop } = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      handler: async () => ({}),
      baseUrl: 'http://test',
    });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'terminal-pv', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const publishCalls = pubnub.publish.mock.calls;
      const terminalCall = publishCalls.find((call: unknown[]) => {
        const args = call[0] as { message?: { type?: string } };
        return args.message?.type === 'terminal';
      });
      expect(terminalCall).toBeDefined();
    });

    const publishCalls = pubnub.publish.mock.calls;
    const terminalCall = publishCalls.find((call: unknown[]) => {
      const args = call[0] as { message?: { type?: string } };
      return args.message?.type === 'terminal';
    });
    const args = terminalCall![0] as {
      message?: { protocolVersion?: string };
      meta?: { protocolVersion?: string };
    };
    expect(args.message?.protocolVersion).toBe(CURRENT_PROTOCOL_VERSION);
    expect(args.meta?.protocolVersion).toBe(CURRENT_PROTOCOL_VERSION);

    stop();
  });

  it('all task event meta includes protocolVersion alongside agentName and taskId', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const { stop } = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      handler: async () => ({}),
      baseUrl: 'http://test',
    });
    await vi.waitFor(() => expect(pubnub.subscribe).toHaveBeenCalled());

    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'meta-check', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const publishCalls = pubnub.publish.mock.calls;
      const taskPublish = publishCalls.find((call: unknown[]) => {
        const args = call[0] as { channel?: string };
        return args.channel?.startsWith('u.');
      });
      expect(taskPublish).toBeDefined();
    });

    // Check all task channel publishes have protocolVersion in meta
    const taskPublishes = pubnub.publish.mock.calls.filter((call: unknown[]) => {
      const args = call[0] as { channel?: string };
      return args.channel?.startsWith('u.');
    });

    for (const call of taskPublishes) {
      const args = call[0] as {
        meta?: { agentName?: string; taskId?: string; protocolVersion?: string };
      };
      expect(args.meta?.protocolVersion).toBe(CURRENT_PROTOCOL_VERSION);
      expect(args.meta?.agentName).toBe('acme_echo');
    }

    stop();
  });
});

// ============================================================================
// Presence state version fields
// ============================================================================

describe('presence state includes protocol version fields', () => {
  it('sets preferredProtocolVersion and protocolVersions on startup', async () => {
    const { pubnub } = createFakePubNub();

    const { instanceId, stop } = await startAgentInstance({
      pubnub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      concurrency: 2,
      baseUrl: 'http://test',
    });

    // setState happens asynchronously after registration
    await vi.waitFor(() => expect(pubnub.setState).toHaveBeenCalled());

    expect(pubnub.setState).toHaveBeenCalledWith({
      channels: [`agent.${TEST_AGENT_ID}.control`],
      state: expect.objectContaining({
        instanceId,
        preferredProtocolVersion: CURRENT_PROTOCOL_VERSION,
        protocolVersions: [...SUPPORTED_PROTOCOL_VERSIONS],
      }),
    });

    stop();
  });
});
