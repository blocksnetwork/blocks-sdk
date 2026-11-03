import type PubNub from 'pubnub';
import type { ArtifactRef } from '../artifacts.js';
import { createLogger } from '../logger.js';
import { textPart } from '../part-helpers.js';
import { CURRENT_PROTOCOL_VERSION } from '../protocol-version.js';
import type { SendMessageRequestPart } from '../task-client.js';
import { mapTransportCategory } from '../transport-categories.js';
import { ConsumerSessionReceiver } from './consumer-receiver.js';
import { ConsumerTokenRotation } from './consumer-token.js';
import {
  SESSION_ACCESS_REVOKED as ACCESS_REVOKED,
  SessionBackpressureError,
  SessionError,
  SessionFailedError,
} from './session-errors.js';
import { succeedsWithin, withinDeadline } from './deadlines.js';
import { ManagedExchanges } from './managed-exchanges.js';
import {
  isAlreadyOpen,
  isTransientError,
  refusesAccess,
  retry,
  sendManagedClosedBeacon,
} from './managed-retry.js';
import {
  answersFinal,
  type OpenedSession,
  type SessionBeacon,
  type SessionHttp,
  type SessionSnapshot,
} from './session-http.js';
import { SessionStreams, type ExchangeStream } from './session-streams.js';
import type { Message } from './stream-types.js';
import type {
  ExchangeOutcome as ReceivedOutcome,
  ExchangeOutcomeState,
  ExchangeProgress,
  SessionState,
} from './types.js';

const logger = createLogger('Session');

/** In flight plus queued, per session. */
export const MAX_PENDING_EXCHANGES = 8;
/** How long `close()` waits for the counterpart's `session_closed` before ending locally. */
export const SESSION_CLOSE_TIMEOUT_MS = 5_000;
export const SESSION_READY_TIMEOUT_MS = 30_000;
/** The backend cancels a session still unready after 5 minutes, so a wait must end well before. */
export const MAX_SESSION_READY_TIMEOUT_MS = 120_000;
const NOT_READY = 'session_not_ready';
const HISTORY_PAGE = 100;
// A session replays at most this many pages; the snapshot then settles what history did not hold.
const HISTORY_MAX_PAGES = 10;
const HISTORY_TIMEOUT_MS = 10_000;

/** A string is sent as one text part. */
export type SessionInput = string | SendMessageRequestPart[];

export type { ExchangeProgress, SessionState };

/** @experimental Part of the sessions preview; see {@link Session}. */
export interface ExchangeOutcome {
  exchangeId: string;
  state: ExchangeOutcomeState;
  error?: string;
  reason?: string;
  artifacts: ArtifactRef[];
}

export type Unsubscribe = () => void;

/**
 * One correlated input inside a session; it ends in exactly one outcome.
 *
 * @experimental Part of the sessions preview; see {@link Session}.
 */
export interface Exchange {
  readonly exchangeId: string;
  onProgress(cb: (progress: ExchangeProgress) => void): Unsubscribe;
  /** Also replays the artifacts already received. */
  onArtifact(cb: (artifactRef: ArtifactRef) => void): Unsubscribe;
  /** Resolves with the one outcome; rejects only when `timeoutMs` passes first. */
  waitForOutcome(timeoutMs?: number): Promise<ExchangeOutcome>;
  /** Asks the provider to cancel; the outcome is still delivered. */
  cancel(): Promise<void>;
  /**
   * The messages of the session stream this exchange started, delivered at most once. May be
   * called once, any time after `send()`; the SDK starts the stream as soon as the exchange
   * settles, buffers up to 1000 messages (the oldest are dropped) and hands them over as you
   * iterate. Iteration ends when you leave the loop, the stream stops or the session closes. An
   * exchange that settles `completed` without a stream ends empty. It throws:
   * - `SessionFailedError` when the session fails, or with `session_access_revoked` when the
   *   stream's access could not be renewed (the session is then closed);
   * - `SessionStreamError` when the exchange settles `failed` or `canceled` without a stream (for
   *   example, canceled before its stream started), or the stream is of a kind this SDK does not
   *   consume (`stream_kind_unsupported`).
   *
   * Leaving the loop stops the stream for good, not the session: the session cannot stream again
   * (a further Subscribe in it is refused), and as nothing then renews its lease, it closes idle
   * within about 15 minutes. `cancel()` does not stop a running stream.
   *
   * A stream that starts without its `stream_started` having been seen (the exchange was settled
   * by recovery) carries no `templateValue`.
   *
   * A second call to `stream()` throws a `TypeError`.
   *
   * @experimental Part of the sessions preview; see {@link Session}.
   */
  stream(): AsyncIterable<Message>;
}

