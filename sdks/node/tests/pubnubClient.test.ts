import { describe, expect, it, vi, afterEach } from 'vitest';

const setup = async () => {
  vi.resetModules();

  const instances: Array<Record<string, unknown>> = [];
  const PubNub = vi.fn(function(this: unknown, opts: Record<string, unknown>) {
    instances.push(opts);
  }) as unknown as {
    (opts: unknown): void;
    ExponentialRetryPolicy: (cfg: { minimumDelay: number; maximumDelay: number; maximumRetry: number }) => unknown;
    LogLevel: { Trace: number; Debug: number; Info: number; Warn: number; Error: number; None: number };
  };
  // Mirror the real PubNub's static — used by buildUnboundedRetryPolicy
  // to obtain a policy object whose maximumRetry can be mutated past
  // the constructor's 6-cap validate() guard.
  PubNub.LogLevel = { Trace: 0, Debug: 1, Info: 2, Warn: 3, Error: 4, None: 5 };
  PubNub.ExponentialRetryPolicy = (cfg) => ({
    minimumDelay: cfg.minimumDelay,
    maximumDelay: cfg.maximumDelay,
    maximumRetry: cfg.maximumRetry,
    excluded: [],
    shouldRetry: () => true,
    getDelay: () => 1000,
    validate: () => true,
  });

  vi.doMock('pubnub', () => ({ default: PubNub }));

  const { createPubNubClient } = await import('../src/runtime/pubnub-client.js');
  return { createPubNubClient, instances };
};

afterEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
  vi.unmock('pubnub');
});

