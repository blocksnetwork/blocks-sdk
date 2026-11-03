import { createLogger } from '../logger.js';
import { retryAfterOf } from '../rate-limit.js';
import { isRefusal, type SessionToken } from './session-http.js';
import { TOKEN_REFRESH_AT, TOKEN_RETRY_MAX_MS, TOKEN_RETRY_MIN_MS } from './token-schedule.js';

const logger = createLogger('Session');

export interface ConsumerTokenOptions {
  sessionId: string;
  refresh(): Promise<SessionToken>;
  setToken(pamToken: string): void;
  /** Resumes the subscription PubNub parked on the denial. */
  resume(): void;
  /** The one refresh after a denial failed. */
  onRevoked(): void;
}

/** Rotates a consumer session token on TTL, and refreshes it once when PubNub denies it. */
export class ConsumerTokenRotation {
  private timer: ReturnType<typeof setTimeout> | undefined;
  private retryMs = TOKEN_RETRY_MIN_MS;
  private expiresAt = 0;
  private denialRefresh?: Promise<void>;
  /** A denial refresh succeeded and the subscription has not reconnected since. */
  private awaitingReconnect = false;
  private stopped = false;

  constructor(private readonly options: ConsumerTokenOptions) {}

  start(ttlMinutes: number): void {
    this.schedule(ttlMinutes);
  }

  stop(): void {
    this.stopped = true;
    clearTimeout(this.timer);
  }

  onConnected(): void {
    this.awaitingReconnect = false;
  }

  onDenied(): void {
    if (this.stopped || this.denialRefresh) return;
    if (this.awaitingReconnect) {
      this.revoke();
      return;
    }
    this.denialRefresh = this.options
      .refresh()
      .then(
        (token) => {
          if (this.stopped) return;
          this.apply(token);
          this.awaitingReconnect = true;
          this.options.resume();
        },
        (err: unknown) => {
          if (this.stopped) return;
          logger('warn', 'session token refresh after a denial failed', {
            sessionId: this.options.sessionId,
            error: String(err),
          });
          this.revoke();
        },
      )
      .finally(() => {
        this.denialRefresh = undefined;
      });
  }

  private revoke(): void {
    this.stop();
    this.options.onRevoked();
  }

  private apply(token: SessionToken): void {
    this.options.setToken(token.pamToken);
    this.retryMs = TOKEN_RETRY_MIN_MS;
    this.schedule(token.ttlMinutes);
  }

  private schedule(ttlMinutes: number): void {
    const ttlMs = ttlMinutes * 60_000;
    this.expiresAt = Date.now() + ttlMs;
    this.setTimer(ttlMs * TOKEN_REFRESH_AT);
  }

  private setTimer(delayMs: number): void {
    clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.rotate(), delayMs);
    this.timer.unref?.();
  }

  private async rotate(): Promise<void> {
    try {
      const token = await this.options.refresh();
      if (!this.stopped) this.apply(token);
    } catch (err) {
      if (this.stopped) return;
      // A refusal is final: the token lapses or is revoked, and the denial then ends the session.
      const delayMs = Math.max(this.retryMs, retryAfterOf(err) ?? 0);
      const retry = !isRefusal(err) && Date.now() + delayMs < this.expiresAt;
      const message = retry ? 'retrying' : 'giving up';
      logger('warn', `session token refresh failed; ${message}`, {
        sessionId: this.options.sessionId,
        error: String(err),
      });
      if (!retry) return;
      this.setTimer(delayMs);
      this.retryMs = Math.min(this.retryMs * 2, TOKEN_RETRY_MAX_MS);
    }
  }
}
