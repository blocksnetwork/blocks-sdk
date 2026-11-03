import type PubNub from 'pubnub';
import type { ArtifactEntry, RequestPart } from '../agent-instance.js';
import { buildArtifactRef, downloadArtifact, shouldInlineArtifact } from '../artifacts.js';
import { DEFAULTS } from '../../defaults.js';
import { createLogger } from '../logger.js';
import { CURRENT_PROTOCOL_VERSION } from '../protocol-version.js';
import { toPayload, type JsonValue } from '../pubnub-types.js';
import type { SessionManifest } from './provider-api.js';
import type { CheckpointEvent } from './provider-checkpoints.js';
import { ProviderSessionReceiver } from './provider-receiver.js';
import type {
  ExchangeContext,
  PublishArtifactOptions,
  SessionContext,
  SessionProviderOptions,
} from './provider-types.js';
import type { ExchangeOutcomeState, StartedExchange } from './types.js';

const logger = createLogger('SessionProvider');

/** In flight plus queued, per session. */
export const MAX_PENDING_EXCHANGES = 8;
/** Settled `exchangeId`s remembered per session, so a re-sent exchange is not run twice. */
export const MAX_SETTLED_EXCHANGE_IDS = 256;
export const STATUS_THROTTLE_MS = 1_000;
/** Matches the longest ready wait a consumer may set, so no consumer is still waiting when it expires. */
export const SESSION_OPEN_TIMEOUT_MS = 120_000;
const PUBLISH_ATTEMPTS = 2;

export type SessionHandlers = SessionProviderOptions &
  Required<Pick<SessionProviderOptions, 'onExchange'>>;

export interface ProviderSessionOptions {
  manifest: SessionManifest;
  /** The session channel as delivered on the route subscription. */
  channel: string;
  backendUuid: string | undefined;
  pubnub: PubNub;
  handlers: SessionHandlers;
  record(event: CheckpointEvent): void;
  onEnded(): void;
}

class StatusThrottle {
  private last = 0;
  private pending: string | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(private readonly emit: (message: string) => void) {}

  report(message: string): void {
    const wait = this.last + STATUS_THROTTLE_MS - Date.now();
    if (wait <= 0 && this.timer === undefined) {
      this.publish(message);
      return;
    }
    this.pending = message;
    this.timer ??= setTimeout(() => this.flush(), Math.max(wait, 0));
  }

  flush(): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    if (this.pending === undefined) return;
    const message = this.pending;
    this.pending = undefined;
    this.publish(message);
  }

  discard(): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    this.pending = undefined;
  }

  private publish(message: string): void {
    this.last = Date.now();
    this.emit(message);
  }
}

interface RunningExchange {
  readonly exchangeId: string;
  readonly controller: AbortController;
  readonly status: StatusThrottle;
  settled: boolean;
}

export class ProviderSession {
  readonly ctx: SessionContext;
  private readonly receiver: ProviderSessionReceiver;
  private readonly queue: StartedExchange[] = [];
  private readonly settledIds = new Set<string>();
  private readonly outcomesInFlight = new Set<Promise<boolean>>();
  private current: RunningExchange | undefined;
  private seq = 0;
  private outbox: Promise<boolean> = Promise.resolve(true);
  private ready = false;
  private openPending = false;
  private openTimer: ReturnType<typeof setTimeout> | undefined;
  private refusalsInFlight = 0;
  private deferredClose: string | undefined;
  private openReturned = false;
  private endingState = false;
  private silenced = false;
  private closeHandled = false;
  private finished = false;
  private endReason = '';

  constructor(private readonly options: ProviderSessionOptions) {
    const { manifest } = options;
    this.ctx = {
      sessionId: manifest.sessionId,
      agentName: manifest.agentName,
      orgId: manifest.orgId,
      ownerId: manifest.ownerId,
      callerClaims: manifest.callerClaims,
      close: () => this.close('provider_closed'),
    };
    this.receiver = new ProviderSessionReceiver({
      parties: {
        sessionId: manifest.sessionId,
        counterpartUuid: manifest.consumerTransportUuid,
        backendUuid: options.backendUuid ?? '',
      },
      events: {
        onSessionOpen: () => void this.open(),
        onExchange: (exchange) => this.enqueue(exchange),
        onExchangeCancel: (exchangeId) => this.cancelExchange(exchangeId),
        onCloseRequested: () => void this.close('consumer_closed'),
        onBackendNotice: (state, detail) => void this.endLocally(detail ?? state, state),
      },
    });
  }

