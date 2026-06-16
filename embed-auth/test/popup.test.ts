import {
  describe,
  it,
  expect,
  beforeEach,
  afterEach,
  vi,
  type MockInstance,
} from 'vitest';

import {
  openPopupAndAwaitEnvelope,
  __testing,
  type OpenPopupArgs,
} from '../src/popup.js';
import { BlocksAuthError } from '../src/types.js';
import {
  POPUP_TIMEOUT_MS,
  POPUP_CLOSE_POLL_MS,
  EMBED_POPUP_NAME,
} from '../src/constants.js';

const BACKEND = 'https://blocks.ai';
const BACKEND_ORIGIN = 'https://blocks.ai';
const PAGE_ORIGIN = 'https://partner.example';

const AGENT_ID = '11111111-1111-1111-1111-111111111111';
const AGENT_ID_2 = '22222222-2222-2222-2222-222222222222';
const ORG_ID = '33333333-3333-3333-3333-333333333333';
const USER_ID = '44444444-4444-4444-4444-444444444444';

function makeSuccessEnvelope(state: string, names: string[] = ['translator']) {
  const ids = names.map((_, i) =>
    i === 0 ? AGENT_ID : AGENT_ID_2.slice(0, -1) + String(i),
  );
  return {
    type: 'blocks-auth-success' as const,
    version: 1 as const,
    state,
    jwt: 'h.p.s',
    refreshToken: 'r'.repeat(32),
    expiresAt: Date.now() + 60_000,
    agentIds: ids,
    agents: names.map((name, i) => ({
      name,
      id: ids[i]!,
      billingMode: 'free' as const,
    })),
    orgId: ORG_ID,
    userId: USER_ID,
  };
}

function makeErrorEnvelope(state: string, overrides: Record<string, unknown> = {}) {
  return {
    type: 'blocks-auth-error',
    version: 1,
    state,
    code: 'USER_CANCELLED',
    message: 'User cancelled',
    ...overrides,
  };
}

let openSpy: MockInstance<Parameters<Window['open']>, ReturnType<Window['open']>>;
let addEventSpy: MockInstance<Parameters<Window['addEventListener']>, ReturnType<Window['addEventListener']>>;
let removeEventSpy: MockInstance<Parameters<Window['removeEventListener']>, ReturnType<Window['removeEventListener']>>;
let popupHandle: { closed: boolean; close: () => void } | null;

function dispatchMessage(
  data: unknown,
  origin: string = BACKEND_ORIGIN,
  source: MessageEventSource | null = popupHandle as unknown as MessageEventSource,
): void {
  const event = new MessageEvent('message', { data, origin, source });
  window.dispatchEvent(event);
}

beforeEach(() => {
  vi.useFakeTimers();
  __testing.inFlightPopups.clear();
  popupHandle = { closed: false, close: () => {} };
  openSpy = vi
    .spyOn(window, 'open')
    .mockImplementation(() => popupHandle as unknown as Window);
  addEventSpy = vi.spyOn(window, 'addEventListener');
  removeEventSpy = vi.spyOn(window, 'removeEventListener');
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  __testing.inFlightPopups.clear();
});

const baseArgs = (over: Partial<OpenPopupArgs> = {}): OpenPopupArgs => ({
  agents: ['translator'],
  backendBaseUrl: BACKEND,
  pageOrigin: PAGE_ORIGIN,
  ...over,
});

describe('state nonce', () => {
  it('generates ≥22 chars (≥128 bits base64-url)', () => {
    for (let i = 0; i < 20; i++) {
      const nonce = __testing.generateStateNonce();
      expect(nonce.length).toBeGreaterThanOrEqual(22);
      expect(nonce).toMatch(/^[A-Za-z0-9_-]+$/);
    }
  });

  it('produces unique nonces across 100 generations', () => {
    const set = new Set<string>();
    for (let i = 0; i < 100; i++) set.add(__testing.generateStateNonce());
    expect(set.size).toBe(100);
  });
});

