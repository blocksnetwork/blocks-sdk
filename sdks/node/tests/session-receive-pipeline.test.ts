// Covers what the shared session fixtures cannot reach: their `recover()` never finds history.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  type ConsumerReceiverEvents,
  ConsumerSessionReceiver,
} from '../src/runtime/sessions/consumer-receiver.js';
import { InOrderDelivery, SESSION_GAP_RECOVER_MS } from '../src/runtime/sessions/in-order-delivery.js';
import {
  type ProviderReceiverEvents,
  ProviderSessionReceiver,
} from '../src/runtime/sessions/provider-receiver.js';

const PROVIDER = 'uuid-provider';
const CONSUMER = 'uuid-consumer';
const BACKEND = 'uuid-backend';

function progress(seq: number, message = `m${seq}`) {
  return {
    type: 'exchange_progress',
    protocolVersion: '2026-05-01',
    sessionId: 's-1',
    seq,
    ts: 0,
    exchangeId: 'ex-1',
    message,
  };
}

function consumer(recover: (receiver: ConsumerSessionReceiver) => Promise<void> = async () => {}) {
  const delivered: string[] = [];
  const recoverSpy = vi.fn();
  const receiver: ConsumerSessionReceiver = new ConsumerSessionReceiver({
    parties: { sessionId: 's-1', counterpartUuid: PROVIDER, backendUuid: BACKEND },
    state: 'ready',
    recover: () => {
      recoverSpy();
      return recover(receiver);
    },
    events: { onProgress: (event) => delivered.push(event.message ?? '') },
  });
  receiver.trackExchange('ex-1');
  return { receiver, delivered, recoverSpy };
}