/**
 * One durable interaction with one agent, from `BlocksClient.openSession()`: many exchanges over
 * one channel.
 *
 * @experimental Sessions are a preview: refused with `SESSIONS_DISABLED` until enabled for
 * your org, and may change before general availability.
 */
export interface Session {
  readonly sessionId: string;
  readonly state: SessionState;
  /** Throws `SessionBackpressureError` rather than queue beyond the pending bound. */
  send(input: SessionInput): Exchange;
  onStateChange(cb: (state: SessionState) => void): Unsubscribe;
  /**
   * Catches up after a gap or a reconnect: replays the session channel's history through the
   * usual filtering and dedup, then applies the backend's record of the session. Runs on its own
   * when a `seq` gap persists and after the subscription reconnects.
   */
  recover(): Promise<void>;
  /** Idempotent. Resolves once the session is terminal and its connection released. */
  close(): Promise<void>;
  [Symbol.asyncDispose](): Promise<void>;
}

export interface SessionTransport {
  pubnub: PubNub;
  http: SessionHttp;
}

/** Lets the owning client track a session from creation until its connection is released. */
export interface SessionLifecycle {
  onCreated(session: ClientSession): void;
  onReleased(session: ClientSession): void;
}

function invoke<A extends unknown[]>(name: string, cb: (...args: A) => void, ...args: A): void {
  try {
    cb(...args);
  } catch (err) {
    logger('error', 'session callback threw', { callback: name, error: String(err) });
  }
}

export function toRequestParts(input: SessionInput): SendMessageRequestPart[] {
  const parts = typeof input === 'string' ? [textPart(input)] : input;
  if (!Array.isArray(parts))
    throw new TypeError('session input must be a string or an array of parts');
  if (parts.some((part) => part.file !== undefined)) {
    throw new TypeError('file parts are not supported on sessions yet');
  }
  return parts;
}

class SessionExchange implements Exchange {
  private outcome?: ExchangeOutcome;
  private readonly artifacts: ArtifactRef[] = [];
  private readonly progressCbs = new Set<(progress: ExchangeProgress) => void>();
  private readonly artifactCbs = new Set<(artifactRef: ArtifactRef) => void>();
  private readonly waiters = new Set<(outcome: ExchangeOutcome) => void>();

  constructor(
    readonly exchangeId: string,
    private readonly requestCancel: (exchangeId: string) => Promise<void>,
    private readonly exchangeStream: ExchangeStream,
  ) {}

  get settled(): boolean {
    return this.outcome !== undefined;
  }

  onProgress(cb: (progress: ExchangeProgress) => void): Unsubscribe {
    this.progressCbs.add(cb);
    return () => this.progressCbs.delete(cb);
  }

  onArtifact(cb: (artifactRef: ArtifactRef) => void): Unsubscribe {
    for (const ref of this.artifacts) invoke('onArtifact', cb, ref);
    this.artifactCbs.add(cb);
    return () => this.artifactCbs.delete(cb);
  }

  waitForOutcome(timeoutMs?: number): Promise<ExchangeOutcome> {
    const { outcome } = this;
    if (outcome) return Promise.resolve(outcome);
    return new Promise((resolve, reject) => {
      const timer =
        timeoutMs === undefined
          ? undefined
          : setTimeout(() => {
              this.waiters.delete(settle);
              reject(
                new Error(`exchange ${this.exchangeId} did not settle within ${timeoutMs} ms`),
              );
            }, timeoutMs);
      const settle = (settled: ExchangeOutcome) => {
        clearTimeout(timer);
        resolve(settled);
      };
      this.waiters.add(settle);
    });
  }

