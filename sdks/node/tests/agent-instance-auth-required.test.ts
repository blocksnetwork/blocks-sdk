import { describe, expect, it, vi } from 'vitest';
import { startAgentInstance } from '../src/runtime/agent-instance.js';
import { RelayedAuthRequiredError } from '../src/runtime/auth-required-terminal.js';
import { ConnectionRequiredError } from '../src/runtime/connections.js';
import { AuthRequiredError } from '../src/runtime/rpc-client.js';
import { makeTestCard } from './helpers/test-card.js';

const published: Array<Record<string, unknown>> = [];

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
    publish: vi.fn(async (args: { message: Record<string, unknown> }) => {
      published.push(args.message);
      return { timetoken: '123' };
    }),
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

const DETAILS = {
  integration: 'salesforce',
  reason: 'not_approved',
  connectUrl: 'https://app.example/connect/org-1/salesforce?agent=b&via=a',
};

async function terminalAfterHandlerThrows(thrown: Error) {
  published.length = 0;
  const pn = createMockPubNub();
  const handle = await startAgentInstance({
    agentName: 'uno',
    card: makeTestCard(),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    pubnub: pn as any,
    subscribeKey: 'sub-key',
    publishKey: 'pub-key',
    handler: async () => {
      throw thrown;
    },
  });
  pn.simulate(
    'agent.uno.control',
    { type: 'StartTask', taskId: 't-1', ownerId: 'alice' },
    { instance: handle.instanceId },
  );
  const terminal = await vi.waitFor(() => {
    const found = published.find((m) => m.type === 'terminal');
    expect(found).toBeDefined();
    return found as Record<string, unknown>;
  });
  handle.stop();
  return terminal;
}

describe('a handler that lets an error escape', () => {
  it.each([
    [
      "its own connection being refused",
      new ConnectionRequiredError(DETAILS.integration, DETAILS.reason, DETAILS.connectUrl),
      'Integration connection required: salesforce (not_approved)',
    ],
    [
      'an agent it called being refused',
      new AuthRequiredError('Approval required', DETAILS.integration, DETAILS.reason, DETAILS.connectUrl),
      'Approval required',
    ],
    [
      'a refusal relayed from further down the chain',
      new RelayedAuthRequiredError('Relayed', DETAILS.integration, DETAILS.reason, DETAILS.connectUrl),
      'Relayed',
    ],
    [
      'the same error from another copy of the SDK, matched by name',
      Object.assign(new Error('Other copy'), { name: 'ConnectionRequiredError', ...DETAILS }),
      'Other copy',
    ],
  ])('fails the task with the AuthRequired code and details for %s', async (_case, thrown, error) => {
    expect(await terminalAfterHandlerThrows(thrown)).toMatchObject({
      state: 'failed',
      error,
      errorCode: 'AuthRequired',
      errorDetails: DETAILS,
    });
  });

  it.each([
    ['any other error', new Error('boom')],
    [
      'an approval error whose link is not http(s)',
      new ConnectionRequiredError(DETAILS.integration, DETAILS.reason, 'javascript:alert(1)'),
    ],
    [
      'an error with the fields but another name',
      Object.assign(new Error('lookalike'), DETAILS),
    ],
  ])('fails the task with only its message for %s', async (_case, thrown) => {
    const terminal = await terminalAfterHandlerThrows(thrown);

    expect(terminal).toMatchObject({ state: 'failed', error: thrown.message });
    expect(terminal).not.toHaveProperty('errorCode');
    expect(terminal).not.toHaveProperty('errorDetails');
  });
});
