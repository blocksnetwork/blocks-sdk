import { createLogger } from '../logger.js';
import { SESSION_GAP_RECOVER_MS } from './in-order-delivery.js';
import { hasCode, isTransientError, retry, withDeadline } from './managed-retry.js';
import {
  MANAGED_ERROR_CODES,
  SESSION_ACCESS_REVOKED,
  SessionError,
  SessionFailedError,
  SessionStreamError,
} from './session-errors.js';
import type { ManagedAnswer, SessionHttp } from './session-http.js';
import { startForeignStream } from './stream-subscriber.js';
import type {
  ForeignStream,
  Message,
  StreamCredentials,
  StreamEndReason,
  StreamStarted,
} from './stream-types.js';
import type { ExchangeOutcomeState, SessionState } from './types.js';

const logger = createLogger('Session');

/** The backend ends a stream's lease 15 minutes after its last renewal. */
const STREAM_ALIVE_INTERVAL_MS = 5 * 60_000;
/**
 * Bounds each stream token fetch. The endpoint and the channel's delivery of `stream_started` can
 * both lag the exchange, and a 401 awaits an auth refresh that ignores the signal.
 */
const STREAM_TOKEN_BUDGET_MS = 10_000;
/** The only stream kind this SDK consumes. */
const FOREIGN_SUBSCRIBE = 'foreign_subscribe';

export interface SessionStreamsHost {
  sessionId: string;
  http: Pick<SessionHttp, 'fetchStreamToken' | 'sendBeacon'>;
  signal: AbortSignal;
  /** Whether each exchange's request is answered over HTTPS, an answer that may carry the credentials. */
  answeredOverHttps: boolean;
  closeSession(): Promise<void>;
}

interface Deferred<T> {
  promise: Promise<T>;
  resolve(value: T): void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

async function within<T>(promise: Promise<T>, ms: number): Promise<T | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const expired = new Promise<undefined>((resolve) => {
    timer = setTimeout(() => resolve(undefined), ms);
  });
  try {
    return await Promise.race([promise, expired]);
  } finally {
    clearTimeout(timer);
  }
}

function endClient(foreign: ForeignStream | undefined, error?: Error): void {
  foreign?.end(error).catch((err: unknown) => {
    logger('warn', 'ending the stream failed', { error: String(err) });
  });
}

/**
 * One `alive` interval per session while any stream client is connected. It is not restarted on a
 * reconnect, as a flapping connection would starve the lease of renewals; a tick is skipped while
 * no client is connected.
 */
class AliveLease {
  private readonly clients = new Map<object, boolean>();
  private timer: ReturnType<typeof setInterval> | undefined;

  constructor(private readonly host: SessionStreamsHost) {}

  connectionChanged(client: object, connected: boolean): void {
    if (this.host.signal.aborted) return;
    this.clients.set(client, connected);
    if (!connected || this.timer) return;
    this.timer = setInterval(() => this.tick(), STREAM_ALIVE_INTERVAL_MS);
    this.timer.unref?.();
  }

  release(client: object): void {
    this.clients.delete(client);
    if (this.clients.size === 0) this.stop();
  }

  stop(): void {
    clearInterval(this.timer);
    this.timer = undefined;
    this.clients.clear();
  }

  private tick(): void {
    if (![...this.clients.values()].some(Boolean)) return;
    const { sessionId, http, signal } = this.host;
    http.sendBeacon(sessionId, 'alive', undefined, signal).catch((err: unknown) => {
      logger('debug', 'alive beacon failed', { error: String(err) });
    });
  }
}

interface SettledOutcome {
  state: ExchangeOutcomeState;
  error?: string;
  reason?: string;
}

/**
 * The stream of one exchange. The SDK starts its client as soon as the exchange settles, whether
 * or not `stream()` is being iterated; the client buffers until it is.
 */