  async cancel(): Promise<void> {
    if (!this.settled) await this.requestCancel(this.exchangeId);
  }

  stream(): AsyncIterable<Message> {
    return this.exchangeStream.stream();
  }

  progress(progress: ExchangeProgress): void {
    for (const cb of this.progressCbs) invoke('onProgress', cb, progress);
  }

  artifact(ref: ArtifactRef): void {
    this.artifacts.push(ref);
    for (const cb of this.artifactCbs) invoke('onArtifact', cb, ref);
  }

  settle(outcome: ExchangeOutcome): void {
    if (this.outcome) return;
    this.outcome = outcome;
    for (const waiter of this.waiters) waiter(outcome);
    this.waiters.clear();
    this.progressCbs.clear();
    this.artifactCbs.clear();
  }
}

/** How long an open waits, from its request to `session_ready`, and when that wait ends. */
export interface ReadyDeadline {
  timeoutMs: number;
  at: number;
}

export function readyDeadline(timeoutMs = SESSION_READY_TIMEOUT_MS): ReadyDeadline {
  if (
    typeof timeoutMs !== 'number' ||
    !(timeoutMs > 0 && timeoutMs <= MAX_SESSION_READY_TIMEOUT_MS)
  ) {
    throw new RangeError(
      `readyTimeoutMs must be a number greater than 0 and at most ${MAX_SESSION_READY_TIMEOUT_MS}`,
    );
  }
  return { timeoutMs, at: Date.now() + timeoutMs };
}

/** The session ends `canceled` when its ready deadline passes, so the open rejects with that code. */
export const readyTimeoutError = ({ timeoutMs }: ReadyDeadline) =>
  new SessionError('session_canceled', `session was not ready within ${timeoutMs} ms`);

/** The error an open rejects with when the session ends before it is ready. */
function terminalError(state: SessionState, error: string | undefined): SessionError {
  return state === 'failed'
    ? new SessionFailedError(error ?? 'session_failed')
    : new SessionError(`session_${state}`, `Session ${state} before it was ready`);
}

interface TrackedExchange {
  handle: SessionExchange;
  exchangeId: string;
  requestParts: SendMessageRequestPart[];
}

export class ClientSession implements Session {
  private seq = 0;
  private failure?: string;
  private readonly pending = new Map<string, SessionExchange>();
  private readonly stateCbs = new Set<(state: SessionState) => void>();
  private readonly terminalWaiters = new Set<() => void>();
  private readonly receiver: ConsumerSessionReceiver;
  private readonly listener: PubNub.Listener;
  private readonly token: ConsumerTokenRotation;
  private publishQueue: Promise<unknown> = Promise.resolve();
  private closing?: Promise<void>;
  private openPublished = false;
  /** Set by close() alone; `closing` is also set when the counterpart ends the session. */
  private closeRequested = false;
  private released = false;
  private recovering?: Promise<void>;
  private readonly lifetime = new AbortController();
  private readonly managed?: ManagedExchanges;
  private readonly streams: SessionStreams;
  /**
   * The `opened` beacon answered a final state, was refused as already claimed, or the one-shot's
   * outcome arrived: the backend ends the session itself, and a `closed` beacon sent ahead of its
   * record of the exchange would drop that record.
   */
  private skipClosedBeacon = false;
  /** The exchange the `opened` beacon carries; a request-kind one makes the session a one-shot. */
  private oneShotExchangeId?: string;
  /** Aborted when a managed close starts, which ends an `opened` beacon still being retried. */
  private readonly closeStarted = new AbortController();
  /** The newest timetoken received with no gap open; history replays from here. */
  private lastTimetoken?: string;