  get sessionId(): string {
    return this.ctx.sessionId;
  }

  /** Ending sessions still receive (a backend notice during closing must land) but hold no seat. */
  get ending(): boolean {
    return this.endingState;
  }

  receive(publisher: string, message: unknown): boolean {
    return this.receiver.receive(publisher, message);
  }

  rejectAtCapacity(): Promise<void> {
    return this.fail('provider_at_capacity');
  }

  async endLocally(reason: string, state: 'closed' | 'failed' = 'failed'): Promise<void> {
    if (this.silenced) return;
    this.endingState = true;
    this.silenced = true;
    this.endReason = reason;
    this.queue.length = 0;
    if (this.current) {
      this.current.settled = true;
      this.current.status.discard();
      this.current.controller.abort();
    }
    this.receiver.terminate(state);
    await this.runSessionClose(reason);
    this.finish();
  }

  private async open(): Promise<void> {
    this.openPending = true;
    // A close is deferred while the handler runs, so one that never settles would hold the seat.
    this.openTimer = setTimeout(() => {
      logger('error', 'onSessionOpen did not return in time; failing the session', {
        sessionId: this.sessionId,
        timeoutMs: SESSION_OPEN_TIMEOUT_MS,
      });
      void this.fail('provider_error');
    }, SESSION_OPEN_TIMEOUT_MS);
    try {
      await this.options.handlers.onSessionOpen?.(this.ctx);
    } catch (err) {
      clearTimeout(this.openTimer);
      this.openPending = false;
      logger('error', 'onSessionOpen threw; failing the session', {
        sessionId: this.sessionId,
        error: String(err),
      });
      await this.fail('provider_error');
      return;
    }
    clearTimeout(this.openTimer);
    this.openPending = false;
    this.openReturned = true;
    // Checked before a deferred close: a backend notice or the open timeout may have ended the
    // session meanwhile, and close() does nothing once it has, so the close handler would never run.
    if (this.endingState) {
      await this.runSessionClose(this.endReason);
      return;
    }
    if (this.deferredClose !== undefined) {
      await this.close(this.deferredClose);
      return;
    }
    if (!(await this.send('session_ready', {}))) {
      await this.fail('provider_error');
      return;
    }
    this.record({ type: 'session_ready', sessionId: this.sessionId });
    this.ready = true;
    this.pump();
  }

  private async fail(error: string): Promise<void> {
    if (this.endingState) return;
    this.endingState = true;
    this.endReason = error;
    // Exchanges that never started are the consumer's to settle `failed` with the session's error.
    if (!this.ready) this.queue.length = 0;
    await this.completeFail(error);
  }

  private async completeFail(error: string): Promise<void> {
    await this.cancelAll();
    await this.runSessionClose(error);
    await this.send('session_failed', { error });
    this.record({ type: 'session_failed', sessionId: this.sessionId, error });
    this.receiver.terminate('failed');
    this.finish();
  }

  private async close(reason: string): Promise<void> {
    if (this.endingState) return;
    // onSessionClose must follow a returned onSessionOpen, so a close requested meanwhile waits for it.
    if (this.openPending) {
      this.deferredClose ??= reason;
      return;
    }
    this.endingState = true;
    this.endReason = reason;
    if (!(await this.cancelAll())) {
      // An outcome the consumer never got makes this a failure, not a close.
      this.endReason = 'provider_error';
      await this.completeFail('provider_error');
      return;
    }
    await this.runSessionClose(reason);
    await this.send('session_closed', { reason });
    this.record({ type: 'session_closed', sessionId: this.sessionId, reason });
    this.receiver.terminate('closed');
    this.finish();
  }

  private enqueue(exchange: StartedExchange): void {
    if (this.endingState) return;
    if (this.isKnownExchange(exchange.exchangeId)) {
      logger('warn', 'dropping a re-sent exchange', {
        sessionId: this.sessionId,
        exchangeId: exchange.exchangeId,
      });
      return;
    }
    const pending = this.queue.length + (this.current ? 1 : 0);
    if (pending >= MAX_PENDING_EXCHANGES) {
      this.refuse(exchange.exchangeId);
      return;
    }
    this.queue.push(exchange);
    this.pump();
  }