describe('popup URL construction', () => {
  it('encodes single agent + replyOrigin + state', () => {
    const url = __testing.buildPopupUrl(baseArgs(), 'STATE_TOKEN');
    expect(url).toBe(
      `${BACKEND}/api/v1/auth/embed/popup?agents=translator&replyOrigin=${encodeURIComponent(PAGE_ORIGIN)}&state=STATE_TOKEN`,
    );
  });

  it('joins multi-agent CSV (bare names, no slashes)', () => {
    const url = __testing.buildPopupUrl(
      baseArgs({ agents: ['a', 'b', 'c'] }),
      's',
    );
    const params = new URL(url).searchParams;
    expect(params.get('agents')).toBe('a,b,c');
  });

  it('uses `replyOrigin` for the page origin (not the legacy `origin` param)', () => {
    const url = __testing.buildPopupUrl(baseArgs(), 's');
    const params = new URL(url).searchParams;
    expect(params.get('replyOrigin')).toBe(PAGE_ORIGIN);
    // Backward-compat shim is intentionally absent — no `origin` param.
    expect(params.has('origin')).toBe(false);
  });

  it('does not emit any devGrant query parameter', () => {
    const url = __testing.buildPopupUrl(baseArgs(), 's');
    expect(url.includes('devGrant=')).toBe(false);
  });

  it('strips a trailing slash on backendBaseUrl', () => {
    const url = __testing.buildPopupUrl(
      baseArgs({ backendBaseUrl: 'https://blocks.ai/' }),
      's',
    );
    expect(url.startsWith('https://blocks.ai/api/v1/auth/embed/popup?')).toBe(true);
  });
});

describe('openPopupAndAwaitEnvelope — happy path', () => {
  it('resolves with the validated envelope on a matching success message', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    expect(openSpy).toHaveBeenCalledTimes(1);
    expect(openSpy.mock.calls[0]![1]).toBe(EMBED_POPUP_NAME);

    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    expect(record).toBeDefined();

    dispatchMessage(makeSuccessEnvelope(record.state));
    const result = await promise;
    expect(result.envelope.userId).toBe(USER_ID);
    expect(result.state).toBe(record.state);
    expect(__testing.inFlightPopups.has(BACKEND_ORIGIN)).toBe(false);
  });
});

describe('origin / schema / state defenses', () => {
  it('drops messages from a foreign origin silently', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    let settled = false;
    promise.then(() => { settled = true; }, () => { settled = true; });

    dispatchMessage(makeSuccessEnvelope(record.state), 'https://attacker.example');
    await Promise.resolve();
    expect(settled).toBe(false);

    // Cleanup so the test doesn't leak.
    record.reject(new BlocksAuthError('USER_CANCELLED'));
    await promise.catch(() => {});
  });

  it('drops schema-invalid messages silently', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    let settled = false;
    promise.then(() => { settled = true; }, () => { settled = true; });

    dispatchMessage({ random: 'noise' });
    await Promise.resolve();
    expect(settled).toBe(false);

    record.reject(new BlocksAuthError('USER_CANCELLED'));
    await promise.catch(() => {});
  });

  it('drops messages whose `state` does not match', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    let settled = false;
    promise.then(() => { settled = true; }, () => { settled = true; });

    dispatchMessage(makeSuccessEnvelope('s'.repeat(22))); // wrong state
    await Promise.resolve();
    expect(settled).toBe(false);

    record.reject(new BlocksAuthError('USER_CANCELLED'));
    await promise.catch(() => {});
  });

  it('drops a valid envelope whose `source` is NOT the popup we opened', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    let settled = false;
    promise.then(() => { settled = true; }, () => { settled = true; });

    // Correct origin, correct state, but a different sender window (e.g. a
    // second tab or iframe on the backend origin forging the envelope).
    const forgedSource = { closed: false, close: () => {} } as unknown as MessageEventSource;
    dispatchMessage(makeSuccessEnvelope(record.state), BACKEND_ORIGIN, forgedSource);
    await Promise.resolve();
    expect(settled).toBe(false);

    record.reject(new BlocksAuthError('USER_CANCELLED'));
    await promise.catch(() => {});
  });
});

describe('error-envelope handling', () => {
  it('rejects with a typed BlocksAuthError carrying code + agent + message', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;

    dispatchMessage(
      makeErrorEnvelope(record.state, {
        code: 'AGENT_DISABLED',
        agent: 'translator',
        message: 'Agent is disabled',
      }),
    );
    await expect(promise).rejects.toMatchObject({
      name: 'BlocksAuthError',
      code: 'AGENT_DISABLED',
      agent: 'translator',
      message: 'Agent is disabled',
    });
    expect(__testing.inFlightPopups.has(BACKEND_ORIGIN)).toBe(false);
  });
});