export class ExchangeStream {
  private readonly answered = deferred<void>();
  private hasAnswer: boolean;
  private credentials?: StreamCredentials;
  private started?: StreamStarted;
  private outcome?: SettledOutcome;
  /** Why a stream could not start. */
  private failure?: unknown;
  private foreign?: ForeignStream;
  /** Set when the session ends, so a client is never started on a dead session. */
  private over?: { error?: SessionFailedError };
  private claimed = false;
  private iterated = false;
  /** The consumer returned the iterator: no client is started for it, and a client already started is stopped. */
  private returned = false;
  private readonly returnedSignal = deferred<void>();
  /** Resolves once what `stream()` iterates is known: a client, a refusal or an empty stream. */
  private readonly decided = deferred<void>();

  constructor(
    private readonly host: SessionStreamsHost,
    private readonly lease: AliveLease,
    private readonly onClientEnded: () => void,
  ) {
    this.hasAnswer = !host.answeredOverHttps;
  }

  get hasClient(): boolean {
    return this.foreign !== undefined;
  }

  stream(): AsyncIterable<Message> {
    if (this.claimed) throw new TypeError('An exchange has one stream; call stream() only once');
    this.claimed = true;
    return {
      [Symbol.asyncIterator]: () => {
        if (this.iterated) throw new TypeError('A stream can be iterated only once');
        this.iterated = true;
        return this.messages();
      },
    };
  }

  /** The HTTPS answer of the exchange's request, a `202` included. */
  answer(answer: ManagedAnswer): void {
    this.hasAnswer = true;
    if (answer.stream) this.credentials ??= answer.stream;
    this.answered.resolve();
  }

  streamStarted(started: StreamStarted): void {
    this.started ??= started;
  }

  /** `stream_started` precedes the outcome on the channel, so a completed exchange with none has no stream. */
  async exchangeSettled(outcome: SettledOutcome): Promise<void> {
    this.outcome = outcome;
    try {
      if (outcome.state === 'completed') await this.startClient();
    } catch (err) {
      this.failure = err;
    }
    this.decided.resolve();
  }

  sessionEnded(state: SessionState, failure?: string): void {
    this.over =
      state === 'failed' ? { error: new SessionFailedError(failure ?? 'session_failed') } : {};
    endClient(this.foreign, this.over.error);
    // Ends the wait for an answer that will not matter, and clears its timer.
    this.answered.resolve();
    this.decided.resolve();
  }

  /**
   * Not an async generator: its `return()` would queue behind a read still waiting on a quiet
   * stream, so leaving the loop that way would never stop the client.
   */
  private messages(): AsyncIterator<Message> {
    let live: AsyncIterator<Message> | undefined;
    // Raced once: racing on every read would pile reactions onto the still-pending return signal.
    const startup = Promise.race([this.decided.promise, this.returnedSignal.promise]);
    return {
      next: async () => {
        await startup;
        if (this.returned) return { done: true, value: undefined };
        if (!this.foreign) return this.endWithoutStream();
        live ??= this.foreign.subscription[Symbol.asyncIterator]();
        return live.next();
      },
      return: async () => {
        this.returned = true;
        this.returnedSignal.resolve();
        await this.foreign?.subscription.stop();
        return { done: true, value: undefined };
      },
    };
  }

  private endWithoutStream(): IteratorResult<Message> {
    // The session's end explains whatever else went wrong while it was ending, an aborted fetch included.
    if (this.over) {
      if (this.over.error) throw this.over.error;
    } else {
      if (this.failure) throw this.failure;
      const { outcome } = this;
      if (outcome && outcome.state !== 'completed') {
        throw new SessionStreamError(
          outcome.reason ?? outcome.error ?? outcome.state,
          outcome.state,
        );
      }
    }
    return { done: true, value: undefined };
  }