  private constructor(
    private readonly opened: OpenedSession,
    private readonly transport: SessionTransport,
    private readonly lifecycle: SessionLifecycle,
  ) {
    this.streams = new SessionStreams({
      sessionId: opened.sessionId,
      http: transport.http,
      signal: this.lifetime.signal,
      answeredOverHttps: opened.managed === true,
      closeSession: () => this.close(),
    });
    this.receiver = new ConsumerSessionReceiver({
      parties: {
        sessionId: opened.sessionId,
        counterpartUuid: opened.providerTransportUuid,
        backendUuid: opened.backendTransportUuid,
      },
      recover: () => this.recover(),
      events: {
        onStateChange: (state, error) => {
          if (state === 'failed') this.failure = error;
          this.stateChanged(state);
        },
        onProgress: (progress) => this.pending.get(progress.exchangeId)?.progress(progress),
        onArtifact: (artifact) =>
          this.pending.get(artifact.exchangeId)?.artifact(artifact.artifactRef as ArtifactRef),
        onOutcome: (outcome) => {
          if (outcome.exchangeId === this.oneShotExchangeId) this.skipClosedBeacon = true;
          this.settle(outcome);
        },
        onStreamStarted: (started) => this.streams.streamStarted(started),
      },
    });
    if (opened.managed === true) {
      const { http } = transport;
      this.managed = new ManagedExchanges({
        sessionId: opened.sessionId,
        http,
        signal: this.lifetime.signal,
        isPending: (exchangeId) => this.pending.has(exchangeId),
        rejectExchange: (exchangeId) => this.settleUnsent(exchangeId, 'failed', 'publish_failed'),
        cancelLocally: (exchangeId) => this.settleUnsent(exchangeId, 'canceled'),
        revokeAccess: () => this.receiver.terminate('failed', ACCESS_REVOKED),
        recover: () =>
          this.recover().catch((err: unknown) =>
            logger('warn', 'session recovery after an exchange answer failed', {
              error: String(err),
            }),
          ),
      });
    }
    this.token = new ConsumerTokenRotation({
      sessionId: opened.sessionId,
      refresh: () => transport.http.refreshToken(opened.sessionId),
      setToken: (pamToken) => transport.pubnub.setToken(pamToken),
      resume: () => transport.pubnub.reconnect(),
      onRevoked: () => this.receiver.terminate('failed', ACCESS_REVOKED),
    });
    this.listener = {
      message: (event) => {
        if (event.channel === opened.sessionChannel && event.publisher !== undefined) {
          this.receive(event.publisher, event.message, event.timetoken);
        }
      },
    };
  }

  /**
   * Subscribes, publishes `session_open` (carrying `firstParts` inline when given)
   * and resolves once the provider reports the session ready. A managed session publishes
   * nothing: its `opened` beacon carries `firstParts` over HTTPS.
   */
  static async open(
    opened: OpenedSession,
    transport: SessionTransport,
    options: { firstParts?: SendMessageRequestPart[]; ready: ReadyDeadline },
    lifecycle: SessionLifecycle,
  ): Promise<{ session: ClientSession; exchange?: Exchange }> {
    const session = new ClientSession(opened, transport, lifecycle);
    lifecycle.onCreated(session);
    let timer: ReturnType<typeof setTimeout> | undefined;
    const deadline = new Promise<never>((_, reject) => {
      timer = setTimeout(
        () => reject(readyTimeoutError(options.ready)),
        Math.max(0, options.ready.at - Date.now()),
      );
    });
    try {
      const ready = session.untilReady();
      // Awaited below; a terminal state can reject it while subscribe is still pending.
      ready.catch(() => undefined);
      // `ready` cannot resolve before session_open is published, only reject on a terminal state.
      await Promise.race([session.subscribeChannel(), ready, deadline]);
      const exchange =
        options.firstParts === undefined ? undefined : session.track(options.firstParts);
      if (session.managed) {
        session.oneShotExchangeId = exchange?.exchangeId;
        const sent = session.sendOpenedBeacon(exchange, options.ready.at);
        sent.catch(() => undefined);
        const result = await Promise.race([sent, ready, deadline]);
        // The input was turned down, so the session never becomes ready; its exchange has failed.
        if (result === 'rejected') return { session, exchange: exchange?.handle };
      } else {
        const published = session.publish({
          type: 'session_open',
          ...(exchange
            ? { exchange: { exchangeId: exchange.exchangeId, requestParts: exchange.requestParts } }
            : {}),
        });
        // Set at enqueue: a close() from here on queues session_close behind session_open.
        session.openPublished = true;
        await Promise.race([published, ready, deadline]);
        session.beacon('opened');
      }
      await Promise.race([ready, deadline]);
      // session_ready can land while a close (from destroy(), say) is publishing session_close.
      if (session.closeRequested) {
        throw new SessionError('session_canceled', 'The session was closed while opening');
      }
      // Or the session can end in the same tick it became ready. A one-shot call ends that way
      // by design, its result already delivered.
      if (session.receiver.isTerminal && !(session.managed && exchange?.handle.settled)) {
        throw terminalError(session.state, session.failure);
      }
      return { session, exchange: exchange?.handle };
    } catch (err) {
      // Not awaited: the deadline bounds the open, and close() is bounded on its own.
      session.close().catch((closeErr: unknown) => {
        logger('warn', 'session close after a failed open failed', { error: String(closeErr) });
      });
      throw err;
    } finally {
      clearTimeout(timer);
    }
  }

