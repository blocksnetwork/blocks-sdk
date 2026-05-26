import PubNub from 'pubnub';
import { createLogger, isDebugSubsystemEnabled } from './logger.js';

export interface PubNubClientConfig {
  subscribeKey: string;
  publishKey: string;
  userId?: string;
  presenceTimeout?: number;
  /**
   * Phase 1 connectivity diagnostic: when true, PubNub emits a status event
   * on every successful heartbeat, so the SDK status listener can prove
   * "heartbeats stopped" during a network outage. Disabled by default
   * because heartbeats fire every ~10 s per client and would otherwise
   * spam the log.
   */
  announceSuccessfulHeartbeats?: boolean;
  /**
   * BLOCKS-129 silent-park fix. When true, the client uses an
   * ExponentialRetryPolicy with maximumRetry expanded to ~30 days
   * (43_200 attempts at the 60s cap). PubNub's Event Engine otherwise
   * exhausts the default 6-attempt budget after ~3.5 min and parks in
   * RECEIVE_FAILED/HEARTBEAT_FAILED, going silent until reconnect()
   * is called manually. The PubNub JS SDK docs confirm maximumRetry has
   * no upper bound; the constructor's validate() does enforce 6, so we
   * mutate maximumRetry on the policy object after construction.
   *
   * Default: true (parity with the Python SDK). Per-task / ephemeral /
   * per-stream call sites MUST pass `subscribeRetryUnbounded: false`
   * explicitly so they keep the fail-fast retry budget — a stuck task
   * is preferable to a stuck loop. See dev_docs/SDK_CONTRACT.md
   * §Cross-SDK retry-budget defaults for the contract.
   */
  subscribeRetryUnbounded?: boolean;
}

// 30 days at the 60s exponential-backoff cap = 43_200 retries. PubNub's
// validate() refuses values > 6, so we construct with 6 and mutate.
const UNBOUNDED_MAX_RETRY = 43_200;
const UNBOUNDED_MAX_DELAY_S = 60;

// Returns null when PubNub.ExponentialRetryPolicy isn't available — happens
// in unit tests that mock the pubnub module's default export with a bare
// constructor stub. In production the static is always present.
// Also returns null if the policy object resists the maximumRetry/validate
// mutation (frozen, getter-only descriptor, etc.). A future PubNub release
// that hardens the policy shape would otherwise throw at boot or silently
// leave maximumRetry at 6; degrading to the SDK's default retry budget is
// preferable to either failure mode.
const buildUnboundedRetryPolicy = () => {
  const ExponentialRetryPolicy = (
    PubNub as unknown as {
      ExponentialRetryPolicy?: (cfg: {
        minimumDelay: number;
        maximumDelay: number;
        maximumRetry: number;
      }) => unknown;
    }
  ).ExponentialRetryPolicy;
  if (typeof ExponentialRetryPolicy !== 'function') return null;
  const policy = ExponentialRetryPolicy({
    minimumDelay: 2,
    maximumDelay: UNBOUNDED_MAX_DELAY_S,
    maximumRetry: 6,
  }) as { maximumRetry: number; validate: () => void };
  try {
    policy.maximumRetry = UNBOUNDED_MAX_RETRY;
    // PubNub's makeConfiguration() re-validates the policy after we hand it
    // over; the built-in validate() throws on maximumRetry > 6. The docs say
    // that cap is not enforced — neutralize the guard so our raised cap takes
    // effect. shouldRetry/getDelay still read maximumRetry at runtime.
    policy.validate = () => {};
  } catch {
    return null;
  }
  // Catch the silent-no-op case: assignment succeeded without throwing but
  // the field is still 6 (e.g., getter-only descriptor). Without this guard
  // we would hand PubNub a policy whose maximumRetry is the pre-fix default,
  // re-introducing the silent-park bug this whole function exists to fix.
  if (policy.maximumRetry !== UNBOUNDED_MAX_RETRY) return null;
  return policy;
};

// PubNub's LogLevel enum: Trace=0, Debug=1, Info=2, Warn=3, Error=4, None=5.
// The numeric values are stable across PubNub JS SDK versions; we keep
// defensive fallbacks here in case the enum export disappears or shifts
// (unit tests mock the pubnub module with a bare constructor stub).
const _pnLogLevel = (key: 'Trace' | 'None' | 'Warn', fallback: number): number => {
  const enumLike = (PubNub as unknown as { LogLevel?: Record<string, number> }).LogLevel;
  return typeof enumLike?.[key] === 'number' ? enumLike[key] : fallback;
};

