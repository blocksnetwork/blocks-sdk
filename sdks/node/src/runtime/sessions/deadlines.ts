import { createLogger } from '../logger.js';

const logger = createLogger('Session');

/** Rejects once `timeoutMs` passes before `work` settles, calling `onExpired` with that error. */
export async function withinDeadline<T>(
  work: Promise<T>,
  timeoutMs: number,
  what: string,
  onExpired?: (err: Error) => void,
): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const expired = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      const err = new Error(`${what} did not answer within ${timeoutMs} ms`);
      onExpired?.(err);
      reject(err);
    }, timeoutMs);
  });
  try {
    return await Promise.race([work, expired]);
  } finally {
    clearTimeout(timer);
  }
}

/** True once `work` succeeds; false when it fails, logged as `what`, or `timeoutMs` passes first. */
export function succeedsWithin(
  work: Promise<unknown>,
  timeoutMs: number,
  what: string,
): Promise<boolean> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const expired = new Promise<boolean>((resolve) => {
    timer = setTimeout(() => resolve(false), timeoutMs);
  });
  const done = work.then(
    () => true,
    (err: unknown) => {
      logger('warn', `${what} failed`, { error: String(err) });
      return false;
    },
  );
  return Promise.race([done, expired]).finally(() => clearTimeout(timer));
}
