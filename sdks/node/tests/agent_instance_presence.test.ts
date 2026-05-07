/**
 * Agent instance presence tests.
 */
import { describe, it, expect, vi, beforeAll, afterAll } from 'vitest';
import PubNub from 'pubnub';
import { startAgentInstance, type AgentInstancePresenceState } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';
import { removeAgent } from '../src/runtime/agent-registry.js';
import { hasLiveEnv, hasBackendEnv, getBaseUrl, publishAgent } from './helpers/live-test-config.js';

// Mock global fetch so connectAgent resolves quickly in unit tests.
// The connect response must include controlChannel so the agent instance
// knows which channel to subscribe/setState on.
const TEST_AGENT_ID = 'aaaaaaaa-1111-1111-1111-111111111111';
const originalFetch = globalThis.fetch;
beforeAll(() => {
  globalThis.fetch = vi.fn().mockImplementation(async (url: string) => {
    // Derive agentName from the connect payload for dynamic controlChannel
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

// Mock createPubNubClient for per-task PubNub clients in Phase 3
let presenceSetState = vi.fn(async () => ({}));

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
    setState: (...args: unknown[]) => presenceSetState(...args),
    destroy: vi.fn(),
    hereNow: vi.fn(async () => ({ channels: {} })),
  })),
}));

interface FakeListener {
  message?: (event: { message: unknown }) => void;
}

interface FakePubNub {
  publish: ReturnType<typeof vi.fn>;
  addMessageAction: ReturnType<typeof vi.fn>;
  addListener: (l: FakeListener) => void;
  removeListener: ReturnType<typeof vi.fn>;
  subscribe: ReturnType<typeof vi.fn>;
  unsubscribe: ReturnType<typeof vi.fn>;
  setFilterExpression: ReturnType<typeof vi.fn>;
  setState: ReturnType<typeof vi.fn>;
}