  get sessionId(): string {
    return this.opened.sessionId;
  }

  get state(): SessionState {
    return this.receiver.state;
  }

  send(input: SessionInput): Exchange {
    this.admitExchange();
    const tracked = this.track(toRequestParts(input));
    if (this.managed) return this.sendManaged(tracked);
    const { handle, exchangeId, requestParts } = tracked;
    this.publish({ type: 'exchange', exchangeId, requestParts }).catch((err: unknown) => {
      logger('warn', 'exchange publish failed', { exchangeId, error: String(err) });
      this.receiver.forgetExchange(exchangeId);
      this.settle({ exchangeId, state: 'failed', error: 'publish_failed', artifacts: [] });
    });
    return handle;
  }

  private admitExchange(): void {
    if (this.state !== 'ready' || this.closeRequested || this.receiver.isEnding) {
      const ending = this.closeRequested || this.receiver.isEnding;
      const state = ending && !this.receiver.isTerminal ? 'closing' : this.state;
      throw new SessionError(NOT_READY, `Cannot send on a session that is ${state}`);
    }
    if (this.pending.size >= MAX_PENDING_EXCHANGES) {
      throw new SessionBackpressureError(MAX_PENDING_EXCHANGES);
    }
  }

  private sendManaged({ handle, exchangeId, requestParts }: TrackedExchange): Exchange {
    this.managed?.enqueue({
      exchangeId,
      requestParts,
      onAnswer: (answer) => this.streams.answered(exchangeId, answer),
    });
    return handle;
  }

  private settleUnsent(exchangeId: string, state: 'failed' | 'canceled', error?: string): void {
    this.receiver.forgetExchange(exchangeId);
    this.settle({ exchangeId, state, ...(error ? { error } : {}), artifacts: [] });
  }

  /** Serialized: overlapping triggers (a gap and a reconnect) run one recovery after another. */
  recover(): Promise<void> {
    const previous = this.recovering?.catch(() => {}) ?? Promise.resolve();
    const run = previous.then(() => this.recoverOnce());
    this.recovering = run;
    return run;
  }