  private async startClient(): Promise<void> {
    const credentials = await this.credentialsAfterOutcome();
    if (!credentials || this.over || this.returned) return;
    const { sessionId, http, signal } = this.host;
    const foreign = startForeignStream({
      sessionId,
      credentials,
      templateParam: this.started?.templateParam,
      templateValue: this.started?.templateValue,
      refresh: () =>
        withDeadline(Date.now() + STREAM_TOKEN_BUDGET_MS, signal, (bounded) =>
          http.fetchStreamToken(sessionId, credentials.streamId, bounded),
        ),
      onConnectionChange: (connected) => this.lease.connectionChanged(this, connected),
      onEnded: (reason) => this.clientEnded(reason),
    });
    this.foreign = foreign;
    if (foreign.connected) this.lease.connectionChanged(this, true);
  }

  /** The answer's credentials, else the token endpoint's; undefined when the exchange started no stream. */
  private async credentialsAfterOutcome(): Promise<StreamCredentials | undefined> {
    // Only a managed session's exchange answers with credentials or has a token endpoint to ask.
    if (this.started && (this.started.kind !== FOREIGN_SUBSCRIBE || !this.host.answeredOverHttps)) {
      throw new SessionStreamError('stream_kind_unsupported', 'failed');
    }
    // The answer, which may carry the credentials, can trail the channel's outcome.
    if (!this.credentials && !this.hasAnswer) {
      await within(this.answered.promise, SESSION_GAP_RECOVER_MS);
    }
    if (this.credentials) return this.credentials;
    const { started } = this;
    if (!started) return undefined;
    const { sessionId, http, signal } = this.host;
    const until = Date.now() + STREAM_TOKEN_BUDGET_MS;
    try {
      return await retry(
        () =>
          withDeadline(until, signal, (attempt) =>
            http.fetchStreamToken(sessionId, started.streamId, attempt),
          ),
        {
          retryable: (err) =>
            isTransientError(err) || hasCode(err, MANAGED_ERROR_CODES.streamNotFound),
          signal,
          until,
        },
      );
    } catch (err) {
      if (err instanceof SessionError) throw err;
      throw new SessionStreamError('stream_credentials_unavailable', 'failed');
    }
  }

  private clientEnded(reason: StreamEndReason): void {
    this.lease.release(this);
    this.onClientEnded();
    if (reason !== 'access_revoked') return;
    // Ended first, with the cause, so iteration throws it instead of completing as a close would.
    endClient(this.foreign, new SessionFailedError(SESSION_ACCESS_REVOKED));
    this.host.closeSession().catch((err: unknown) => {
      logger('warn', 'closing the session after its stream lost access failed', {
        error: String(err),
      });
    });
  }
}

/** The streams of a session's exchanges: routes what the session learns to the exchange it concerns. */
export class SessionStreams {
  private readonly streams = new Map<string, ExchangeStream>();
  private readonly lease: AliveLease;

  constructor(private readonly host: SessionStreamsHost) {
    this.lease = new AliveLease(host);
  }

  track(exchangeId: string): ExchangeStream {
    const stream = new ExchangeStream(this.host, this.lease, () => this.streams.delete(exchangeId));
    this.streams.set(exchangeId, stream);
    return stream;
  }

  answered(exchangeId: string, answer: ManagedAnswer): void {
    this.streams.get(exchangeId)?.answer(answer);
  }

  streamStarted(started: StreamStarted): void {
    this.streams.get(started.exchangeId)?.streamStarted(started);
  }

  settled(exchangeId: string, outcome: SettledOutcome): void {
    const stream = this.streams.get(exchangeId);
    if (!stream) return;
    // Dropped a tick late: a session ending in the same step as this outcome has yet to be told.
    void stream.exchangeSettled(outcome).then(() => {
      if (!stream.hasClient) this.streams.delete(exchangeId);
    });
  }

  sessionEnded(state: SessionState, failure?: string): void {
    this.lease.stop();
    for (const stream of this.streams.values()) stream.sessionEnded(state, failure);
    this.streams.clear();
  }

  stop(): void {
    this.lease.stop();
  }
}
