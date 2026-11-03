import { createLogger } from '../logger.js';

const logger = createLogger('Session');

/**
 * Wraps each application callback so a throw is logged instead of
 * unwinding the receiver mid-update: dedup, settlement and state changes
 * must complete whatever a callback does.
 */
export function isolateCallbacks<T extends object>(events: T): T {
  return Object.fromEntries(
    Object.entries(events).map(([name, callback]) => [
      name,
      typeof callback === 'function'
        ? (...args: unknown[]) => {
            try {
              callback.apply(events, args);
            } catch (err) {
              logger('error', 'session callback threw', { callback: name, error: String(err) });
            }
          }
        : callback,
    ]),
  ) as T;
}
