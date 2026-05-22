import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AgentAuth } from '../src/runtime/agent-auth.js';
import { TaskClient } from '../src/runtime/task-client.js';
import { TaskSession } from '../src/runtime/task-session.js';

const TEST_API_KEY = 'bk_test-api-key-blocks-111';
const TEST_BASE_URL = 'http://localhost:3001';
const TEST_SUBSCRIBE_KEY = 'sub-c-test';

const TASK_ID = 'task-blocks-111';
const CALLER_ORG_ID = 'org-caller';
const READ_TOKEN = 'T4-read-token-blocks-111';
const STATUS_CHANNEL = `u.${CALLER_ORG_ID}.${TASK_ID}`;

// ---------------------------------------------------------------------------
// Fake PubNub registry — populated by the `vi.mock('pubnub')` factory below.
// Mirrors the pattern in tests/taskClient.test.ts (lines 8-63).
// ---------------------------------------------------------------------------

const sessionPubNubInstances: Array<ReturnType<typeof createFakePubNub>> = [];

vi.mock('pubnub', () => ({
  default: vi.fn().mockImplementation(() => {
    const fake = createFakePubNub();
    sessionPubNubInstances.push(fake);
    return fake.pubnub;
  }),
}));

function createFakePubNub() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  const subscribedChannels: string[] = [];
  const setTokenCalls: Array<string | null> = [];

  const pubnub = {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    addListener: (l: any) => listeners.push(l),
    removeListener: vi.fn(),
    subscribe: vi.fn(({ channels }: { channels: string[] }) => {
      subscribedChannels.push(...channels);
    }),
    unsubscribe: vi.fn(),
    setToken: vi.fn((tok: string | null) => {
      setTokenCalls.push(tok);
    }),
    destroy: vi.fn(),
    time: vi.fn(async () => ({ timetoken: '17000000000000000' })),
    fetchMessages: vi.fn(async ({ channels }: { channels: string[] }) => ({
      channels: { [channels[0]]: [] },
    })),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any;

  return { pubnub, listeners, subscribedChannels, setTokenCalls };
}

// ---------------------------------------------------------------------------
// Fetch routing — ONE spy dispatches by URL:
//   POST /api/v1/auth/agent/connect → AgentAuth.init() response
//   POST /api/v1/rpc                → SendMessage JSON-RPC response
// ---------------------------------------------------------------------------

const RPC_URL = `${TEST_BASE_URL}/api/v1/rpc`;
const CONNECT_URL = `${TEST_BASE_URL}/api/v1/auth/agent/connect`;
const REFRESH_URL = `${TEST_BASE_URL}/api/v1/auth/agent/refresh`;

function defaultSendMessageResult() {
  // Shape matches task.controller.ts:48-69 (extensions.blocks.{readToken,
  // streamChannels.status, subscribeKey, publishKey}).
  return {
    taskId: TASK_ID,
    orgId: CALLER_ORG_ID,
    idempotent: false,
    queued: false,
    state: 'pending',
    extensions: {
      blocks: {
        streamChannels: { status: STATUS_CHANNEL },
        readToken: READ_TOKEN,
        subscribeKey: TEST_SUBSCRIBE_KEY,
        publishKey: 'pub-c-test',
      },
    },
  };
}

function makeFetchRouter(opts: { sendMessageResult: Record<string, unknown> }) {
  return vi.fn(async (url: string, _init?: RequestInit) => {
    if (url === CONNECT_URL) {
      return {
        ok: true,
        headers: new Headers(),
        json: async () => ({
          agentName: 'transplant_web_caller',
          name: 'transplant_web_caller',
          accessToken: 'jwt-agent-token',
          refreshToken: 'rt-agent-token',
          expiresIn: 3600,
        }),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any;
    }
    if (url === RPC_URL) {
      return {
        ok: true,
        headers: new Headers(),
        json: async () => ({
          jsonrpc: '2.0',
          id: 'x',
          result: opts.sendMessageResult,
        }),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any;
    }
    if (url === REFRESH_URL) {
      return {
        ok: true,
        headers: new Headers(),
        json: async () => ({
          accessToken: 'jwt-agent-token-refreshed',
          refreshToken: 'rt-agent-token-refreshed',
          expiresIn: 3600,
        }),
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
      } as any;
    }
    throw new Error(`Unexpected fetch URL: ${url}`);
  });
}

const originalFetch = globalThis.fetch;
let fetchSpy: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchSpy = makeFetchRouter({ sendMessageResult: defaultSendMessageResult() });
  globalThis.fetch = fetchSpy;
  sessionPubNubInstances.length = 0;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.clearAllMocks();
});