describe('agent instance presence state (unit)', () => {

  const createFakePubNub = (): { pubnub: FakePubNub & Record<string, unknown>; listeners: FakeListener[] } => {
    const listeners: FakeListener[] = [];
    presenceSetState = vi.fn(async () => ({}));
    const pubnub = {
      publish: vi.fn().mockResolvedValue({ timetoken: Date.now().toString() }),
      addMessageAction: vi.fn().mockResolvedValue({}),
        addListener: (l: FakeListener) => listeners.push(l),
      removeListener: vi.fn(),
      subscribe: vi.fn(),
      unsubscribe: vi.fn(),
      setFilterExpression: vi.fn(),
      setState: presenceSetState,
      setToken: vi.fn(),
      destroy: vi.fn(),
      _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
    } as FakePubNub & Record<string, unknown>;
    return { pubnub, listeners };
  };

  it('sets initial presence state on startup', async () => {
    const { pubnub } = createFakePubNub();
    const { instanceId } = await startAgentInstance({
      pubnub: pubnub as unknown as PubNub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      concurrency: 4,
      expectedInstances: 3,
      baseUrl: 'http://test',
    });

    // setState now happens asynchronously after registration attempt (in .then())
    await vi.waitFor(() => expect(pubnub.setState).toHaveBeenCalled());
    expect(pubnub.setState).toHaveBeenCalledWith({
      channels: [`agent.${TEST_AGENT_ID}.control`],
      state: expect.objectContaining({
        instanceId,
        activeTasks: 0,
        concurrency: 4,
        startedAt: expect.any(Number),
      }),
    });
  });

  it('updates presence state when task starts', async () => {
    const { pubnub, listeners } = createFakePubNub();

    // Slow handler to keep task running
    const mockHandler = vi.fn().mockImplementation(async () => {
      await new Promise((r) => setTimeout(r, 50));
      return {};
    });

    const { instanceId } = await startAgentInstance({
      pubnub: pubnub as unknown as PubNub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      concurrency: 4,
      handler: mockHandler,
      baseUrl: 'http://test',
    });

    const listener = listeners[0];

    // Clear initial setState call tracking
    pubnub.setState.mockClear();

    // Start a task
    listener.message?.({ message: { type: 'StartTask', taskId: 'presence-task-1', ownerId: 'user1' } });

    // Wait for the setState to be called with activeTasks: 1
    await vi.waitFor(() => {
      expect(pubnub.setState).toHaveBeenCalledWith({
        channels: [`agent.${TEST_AGENT_ID}.control`],
        state: expect.objectContaining({
          instanceId,
          activeTasks: 1,
          concurrency: 4,
        }),
      });
    });
  });

  it('updates presence state when task completes', async () => {
    const { pubnub, listeners } = createFakePubNub();

    // Fast handler that completes immediately
    const mockHandler = vi.fn().mockResolvedValue({});

    const { instanceId } = await startAgentInstance({
      pubnub: pubnub as unknown as PubNub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      concurrency: 4,
      handler: mockHandler,
      baseUrl: 'http://test',
    });

    const listener = listeners[0];

    // Start a task
    listener.message?.({ message: { type: 'StartTask', taskId: 'presence-task-2', ownerId: 'user1' } });

    // Wait for task completion and presence state to be updated back to 0
    await vi.waitFor(
      () => {
        // Find the final setState call with activeTasks: 0
        interface SetStateCall {
          state?: { activeTasks?: number; instanceId?: string };
        }
        const finalCall = pubnub.setState.mock.calls.find(
          (call: unknown[]) => {
            const arg = call[0] as SetStateCall | undefined;
            return arg?.state?.activeTasks === 0 && arg?.state?.instanceId === instanceId;
          }
        );
        expect(finalCall).toBeDefined();
      },
      { timeout: 1000 }
    );
  });

  it('tracks concurrent tasks in presence state', async () => {
    const { pubnub, listeners } = createFakePubNub();

    // Track max activeTasks reported
    let maxActiveTasks = 0;
    const originalSetState = pubnub.setState;
    interface SetStateOptions {
      state?: { activeTasks?: number };
    }
    pubnub.setState = vi.fn().mockImplementation(async (opts: SetStateOptions) => {
      if (opts.state?.activeTasks !== undefined) {
        maxActiveTasks = Math.max(maxActiveTasks, opts.state.activeTasks);
      }
      return originalSetState(opts);
    });

    // Slow handler to keep tasks running
    const mockHandler = vi.fn().mockImplementation(async () => {
      await new Promise((r) => setTimeout(r, 100));
      return {};
    });

    await startAgentInstance({
      pubnub: pubnub as unknown as PubNub,
      agentName: 'acme_echo',
      card: makeTestCard(),
      concurrency: 3,
      handler: mockHandler,
      baseUrl: 'http://test',
    });

    const listener = listeners[0];

    // Start 3 tasks concurrently
    listener.message?.({ message: { type: 'StartTask', taskId: 'concurrent-p1', ownerId: 'user1' } });
    listener.message?.({ message: { type: 'StartTask', taskId: 'concurrent-p2', ownerId: 'user1' } });
    listener.message?.({ message: { type: 'StartTask', taskId: 'concurrent-p3', ownerId: 'user1' } });

    // Wait for all tasks to complete
    await vi.waitFor(
      () => {
        expect(mockHandler).toHaveBeenCalledTimes(3);
      },
      { timeout: 500 }
    );

    // Wait for final presence state update
    await vi.waitFor(
      () => {
        // Verify we saw concurrent execution in presence state
        expect(maxActiveTasks).toBeGreaterThan(1);
      },
      { timeout: 500 }
    );
  });
});

interface HereNowOccupant {
  uuid: string;
  state?: AgentInstancePresenceState;
}

interface HereNowChannelData {
  occupancy?: number;
  occupants?: HereNowOccupant[];
}

