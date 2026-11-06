import { createLogger } from '../logger.js';
import { isRateLimited, rateLimitBackoffMs, retryAfterOf } from '../rate-limit.js';
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
  /** A refresh after a denial is in flight or waiting out a rate limit. */
  private denialRefreshing = false;
  private denialTimer: ReturnType<typeof setTimeout> | undefined;
  private denial = 0;
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
    clearTimeout(this.denialTimer);
  }

  onConnected(): void {
    this.awaitingReconnect = false;
  }

  onDenied(): void {
    if (this.stopped || this.denialRefreshing) return;
    if (this.awaitingReconnect) {
      this.revoke();
      return;
    }
    this.denialRefreshing = true;
    void this.refreshAfterDenial(++this.denial, 0);
  }

  /** `denial` names the denial this refresh answers; a later recovery or denial supersedes it. */
  private async refreshAfterDenial(denial: number, rateLimitedAttempts: number): Promise<void> {
    let token: SessionToken;
    try {
      token = await this.options.refresh();
    } catch (err) {
      if (this.stopped || denial !== this.denial) return;
      // A rate-limited refresh is a wait, not a refusal: the denial still gets its one real answer.
      if (isRateLimited(err)) {
        const delayMs = rateLimitBackoffMs(rateLimitedAttempts, retryAfterOf(err));
        logger('warn', 'session token refresh after a denial was rate limited; retrying', {
          sessionId: this.options.sessionId,
          retryInMs: delayMs,
        });
        // Rotation would call the same endpoint inside the wait; a successful refresh reschedules it.
        clearTimeout(this.timer);
        clearTimeout(this.denialTimer);
        this.denialTimer = setTimeout(
          () => void this.refreshAfterDenial(denial, rateLimitedAttempts + 1),
          delayMs,
        );
        this.denialTimer.unref?.();
        return;
      }
      this.denialRefreshing = false;
      logger('warn', 'session token refresh after a denial failed', {
        sessionId: this.options.sessionId,
        error: String(err),
      });
      this.revoke();
      return;
    }
    if (this.stopped) return;
    // A rotation that landed meanwhile already resumed the subscription.
    if (denial === this.denial && this.denialRefreshing) this.recoverFromDenial(token);
    else this.apply(token);
  }

  private recoverFromDenial(token: SessionToken): void {
    this.denial++;
    clearTimeout(this.denialTimer);
    this.denialRefreshing = false;
    this.apply(token);
    this.awaitingReconnect = true;
    this.options.resume();
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
      if (this.stopped) return;
      // Any fresh token answers a pending denial, whichever request brought it.
      if (this.denialRefreshing) this.recoverFromDenial(token);
      else this.apply(token);
    } catch (err) {
      // While a denial refresh is pending it owns the retries, and their rate-limit wait.
      if (this.stopped || this.denialRefreshing) return;
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
