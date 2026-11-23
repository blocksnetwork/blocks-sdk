import { describe, expect, it, vi } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { PARENT_TASK_HEADER } from '../src/runtime/parent-task-client.js';
import type { RpcClientConfig } from '../src/runtime/rpc-client.js';
import { makeTestCard } from './helpers/test-card.js';

function createMockPubNub() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  return {
    addListener: vi.fn((l) => listeners.push(l)),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    destroy: vi.fn(),
    setToken: vi.fn(),
    setFilterExpression: vi.fn(),
    setState: vi.fn(async () => {}),
    publish: vi.fn(async () => ({ timetoken: '123' })),
    hereNow: vi.fn(async () => ({ channels: {} })),
    _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
    simulate(channel: string, message: unknown, userMetadata?: Record<string, unknown>) {
      for (const l of listeners) l?.message?.({ channel, message, userMetadata });
    },
  };
}

vi.mock('../src/runtime/agent-registry.js', () => ({
  connectAgent: vi.fn(async () => ({ pamToken: undefined })),
  fetchAgentRegistry: vi.fn(async () => ({})),
  getAgent: vi.fn(async () => null),
  removeAgent: vi.fn(async () => {}),
  fetchAgentsByTag: vi.fn(async () => []),
  fetchAgentsByListing: vi.fn(async () => []),
}));

vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => createMockPubNub()),
}));

const headerOf = (client: unknown) =>
  (client as { config: RpcClientConfig }).config.rpcHeaders?.[PARENT_TASK_HEADER];

describe('the task client a handler receives', () => {
  it('names the task it runs in, while the instance handle client names none', async () => {
    const pn = createMockPubNub();
    const seen: Record<string, unknown> = {};
    const handle = await startAgentInstance({
      agentName: 'uno',
      card: makeTestCard(),
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pubnub: pn as any,
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      handler: async (task, ctx) => {
        seen[task.taskId] = headerOf(ctx?.taskClient);
        return {};
      },
    });

    for (const [i, taskId] of ['t-1', 't-2'].entries()) {
      pn.simulate(
        'agent.uno.control',
        { type: 'StartTask', taskId, ownerId: 'alice' },
        { instance: handle.instanceId },
      );
      await vi.waitFor(() => expect(Object.keys(seen)).toHaveLength(i + 1));
    }

    expect(seen).toEqual({ 't-1': 't-1', 't-2': 't-2' });
    expect(headerOf(handle.taskClient)).toBeUndefined();
    handle.stop();
  });
});
