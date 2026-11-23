import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  authRequiredFromTerminal,
  RelayedAuthRequiredError,
} from '../src/runtime/auth-required-terminal.js';
import { AuthRequiredError } from '../src/runtime/rpc-client.js';
import { TaskClient } from '../src/runtime/task-client.js';
import { TaskSession, type TerminalEvent } from '../src/runtime/task-session.js';

const DETAILS = {
  integration: 'salesforce',
  reason: 'not_approved',
  connectUrl: 'https://app.example/connect/org-1/salesforce?agent=b&via=a',
};
const REFUSED: TerminalEvent = {
  type: 'terminal',
  taskId: 'task-1',
  state: 'failed',
  error: 'Integration connection required',
  errorCode: 'AuthRequired',
  errorDetails: DETAILS,
};

let history: unknown[] = [];

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
      channels: { [channels[0]]: history },
    })),
  })),
}));

function liveSession() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  let listener: any = null;
  const pubnub = {
    addListener: vi.fn((l) => {
      listener = l;
    }),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    destroy: vi.fn(),
  };
  const session = new TaskSession({
    taskId: 'task-1',
    ownerId: 'alice',
    readToken: 't4',
    agentName: 'agent-b',
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    pubnub: pubnub as any,
    sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
  });
  return {
    session,
    deliver: (message: unknown) => listener.message({ channel: 'u.alice.task-1', message }),
  };
}

describe('a task that failed because its user must connect or approve an integration', () => {
  it('rejects waitForTerminal with a RelayedAuthRequiredError, and still hands onTerminal the event', async () => {
    const { session, deliver } = liveSession();
    const seen: TerminalEvent[] = [];
    session.onTerminal((event) => seen.push(event));
    const waiting = session.waitForTerminal();

    deliver(REFUSED);

    const err = await waiting.catch((e: unknown) => e);
    expect(err).toBeInstanceOf(RelayedAuthRequiredError);
    // Not the class a backend refusal throws, so code that trusts that one's link does not pick this one up.
    expect(err).not.toBeInstanceOf(AuthRequiredError);
    expect(err).toMatchObject({ ...DETAILS, message: 'Integration connection required' });
    expect(seen).toEqual([REFUSED]);
    session.close();
  });

  it.each([
    ['another failure', { ...REFUSED, errorCode: undefined, errorDetails: undefined }],
    ['an unknown code', { ...REFUSED, errorCode: 'Other' }],
    ['details missing the link', { ...REFUSED, errorDetails: { integration: 'x', reason: 'y' } }],
    ['a link that is not http(s)', { ...REFUSED, errorDetails: { ...DETAILS, connectUrl: 'javascript:alert(1)' } }],
    ['a link that does not parse', { ...REFUSED, errorDetails: { ...DETAILS, connectUrl: 'http://[broken' } }],
    ['a link a URL parser would repair', { ...REFUSED, errorDetails: { ...DETAILS, connectUrl: 'https:evil.example/x' } }],
    ['a link with a space in it', { ...REFUSED, errorDetails: { ...DETAILS, connectUrl: 'http://ex ample.com/x' } }],
    ['a link with no host', { ...REFUSED, errorDetails: { ...DETAILS, connectUrl: 'http://?x' } }],
    ['a link with a bad port', { ...REFUSED, errorDetails: { ...DETAILS, connectUrl: 'https://example.com:bad' } }],
    ['a link with only userinfo', { ...REFUSED, errorDetails: { ...DETAILS, connectUrl: 'http://user@/x' } }],
    ['a link with a trailing newline', { ...REFUSED, errorDetails: { ...DETAILS, connectUrl: `${DETAILS.connectUrl}\n` } }],
    ['an oversized field', { ...REFUSED, errorDetails: { ...DETAILS, integration: 'x'.repeat(129) } }],
    ['a task that completed', { ...REFUSED, state: 'completed' as const }],
  ])('resolves waitForTerminal with the event for %s', async (_case, event) => {
    const { session, deliver } = liveSession();
    const waiting = session.waitForTerminal();

    deliver(event);

    await expect(waiting).resolves.toEqual(event);
    expect(authRequiredFromTerminal(event)).toBeNull();
    session.close();
  });
});

describe('a task that already failed before the caller subscribed', () => {
  const originalFetch = globalThis.fetch;
  beforeEach(() => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          taskId: 'task-1',
          extensions: { blocks: { streamChannels: { status: 'u.user-1.task-1' }, readToken: 'T4' } },
        },
      }),
    });
  });
  afterEach(() => {
    globalThis.fetch = originalFetch;
    history = [];
  });

  const send = () =>
    new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-c-test',
      baseUrl: 'http://localhost:3001',
    }).sendMessage({
      agentName: 'agent-b',
      requestParts: [{ type: 'text', text: 'hi' }],
      ownerId: 'user-1',
    });

  it("keeps history's terminal event, so the refusal still reaches the caller", async () => {
    history = [{ timetoken: '17000000000000001', message: REFUSED }];

    const session = await send();

    await expect(session.waitForTerminal()).rejects.toMatchObject(DETAILS);
    const seen: TerminalEvent[] = [];
    session.onTerminal((event) => seen.push(event));
    expect(seen).toEqual([REFUSED]);
    session.close();
  });

  it('cuts an oversized message, so a called agent cannot size its caller\'s terminal', async () => {
    history = [{ timetoken: '17000000000000001', message: { ...REFUSED, error: 'x'.repeat(5000) } }];

    const session = await send();

    const err = await session.waitForTerminal().catch((e: Error) => e);
    expect((err as Error).message).toHaveLength(500);
    session.close();
  });

  it("hands back another failure's message instead of a bare state", async () => {
    const failed = { type: 'terminal', taskId: 'task-1', state: 'failed', error: 'boom' };
    history = [{ timetoken: '17000000000000001', message: failed }];

    const session = await send();

    await expect(session.waitForTerminal()).resolves.toEqual(failed);
    session.close();
  });
});