  /**
   * One recovery, in the order the receiver owns: terminal messages are deferred, history
   * replays, then the backend's record (read after history, so it is the fresher of the two)
   * settles what history did not hold. Recovery fails only if both sources do.
   */
  private async recoverOnce(): Promise<void> {
    if (this.released || this.receiver.isTerminal) return;
    // Taken now: a live message accepted during recovery must not move the replay point past
    // what was missed before recovery began.
    const replayFrom = this.lastTimetoken;
    this.receiver.beginRecovery();
    let historyError: unknown;
    let snapshotError: unknown;
    let snapshot: SessionSnapshot | undefined;
    try {
      await this.replayHistory(replayFrom).catch((err: unknown) => {
        historyError = err;
      });
      this.receiver.historyReplayed();
      snapshot = await this.transport.http
        .getSession(this.opened.sessionId)
        .catch((err: unknown) => {
          snapshotError = err;
          return undefined;
        });
    } finally {
      if (!this.released) this.receiver.finishRecovery(snapshot);
    }
    if (historyError !== undefined && snapshotError !== undefined) {
      throw Object.assign(
        new Error(
          `session recovery failed: history (${String(historyError)}) and snapshot (${String(snapshotError)}) both unavailable`,
        ),
        { errors: [historyError, snapshotError] },
      );
    }
    if (historyError !== undefined) {
      logger('warn', 'session history replay failed; recovered from the snapshot alone', {
        error: String(historyError),
      });
    }
    if (snapshotError !== undefined) {
      logger('warn', 'session snapshot read failed; recovered from history alone', {
        error: String(snapshotError),
      });
    }
  }

  /** A message held behind a gap must not move the replay point past the message it waits for. */
  private receive(publisher: string, message: unknown, timetoken: unknown, replayed = false): void {
    // A dropped message (the consumer's own echo, a stranger, a duplicate) is no evidence that
    // everything before it arrived, so only an accepted one moves the replay point.
    const accepted = this.receiver.receive(publisher, message, replayed);
    if (accepted && !this.receiver.hasGap) this.noteTimetoken(timetoken);
  }

  /** Gives up after {@link HISTORY_TIMEOUT_MS}, replaying nothing, so the snapshot settles alone. */
  private async replayHistory(replayFrom: string | undefined): Promise<void> {
    let expired = false;
    const fetched = await withinDeadline(
      this.fetchHistory(replayFrom, () => expired),
      HISTORY_TIMEOUT_MS,
      'session history',
      () => {
        expired = true;
      },
    );
    for (const entry of fetched) {
      if (this.released || this.receiver.isTerminal) return;
      if (entry.uuid === undefined) continue;
      this.receive(entry.uuid, entry.message, entry.timetoken, true);
    }
  }

  /**
   * Pages back from now to the last timetoken seen, oldest first; `end` is inclusive, and dedup
   * drops the repeat.
   */
  private async fetchHistory(replayFrom: string | undefined, expired: () => boolean) {
    const channel = this.opened.sessionChannel;
    const fetched: { timetoken: string; uuid?: string; message: unknown }[] = [];
    let start: string | undefined;
    for (let page = 0; page < HISTORY_MAX_PAGES && !expired(); page++) {
      const response = await this.transport.pubnub.fetchMessages({
        channels: [channel],
        count: HISTORY_PAGE,
        stringifiedTimeToken: true,
        ...(replayFrom !== undefined ? { end: replayFrom } : {}),
        ...(start !== undefined ? { start } : {}),
      });
      const messages = (response.channels[channel] ?? []).map((m) => ({
        timetoken: String(m.timetoken),
        uuid: m.uuid,
        message: m.message,
      }));
      fetched.push(...messages);
      if (messages.length < HISTORY_PAGE) break;
      start = messages.reduce(
        (oldest, m) => (BigInt(m.timetoken) < BigInt(oldest) ? m.timetoken : oldest),
        messages[0].timetoken,
      );
    }
    return fetched.sort((a, b) => (BigInt(a.timetoken) < BigInt(b.timetoken) ? -1 : 1));
  }

  private noteTimetoken(timetoken: unknown): void {
    if (typeof timetoken !== 'string' && typeof timetoken !== 'number') return;
    const value = String(timetoken);
    if (this.lastTimetoken === undefined || BigInt(value) > BigInt(this.lastTimetoken)) {
      this.lastTimetoken = value;
    }
  }

  onStateChange(cb: (state: SessionState) => void): Unsubscribe {
    this.stateCbs.add(cb);
    return () => this.stateCbs.delete(cb);
  }

  close(): Promise<void> {
    this.closeRequested = true;
    // Deferred a tick so `closing` is set before runClose() emits a state change a callback could re-enter.
    this.closing ??= Promise.resolve().then(() => this.runClose());
    return this.closing;
  }

