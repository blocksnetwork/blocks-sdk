import type { ArtifactRef } from '../artifacts.js';
import { createLogger } from '../logger.js';
import { retryAfterOf } from '../rate-limit.js';
import {
  ProviderApiError,
  ROUTE_LOST,
  errorCodeOf,
  isCredentialRefusal,
} from './provider-api.js';
import type { ExchangeOutcomeState } from './types.js';

const logger = createLogger('SessionProvider');

export const CHECKPOINT_FLUSH_MS = 2_000;
export const CHECKPOINT_MAX_EVENTS = 64;
export const CHECKPOINT_MAX_OPEN_SESSIONS = 256;
const CHECKPOINT_MAX_PROBE_ECHOES = 256;
const CHECKPOINT_MAX_ID_LENGTH = 256;
/** Malformed (400) or oversized (413): the backend refuses the batch the same way on every replay. */
const UNREPLAYABLE_STATUSES: ReadonlySet<number> = new Set([400, 413]);

type Delivery = 'acknowledged' | 'dropped' | 'retry';

/** Lifecycle facts only: never request parts or inline artifact data. */
export type CheckpointEvent =
  | { type: 'session_ready'; sessionId: string }
  | {
      type: 'exchange_outcome';
      sessionId: string;
      exchangeId: string;
      state: ExchangeOutcomeState;
      error?: string;
      artifactRefs: ArtifactRef[];
    }
  | { type: 'session_closed'; sessionId: string; reason: string }
  | { type: 'session_failed'; sessionId: string; error: string };

export interface CheckpointBatch {
  routeId: string;
  idempotencyKey: string;
  openSessions: string[];
  events: CheckpointEvent[];
  probeEchoes: string[];
}

export interface CheckpointLoopOptions {
  routeId: string;
  post(batch: CheckpointBatch): Promise<void>;
  openSessions(): string[];
  /** The backend failed the route, or refused the agent credential; the loop has stopped. */
  onRouteLost(): void;
}

export class CheckpointLoop {
  private pending: CheckpointEvent[] = [];
  private echoes: string[] = [];
  private readonly unacked: CheckpointBatch[] = [];
  private timer: ReturnType<typeof setInterval> | undefined;
  private flushing: Promise<void> | undefined;
  private flushAgain = false;
  private stopped = false;
  /** No batch is posted before this time: the last failure carried a Retry-After. */
  private heldUntil = 0;

  constructor(private readonly options: CheckpointLoopOptions) {}

  start(): void {
    this.timer = setInterval(() => void this.flush(), CHECKPOINT_FLUSH_MS);
    this.timer.unref?.();
  }

  stop(): void {
    this.stopped = true;
    clearInterval(this.timer);
  }

  record(event: CheckpointEvent): void {
    if (this.stopped) return;
    this.pending.push(event);
    // A session's end is sent at once: it is what frees the session's seat for a new open.
    const ends = event.type === 'session_closed' || event.type === 'session_failed';
    if (ends || this.pending.length >= CHECKPOINT_MAX_EVENTS) void this.flush();
  }

  /** Echoed in the next batch; a nonce the backend could not accept is dropped. */
  echoProbe(nonce: unknown): void {
    if (this.stopped || typeof nonce !== 'string') return;
    if (nonce.length === 0 || nonce.length > CHECKPOINT_MAX_ID_LENGTH) return;
    if (this.echoes.includes(nonce) || this.echoes.length >= CHECKPOINT_MAX_PROBE_ECHOES) return;
    this.echoes.push(nonce);
  }

  flush(): Promise<void> {
    if (this.flushing) {
      this.flushAgain = true;
      return this.flushing;
    }
    this.flushing = this.drain().finally(() => {
      this.flushing = undefined;
    });
    return this.flushing;
  }

  private async drain(): Promise<void> {
    do {
      this.flushAgain = false;
      this.cutBatches();
      await this.sendInOrder();
    } while (this.flushAgain && !this.stopped);
  }

  // A batch carrying echoes is retained like one carrying events: a lost echo is a probe miss.
  private cutBatches(): void {
    while (this.pending.length > 0 || this.echoes.length > 0) {
      this.unacked.push(
        this.batch(this.pending.splice(0, CHECKPOINT_MAX_EVENTS), this.echoes.splice(0)),
      );
    }
  }

  private async sendInOrder(): Promise<void> {
    if (this.unacked.length === 0) {
      // A failed heartbeat is not retained: the next tick sends a fresh one.
      await this.deliver(this.batch([]));
      return;
    }
    while (this.unacked.length > 0 && !this.stopped) {
      if ((await this.deliver(this.unacked[0])) === 'retry') return;
      this.unacked.shift();
    }
  }

  private async deliver(batch: CheckpointBatch): Promise<Delivery> {
    if (this.stopped || Date.now() < this.heldUntil) return 'retry';
    try {
      await this.options.post(batch);
      return 'acknowledged';
    } catch (err) {
      const credentialRefused = isCredentialRefusal(err);
      if (credentialRefused || errorCodeOf(err) === ROUTE_LOST) {
        // Retrying a refused credential every tick is a storm; re-registering backs off.
        if (credentialRefused) {
          logger('error', 'agent credential refused; ending the route', {
            routeId: this.options.routeId,
            error: String(err),
          });
        }
        this.stop();
        this.options.onRouteLost();
        return 'retry';
      }
      if (err instanceof ProviderApiError && UNREPLAYABLE_STATUSES.has(err.status)) {
        logger('error', 'checkpoint batch refused; dropping it', {
          routeId: this.options.routeId,
          idempotencyKey: batch.idempotencyKey,
          error: String(err),
        });
        return 'dropped';
      }
      const retryAfterMs = retryAfterOf(err);
      if (retryAfterMs !== undefined) this.heldUntil = Date.now() + retryAfterMs;
      logger('warn', 'checkpoint failed; retrying on the next tick', {
        routeId: this.options.routeId,
        retained: this.unacked.length,
        error: String(err),
      });
      return 'retry';
    }
  }

  private batch(events: CheckpointEvent[], probeEchoes: string[] = []): CheckpointBatch {
    return {
      routeId: this.options.routeId,
      idempotencyKey: crypto.randomUUID(),
      openSessions: this.options.openSessions(),
      events,
      probeEchoes,
    };
  }
}