describe('agent-set match', () => {
  async function expectMismatch(envelopeFactory: (state: string) => unknown) {
    const promise = openPopupAndAwaitEnvelope(
      baseArgs({ agents: ['translator', 'summarizer'] }),
    );
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    dispatchMessage(envelopeFactory(record.state));
    await expect(promise).rejects.toMatchObject({
      name: 'BlocksAuthError',
      code: 'AGENT_SET_MISMATCH',
    });
  }

  it('rejects when envelope has an extra agent', () =>
    expectMismatch((state) =>
      makeSuccessEnvelope(state, ['translator', 'summarizer', 'extra']),
    ));

  it('accepts a narrowed scope (returned set ⊂ requested set) — the backend filters to reachable agents', async () => {
    const promise = openPopupAndAwaitEnvelope(
      baseArgs({ agents: ['translator', 'summarizer'] }),
    );
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    dispatchMessage(makeSuccessEnvelope(record.state, ['translator']));
    const result = await promise;
    expect(result.envelope.agents.map((a) => a.name)).toEqual(['translator']);
  });

  it('rejects on duplicate agent names in the envelope', () =>
    expectMismatch((state) => {
      const env = makeSuccessEnvelope(state, ['translator', 'summarizer']);
      env.agents[1] = { ...env.agents[0]!, id: env.agents[1]!.id };
      return env;
    }));

  it('rejects on agentIds[*] not matching agents[*].id (1:1 mapping violation)', () =>
    expectMismatch((state) => {
      const env = makeSuccessEnvelope(state, ['translator', 'summarizer']);
      // Replace one agentIds entry with a UUID that does NOT appear in
      // agents[*].id. Schema still passes (both arrays unique, 1..25);
      // the popup-level 1:1 cross-check is what catches it.
      env.agentIds = [env.agentIds[0]!, '99999999-9999-9999-9999-999999999999'];
      return env;
    }));

  it('rejects when the envelope name differs only in case (case-sensitive contract)', async () => {
    // Backend treats `Foo` and `foo` as distinct agents (validator
    // `^[a-zA-Z0-9_]+$`, no fold; DB unique index on plain `text`). The
    // widget must NOT silently accept `foo` when the page requested
    // `Foo` — that would hand back a different agent than asked for.
    const promise = openPopupAndAwaitEnvelope(
      baseArgs({ agents: ['Translator'] }),
    );
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    dispatchMessage(makeSuccessEnvelope(record.state, ['translator']));
    await expect(promise).rejects.toMatchObject({
      name: 'BlocksAuthError',
      code: 'AGENT_SET_MISMATCH',
    });
  });

  it('accepts an exact case-match (case-sensitive contract)', async () => {
    const promise = openPopupAndAwaitEnvelope(
      baseArgs({ agents: ['Translator'] }),
    );
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    dispatchMessage(makeSuccessEnvelope(record.state, ['Translator']));
    const result = await promise;
    expect(result.envelope.agents[0]!.name).toBe('Translator');
  });
});

describe('timeout fallback', () => {
  it('rejects with USER_CANCELLED after POPUP_TIMEOUT_MS, removing the listener', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    const beforeRemoves = removeEventSpy.mock.calls.length;

    vi.advanceTimersByTime(POPUP_TIMEOUT_MS);

    await expect(promise).rejects.toMatchObject({
      name: 'BlocksAuthError',
      code: 'USER_CANCELLED',
    });
    const afterRemoves = removeEventSpy.mock.calls.length;
    expect(afterRemoves - beforeRemoves).toBeGreaterThanOrEqual(1);
    expect(__testing.inFlightPopups.has(BACKEND_ORIGIN)).toBe(false);
  });
});

