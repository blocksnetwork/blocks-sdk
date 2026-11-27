import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it, vi } from 'vitest';
import { policyDenialFromTerminal } from '../src/runtime/policy-denial-terminal.js';
import { TaskSession, type TerminalEvent } from '../src/runtime/task-session.js';

// The Python SDK reads the same file.
const FIXTURES = fileURLToPath(new URL('../../../fixtures/policy-denial.json', import.meta.url));
const { cases } = JSON.parse(readFileSync(FIXTURES, 'utf8')) as {
  cases: { name: string; event: TerminalEvent; expected: unknown }[];
};

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
    taskId: 't_42',
    ownerId: 'alice',
    readToken: 't4',
    agentName: 'agent-b',
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    pubnub: pubnub as any,
    sdkOptions: { subscribeKey: 'sub-key', publishKey: 'pub-key' },
  });
  return {
    session,
    deliver: (message: unknown) => listener.message({ channel: 'u.alice.t_42', message }),
  };
}

describe('policyDenialFromTerminal', () => {
  it.each(cases)('$name', ({ event, expected }) => {
    expect(policyDenialFromTerminal(event)).toEqual(expected);
  });
});

describe('a task the policy gate stopped', () => {
  it.each(cases.filter((c) => c.expected))(
    'resolves waitForTerminal with the terminal, not an error: $name',
    async ({ event }) => {
      const { session, deliver } = liveSession();
      const waiting = session.waitForTerminal();

      deliver(event);

      await expect(waiting).resolves.toEqual(event);
      session.close();
    },
  );
});