describe('createPubNubClient', () => {
  it('throws when PubNub keys are missing', async () => {
    const { createPubNubClient } = await setup();

    expect(() => createPubNubClient({
      publishKey: '',
      subscribeKey: '',
    })).toThrow('PUBNUB keys not configured');
  });

  it('creates client with explicit config', async () => {
    const { createPubNubClient, instances } = await setup();

    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
      userId: 'explicit-user',
    });
    expect(instances[0]).toMatchObject({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
      userId: 'explicit-user',
    });
  });

  it('does not accept secretKey', async () => {
    const { createPubNubClient, instances } = await setup();

    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
    });
    expect(instances[0]).not.toHaveProperty('secretKey');
  });

  it('falls back to default userId when none provided', async () => {
    const { createPubNubClient, instances } = await setup();

    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
    });
    expect(instances[0]?.userId).toBe('blocks-agent');
  });

  // Silent-park fix: the agent's long-lived control client
  // must not give up reconnect attempts after the default 6-step
  // ExponentialRetryPolicy budget runs out, because the PubNub Event
  // Engine then parks in RECEIVE_FAILED and stops emitting status events.
  // The fix is to extend maximumRetry to a 30-day equivalent (43_200
  // attempts at the 60s cap). The PubNub constructor enforces a 6-cap
  // via validate(); we mutate the policy object after construction to
  // bypass that guard, mirroring how the docs say it's permitted.
  it('configures unbounded subscribe retry by default (30-day equivalent)', async () => {
    const { createPubNubClient, instances } = await setup();

    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
    });
    const opts = instances[0];
    expect(opts).toHaveProperty('retryConfiguration');
    const retry = opts.retryConfiguration as { maximumRetry: number };
    expect(retry.maximumRetry).toBe(43_200);
  });

  // Per-task / per-stream PubNub clients are short-lived. If their
  // subscribe retries fail, the parent task fails cleanly rather than
  // looping forever, which is the desired semantics. Setting
  // subscribeRetryUnbounded:false MUST keep the PubNub default.
  it('respects subscribeRetryUnbounded:false (omits retryConfiguration → PubNub default applies)', async () => {
    const { createPubNubClient, instances } = await setup();

    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
      subscribeRetryUnbounded: false,
    });
    expect(instances[0]).not.toHaveProperty('retryConfiguration');
  });

  it('always installs a silent logger (no onRetry needed)', async () => {
    const { createPubNubClient, instances } = await setup();
    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
    });
    expect(instances[0]).toHaveProperty('loggers');
    const loggers = instances[0].loggers as unknown[];
    expect(Array.isArray(loggers)).toBe(true);
    expect(loggers).toHaveLength(1);
  });

  it('always uses logLevel None (5) regardless of retryPolicy', async () => {
    const { createPubNubClient, instances } = await setup();
    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
      subscribeRetryUnbounded: true,
    });
    // _PUBNUB_LOG_LEVEL_NONE = 5
    expect(instances[0]?.logLevel).toBe(5);
  });

  // Future-proofing. If a future PubNub release freezes
  // the policy object, the current `policy.maximumRetry = X` assignment
  // throws in strict mode (ES modules are always strict). The fix wraps
  // the mutation in try/catch and returns null so createPubNubClient
  // omits retryConfiguration and lets PubNub apply its default budget
  // rather than crashing the agent at boot.
  it('falls back to default retry policy when policy object is frozen', async () => {
    vi.resetModules();
    const instances: Array<Record<string, unknown>> = [];
    const PubNub = vi.fn(function(this: unknown, opts: Record<string, unknown>) {
      instances.push(opts);
    }) as unknown as {
      (opts: unknown): void;
      ExponentialRetryPolicy: (cfg: { minimumDelay: number; maximumDelay: number; maximumRetry: number }) => unknown;
      LogLevel: { Trace: number; Debug: number; Info: number; Warn: number; Error: number; None: number };
    };
    PubNub.LogLevel = { Trace: 0, Debug: 1, Info: 2, Warn: 3, Error: 4, None: 5 };
    PubNub.ExponentialRetryPolicy = (cfg) =>
      Object.freeze({
        minimumDelay: cfg.minimumDelay,
        maximumDelay: cfg.maximumDelay,
        maximumRetry: cfg.maximumRetry,
        validate: () => true,
      });
    vi.doMock('pubnub', () => ({ default: PubNub }));

    const { createPubNubClient } = await import('../src/runtime/pubnub-client.js');
    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
    });
    expect(instances[0]).not.toHaveProperty('retryConfiguration');
  });

  // Silent-no-op path. If a future PubNub release backs maximumRetry with
  // a getter-only descriptor (or a setter that swallows writes), the
  // assignment succeeds without throwing but the field stays at 6. The
  // raised-cap assertion must catch this and force the same fall-through
  // to PubNub's default retry budget.
  it('falls back to default retry policy when maximumRetry assignment silently no-ops', async () => {
    vi.resetModules();
    const instances: Array<Record<string, unknown>> = [];
    const PubNub = vi.fn(function(this: unknown, opts: Record<string, unknown>) {
      instances.push(opts);
    }) as unknown as {
      (opts: unknown): void;
      ExponentialRetryPolicy: (cfg: { minimumDelay: number; maximumDelay: number; maximumRetry: number }) => unknown;
      LogLevel: { Trace: number; Debug: number; Info: number; Warn: number; Error: number; None: number };
    };
    PubNub.LogLevel = { Trace: 0, Debug: 1, Info: 2, Warn: 3, Error: 4, None: 5 };
    PubNub.ExponentialRetryPolicy = (cfg) => {
      const obj: Record<string, unknown> = {
        minimumDelay: cfg.minimumDelay,
        maximumDelay: cfg.maximumDelay,
        validate: () => true,
      };
      // Read returns the seeded 6; write is swallowed.
      Object.defineProperty(obj, 'maximumRetry', {
        configurable: false,
        enumerable: true,
        get: () => 6,
        set: () => { /* swallow */ },
      });
      return obj;
    };
    vi.doMock('pubnub', () => ({ default: PubNub }));

    const { createPubNubClient } = await import('../src/runtime/pubnub-client.js');
    createPubNubClient({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
    });
    expect(instances[0]).not.toHaveProperty('retryConfiguration');
  });
});

// Cross-SDK parity with Python; see SDK_CONTRACT.md §Cross-SDK retry-budget defaults.
describe('createPubNubClient — default subscribeRetryUnbounded parity', () => {
  it('implicit default and explicit true produce identical retry config', async () => {
    const { createPubNubClient: createImplicit, instances: implicitInstances } = await setup();
    createImplicit({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
    });

    const { createPubNubClient: createExplicit, instances: explicitInstances } = await setup();
    createExplicit({
      publishKey: 'pub-key',
      subscribeKey: 'sub-key',
      subscribeRetryUnbounded: true,
    });

    const implicitRetry = implicitInstances[0]?.retryConfiguration as
      | { minimumDelay: number; maximumDelay: number; maximumRetry: number }
      | undefined;
    const explicitRetry = explicitInstances[0]?.retryConfiguration as
      | { minimumDelay: number; maximumDelay: number; maximumRetry: number }
      | undefined;

    expect(implicitRetry).toBeDefined();
    expect(explicitRetry).toBeDefined();
    expect(implicitRetry?.maximumRetry).toBe(43_200);
    expect(explicitRetry?.maximumRetry).toBe(implicitRetry?.maximumRetry);
    expect(explicitRetry?.minimumDelay).toBe(implicitRetry?.minimumDelay);
    expect(explicitRetry?.maximumDelay).toBe(implicitRetry?.maximumDelay);
  });
});