describe('manual close detection', () => {
  it('rejects with USER_CANCELLED soon after the user closes the popup, well before the 5-min timeout', async () => {
    // MAJOR (review #2): the only non-message settle path was the 5-min
    // POPUP_TIMEOUT_MS timer. A user who manually closes the popup sends
    // no message and triggers no timer, so signInAndGetClient(s) hung for
    // the full timeout. The widget must poll `popupWindow.closed` and
    // reject promptly with USER_CANCELLED.
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    let settled = false;
    promise.then(() => { settled = true; }, () => { settled = true; });

    // User closes the popup window.
    popupHandle!.closed = true;

    // Advance far less than the full timeout — a close-poll must fire.
    vi.advanceTimersByTime(2_000);
    expect(2_000).toBeLessThan(POPUP_TIMEOUT_MS);

    await expect(promise).rejects.toMatchObject({
      name: 'BlocksAuthError',
      code: 'USER_CANCELLED',
    });
    expect(settled).toBe(true);
    // Listener + timers cleaned up.
    expect(__testing.inFlightPopups.has(BACKEND_ORIGIN)).toBe(false);
  });

  it('resolves on success even when the popup is observed closed first', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;

    // Popup reports closed BEFORE its queued success message dispatches.
    popupHandle!.closed = true;
    // The close-poll fires (≥500ms) and schedules the deferred reject.
    vi.advanceTimersByTime(POPUP_CLOSE_POLL_MS);
    // ...but the success message arrives within the grace window.
    dispatchMessage(makeSuccessEnvelope(record.state));

    await expect(promise).resolves.toMatchObject({ state: record.state });
    expect(__testing.inFlightPopups.has(BACKEND_ORIGIN)).toBe(false);
  });

  it('does NOT reject while the popup remains open and no message arrives', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    let settled = false;
    promise.then(() => { settled = true; }, () => { settled = true; });

    // Popup stays open; advance past several poll intervals but below timeout.
    popupHandle!.closed = false;
    vi.advanceTimersByTime(5_000);
    await Promise.resolve();
    expect(settled).toBe(false);

    // Cleanup.
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    record.reject(new BlocksAuthError('USER_CANCELLED'));
    await promise.catch(() => {});
  });
});

describe('single-popup-in-flight policy (C345-3-3)', () => {
  it('replaces the older popup, rejects the older promise with POPUP_REPLACED, and resolves the new one', async () => {
    const first = openPopupAndAwaitEnvelope(baseArgs());
    const beforeRemoves = removeEventSpy.mock.calls.length;

    const second = openPopupAndAwaitEnvelope(baseArgs());

    await expect(first).rejects.toMatchObject({
      name: 'BlocksAuthError',
      code: 'POPUP_REPLACED',
    });

    const afterReplace = removeEventSpy.mock.calls.length;
    expect(afterReplace - beforeRemoves).toBeGreaterThanOrEqual(1);

    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    expect(record).toBeDefined();
    dispatchMessage(makeSuccessEnvelope(record.state));
    const result = await second;
    expect(result.envelope.userId).toBe(USER_ID);
    expect(__testing.inFlightPopups.has(BACKEND_ORIGIN)).toBe(false);
  });

  it('does NOT replace popups from a different backend origin', async () => {
    const first = openPopupAndAwaitEnvelope(baseArgs());
    const _second = openPopupAndAwaitEnvelope(
      baseArgs({ backendBaseUrl: 'https://other.example' }),
    );
    expect(__testing.inFlightPopups.size).toBe(2);

    const recordA = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    dispatchMessage(makeSuccessEnvelope(recordA.state));
    await expect(first).resolves.toBeDefined();

    const recordB = __testing.inFlightPopups.get('https://other.example')!;
    dispatchMessage(makeSuccessEnvelope(recordB.state), 'https://other.example');
    await expect(_second).resolves.toBeDefined();
  });
});

describe('window.open blocked', () => {
  it('throws POPUP_BLOCKED when window.open returns null', async () => {
    openSpy.mockImplementation(() => null);
    await expect(openPopupAndAwaitEnvelope(baseArgs())).rejects.toMatchObject({
      name: 'BlocksAuthError',
      code: 'POPUP_BLOCKED',
    });
    // No record was registered, no listener was added for the failed call.
    expect(__testing.inFlightPopups.has(BACKEND_ORIGIN)).toBe(false);
  });
});

describe('listener cleanup invariant', () => {
  it('removes the listener on success', async () => {
    const beforeAdds = addEventSpy.mock.calls.length;
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    const addedDuring = addEventSpy.mock.calls.length - beforeAdds;
    expect(addedDuring).toBeGreaterThanOrEqual(1);

    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    const removesBefore = removeEventSpy.mock.calls.length;
    dispatchMessage(makeSuccessEnvelope(record.state));
    await promise;
    expect(removeEventSpy.mock.calls.length - removesBefore).toBeGreaterThanOrEqual(1);
  });

  it('removes the listener on error', async () => {
    const promise = openPopupAndAwaitEnvelope(baseArgs());
    const record = __testing.inFlightPopups.get(BACKEND_ORIGIN)!;
    const removesBefore = removeEventSpy.mock.calls.length;
    dispatchMessage(makeErrorEnvelope(record.state));
    await promise.catch(() => {});
    expect(removeEventSpy.mock.calls.length - removesBefore).toBeGreaterThanOrEqual(1);
  });
});