  [Symbol.asyncDispose](): Promise<void> {
    return this.close();
  }

  private async runClose(): Promise<void> {
    if (this.managed) return this.runManagedClose();
    if (!this.receiver.isTerminal && !this.openPublished) {
      // The provider never heard of this session; only the backend needs telling.
      this.receiver.terminate('canceled');
      this.beacon('closed');
    } else if (!this.receiver.isTerminal) {
      if (this.state !== 'open') this.receiver.beginClosing();
      // Bounded: it queues behind any publish still in flight, which may never finish.
      const published = await succeedsWithin(
        this.publish({ type: 'session_close' }),
        SESSION_CLOSE_TIMEOUT_MS,
        'session publish',
      );
      // Re-read: session_ready may have arrived while session_close was in flight.
      if (this.state === 'open') {
        this.receiver.terminate('canceled');
      } else {
        this.receiver.beginClosing();
        if (published) await this.untilTerminal(SESSION_CLOSE_TIMEOUT_MS);
        this.receiver.terminate('closed');
      }
      this.beacon('closed');
    }
    this.release();
  }

  /** A managed session ends through its `closed` beacon, not `session_close`. */
  private async runManagedClose(): Promise<void> {
    this.closeStarted.abort();
    if (!this.receiver.isTerminal) {
      if (this.state !== 'open') this.receiver.beginClosing();
      this.managed?.stop();
      if (!this.skipClosedBeacon) {
        await succeedsWithin(
          sendManagedClosedBeacon(
            this.transport.http,
            this.sessionId,
            SESSION_CLOSE_TIMEOUT_MS,
            this.lifetime.signal,
          ),
          SESSION_CLOSE_TIMEOUT_MS,
          'managed closed beacon',
        );
      }
      // Re-read: session_ready may have arrived while the beacon was in flight.
      this.receiver.terminate(this.state === 'open' ? 'canceled' : 'closed');
    }
    this.release();
  }

  /** `rejected` when the backend turned the first exchange down; an access refusal fails the session. */
  private async sendOpenedBeacon(
    exchange: TrackedExchange | undefined,
    until: number,
  ): Promise<'answered' | 'rejected'> {
    const { http } = this.transport;
    const signal = AbortSignal.any([this.lifetime.signal, this.closeStarted.signal]);
    const exchangeId = exchange?.exchangeId;
    if (exchangeId) this.managed?.beginBeacon(exchangeId);
    try {
      const answer = await retry(
        () =>
          http.sendBeacon(
            this.sessionId,
            'opened',
            exchange && { exchangeId: exchange.exchangeId, requestParts: exchange.requestParts },
            signal,
          ),
        { retryable: isTransientError, signal, until },
      );
      if (exchangeId) {
        this.managed?.noteAnswer(exchangeId, answer);
        this.streams.answered(exchangeId, answer);
        if (answersFinal(answer)) this.skipClosedBeacon = true;
      }
    } catch (err) {
      // A transport error that outlasts the deadline is the open's ready timeout.
      if (signal.aborted || isTransientError(err)) return 'answered';
      if (isAlreadyOpen(err)) {
        // Another holder claimed the session; closing it would end it for them.
        this.skipClosedBeacon = true;
        throw err;
      }
      if (refusesAccess(err)) {
        this.receiver.terminate('failed', ACCESS_REVOKED);
      } else if (exchangeId) {
        this.settleUnsent(exchangeId, 'failed', 'publish_failed');
        return 'rejected';
      } else {
        throw err;
      }
    } finally {
      if (exchangeId) this.managed?.endBeacon(exchangeId);
    }
    return 'answered';
  }

  private track(requestParts: SendMessageRequestPart[]): TrackedExchange {
    const exchangeId = crypto.randomUUID();
    const handle = new SessionExchange(
      exchangeId,
      (id) =>
        this.managed
          ? this.managed.cancel(id)
          : this.publish({ type: 'exchange_cancel', exchangeId: id }),
      this.streams.track(exchangeId),
    );
    this.pending.set(exchangeId, handle);
    this.receiver.trackExchange(exchangeId);
    return { handle, exchangeId, requestParts };
  }