  private isKnownExchange(exchangeId: string): boolean {
    return (
      this.current?.exchangeId === exchangeId ||
      this.queue.some((queued) => queued.exchangeId === exchangeId) ||
      this.settledIds.has(exchangeId)
    );
  }

  /** Called before the outcome publishes, so a duplicate arriving meanwhile is already known. */
  private rememberSettled(exchangeId: string): void {
    this.settledIds.add(exchangeId);
    if (this.settledIds.size <= MAX_SETTLED_EXCHANGE_IDS) return;
    const [oldest] = this.settledIds;
    this.settledIds.delete(oldest);
  }

  /**
   * A conforming consumer never draws a refusal, so one that outruns the publishes refusing it is
   * failed rather than left to grow the outbox. Dropping refusals instead would leave exchanges
   * with no outcome; failing settles them on the consumer through the session's terminal state.
   * The error is `provider_error` because the backend records no other session error for this.
   */
  private refuse(exchangeId: string): void {
    if (this.refusalsInFlight >= MAX_PENDING_EXCHANGES) {
      logger('warn', 'consumer keeps exceeding the pending-exchange bound; failing the session', {
        sessionId: this.sessionId,
      });
      void this.fail('provider_error');
      return;
    }
    this.refusalsInFlight++;
    void this.publishOutcome(exchangeId, 'failed', 'session_backpressure').finally(() => {
      this.refusalsInFlight--;
    });
  }

  private pump(): void {
    if (!this.ready || this.endingState || this.current) return;
    const next = this.queue.shift();
    if (!next) return;
    void this.run(next).finally(() => {
      this.current = undefined;
      this.pump();
    });
  }

  private cancelExchange(exchangeId: string): void {
    if (this.current?.exchangeId === exchangeId) {
      this.current.controller.abort();
      return;
    }
    const index = this.queue.findIndex((queued) => queued.exchangeId === exchangeId);
    if (index < 0) return;
    this.queue.splice(index, 1);
    void this.publishOutcome(exchangeId, 'canceled', undefined);
  }

  /**
   * Settles the in-flight exchange and every queued one as `canceled`, without waiting for handlers.
   * False when any outcome, including one already publishing, was not delivered.
   */
  private async cancelAll(): Promise<boolean> {
    const settling = [...this.outcomesInFlight];
    if (this.current) {
      this.current.controller.abort();
      settling.push(this.settle(this.current, 'canceled'));
    }
    for (const queued of this.queue.splice(0)) {
      settling.push(this.publishOutcome(queued.exchangeId, 'canceled', undefined));
    }
    return (await Promise.all(settling)).every(Boolean);
  }

  private async run(exchange: StartedExchange): Promise<void> {
    const running: RunningExchange = {
      exchangeId: exchange.exchangeId,
      controller: new AbortController(),
      status: new StatusThrottle((message) => {
        void this.send('exchange_progress', { exchangeId: exchange.exchangeId, message });
      }),
      settled: false,
    };
    this.current = running;
    const context = this.exchangeContext(running, exchange.requestParts as RequestPart[]);
    try {
      const result = await this.options.handlers.onExchange(this.ctx, context);
      if (running.controller.signal.aborted) {
        await this.settle(running, 'canceled');
        return;
      }
      for (const artifact of result?.artifacts ?? []) {
        if (running.controller.signal.aborted) break;
        await this.publishArtifact(running, artifact);
      }
      await this.settle(running, running.controller.signal.aborted ? 'canceled' : 'completed');
    } catch (err) {
      if (running.controller.signal.aborted) {
        await this.settle(running, 'canceled');
        return;
      }
      logger('error', 'onExchange threw', {
        sessionId: this.sessionId,
        exchangeId: running.exchangeId,
        error: String(err),
      });
      await this.settle(running, 'failed', 'handler_error');
    }
  }

  private async settle(
    running: RunningExchange,
    state: ExchangeOutcomeState,
    error?: string,
  ): Promise<boolean> {
    // An exchange settled earlier has its outcome in `outcomesInFlight` until it is delivered.
    if (running.settled) return true;
    running.settled = true;
    if (state === 'canceled') running.status.discard();
    else running.status.flush();
    return this.publishOutcome(running.exchangeId, state, error);
  }

