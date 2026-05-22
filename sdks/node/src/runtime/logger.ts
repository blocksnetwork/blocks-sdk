import { getEnv } from '../env.js';

/** Numeric precedence for LOG_LEVEL threshold filtering. */
export const _LOG_LEVEL_ORDER: Record<string, number> = {
  error: 0,
  warn: 1,
  info: 2,
  debug: 3,
};

/** Resolve the effective LOG_LEVEL from env (default: "info"). */
export function _resolveLogLevel(): number {
  const raw = (getEnv('LOG_LEVEL') ?? 'info').toLowerCase();
  return _LOG_LEVEL_ORDER[raw] ?? _LOG_LEVEL_ORDER.info;
}

/**
 * True iff the caller has opted into SDK-internal diagnostics output
 * (currently: the BLOCKS-129 PubNub connectivity-diagnostics surface —
 * `pubnub_diagnostics_armed`, status transitions, alive snapshots).
 * Two equivalent opt-ins:
 *
 *   BLOCKS_DEBUG_INTERNAL=1   (explicit subsystem toggle)
 *   LOG_LEVEL=debug           (general SDK debug — implies internal too)
 *
 * Defaults to OFF. Without the opt-in, the diagnostics block is a no-op:
 * no listener attached, no timer armed, no per-status emission, no
 * `pubnub_diagnostics_armed` boot line.
 */
export function isInternalDebugEnabled(): boolean {
  const raw = getEnv('BLOCKS_DEBUG_INTERNAL');
  if (raw !== undefined) {
    const v = raw.trim().toLowerCase();
    if (v && v !== '0' && v !== 'false') return true;
  }
  return _resolveLogLevel() >= _LOG_LEVEL_ORDER.debug;
}

/**
 * Route a tagged, LOG_LEVEL-filtered log entry to console.error / warn / log.
 *
 * Tag is required (e.g. `'[AgentInstance]'`, `'[StreamClient]'`) so call
 * sites identify which subsystem emitted the line without relying on
 * filename + line number. Entries below the configured LOG_LEVEL threshold
 * are dropped silently.
 */
export function log(
  tag: string,
  level: 'debug' | 'info' | 'warn' | 'error',
  message: string,
  meta?: Record<string, unknown>,
): void {
  const threshold = _resolveLogLevel();
  const lvl = _LOG_LEVEL_ORDER[level] ?? _LOG_LEVEL_ORDER.info;
  if (lvl > threshold) return;

  const entry = { level, message, ts: Date.now(), ...meta };
  if (level === 'error') console.error(tag, entry);
  else if (level === 'warn') console.warn(tag, entry);
  else console.log(tag, entry);
}
