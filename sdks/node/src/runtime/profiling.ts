import { getEnv } from '../env.js';
import { log } from './logger.js';

export function isProfilingEnabled(): boolean {
  const raw = getEnv('BLOCKS_PROFILE') ?? '';
  return raw.split(',').some((s) => s.trim() === 'timing');
}

/**
 * Emit a single-clock dispatch timing line for one task. All timestamps are
 * Date.now() from the same process clock, so deltas are skew-free locally.
 * No-op unless BLOCKS_PROFILE includes `timing`.
 *
 * Chronological order of the marks is: StartTask received → `running` event
 * published → user handler invoked. The runtime publishes `running` before
 * invoking the handler, so the phases are decomposed as
 * received→running and running→handler (both non-negative by construction).
 */
export function logDispatchTiming(
  taskId: string,
  marks: { receivedMs: number; runningMs: number; handlerMs: number },
): void {
  if (!isProfilingEnabled()) return;
  log('[profile]', 'info', 'dispatch timing', {
    taskId,
    received_to_running_ms: marks.runningMs - marks.receivedMs,
    running_to_handler_ms: marks.handlerMs - marks.runningMs,
    received_to_handler_ms: marks.handlerMs - marks.receivedMs,
  });
}