  private settle(outcome: ReceivedOutcome): void {
    const exchange = this.pending.get(outcome.exchangeId);
    if (!exchange) return;
    this.pending.delete(outcome.exchangeId);
    this.managed?.settled(outcome.exchangeId);
    this.streams.settled(outcome.exchangeId, outcome);
    exchange.settle({ ...outcome, artifacts: outcome.artifacts as ArtifactRef[] });
  }

  private subscribeChannel(): Promise<void> {
    return new Promise((resolve) => {
      const { pubnub } = this.transport;
      this.listener.status = (status) => {
        const category = mapTransportCategory(status);
        if (category === 'connected' || category === 'reconnected') {
          this.token.onConnected();
          resolve();
        }
        // A blip resumes the same subscription; what was published meanwhile comes from history.
        if (category === 'reconnected') {
          this.recover().catch((err) =>
            logger('warn', 'session recovery after reconnect failed', { error: String(err) }),
          );
        }
        // A denial the one refresh cannot cure fails the session, which rejects the open.
        if (category === 'access_denied') this.token.onDenied();
      };
      pubnub.setToken(this.opened.pamToken);
      this.token.start(this.opened.ttlMinutes);
      pubnub.addListener(this.listener);
      pubnub.subscribe({ channels: [this.opened.sessionChannel] });
    });
  }

  private publish(fields: { type: string } & Record<string, unknown>): Promise<void> {
    const { sessionId, sessionChannel } = this.opened;
    const message = {
      ...fields,
      protocolVersion: CURRENT_PROTOCOL_VERSION,
      sessionId,
      seq: ++this.seq,
      ts: Date.now(),
    };
    // Serialized so the provider receives this sender's seqs in order.
    // Checked when the publish runs: one queued behind a stalled publish may outlive the session.
    const sent = this.publishQueue.then(() => {
      if (this.released) throw new SessionError(NOT_READY, 'The session is no longer open');
      return this.transport.pubnub.publish({
        channel: sessionChannel,
        message,
        storeInHistory: true,
        meta: { sessionId, protocolVersion: CURRENT_PROTOCOL_VERSION },
      });
    });
    this.publishQueue = sent.catch(() => undefined);
    return sent.then(() => undefined);
  }

  private beacon(beacon: SessionBeacon): void {
    this.transport.http.sendBeacon(this.sessionId, beacon).catch((err: unknown) => {
      logger('debug', 'session beacon failed', { beacon, error: String(err) });
    });
  }

  private untilReady(): Promise<void> {
    return new Promise((resolve, reject) => {
      const unsubscribe = this.onStateChange((state) => {
        if (state === 'ready') resolve();
        else if (this.receiver.isTerminal) reject(terminalError(state, this.failure));
        else return;
        unsubscribe();
      });
    });
  }

  private untilTerminal(timeoutMs: number): Promise<void> {
    if (this.receiver.isTerminal) return Promise.resolve();
    return new Promise((resolve) => {
      const done = () => {
        clearTimeout(timer);
        this.terminalWaiters.delete(done);
        resolve();
      };
      const timer = setTimeout(done, timeoutMs);
      this.terminalWaiters.add(done);
    });
  }

  private stateChanged(state: SessionState): void {
    for (const cb of this.stateCbs) invoke('onStateChange', cb, state);
    if (!this.receiver.isTerminal) return;
    this.streams.sessionEnded(state, this.failure);
    for (const done of [...this.terminalWaiters]) done();
    // A terminal session publishes nothing more; close() releases it once it has run.
    if (!this.closing) this.closing = Promise.resolve().then(() => this.release());
  }

  private release(): void {
    if (this.released) return;
    this.released = true;
    this.token.stop();
    this.managed?.stop();
    this.streams.stop();
    this.lifetime.abort();
    const { pubnub } = this.transport;
    pubnub.removeListener(this.listener);
    pubnub.unsubscribeAll();
    pubnub.destroy();
    this.lifecycle.onReleased(this);
  }
}
