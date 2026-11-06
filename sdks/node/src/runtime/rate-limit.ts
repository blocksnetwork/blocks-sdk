/**
 * Rate-limit handling shared by every API-key-authenticated call: the backend
 * answers an over-limit key with HTTP 429 and a `Retry-After` header. A 429 is
 * retryable, never fatal.
 */

export const HTTP_TOO_MANY_REQUESTS = 429;

/** The longest wait honored from one `Retry-After`: the backend's per-key limit window. */
export const RETRY_AFTER_MAX_MS = 3_600_000;

/** Used when a 429 carries no usable `Retry-After`: 30 s, doubling per consecutive 429, to 10 min. */
export const RATE_LIMIT_BACKOFF_BASE_MS = 30_000;
export const RATE_LIMIT_BACKOFF_MAX_MS = 600_000;
/** A zero or past `Retry-After` must not turn a retry into a busy loop. */
export const RATE_LIMIT_MIN_WAIT_MS = 1_000;
/** How long a `RateLimitHold` holds after a 429 that carries no usable `Retry-After`. */
export const RATE_LIMIT_NO_HINT_HOLD_MS = 30_000;
/** A connect retry waits up to this fraction longer, so instances sharing a key spread out. */
export const RATE_LIMIT_JITTER_FRACTION = 0.1;

/**
 * Parses a `Retry-After` value, delta-seconds or HTTP-date, into milliseconds
 * capped at `RETRY_AFTER_MAX_MS`. Returns undefined when absent or unparseable.
 */
export function parseRetryAfter(
  value: string | null | undefined,
  now: number = Date.now(),
): number | undefined {
  const text = value?.trim();
  if (!text) return undefined;
  let ms: number;
  if (/^\d+$/.test(text)) {
    ms = Number(text) * 1000;
  } else {
    // A date always names its month or weekday; without this, `Date.parse` reads "-5" as a year.
    const at = /[a-z]/i.test(text) ? Date.parse(text) : NaN;
    if (Number.isNaN(at)) return undefined;
    ms = Math.max(0, at - now);
  }
  return Math.min(ms, RETRY_AFTER_MAX_MS);
}

/** `parseRetryAfter` over a response's headers; tolerates a response without them. */
export function retryAfterFrom(headers: Headers | undefined): number | undefined {
  if (!headers || typeof headers.get !== 'function') return undefined;
  return parseRetryAfter(headers.get('retry-after'));
}

/** The wait before retrying after the `attempt`th consecutive 429 (0-based). */
export function rateLimitBackoffMs(attempt: number, retryAfterMs: number | undefined): number {
  return Math.max(
    RATE_LIMIT_MIN_WAIT_MS,
    retryAfterMs ?? Math.min(RATE_LIMIT_BACKOFF_BASE_MS * 2 ** attempt, RATE_LIMIT_BACKOFF_MAX_MS),
  );
}

/** The wait before the `attempt`th consecutive rate-limited connect is retried, with jitter. */
export function connectRetryDelayMs(
  attempt: number,
  retryAfterMs: number | undefined,
  random: () => number = Math.random,
): number {
  return rateLimitBackoffMs(attempt, retryAfterMs) * (1 + random() * RATE_LIMIT_JITTER_FRACTION);
}

/**
 * The backend rate-limited the API key. Retryable: wait `retryAfterMs` (when the
 * backend said) before trying again.
 */
export class RateLimitedError extends Error {
  readonly status = HTTP_TOO_MANY_REQUESTS;
  constructor(
    message: string,
    readonly retryAfterMs: number | undefined,
  ) {
    super(message);
    this.name = 'RateLimitedError';
  }
}

function* errorChain(err: unknown): Generator<Error> {
  let current = err;
  for (let depth = 0; current instanceof Error && depth < 4; depth++) {
    yield current;
    current = (current as { cause?: unknown }).cause;
  }
}

/**
 * A Blocks endpoint's HTTP error. Only these, and `RateLimitedError`, can be the Blocks rate
 * limit: a customer token endpoint's or token provider's error with a `status` of 429 is a failed
 * refresh, not a wait.
 */
export class BlocksHttpError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** The response's `Retry-After`, when it had one. */
    readonly retryAfterMs?: number,
  ) {
    super(message);
  }
}

/** True when `err`, or an error it wraps as `cause`, is a `RateLimitedError` or a Blocks 429. */
export function isRateLimited(err: unknown): boolean {
  for (const e of errorChain(err)) {
    if (e instanceof RateLimitedError) return true;
    if (e instanceof BlocksHttpError && e.status === HTTP_TOO_MANY_REQUESTS) return true;
  }
  return false;
}

/** The server-requested wait carried by `err` or an error it wraps as `cause`. */
export function retryAfterOf(err: unknown): number | undefined {
  for (const e of errorChain(err)) {
    const ms = (e as { retryAfterMs?: unknown }).retryAfterMs;
    if (typeof ms === 'number') return ms;
  }
  return undefined;
}

/**
 * Holds API-key calls back until a 429's wait has elapsed, so one owner of the
 * key honors the limit for all of its callers without each re-asking the backend.
 */
export class RateLimitHold {
  private until = 0;

  /** Throws a `RateLimitedError` carrying the remaining wait while a hold is in force. */
  check(what: string): void {
    const remaining = this.until - Date.now();
    if (remaining > 0) {
      throw new RateLimitedError(
        `${what} rate limited; retry in ${Math.ceil(remaining / 1000)}s`,
        remaining,
      );
    }
  }

  /** Starts a hold from a 429 response and returns the error to throw. */
  record(what: string, response: { headers?: Headers }): RateLimitedError {
    const retryAfterMs = retryAfterFrom(response.headers);
    // Concurrent 429s can land out of order; a later, shorter hint must not shorten the hold.
    this.until = Math.max(this.until, Date.now() + (retryAfterMs ?? RATE_LIMIT_NO_HINT_HOLD_MS));
    const hint = retryAfterMs === undefined ? '' : `; retry in ${Math.ceil(retryAfterMs / 1000)}s`;
    return new RateLimitedError(`${what} rate limited (HTTP 429)${hint}`, retryAfterMs);
  }
}
