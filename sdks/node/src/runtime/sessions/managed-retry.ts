import { retryAfterOf } from '../rate-limit.js';
import { MANAGED_ERROR_CODES, SESSION_ALREADY_OPEN, SessionError } from './session-errors.js';
import { isRefusalStatus, SessionHttpError, type SessionHttp } from './session-http.js';

const BACKOFF_INITIAL_MS = 250;
const BACKOFF_CAP_MS = 4_000;
const CLOSED_BEACON_MAX_TRIES = 3;

export function backoffDelay(attempt: number): number {
  return Math.random() * Math.min(BACKOFF_CAP_MS, BACKOFF_INITIAL_MS * 2 ** attempt);
}

export function pause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) return reject(signal.reason);
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal.reason);
    };
    signal.addEventListener('abort', onAbort, { once: true });
  });
}

/** A failure in transit, or one the backend marks as passing: a timeout, 429 or 5xx. */
export function isTransientError(err: unknown): boolean {
  if (err instanceof SessionError) return false;
  return !(err instanceof SessionHttpError) || !isRefusalStatus(err.status);
}

export const hasCode = (err: unknown, code: string): boolean =>
  err instanceof SessionError && err.code === code;

export const isBusy = (err: unknown): boolean => hasCode(err, MANAGED_ERROR_CODES.busy);

export const isAlreadyOpen = (err: unknown): boolean => hasCode(err, SESSION_ALREADY_OPEN);

const EXCHANGE_LEVEL_CODES: ReadonlySet<string> = new Set([
  ...Object.values(MANAGED_ERROR_CODES),
  SESSION_ALREADY_OPEN,
]);

/** The caller may no longer use the session: a 401, 403, 404 or session-level code. */
export function refusesAccess(err: unknown): boolean {
  if (err instanceof SessionError) return !EXCHANGE_LEVEL_CODES.has(err.code);
  return err instanceof SessionHttpError && [401, 403, 404].includes(err.status);
}

/** Rejects with the signal's reason once it aborts, even if `promise` never settles. */
function rejectOnAbort<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) return reject(signal.reason);
    const onAbort = () => reject(signal.reason);
    signal.addEventListener('abort', onAbort, { once: true });
    promise
      .finally(() => signal.removeEventListener('abort', onAbort))
      .then(resolve, reject);
  });
}

/**
 * Runs one attempt under `signal`, ended too once `until` (epoch ms) passes. The attempt is also
 * abandoned, not only signalled, since a step of it that ignores the signal (an auth refresh) could
 * otherwise outlast the deadline.
 */
export async function withDeadline<T>(
  until: number,
  signal: AbortSignal,
  attempt: (signal: AbortSignal) => Promise<T>,
): Promise<T> {
  const deadline = new AbortController();
  const timer = setTimeout(
    () => deadline.abort(new Error('attempt deadline passed')),
    Math.max(0, until - Date.now()),
  );
  try {
    const combined = AbortSignal.any([signal, deadline.signal]);
    return await rejectOnAbort(attempt(combined), combined);
  } finally {
    clearTimeout(timer);
  }
}

export interface RetryOptions {
  retryable(err: unknown): boolean;
  signal: AbortSignal;
  /** Epoch ms after which a failure is final. */
  until?: number;
  maxTries?: number;
}

export async function retry<T>(op: () => Promise<T>, options: RetryOptions): Promise<T> {
  const { retryable, signal, until, maxTries = Infinity } = options;
  for (let attempt = 0; ; attempt++) {
    signal.throwIfAborted();
    try {
      return await op();
    } catch (err) {
      const remaining = until === undefined ? Infinity : until - Date.now();
      if (!retryable(err) || attempt + 1 >= maxTries || remaining <= 0) throw err;
      // A wait the server asks past the budget cannot be honored, so the failure is final.
      const asked = retryAfterOf(err) ?? 0;
      if (asked > remaining) throw err;
      await pause(Math.min(Math.max(backoffDelay(attempt), asked), remaining), signal);
    }
  }
}

/** The consumer's only way to end a managed session, so a transport error is retried. */
export function sendManagedClosedBeacon(
  http: Pick<SessionHttp, 'sendBeacon'>,
  sessionId: string,
  budgetMs: number,
  signal: AbortSignal = new AbortController().signal,
): Promise<unknown> {
  return retry(() => http.sendBeacon(sessionId, 'closed', undefined, signal), {
    retryable: isTransientError,
    signal,
    maxTries: CLOSED_BEACON_MAX_TRIES,
    until: Date.now() + budgetMs,
  });
}
