import { createLogger } from '../logger.js';
import type { SendMessageRequestPart } from '../task-client.js';
import { SESSION_GAP_RECOVER_MS } from './in-order-delivery.js';
import {
  isBusy,
  isTransientError,
  hasCode,
  pause,
  backoffDelay,
  refusesAccess,
  retry,
  withDeadline,
} from './managed-retry.js';
import { MANAGED_ERROR_CODES } from './session-errors.js';
import { answersFinal, type ManagedAnswer, type SessionHttp } from './session-http.js';

const logger = createLogger('Session');

/** How long one exchange is retried before it settles `failed` with `publish_failed`; also bounds a cancel. */
const EXCHANGE_SEND_BUDGET_MS = 30_000;

export interface ManagedExchangeRequest {
  exchangeId: string;
  requestParts: SendMessageRequestPart[];
  /** Called with the HTTPS answer, a `202` included. */
  onAnswer?(answer: ManagedAnswer): void;
}

export interface ManagedExchangesHost {
  sessionId: string;
  http: Pick<SessionHttp, 'postExchange' | 'cancelExchange'>;
  signal: AbortSignal;
  isPending(exchangeId: string): boolean;
  rejectExchange(exchangeId: string): void;
  cancelLocally(exchangeId: string): void;
  revokeAccess(): void;
  recover(): void;
}

/** `requesting` spans retries after a transport error, as the backend may have seen the request. */
type Phase = 'waiting' | 'requesting' | 'busy' | 'answered';

interface QueuedExchange extends ManagedExchangeRequest {
  phase: Phase;
  abort: AbortController;
  /** Set by `cancel()`, so a `SESSION_BUSY` answer settles the exchange instead of retrying it. */
  cancelRequested: boolean;
}

/** One request in flight, in order; a `202` holds the slot until the channel outcome arrives. */
export class ManagedExchanges {
  private readonly queue: QueuedExchange[] = [];
  private pumping = false;
  private stopped = false;
  /** Exchanges carried by an `opened` beacon that has not answered. */
  private readonly beacons = new Set<string>();
  private readonly outcomeWaiters = new Map<string, () => void>();
  private readonly recoverTimers = new Map<string, ReturnType<typeof setTimeout>>();

  constructor(private readonly host: ManagedExchangesHost) {}

  enqueue(request: ManagedExchangeRequest): void {
    if (this.stopped) return;
    this.queue.push({ ...request, phase: 'waiting', abort: new AbortController(), cancelRequested: false });
    void this.pump();
  }

  beginBeacon(exchangeId: string): void {
    this.beacons.add(exchangeId);
  }

  endBeacon(exchangeId: string): void {
    this.beacons.delete(exchangeId);
  }

  /** A final answer is not the outcome; if the channel never delivers it no `seq` gap opens, so recovery asks. */
  noteAnswer(exchangeId: string, answer: ManagedAnswer): void {
    if (!answersFinal(answer) || this.stopped || !this.host.isPending(exchangeId)) return;
    this.recoverTimers.set(
      exchangeId,
      setTimeout(() => {
        this.recoverTimers.delete(exchangeId);
        if (this.host.isPending(exchangeId)) this.host.recover();
      }, SESSION_GAP_RECOVER_MS),
    );
  }

  settled(exchangeId: string): void {
    clearTimeout(this.recoverTimers.get(exchangeId));
    this.recoverTimers.delete(exchangeId);
    this.outcomeWaiters.get(exchangeId)?.();
  }

