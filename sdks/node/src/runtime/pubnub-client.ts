import PubNub from 'pubnub';

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
  /**
   * BLOCKS-129 Phase 2B: surfaces the SDK's transport-layer retry
   * activity during a network outage. PubNub's middleware logs
   * `'HTTP request retry #N in Nms.'` at warn level whenever the
   * transport schedules a retry; with subscribeRetryUnbounded on these
   * fire continuously through an outage but never bubble up to the
   * Event Engine listener. Wiring this callback turns those warnings
   * into a visible signal so a human watching the log can tell the
   * difference between "agent retrying" and "agent dead".
   */
  onRetry?: (message: string) => void;
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

// Resolve PubNub.LogLevel.Warn defensively. In production it's always 3.
// In unit tests with a mocked pubnub module the static may be absent —
// we fall back to the literal value matching the real enum so the test
// path doesn't crash.
const getPubNubLogLevelWarn = (): number => {
  const enumLike = (PubNub as unknown as { LogLevel?: { Warn?: number } }).LogLevel;
  return typeof enumLike?.Warn === 'number' ? enumLike.Warn : 3;
};

// PubNub LogMessage shape we care about. We only forward warn-level
// transport-retry messages; everything else is a no-op so the SDK's
// chatty internal logs don't bleed into the agent timeline.
interface PubNubLogMessage {
  location?: string;
  messageType?: string;
  message?: unknown;
}

const buildRetryLogger = (onRetry: (message: string) => void) => ({
  trace: () => {},
  debug: () => {},
  info: () => {},
  error: () => {},
  warn: (entry: PubNubLogMessage) => {
    if (entry.location !== 'PubNubMiddleware') return;
    if (entry.messageType !== 'text') return;
    if (typeof entry.message !== 'string') return;
    if (!entry.message.includes('HTTP request retry')) return;
    onRetry(entry.message);
  },
});

export const createPubNubClient = (config: PubNubClientConfig) => {
  if (!config.subscribeKey || !config.publishKey) {
    throw new Error('PUBNUB keys not configured');
  }
  // Default true; see JSDoc on `subscribeRetryUnbounded` for the contract.
  const unbounded = config.subscribeRetryUnbounded ?? true;
  const retryPolicy = unbounded ? buildUnboundedRetryPolicy() : null;
  const retryLogger = config.onRetry ? buildRetryLogger(config.onRetry) : null;
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
    // PubNub only invokes user-supplied loggers when its own LoggerManager
    // is configured at the right level; warn covers the transport-retry
    // signal we care about without flipping on the chatty info/debug fan.
    // The level MUST be the numeric enum value (Warn=3); passing the
    // string 'warn' fails the `logLevel < minLogLevel` numeric comparison
    // and lets Trace/Debug through to the built-in ConsoleLogger.
    ...(retryLogger !== null
      ? {
          logLevel: getPubNubLogLevelWarn() as unknown as ConstructorParameters<typeof PubNub>[0]['logLevel'],
          loggers: [retryLogger] as unknown as ConstructorParameters<typeof PubNub>[0]['loggers'],
        }
      : {}),
  });
};