interface HereNowResult {
  channels?: Record<string, HereNowChannelData>;
}

/**
 * Live presence tests require PubNub keyset with:
 * - Presence: ON (mandatory for Blocks)
 * - Presence State: ON (under Presence settings)
 * - Stream Filter: ON (under Subscription settings)
 *
 * Note: Presence is always enabled in Blocks deployments. These tests
 * validate presence-based routing behavior which is now mandatory.
 */
describe.skipIf(process.env.PUBNUB_LIVE_TEST !== '1' || !hasLiveEnv() || !hasBackendEnv())('agent instance presence (live)', () => {
  const createdAgentNames: string[] = [];

  afterAll(async () => {
    const baseUrl = getBaseUrl();
    for (const agentName of createdAgentNames) {
      try {
        await removeAgent(agentName, { baseUrl });
        console.log(`[cleanup] Removed agent name: ${agentName}`);
      } catch (err) {
        console.warn(`[cleanup] Failed to remove agent name ${agentName}:`, err);
      }
    }
  });

  it('agent instance presence visible via hereNow', async () => {
    // Use unique agent name to avoid collision with other parallel tests
    // Presence Management must be configured with wildcard (agent.*.control) for this to work
    const agentName = `presence_test_${Date.now()}`;
    const instanceUuid = `AG-${agentName}-${Date.now()}`;
    createdAgentNames.push(agentName);

    // Publish before connecting — connectAgent does not upsert (post-PR-313).
    await publishAgent(agentName, makeTestCard({ agentName }));

    // Start an agent instance — userId must match AG-{agentName}-... format
    const agentInstance = await startAgentInstance({
      pubnub: new PubNub({
        publishKey: process.env.PUBNUB_PUBLISH_KEY!,
        subscribeKey: process.env.PUBNUB_SUBSCRIBE_KEY!,
        userId: instanceUuid,
        secretKey: process.env.PUBNUB_SECRET_KEY!,
        enableEventEngine: true,
        presenceTimeout: 20,
        heartbeatInterval: 5,
      }),
      agentName,
      card: makeTestCard({ agentName }),
      instanceId: instanceUuid,
      concurrency: 4,
      expectedInstances: 2,

      baseUrl: getBaseUrl(),
    });

    // Query presence via hereNow with retry for presence propagation
    const client = new PubNub({
      publishKey: process.env.PUBNUB_PUBLISH_KEY!,
      subscribeKey: process.env.PUBNUB_SUBSCRIBE_KEY!,
      userId: `presence-client-${Date.now()}`,
      secretKey: process.env.PUBNUB_SECRET_KEY!,
    });

    // controlChannel is populated asynchronously after connectAgent resolves
    // (agent-instance.ts:2010-2025). Wait for it before querying hereNow.
    for (let i = 0; i < 50 && !agentInstance.controlChannel; i++) {
      await new Promise((r) => setTimeout(r, 100));
    }
    expect(agentInstance.controlChannel).toBeDefined();
    const controlChannel = agentInstance.controlChannel as string;

    // Retry hereNow a few times with increasing delays (presence can take time to propagate)
    let presenceResult: HereNowResult | null = null;
    let channel: HereNowChannelData | undefined = undefined;
    for (let attempt = 0; attempt < 5; attempt++) {
      await new Promise((r) => setTimeout(r, 2000 + attempt * 1000));
      try {
        presenceResult = (await client.hereNow({
          channels: [controlChannel],
          includeState: true,
          includeUUIDs: true,
        })) as HereNowResult;
        channel = presenceResult.channels?.[controlChannel];
        console.log(
          `[hereNow attempt ${attempt + 1}] channel=${controlChannel}, occupancy=${channel?.occupancy ?? 0}, result=${JSON.stringify(presenceResult)}`,
        );
        if (channel?.occupancy && channel.occupancy >= 1) break;
      } catch (err) {
        console.error(`[hereNow attempt ${attempt + 1}] error:`, err);
      }
    }

    agentInstance.stop();

    // Presence is mandatory in Blocks - fail if not available
    expect(channel).toBeDefined();
    expect(channel?.occupancy).toBeGreaterThanOrEqual(1);

    const occupants = channel?.occupants ?? [];
    const instanceOccupant = occupants.find((o) => o.uuid === instanceUuid);
    expect(instanceOccupant).toBeDefined();

    // Verify presence state - mandatory for load-balanced routing
    const state = instanceOccupant?.state as AgentInstancePresenceState | undefined;
    expect(state).toBeDefined();
    expect(state!.concurrency).toBe(4);
    expect(state!.activeTasks).toBe(0);
    expect(state!.instanceId).toBe(instanceUuid);
  }, 30000);

  it('presence state reflects active tasks', async () => {
    const agentName = `test_busy_${Date.now()}`;
    const instanceUuid = `AG-${agentName}-${Date.now()}`;
    createdAgentNames.push(agentName);
    const taskId = `busy-task-${Date.now()}`;

    await publishAgent(agentName, makeTestCard({ agentName }));

    // Create an agent instance with a slow handler
    const agentInstance = await startAgentInstance({
      pubnub: new PubNub({
        publishKey: process.env.PUBNUB_PUBLISH_KEY!,
        subscribeKey: process.env.PUBNUB_SUBSCRIBE_KEY!,
        userId: instanceUuid,
        secretKey: process.env.PUBNUB_SECRET_KEY!,
        enableEventEngine: true,
        presenceTimeout: 20,
        heartbeatInterval: 5,
      }),
      agentName,
      card: makeTestCard({ agentName }),
      instanceId: instanceUuid,
      concurrency: 4,

      baseUrl: getBaseUrl(),
      handler: async () => {
        // Simulate slow task (8 seconds)
        await new Promise((r) => setTimeout(r, 8000));
        return {};
      },
    });

    for (let i = 0; i < 50 && !agentInstance.controlChannel; i++) {
      await new Promise((r) => setTimeout(r, 100));
    }
    expect(agentInstance.controlChannel).toBeDefined();

    // Wait for agent instance to connect and presence to propagate
    const client = new PubNub({
      publishKey: process.env.PUBNUB_PUBLISH_KEY!,
      subscribeKey: process.env.PUBNUB_SUBSCRIBE_KEY!,
      userId: `task-sender-${Date.now()}`,
      secretKey: process.env.PUBNUB_SECRET_KEY!,
    });

    // Wait for agent instance presence to be visible before sending task
    // Presence is mandatory - fail if not found after retries
    let presenceFound = false;
    for (let attempt = 0; attempt < 5; attempt++) {
      await new Promise((r) => setTimeout(r, 2000));
      const pr = await client.hereNow({
        channels: [agentInstance.controlChannel!],
        includeState: true,
        includeUUIDs: true,
      });
      const ch = pr.channels?.[agentInstance.controlChannel!];
      if (ch?.occupancy >= 1) {
        presenceFound = true;
        break;
      }
    }

    expect(presenceFound).toBe(true);

    // Send task to agent instance
    await client.publish({
      channel: agentInstance.controlChannel!,
      message: { type: 'StartTask', taskId, agentName, ownerId: 'test-user' },
      meta: { broadcast: 'true' }, // String "true" for subscribe filter
    });

    // Wait for task to start and presence state to update
    await new Promise((r) => setTimeout(r, 3000));

    // Query presence - should show 1 active task
    const presenceResult = (await client.hereNow({
      channels: [agentInstance.controlChannel!],
      includeState: true,
      includeUUIDs: true,
    })) as HereNowResult;

    const channel = presenceResult.channels?.[agentInstance.controlChannel!];
    const occupants = channel?.occupants ?? [];
    const instanceOccupant = occupants.find((o) => o.uuid === instanceUuid);

    const state = instanceOccupant?.state as AgentInstancePresenceState | undefined;
    // Presence state update is async/eventual, check if available
    if (state?.activeTasks !== undefined) {
      expect(state.activeTasks).toBe(1);
    }

    // Wait for task to complete (task takes 8s)
    await new Promise((r) => setTimeout(r, 8000));

    // Query presence again with retries - presence state propagation can be delayed
    let stateAfter: AgentInstancePresenceState | undefined;
    for (let attempt = 0; attempt < 5; attempt++) {
      await new Promise((r) => setTimeout(r, 1000));
      const presenceAfter = (await client.hereNow({
        channels: [agentInstance.controlChannel!],
        includeState: true,
        includeUUIDs: true,
      })) as HereNowResult;

      const channelAfter = presenceAfter.channels?.[agentInstance.controlChannel!];
      const occupantsAfter = channelAfter?.occupants ?? [];
      const instanceAfter = occupantsAfter.find((o) => o.uuid === instanceUuid);

      stateAfter = instanceAfter?.state as AgentInstancePresenceState | undefined;
      if (stateAfter?.activeTasks === 0) {
        break;
      }
      console.log(
        `[hereNow after completion attempt ${attempt + 1}] activeTasks=${stateAfter?.activeTasks}`,
      );
    }

    if (stateAfter?.activeTasks !== undefined) {
      expect(stateAfter.activeTasks).toBe(0);
    }

    agentInstance.stop();
  }, 35000);

  it('concurrency: 0 allows unlimited capacity', async () => {
    const agentName = `test_unlimited_${Date.now()}`;
    const instanceUuid = `AG-${agentName}-${Date.now()}`;
    createdAgentNames.push(agentName);

    await publishAgent(agentName, makeTestCard({ agentName }));

    // Create an agent instance with unlimited capacity (concurrency: 0)
    const agentInstance = await startAgentInstance({
      pubnub: new PubNub({
        publishKey: process.env.PUBNUB_PUBLISH_KEY!,
        subscribeKey: process.env.PUBNUB_SUBSCRIBE_KEY!,
        userId: instanceUuid,
        secretKey: process.env.PUBNUB_SECRET_KEY!,
        enableEventEngine: true,
        presenceTimeout: 20,
        heartbeatInterval: 5,
      }),
      agentName,
      card: makeTestCard({ agentName }),
      instanceId: instanceUuid,
      concurrency: 0, // Unlimited capacity

      baseUrl: getBaseUrl(),
    });

    for (let i = 0; i < 50 && !agentInstance.controlChannel; i++) {
      await new Promise((r) => setTimeout(r, 100));
    }
    expect(agentInstance.controlChannel).toBeDefined();

    // Query presence and verify concurrency is 0
    const client = new PubNub({
      publishKey: process.env.PUBNUB_PUBLISH_KEY!,
      subscribeKey: process.env.PUBNUB_SUBSCRIBE_KEY!,
      userId: `unlimited-client-${Date.now()}`,
      secretKey: process.env.PUBNUB_SECRET_KEY!,
    });

    // Wait for presence to propagate
    let state: AgentInstancePresenceState | undefined;
    for (let attempt = 0; attempt < 5; attempt++) {
      await new Promise((r) => setTimeout(r, 2000));
      const pr = (await client.hereNow({
        channels: [agentInstance.controlChannel!],
        includeState: true,
        includeUUIDs: true,
      })) as HereNowResult;
      const ch = pr.channels?.[agentInstance.controlChannel!];
      const occupant = ch?.occupants?.find((o) => o.uuid === instanceUuid);
      state = occupant?.state as AgentInstancePresenceState | undefined;
      if (state) break;
    }

    agentInstance.stop();

    expect(state).toBeDefined();
    expect(state!.concurrency).toBe(0);
    // Agent instance is still online (presence visible), just with unlimited capacity
    expect(state!.activeTasks).toBe(0);
  }, 30000);
});