  /**
   * An unsent or `SESSION_BUSY` exchange settles locally, and one whose request answers
   * `SESSION_BUSY` later does too. A `404` to a cancel sent while the request was unanswered is
   * resent after backoff, as the request's answer may since have left the exchange running.
   */
  async cancel(exchangeId: string): Promise<void> {
    const { sessionId, http, signal } = this.host;
    const queued = this.queue.find((q) => q.exchangeId === exchangeId);
    if (queued) queued.cancelRequested = true;
    const until = Date.now() + EXCHANGE_SEND_BUDGET_MS;
    try {
      for (let attempt = 0; ; attempt++) {
        if (this.cancelUnsent(exchangeId)) return;
        const unansweredWhenSent = this.isUnanswered(exchangeId);
        try {
          await withDeadline(until, signal, (bounded) =>
            http.cancelExchange(sessionId, exchangeId, bounded),
          );
          return;
        } catch (err) {
          if (!unansweredWhenSent || !hasCode(err, MANAGED_ERROR_CODES.exchangeNotFound)) throw err;
        }
        await pause(backoffDelay(attempt), signal);
        if (!this.host.isPending(exchangeId)) return;
      }
    } catch (err) {
      logger('debug', 'exchange cancel request failed', { exchangeId, error: String(err) });
    }
  }

  stop(): void {
    this.stopped = true;
    for (const queued of this.queue.splice(0)) queued.abort.abort();
    for (const timer of this.recoverTimers.values()) clearTimeout(timer);
    this.recoverTimers.clear();
    for (const wake of [...this.outcomeWaiters.values()]) wake();
  }

  private dequeue(queued: QueuedExchange): void {
    const index = this.queue.indexOf(queued);
    if (index >= 0) this.queue.splice(index, 1);
    queued.abort.abort();
  }

  private isUnanswered(exchangeId: string): boolean {
    return (
      this.beacons.has(exchangeId) ||
      this.queue.find((q) => q.exchangeId === exchangeId)?.phase === 'requesting'
    );
  }

  private cancelUnsent(exchangeId: string): boolean {
    const queued = this.queue.find((q) => q.exchangeId === exchangeId);
    if (!queued || (queued.phase !== 'waiting' && queued.phase !== 'busy')) return false;
    this.dequeue(queued);
    this.host.cancelLocally(exchangeId);
    return true;
  }

  private async pump(): Promise<void> {
    if (this.pumping) return;
    this.pumping = true;
    try {
      for (let head = this.queue[0]; head && !this.stopped; head = this.queue[0]) {
        await this.sendHead(head);
        const index = this.queue.indexOf(head);
        if (index >= 0) this.queue.splice(index, 1);
      }
    } finally {
      this.pumping = false;
    }
  }

  private async sendHead(item: QueuedExchange): Promise<void> {
    const { exchangeId } = item;
    try {
      const answer = await this.post(item);
      item.phase = 'answered';
      item.onAnswer?.(answer);
      this.noteAnswer(exchangeId, answer);
      if (!answersFinal(answer)) await this.untilSettled(exchangeId);
    } catch (err) {
      if (item.abort.signal.aborted) return;
      logger('warn', 'exchange send failed', { exchangeId, error: String(err) });
      if (refusesAccess(err)) this.host.revokeAccess();
      else this.host.rejectExchange(exchangeId);
    }
  }

  private post(item: QueuedExchange): Promise<ManagedAnswer> {
    const { sessionId, http } = this.host;
    const { exchangeId, requestParts } = item;
    const until = Date.now() + EXCHANGE_SEND_BUDGET_MS;
    return retry(
      async () => {
        item.phase = 'requesting';
        try {
          return await withDeadline(until, item.abort.signal, (attempt) =>
            http.postExchange(sessionId, { exchangeId, requestParts }, attempt),
          );
        } catch (err) {
          if (isBusy(err)) {
            item.phase = 'busy';
            if (item.cancelRequested) this.cancelUnsent(item.exchangeId);
          }
          throw err;
        }
      },
      {
        retryable: (err) => isTransientError(err) || isBusy(err),
        signal: item.abort.signal,
        until,
      },
    );
  }

  private untilSettled(exchangeId: string): Promise<void> {
    if (!this.host.isPending(exchangeId) || this.stopped) return Promise.resolve();
    return new Promise((resolve) => {
      this.outcomeWaiters.set(exchangeId, () => {
        this.outcomeWaiters.delete(exchangeId);
        resolve();
      });
    });
  }
}