describe('consumer in-order delivery', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('delivers a gap recovered from history in seq order without skipping', async () => {
    const { receiver, delivered } = consumer(async (r) => {
      await Promise.resolve();
      r.receive(PROVIDER, progress(2));
    });
    receiver.receive(PROVIDER, progress(1));
    receiver.receive(PROVIDER, progress(3));

    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS + 1);

    expect(delivered).toEqual(['m1', 'm2', 'm3']);
    expect(receiver.receive(PROVIDER, progress(2))).toBe(false);
  });

  it('skips the gap and delivers held messages when recovery fails', async () => {
    const { receiver, delivered } = consumer(async () => {
      throw new Error('history unavailable');
    });
    receiver.receive(PROVIDER, progress(1));
    receiver.receive(PROVIDER, progress(4));
    receiver.receive(PROVIDER, progress(3));

    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS + 1);

    expect(delivered).toEqual(['m1', 'm3', 'm4']);
  });

  it('gives a message that arrives during recovery its own gap window', async () => {
    let finishRecovery = () => {};
    const { receiver, delivered, recoverSpy } = consumer(
      () => new Promise<void>((resolve) => (finishRecovery = resolve)),
    );
    receiver.receive(PROVIDER, progress(1));
    receiver.receive(PROVIDER, progress(3));
    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS + 1);

    receiver.receive(PROVIDER, progress(5));
    finishRecovery();
    await vi.advanceTimersByTimeAsync(0);
    expect(delivered).toEqual(['m1', 'm3']);

    receiver.receive(PROVIDER, progress(4));
    expect(delivered).toEqual(['m1', 'm3', 'm4', 'm5']);
    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS + 1);
    expect(recoverSpy).toHaveBeenCalledTimes(1);
  });

  it('recovers a gap opened during recovery 750 ms after it opened', async () => {
    let finishRecovery = () => {};
    const { receiver, recoverSpy } = consumer(
      () => new Promise<void>((resolve) => (finishRecovery = resolve)),
    );
    receiver.receive(PROVIDER, progress(2));
    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS + 1);

    receiver.receive(PROVIDER, progress(4));
    await vi.advanceTimersByTimeAsync(400);
    finishRecovery();
    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS - 400);

    expect(recoverSpy).toHaveBeenCalledTimes(2);
  });

  it('runs a fresh recovery for a gap whose timer fires during a slow recovery', async () => {
    let finishFirst = () => {};
    let calls = 0;
    const { receiver, delivered, recoverSpy } = consumer(async (r) => {
      calls += 1;
      if (calls === 1) return new Promise<void>((resolve) => (finishFirst = resolve));
      r.receive(PROVIDER, progress(3));
    });
    receiver.receive(PROVIDER, progress(2));
    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS + 1);
    receiver.receive(PROVIDER, progress(4));
    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS + 1);
    expect(recoverSpy).toHaveBeenCalledTimes(1);

    finishFirst();
    await vi.advanceTimersByTimeAsync(0);

    expect(recoverSpy).toHaveBeenCalledTimes(2);
    expect(delivered).toEqual(['m2', 'm3', 'm4']);
  });

  it('recovers a partly filled gap 750 ms after the gap opened', async () => {
    const { receiver, delivered, recoverSpy } = consumer();
    receiver.receive(PROVIDER, progress(3));
    await vi.advanceTimersByTimeAsync(700);
    receiver.receive(PROVIDER, progress(1));

    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS - 700);

    expect(recoverSpy).toHaveBeenCalledTimes(1);
    expect(delivered).toEqual(['m1', 'm3']);
  });

  it('drops counterpart traffic when constructed in a terminal state', () => {
    const receiver = new ConsumerSessionReceiver({
      parties: { sessionId: 's-1', counterpartUuid: PROVIDER, backendUuid: BACKEND },
      state: 'closed',
      recover: async () => {},
    });

    expect(receiver.receive(PROVIDER, progress(2))).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('skips a gap up to the largest safe seq without walking it', async () => {
    const { receiver, delivered } = consumer();
    receiver.receive(PROVIDER, progress(Number.MAX_SAFE_INTEGER, 'last'));

    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS + 1);

    expect(delivered).toEqual(['last']);
  });

  it('does not recover a gap once the session is terminal', async () => {
    const { receiver, recoverSpy } = consumer();
    receiver.receive(PROVIDER, progress(2));
    receiver.receive(BACKEND, {
      type: 'session_failed',
      protocolVersion: '2026-05-01',
      sessionId: 's-1',
      seq: 0,
      ts: 0,
      error: 'route_lost',
    });

    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS + 1);

    expect(recoverSpy).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe('recovery cutoffs', () => {
  it('skips each sender only through its own cutoff, leaving a seq held above it', () => {
    const delivered: string[] = [];
    const delivery = new InOrderDelivery<string>({
      deliver: (message) => delivered.push(message),
      recover: async () => {},
    });
    delivery.accept('a', 3, 'a3');
    delivery.accept('a', 6, 'a6');
    delivery.accept('b', 4, 'b4');

    delivery.skipThrough(new Map([['a', 3], ['b', 4]]));

    expect(delivered).toEqual(['a3', 'b4']);
    expect(delivery.heldCutoffs()).toEqual(new Map([['a', 6]]));
    delivery.dispose();
  });

  it('gives a gap left above the cutoff its own reorder window', async () => {
    vi.useFakeTimers();
    const recover = vi.fn(async () => {});
    const delivery = new InOrderDelivery<string>({ deliver: () => {}, recover });
    delivery.accept('a', 3, 'a3');
    await vi.advanceTimersByTimeAsync(600);
    delivery.accept('a', 6, 'a6');

    delivery.skipThrough(new Map([['a', 3]]));
    await vi.advanceTimersByTimeAsync(SESSION_GAP_RECOVER_MS - 1);
    expect(recover).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(recover).toHaveBeenCalledTimes(1);

    delivery.dispose();
    vi.useRealTimers();
  });

  it('closes a gap history revealed, but not one a live message opens after the replay', () => {
    const { receiver, delivered } = consumer();
    receiver.beginRecovery();
    receiver.receive(PROVIDER, progress(3), true);
    receiver.historyReplayed();
    receiver.receive(PROVIDER, progress(5));

    receiver.finishRecovery();

    expect(delivered).toEqual(['m3']);
    receiver.receive(PROVIDER, progress(4));
    expect(delivered).toEqual(['m3', 'm4', 'm5']);
    receiver.terminate('closed');
  });

  it('leaves a gap a live message opens during the replay to its own window', () => {
    const { receiver, delivered } = consumer();
    receiver.beginRecovery();
    receiver.receive(PROVIDER, progress(2));
    receiver.historyReplayed();

    receiver.finishRecovery();

    expect(delivered).toEqual([]);
    receiver.receive(PROVIDER, progress(1));
    expect(delivered).toEqual(['m1', 'm2']);
    receiver.terminate('closed');
  });

  it('closes a gap below a live message that history also returned', () => {
    const { receiver, delivered } = consumer();
    receiver.beginRecovery();
    receiver.receive(PROVIDER, progress(2));
    receiver.receive(PROVIDER, progress(2), true);
    receiver.historyReplayed();

    receiver.finishRecovery();

    expect(delivered).toEqual(['m2']);
    receiver.terminate('closed');
  });

  function settling(events: ConsumerReceiverEvents = {}) {
    const outcomes: { state: string; artifacts: unknown[] }[] = [];
    const receiver = new ConsumerSessionReceiver({
      parties: { sessionId: 's-1', counterpartUuid: PROVIDER, backendUuid: BACKEND },
      state: 'ready',
      recover: async () => {},
      events: { ...events, onOutcome: (outcome) => outcomes.push(outcome) },
    });
    receiver.trackExchange('ex-1');
    return { receiver, outcomes };
  }
  const inline = { kind: 'inline', mimeType: 'text/plain', data: 'aGk=' };
  const artifact = (seq: number) => ({ ...progress(seq), type: 'exchange_artifact', artifactRef: inline });
  const record = (state: string) => ({
    sessionId: 's-1',
    state,
    reason: null,
    error: null,
    exchanges: [{ exchangeId: 'ex-1', state: 'completed', error: null, artifactRefs: [] }],
  });

  it('settles from the record only once an artifact held behind a live gap is released', () => {
    const { receiver, outcomes } = settling();
    receiver.beginRecovery();
    receiver.receive(PROVIDER, artifact(2));
    receiver.historyReplayed();
    receiver.finishRecovery(record('ready'));
    expect(outcomes).toEqual([]);

    receiver.receive(PROVIDER, progress(1));

    expect(outcomes).toEqual([expect.objectContaining({ state: 'completed', artifacts: [inline] })]);
    receiver.terminate('closed');
  });

  it('settles from the record once an ignored outcome held behind a live gap is released', () => {
    const { receiver, outcomes } = settling();
    receiver.beginRecovery();
    receiver.receive(PROVIDER, { ...progress(2), type: 'exchange_outcome', state: 'done' });
    receiver.historyReplayed();
    receiver.finishRecovery(record('ready'));

    receiver.receive(PROVIDER, progress(1));

    expect(outcomes).toEqual([expect.objectContaining({ state: 'completed' })]);
    receiver.terminate('closed');
  });

  it('settles from the record once any held message of its exchange is released', () => {
    const { receiver, outcomes } = settling();
    receiver.beginRecovery();
    receiver.receive(PROVIDER, { ...progress(2), type: 'stream_started' });
    receiver.historyReplayed();
    receiver.finishRecovery(record('ready'));

    receiver.receive(PROVIDER, progress(1));

    expect(outcomes).toEqual([expect.objectContaining({ state: 'completed' })]);
    receiver.terminate('closed');
  });

  it('skips a live gap when the record ends the session, keeping the artifact held behind it', () => {
    const { receiver, outcomes } = settling();
    receiver.beginRecovery();
    receiver.receive(PROVIDER, artifact(2));
    receiver.historyReplayed();

    receiver.finishRecovery(record('closed'));

    expect(outcomes).toEqual([expect.objectContaining({ state: 'completed', artifacts: [inline] })]);
    expect(receiver.state).toBe('closed');
  });

  it('settles a waiting exchange from the record, not canceled, when the session ends locally', () => {
    const { receiver, outcomes } = settling();
    receiver.beginRecovery();
    receiver.receive(PROVIDER, artifact(2));
    receiver.historyReplayed();
    receiver.finishRecovery(record('ready'));

    receiver.terminate('closed');

    expect(outcomes).toEqual([expect.objectContaining({ state: 'completed', artifacts: [inline] })]);
  });

  it('refuses to open a second recovery while one is open', () => {
    const { receiver } = consumer();
    receiver.beginRecovery();
    expect(() => receiver.beginRecovery()).toThrow(/already open/);
    receiver.terminate('closed');
  });

  it('releases a live gap before a deferred terminal ends the session', () => {
    const { receiver, delivered } = consumer();
    receiver.beginRecovery();
    receiver.receive(PROVIDER, progress(2));
    receiver.receive(BACKEND, { ...failedNotice({}), type: 'session_closed' });
    receiver.historyReplayed();

    receiver.finishRecovery();

    expect(delivered).toEqual(['m2']);
    expect(receiver.state).toBe('closed');
  });

  it.each([
    ['a live session_closed', 'canceled', (r: ConsumerSessionReceiver) =>
      r.receive(BACKEND, { ...failedNotice({}), type: 'session_closed' })],
    ['a local failure', 'failed', (r: ConsumerSessionReceiver) => r.terminate('failed', 'boom')],
  ])('keeps an artifact held behind a gap on %s, before the state turns terminal', (_name, state, end) => {
    const statesSeen: string[] = [];
    const { receiver, outcomes } = settling({ onArtifact: () => statesSeen.push(receiver.state) });
    receiver.receive(PROVIDER, artifact(2));

    end(receiver);

    expect(outcomes).toEqual([expect.objectContaining({ state, artifacts: [inline] })]);
    expect(statesSeen).toEqual(['ready']);
  });

  it('keeps the first end when an artifact callback ends the session again', () => {
    const states: string[] = [];
    const { receiver, outcomes } = settling({
      onArtifact: () => receiver.terminate('failed', 'from_callback'),
      onStateChange: (state) => states.push(state),
    });
    receiver.receive(PROVIDER, artifact(2));

    receiver.terminate('closed');

    expect(outcomes).toEqual([expect.objectContaining({ state: 'canceled', artifacts: [inline] })]);
    expect(states).toEqual(['closed']);
    expect(receiver.state).toBe('closed');
  });

  it('keeps the first local end over one a callback starts as recovery winds down', () => {
    const states: string[] = [];
    const { receiver, outcomes } = settling({
      onArtifact: () => receiver.terminate('failed', 'from_callback'),
      onStateChange: (state) => states.push(state),
    });
    receiver.receive(PROVIDER, artifact(2));
    receiver.beginRecovery();

    receiver.terminate('closed');

    expect(outcomes).toEqual([expect.objectContaining({ state: 'canceled', artifacts: [inline] })]);
    expect(states).toEqual(['closed']);
  });

  it('is ending while a local end winds down an open recovery, so send() is refused', () => {
    const endingSeen: boolean[] = [];
    const { receiver } = settling({ onArtifact: () => endingSeen.push(receiver.isEnding) });
    receiver.receive(PROVIDER, artifact(2));
    receiver.beginRecovery();

    receiver.terminate('failed', 'session_access_revoked');

    expect(endingSeen).toEqual([true]);
  });

  it('applies a local end requested during recovery though a later recovery step throws', () => {
    const outcomes: { exchangeId: string; state: string }[] = [];
    const receiver: ConsumerSessionReceiver = new ConsumerSessionReceiver({
      parties: { sessionId: 's-1', counterpartUuid: PROVIDER, backendUuid: BACKEND },
      state: 'ready',
      recover: async () => {},
      events: {
        onOutcome: ({ exchangeId, state }) => {
          outcomes.push({ exchangeId, state });
          receiver.terminate('closed');
        },
      },
    });
    receiver.trackExchange('ex-1');
    receiver.trackExchange('ex-2');
    receiver.beginRecovery();
    // Parsing rejects refs that are not a list; here one stands in for any fault.
    const faulty = { exchangeId: 'ex-2', state: 'completed', error: null, artifactRefs: null };
    const snapshot = { ...record('ready'), exchanges: [...record('ready').exchanges, faulty] };

    expect(() => receiver.finishRecovery(snapshot as never)).toThrow(TypeError);

    expect(receiver.state).toBe('closed');
    expect(outcomes).toEqual([
      { exchangeId: 'ex-1', state: 'completed' },
      { exchangeId: 'ex-2', state: 'canceled' },
    ]);
  });

  it('drops an outcome held behind a gap when the session ends', () => {
    const { receiver, outcomes } = settling();
    receiver.receive(PROVIDER, { ...progress(2), type: 'exchange_outcome', state: 'completed' });

    receiver.terminate('closed');

    expect(outcomes).toEqual([expect.objectContaining({ state: 'canceled' })]);
  });
});

function failedNotice(overrides: Record<string, unknown>) {
  return {
    type: 'session_failed',
    protocolVersion: '2026-05-01',
    sessionId: 's-1',
    seq: 0,
    ts: 0,
    error: 'route_lost',
    ...overrides,
  };
}

describe('session envelope validation', () => {
  it.each([
    ['a provider message with seq 0', PROVIDER, { seq: 0 }],
    ['a provider message with no seq', PROVIDER, { seq: undefined }],
    ['a provider message with a fractional seq', PROVIDER, { seq: 1.5 }],
    ['a provider message with an unsafe seq', PROVIDER, { seq: 2 ** 53 }],
    ['a backend notice with a non-zero seq', BACKEND, { seq: 1 }],
    ['a backend notice with no seq', BACKEND, { seq: undefined }],
    ['a backend notice for another session', BACKEND, { sessionId: 's-2' }],
    ['a provider message for another session', PROVIDER, { seq: 1, sessionId: 's-2' }],
    [
      'a route probe with a valid session envelope',
      PROVIDER,
      { type: 'route_probe', seq: 1, nonce: 'n-1' },
    ],
  ])('%s is dropped without a state change', (_name, publisher, overrides) => {
    const states: string[] = [];
    const receiver = new ConsumerSessionReceiver({
      parties: { sessionId: 's-1', counterpartUuid: PROVIDER, backendUuid: BACKEND },
      state: 'ready',
      recover: async () => {},
      events: { onStateChange: (state) => states.push(state) },
    });

    expect(receiver.receive(publisher, failedNotice(overrides))).toBe(false);
    expect(states).toEqual([]);
  });
});

describe('a stream_started', () => {
  function streaming() {
    const started: unknown[] = [];
    const receiver = new ConsumerSessionReceiver({
      parties: { sessionId: 's-1', counterpartUuid: PROVIDER, backendUuid: BACKEND },
      state: 'ready',
      recover: async () => {},
      events: { onStreamStarted: (event) => started.push(event) },
    });
    receiver.trackExchange('ex-1');
    return { receiver, started };
  }

  const streamStarted = (seq: number, overrides: Record<string, unknown> = {}) => ({
    type: 'stream_started',
    protocolVersion: '2026-05-01',
    sessionId: 's-1',
    seq,
    ts: 0,
    exchangeId: 'ex-1',
    streamId: 'st-1',
    kind: 'foreign_subscribe',
    channel: 'live.eu',
    templateParam: 'show',
    templateValue: 'eu',
    mode: 'access_manager',
    presence: false,
    ...overrides,
  });

  /** What reaches the session when `publisher` sends a stream_started with `overrides`. */
  function reaching(overrides: Record<string, unknown> = {}, publisher = PROVIDER) {
    const { receiver, started } = streaming();
    receiver.receive(publisher, streamStarted(1, overrides));
    return started;
  }

  it.each([
    [
      'a one-channel stream',
      {},
      { channel: 'live.eu', templateParam: 'show', templateValue: 'eu' },
    ],
    [
      'a wildcard stream with no template value',
      { channel: undefined, wildcard: 'live.*', templateParam: undefined, templateValue: undefined },
      { wildcard: 'live.*' },
    ],
    ['a consent stream', { mode: 'consent' }, { mode: 'consent' }],
  ])('reaches the session for %s', (_name, overrides, expected) => {
    expect(reaching(overrides)).toEqual([
      expect.objectContaining({
        exchangeId: 'ex-1',
        streamId: 'st-1',
        kind: 'foreign_subscribe',
        presence: false,
        ...expected,
      }),
    ]);
  });

  it('reaches the session with only its ids for a kind it does not consume', () => {
    const started = reaching({ kind: 'stream', mode: 'open', channel: undefined });
    expect(started).toEqual([{ exchangeId: 'ex-1', streamId: 'st-1', kind: 'stream' }]);
  });

  it('reaches the session with only its ids, as a kind it does not consume, when it has no kind', () => {
    const started = reaching({ kind: undefined, mode: 'open', channel: undefined });
    expect(started).toEqual([{ exchangeId: 'ex-1', streamId: 'st-1', kind: 'session_stream' }]);
  });

  it.each([
    ['an unknown mode', { mode: 'open' }],
    ['both a channel and a wildcard', { wildcard: 'live.*' }],
    ['neither a channel nor a wildcard', { channel: undefined }],
    ['no streamId', { streamId: undefined }],
    ['an exchange this session did not send', { exchangeId: 'ex-2' }],
  ])('is ignored with %s', (_name, overrides) => {
    expect(reaching(overrides)).toEqual([]);
  });

  it('is ignored for an exchange the session forgot', () => {
    const { receiver, started } = streaming();
    receiver.forgetExchange('ex-1');

    receiver.receive(PROVIDER, streamStarted(1));

    expect(started).toEqual([]);
  });

  it('reaches the session once per streamId, even from a later seq', () => {
    const { receiver, started } = streaming();

    receiver.receive(PROVIDER, streamStarted(1));
    receiver.receive(PROVIDER, streamStarted(2));
    receiver.receive(PROVIDER, streamStarted(3, { streamId: 'st-2' }));

    expect(started.map((event) => (event as { streamId: string }).streamId)).toEqual([
      'st-1',
      'st-2',
    ]);
  });

  it.each([
    ['the backend', BACKEND, { seq: 0 }],
    ['a stranger', 'uuid-stranger', {}],
  ])('is ignored when %s publishes it', (_name, publisher, overrides) => {
    expect(reaching(overrides, publisher)).toEqual([]);
  });
});

function providerReceiver(events: ProviderReceiverEvents) {
  return new ProviderSessionReceiver({
    parties: { sessionId: 's-1', counterpartUuid: CONSUMER, backendUuid: BACKEND },
    state: 'ready',
    events,
  });
}

function consumerMessage(type: string, seq: number, fields: Record<string, unknown> = {}) {
  return { type, protocolVersion: '2026-05-01', sessionId: 's-1', seq, ts: 0, ...fields };
}

describe('provider close', () => {
  it('still honors a backend notice while closing, and ends once terminated', () => {
    const notices: string[] = [];
    const receiver = providerReceiver({ onBackendNotice: (state) => notices.push(state) });

    receiver.receive(CONSUMER, consumerMessage('session_close', 1));
    expect(receiver.state).toBe('closing');
    expect(receiver.receive(BACKEND, failedNotice({}))).toBe(true);

    expect(notices).toEqual(['failed']);
    expect(receiver.state).toBe('failed');
    receiver.terminate('closed');
    expect(receiver.state).toBe('failed');
  });

  it('starts no exchange after the consumer asked to close', () => {
    const started: string[] = [];
    const receiver = providerReceiver({ onExchange: (e) => started.push(e.exchangeId) });

    receiver.receive(CONSUMER, consumerMessage('session_close', 1));
    receiver.receive(
      CONSUMER,
      consumerMessage('exchange', 2, { exchangeId: 'ex-2', requestParts: [] }),
    );

    expect(started).toEqual([]);
  });
});

describe('provider exchange ids', () => {
  it.each([
    ['empty', ''],
    ['over 64 characters', 'x'.repeat(65)],
  ])('an %s exchangeId starts no exchange', (_name, exchangeId) => {
    const started: string[] = [];
    const receiver = providerReceiver({ onExchange: (e) => started.push(e.exchangeId) });

    receiver.receive(CONSUMER, consumerMessage('exchange', 1, { exchangeId, requestParts: [] }));

    expect(started).toEqual([]);
  });
});

describe('provider dedup', () => {
  it('dispatches each seq of several interleaved gaps exactly once', () => {
    const started: string[] = [];
    const receiver = providerReceiver({ onExchange: (e) => started.push(e.exchangeId) });
    const send = (seq: number) =>
      receiver.receive(
        CONSUMER,
        consumerMessage('exchange', seq, { exchangeId: `ex-${seq}`, requestParts: [] }),
      );

    const order = [1, 4, 9, 7, 2, 8, 3, 5, 6];
    expect(order.map(send)).toEqual(order.map(() => true));
    expect(order.map(send)).toEqual(order.map(() => false));

    expect(started).toEqual(order.map((seq) => `ex-${seq}`));
  });

  it('dispatches a late seq that arrives after a gap was skipped over', () => {
    const started: string[] = [];
    const receiver = new ProviderSessionReceiver({
      parties: { sessionId: 's-1', counterpartUuid: CONSUMER, backendUuid: BACKEND },
      state: 'ready',
      events: { onExchange: (exchange) => started.push(exchange.exchangeId) },
    });
    const exchange = (seq: number) => ({
      type: 'exchange',
      protocolVersion: '2026-05-01',
      sessionId: 's-1',
      seq,
      ts: 0,
      exchangeId: `ex-${seq}`,
      requestParts: [],
    });

    receiver.receive(CONSUMER, exchange(1));
    receiver.receive(CONSUMER, exchange(3));
    expect(receiver.receive(CONSUMER, exchange(2))).toBe(true);
    expect(receiver.receive(CONSUMER, exchange(3))).toBe(false);

    expect(started).toEqual(['ex-1', 'ex-3', 'ex-2']);
  });
});

describe('throwing callbacks', () => {
  const boom = () => {
    throw new Error('application bug');
  };

  it('still starts the exchange when onGap throws', () => {
    const started: string[] = [];
    const receiver = providerReceiver({
      onGap: boom,
      onExchange: (e) => started.push(e.exchangeId),
    });

    receiver.receive(CONSUMER, consumerMessage('exchange', 1, { exchangeId: 'ex-1', requestParts: [] }));
    receiver.receive(CONSUMER, consumerMessage('exchange', 3, { exchangeId: 'ex-3', requestParts: [] }));

    expect(started).toEqual(['ex-1', 'ex-3']);
  });

  it('settles every exchange and reports the terminal state when onOutcome throws', () => {
    const settled: string[] = [];
    const states: string[] = [];
    const receiver = new ConsumerSessionReceiver({
      parties: { sessionId: 's-1', counterpartUuid: PROVIDER, backendUuid: BACKEND },
      state: 'ready',
      recover: async () => {},
      events: {
        onOutcome: (outcome) => {
          settled.push(outcome.exchangeId);
          boom();
        },
        onStateChange: (state) => states.push(state),
      },
    });
    receiver.trackExchange('ex-1');
    receiver.trackExchange('ex-2');

    receiver.receive(BACKEND, failedNotice({}));

    expect(settled).toEqual(['ex-1', 'ex-2']);
    expect(states).toEqual(['failed']);
  });
});