  private async publishOutcome(
    exchangeId: string,
    state: ExchangeOutcomeState,
    error: string | undefined,
  ): Promise<boolean> {
    this.rememberSettled(exchangeId);
    const sent = this.send('exchange_outcome', { exchangeId, state, ...(error ? { error } : {}) });
    this.outcomesInFlight.add(sent);
    const delivered = await sent;
    this.outcomesInFlight.delete(sent);
    if (!delivered) {
      // Recording an outcome the consumer never got would settle it in the record alone.
      void this.fail('provider_error');
      return false;
    }
    this.record({
      type: 'exchange_outcome',
      sessionId: this.sessionId,
      exchangeId,
      state,
      ...(error ? { error } : {}),
      artifactRefs: [],
    });
    return true;
  }

  private exchangeContext(running: RunningExchange, requestParts: RequestPart[]): ExchangeContext {
    const { signal } = running.controller;
    return {
      exchangeId: running.exchangeId,
      requestParts,
      cancelSignal: signal,
      get isCancelled() {
        return signal.aborted;
      },
      reportStatus: (message) => {
        if (!running.settled) running.status.report(message);
      },
      publishArtifact: async (data, options?: PublishArtifactOptions) => {
        if (running.settled) throw new Error(`exchange ${running.exchangeId} has already settled`);
        await this.publishArtifact(running, {
          ...options,
          data,
          mimeType: options?.mimeType ?? 'application/octet-stream',
        });
      },
      downloadInputArtifact: async (part) => {
        if (!part.artifactRef) {
          throw new Error(`Request part has no artifactRef (partId: ${part.partId ?? 'none'})`);
        }
        const { data } = await downloadArtifact(part.artifactRef, this.options.pubnub);
        return Buffer.from(data);
      },
    };
  }

  private async publishArtifact(running: RunningExchange, artifact: ArtifactEntry): Promise<void> {
    const data =
      typeof artifact.data === 'string' ? Buffer.from(artifact.data, 'utf-8') : artifact.data;
    const { mimeType, fileName, outputId } = artifact;
    if (!shouldInlineArtifact(data.length)) {
      throw new Error(
        `session artifacts are limited to ${DEFAULTS.inlineLimitBytes} bytes; got ${data.length}`,
      );
    }
    if (running.settled) return;
    const delivered = await this.send('exchange_artifact', {
      exchangeId: running.exchangeId,
      artifactRef: buildArtifactRef({ data, mimeType, fileName }) as unknown as JsonValue,
      ...(outputId ? { outputId } : {}),
    });
    if (!delivered) throw new Error(`artifact publish failed for exchange ${running.exchangeId}`);
  }

  /** Publishes in call order; `seq` is assigned when the publish is requested. Resolves true once delivered. */
  private send(type: string, fields: Record<string, JsonValue>): Promise<boolean> {
    if (this.silenced) return Promise.resolve(false);
    const { channel, pubnub } = this.options;
    const message = {
      type,
      protocolVersion: CURRENT_PROTOCOL_VERSION,
      sessionId: this.sessionId,
      seq: ++this.seq,
      ts: Date.now(),
      ...fields,
    };
    const meta = { sessionId: this.sessionId, protocolVersion: CURRENT_PROTOCOL_VERSION };
    const publish = () =>
      pubnub.publish({
        channel,
        message: toPayload(message),
        meta: toPayload(meta),
        storeInHistory: true,
        sendByPost: true,
      });
    this.outbox = this.outbox.then(async () => {
      for (let attempt = 1; ; attempt++) {
        // Rechecked before every attempt: a backend notice may have ended the session while this
        // publish waited behind a slow one, or while its previous attempt was failing.
        if (this.silenced) return false;
        try {
          await publish();
          return true;
        } catch (err) {
          if (attempt < PUBLISH_ATTEMPTS) continue;
          logger('warn', 'session publish failed', {
            sessionId: this.sessionId,
            type,
            seq: message.seq,
            error: String(err),
          });
          return false;
        }
      }
    });
    return this.outbox;
  }

  private record(event: CheckpointEvent): void {
    if (!this.silenced) this.options.record(event);
  }

  private async runSessionClose(reason: string): Promise<void> {
    if (!this.openReturned || this.closeHandled) return;
    this.closeHandled = true;
    try {
      await this.options.handlers.onSessionClose?.(this.ctx, reason);
    } catch (err) {
      logger('error', 'onSessionClose threw', { sessionId: this.sessionId, error: String(err) });
    }
  }

  private finish(): void {
    if (this.finished) return;
    this.finished = true;
    this.silenced = true;
    clearTimeout(this.openTimer);
    this.options.onEnded();
  }
}