/** Exported for tests. Resolves eagerly so the value can be compared with `toBe`. */
export const _PUBNUB_LOG_LEVEL_TRACE = _pnLogLevel('Trace', 0);
/** Exported for tests. */
export const _PUBNUB_LOG_LEVEL_NONE = _pnLogLevel('None', 5);

// PubNub LogMessage shape used by the forwarder logger.
interface PubNubLogMessage {
  location?: string;
  messageType?: string;
  message?: unknown;
}

const transportLogger = createLogger('Transport');

const toMessageString = (raw: unknown): string => {
  if (typeof raw === 'string') return raw;
  try {
    return JSON.stringify(raw);
  } catch {
    return String(raw);
  }
};

/**
 * Forwarder logger that routes every underlying-transport log entry
 * through the Blocks log() helper at the matching level (trace/debug ->
 * debug, info -> info, warn -> warn, error -> error). Used when
 * BLOCKS_DEBUG_INTERNAL=forward_transport is enabled. The Blocks logger
 * then filters via LOG_LEVEL.
 */
const buildPubNubForwarder = () => ({
  trace: (entry: PubNubLogMessage) =>
    transportLogger('debug', toMessageString(entry.message), { location: entry.location }),
  debug: (entry: PubNubLogMessage) =>
    transportLogger('debug', toMessageString(entry.message), { location: entry.location }),
  info: (entry: PubNubLogMessage) =>
    transportLogger('info', toMessageString(entry.message), { location: entry.location }),
  warn: (entry: PubNubLogMessage) =>
    transportLogger('warn', toMessageString(entry.message), { location: entry.location }),
  error: (entry: PubNubLogMessage) =>
    transportLogger('error', toMessageString(entry.message), { location: entry.location }),
});

/** Silent logger — replaces the transport's default console logger so
 * nothing leaks to stderr when forward_transport is off.
 * Belt-and-suspenders alongside logLevel=None. */
const buildSilentLogger = () => ({
  trace: () => {},
  debug: () => {},
  info: () => {},
  warn: () => {},
  error: () => {},
});

/**
 * Returns the { logLevel, loggers } pair to spread into every
 * realtime-client construction site. Forwarding the underlying transport's
 * own log output requires an explicit BLOCKS_DEBUG_INTERNAL=forward_transport;
 * LOG_LEVEL=debug alone does not enable it.
 */
export function buildPubNubLogConfig(): {
  logLevel: number;
  loggers: ReturnType<typeof buildPubNubForwarder>[];
} {
  if (isDebugSubsystemEnabled('forward_transport')) {
    return {
      logLevel: _PUBNUB_LOG_LEVEL_TRACE,
      loggers: [buildPubNubForwarder()],
    };
  }
  return {
    logLevel: _PUBNUB_LOG_LEVEL_NONE,
    loggers: [buildSilentLogger()],
  };
}

export const createPubNubClient = (config: PubNubClientConfig) => {
  if (!config.subscribeKey || !config.publishKey) {
    throw new Error('PUBNUB keys not configured');
  }
  // Default true; see JSDoc on `subscribeRetryUnbounded` for the contract.
  const unbounded = config.subscribeRetryUnbounded ?? true;
  const retryPolicy = unbounded ? buildUnboundedRetryPolicy() : null;
  const { logLevel, loggers } = buildPubNubLogConfig();
  return new PubNub({
    publishKey: config.publishKey,
    subscribeKey: config.subscribeKey,
    userId: config.userId ?? 'blocks-agent',
    enableEventEngine: true,
    ...(config.presenceTimeout !== undefined ? { presenceTimeout: config.presenceTimeout } : {}),
    ...(config.announceSuccessfulHeartbeats === true
      ? { announceSuccessfulHeartbeats: true }
      : {}),
    ...(retryPolicy !== null
      ? {
          retryConfiguration:
            retryPolicy as unknown as ConstructorParameters<typeof PubNub>[0]['retryConfiguration'],
        }
      : {}),
    logLevel: logLevel as unknown as ConstructorParameters<typeof PubNub>[0]['logLevel'],
    loggers: loggers as unknown as ConstructorParameters<typeof PubNub>[0]['loggers'],
  });
};