describe('BLOCKS-111 regression: AgentAuth + TaskClient.sendMessage', () => {
  it('applies readToken to per-session PubNub, subscribes to status channel, and delivers artifact events to onArtifact', async () => {
    const agentAuth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
    await agentAuth.init({
      agentName: 'transplant_web_caller',
      instanceId: 'AG-transplant_web_caller-nextjs',
      billingMode: 'free',
      expectedInstances: 1,
      concurrency: 1,
      sdkLanguage: 'Node',
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: TEST_SUBSCRIBE_KEY,
      baseUrl: TEST_BASE_URL,
      agentAuth,
    });

    const session = await client.sendMessage({
      agentName: 'turkish_hair_transplant',
      requestParts: [{ type: 'text', text: 'Hello' }],
      ownerId: 'user-caller',
    });

    expect(session).toBeInstanceOf(TaskSession);

    expect(sessionPubNubInstances.length).toBe(1);
    const fake = sessionPubNubInstances[0];

    // SMOKING-GUN ASSERTION for the SDK side of BLOCKS-111: the readToken
    // from the RPC response was applied to the per-session PubNub via
    // setToken. Without it, PubNub PAM would reject the subscribe and
    // onArtifact / waitForTerminal would silently never fire.
    expect(fake.setTokenCalls).toEqual([READ_TOKEN]);

    // toContain rather than toEqual: time() and fetchMessages() calls may push
    // additional channels onto subscribedChannels during session setup.
    expect(fake.subscribedChannels).toContain(STATUS_CHANNEL);

    // Register the consumer callback BEFORE pushing the event so this test
    // exercises the live-dispatch path, not the history-replay path.
    const onArtifact = vi.fn();
    session.onArtifact(onArtifact);

    const artifactRef = {
      kind: 'inline',
      partId: 'transplant-preview',
      mimeType: 'image/png',
    };
    // Listener arity is internal SDK plumbing, not BLOCKS-111 behavior; assert
    // at least one listener exists and dispatch through every one with .message
    // so a future second listener (presence keepalive, debug, …) doesn't false-
    // positive this regression test.
    expect(fake.listeners.length).toBeGreaterThan(0);
    for (const l of fake.listeners) {
      if (typeof l.message === 'function') {
        l.message({
          channel: STATUS_CHANNEL,
          message: {
            type: 'artifact',
            taskId: TASK_ID,
            artifactRef,
          },
          timetoken: '17000000000001000',
        });
      }
    }

    expect(onArtifact).toHaveBeenCalledTimes(1);
    const event = onArtifact.mock.calls[0][0];
    expect(event.artifactRef).toEqual(artifactRef);
  });

  it('resolves waitForTerminal and fires onTerminal when a terminal event arrives', async () => {
    const agentAuth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
    await agentAuth.init({
      agentName: 'transplant_web_caller',
      instanceId: 'AG-transplant_web_caller-nextjs',
      billingMode: 'free',
      expectedInstances: 1,
      concurrency: 1,
      sdkLanguage: 'Node',
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: TEST_SUBSCRIBE_KEY,
      baseUrl: TEST_BASE_URL,
      agentAuth,
    });

    const session = await client.sendMessage({
      agentName: 'turkish_hair_transplant',
      requestParts: [{ type: 'text', text: 'Hello' }],
      ownerId: 'user-caller',
    });

    const fake = sessionPubNubInstances[0];
    expect(fake).toBeDefined();

    const onTerminal = vi.fn();
    session.onTerminal(onTerminal);

    // Start waitForTerminal BEFORE the event lands so this exercises the
    // live-dispatch path: the promise must resolve once the listener
    // dispatches the terminal message.
    const terminalPromise = session.waitForTerminal(2_000);

    expect(fake.listeners.length).toBeGreaterThan(0);
    for (const l of fake.listeners) {
      if (typeof l.message === 'function') {
        l.message({
          channel: STATUS_CHANNEL,
          message: {
            type: 'terminal',
            taskId: TASK_ID,
            state: 'completed',
          },
          timetoken: '17000000000002000',
        });
      }
    }

    const resolved = await terminalPromise;
    expect(resolved.state).toBe('completed');

    expect(onTerminal).toHaveBeenCalledTimes(1);
    const terminalEvent = onTerminal.mock.calls[0][0];
    expect(terminalEvent.state).toBe('completed');
  });
});
