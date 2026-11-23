import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PARENT_TASK_HEADER, taskClientForParent } from '../src/runtime/parent-task-client.js';
import type { RpcClientConfig } from '../src/runtime/rpc-client.js';
import { TaskClient } from '../src/runtime/task-client.js';

vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation(() => ({
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setToken: vi.fn(),
    destroy: vi.fn(),
    time: vi.fn(async () => ({ timetoken: '17000000000000000' })),
    fetchMessages: vi.fn(async ({ channels }: { channels: string[] }) => ({
      channels: { [channels[0]]: [] },
    })),
  })),
}));

const sent = (taskId: string) => ({
  ok: true,
  json: async () => ({
    jsonrpc: '2.0',
    id: 'x',
    result: {
      taskId,
      extensions: { blocks: { streamChannels: { status: `u.user-1.${taskId}` }, readToken: 'T4' } },
    },
  }),
});

describe('taskClientForParent', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;
  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  const shared = () =>
    new TaskClient({ billingMode: 'free', subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' });
  const params = { agentName: 'agent-b', requestParts: [{ type: 'text' as const, text: 'hi' }], ownerId: 'user-1' };
  const headerOf = (call: number) =>
    (fetchSpy.mock.calls[call][1].headers as Record<string, string>)[PARENT_TASK_HEADER];

  it("names the parent task on the view's calls, and not on the shared client's", async () => {
    const client = shared();
    fetchSpy.mockResolvedValueOnce(sent('child')).mockResolvedValueOnce(sent('standalone'));

    await taskClientForParent(client, 'parent-1').sendMessage(params);
    await client.sendMessage(params);

    expect(headerOf(0)).toBe('parent-1');
    expect(headerOf(1)).toBeUndefined();
  });

  it('keeps two views over one client apart when they send at once', async () => {
    const client = shared();
    fetchSpy.mockResolvedValueOnce(sent('a')).mockResolvedValueOnce(sent('b'));

    await Promise.all([
      taskClientForParent(client, 'task-of-u1').sendMessage(params),
      taskClientForParent(client, 'task-of-u2').sendMessage(params),
    ]);

    expect([headerOf(0), headerOf(1)].sort()).toEqual(['task-of-u1', 'task-of-u2']);
  });

  it('keeps a key change made through the view on the shared client', async () => {
    const client = shared();
    fetchSpy.mockResolvedValueOnce(sent('after'));

    taskClientForParent(client, 'parent-1').updateKeys('sub-c-new', 'pub-c-new');
    await client.sendMessage(params);

    expect((client as unknown as { config: RpcClientConfig }).config.subscribeKey).toBe('sub-c-new');
    expect(headerOf(0)).toBeUndefined();
  });
});
