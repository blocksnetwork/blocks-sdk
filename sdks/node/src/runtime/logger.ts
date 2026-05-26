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
 * Named subsystems that can be activated via BLOCKS_DEBUG_INTERNAL.
 *   diagnostics       — Blocks-authored transport-connectivity diagnostics
 *                       (per-client status + alive-snapshot timer).
 *   forward_transport — route the underlying realtime transport's own log
 *                       output through the Blocks logger under the
 *                       [Transport] tag.
 *
 * Usage: BLOCKS_DEBUG_INTERNAL=diagnostics,forward_transport
 * Neither subsystem is implied by LOG_LEVEL=debug.
 */
export type DebugSubsystem = 'diagnostics' | 'forward_transport';

/** Authoritative list — must stay in sync with the DebugSubsystem union. */
const _KNOWN_DEBUG_SUBSYSTEMS: readonly DebugSubsystem[] = [
  'diagnostics',
  'forward_transport',
];

let _debugInternalWarned = false;

/**
 * One-time warning if BLOCKS_DEBUG_INTERNAL contains tokens that don't match
 * any known subsystem. Catches typos and the legacy truthy/falsy values
 * (`=1`, `=0`, `=true`, `=false`) which silently disable everything under
 * the comma-separated parser. Routes through log() so it respects LOG_LEVEL
 * (silent under LOG_LEVEL=error) and produces a structured entry consistent
 * with the rest of the SDK's logging discipline.
 */
function _maybeWarnUnknownDebugTokens(raw: string): void {
  if (_debugInternalWarned) return;
  const tokens = raw
    .split(',')
    .map(s => s.trim())
    .filter(s => s.length > 0);
  const unknown = tokens.filter(
    t => !(_KNOWN_DEBUG_SUBSYSTEMS as readonly string[]).includes(t),
  );
  if (unknown.length === 0) return;
  _debugInternalWarned = true;
  log(
    '[Blocks]',
    'warn',
    `BLOCKS_DEBUG_INTERNAL contains unrecognized token(s): ${unknown
      .map(t => JSON.stringify(t))
      .join(', ')}. Supported values: ${_KNOWN_DEBUG_SUBSYSTEMS.join(', ')}. ` +
      `Legacy truthy/falsy values (1, 0, true, false) are not recognized.`,
    { event: 'debug_internal_unknown_token', tokens: unknown },
  );
}

export function isDebugSubsystemEnabled(subsystem: DebugSubsystem): boolean {
  const raw = getEnv('BLOCKS_DEBUG_INTERNAL') ?? '';
  if (raw.length > 0) _maybeWarnUnknownDebugTokens(raw);
  return raw.split(',').some(s => s.trim() === subsystem);
}

export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

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
  level: LogLevel,
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

/** Returns a partial-applied log() bound to a fixed source tag. */
export function createLogger(source: string) {
  return (level: LogLevel, message: string, meta?: Record<string, unknown>) =>
    log(`[${source}]`, level, message, meta);
}
