import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  buildPubNubLogConfig,
  _PUBNUB_LOG_LEVEL_NONE,
  _PUBNUB_LOG_LEVEL_TRACE,
} from '../src/runtime/pubnub-client.js';

describe('buildPubNubLogConfig', () => {
  beforeEach(() => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
  });
  afterEach(() => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    vi.restoreAllMocks();
  });

  it('returns logLevel=None with silent logger when LOG_LEVEL=debug but BLOCKS_DEBUG_INTERNAL is unset', () => {
    process.env.LOG_LEVEL = 'debug';
    const cfg = buildPubNubLogConfig();
    expect(cfg.logLevel).toBe(_PUBNUB_LOG_LEVEL_NONE);
    expect(cfg.loggers).toHaveLength(1);
    const spy = vi.spyOn(console, 'log').mockImplementation(() => {});
    cfg.loggers[0].info({ messageType: 'text', message: 'x' });
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
    delete process.env.LOG_LEVEL;
  });

  it('returns logLevel=None with a single silent logger when BLOCKS_DEBUG_INTERNAL is unset', () => {
    const cfg = buildPubNubLogConfig();
    expect(cfg.logLevel).toBe(_PUBNUB_LOG_LEVEL_NONE);
    expect(cfg.loggers).toHaveLength(1);
    // Silent logger — calling its methods must not call console.*
    const spy = vi.spyOn(console, 'log').mockImplementation(() => {});
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    cfg.loggers[0].trace({ messageType: 'text', message: 'x' });
    cfg.loggers[0].debug({ messageType: 'text', message: 'x' });
    cfg.loggers[0].info({ messageType: 'text', message: 'x' });
    cfg.loggers[0].warn({ messageType: 'text', message: 'x' });
    cfg.loggers[0].error({ messageType: 'text', message: 'x' });
    expect(spy).not.toHaveBeenCalled();
    expect(errSpy).not.toHaveBeenCalled();
  });

  it('returns logLevel=Trace with a forwarder logger when BLOCKS_DEBUG_INTERNAL=forward_transport', () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'forward_transport';
    process.env.LOG_LEVEL = 'debug';
    const cfg = buildPubNubLogConfig();
    expect(cfg.logLevel).toBe(_PUBNUB_LOG_LEVEL_TRACE);
    expect(cfg.loggers).toHaveLength(1);

    const spy = vi.spyOn(console, 'log').mockImplementation(() => {});
    cfg.loggers[0].info({ messageType: 'text', message: 'hello' });
    expect(spy).toHaveBeenCalledWith(
      '[Transport]',
      expect.objectContaining({ level: 'info', message: 'hello' }),
    );
    spy.mockRestore();
    delete process.env.LOG_LEVEL;
  });

  it('maps PubNub trace and debug entries to Blocks debug level', () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'forward_transport';
    process.env.LOG_LEVEL = 'debug';
    const cfg = buildPubNubLogConfig();
    const spy = vi.spyOn(console, 'log').mockImplementation(() => {});
    cfg.loggers[0].trace({ messageType: 'text', message: 't' });
    cfg.loggers[0].debug({ messageType: 'text', message: 'd' });
    expect(spy).toHaveBeenCalledWith(
      '[Transport]',
      expect.objectContaining({ level: 'debug', message: 't' }),
    );
    expect(spy).toHaveBeenCalledWith(
      '[Transport]',
      expect.objectContaining({ level: 'debug', message: 'd' }),
    );
    delete process.env.LOG_LEVEL;
  });

  it('maps PubNub warn entries to Blocks warn level', () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'forward_transport';
    process.env.LOG_LEVEL = 'warn';
    const cfg = buildPubNubLogConfig();
    const spy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    cfg.loggers[0].warn({ messageType: 'text', message: 'careful' });
    expect(spy).toHaveBeenCalledWith(
      '[Transport]',
      expect.objectContaining({ level: 'warn', message: 'careful' }),
    );
    delete process.env.LOG_LEVEL;
  });

  it('maps PubNub error entries to Blocks error level', () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'forward_transport';
    process.env.LOG_LEVEL = 'error';
    const cfg = buildPubNubLogConfig();
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    cfg.loggers[0].error({ messageType: 'text', message: 'boom' });
    expect(spy).toHaveBeenCalledWith(
      '[Transport]',
      expect.objectContaining({ level: 'error', message: 'boom' }),
    );
    delete process.env.LOG_LEVEL;
  });

  it('coerces non-string message payloads to string', () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'forward_transport';
    process.env.LOG_LEVEL = 'debug';
    const cfg = buildPubNubLogConfig();
    const spy = vi.spyOn(console, 'log').mockImplementation(() => {});
    cfg.loggers[0].info({ messageType: 'object', message: { a: 1 } });
    expect(spy).toHaveBeenCalledWith(
      '[Transport]',
      expect.objectContaining({
        level: 'info',
        message: expect.stringContaining('a'),
      }),
    );
    delete process.env.LOG_LEVEL;
  });
});

describe('createPubNubClient log composition', () => {
  // Mock-and-inspect-opts pattern: stub the pubnub module so we can read
  // the loggers array passed into `new PubNub(opts)` instead of reaching
  // into PubNub's private `_configuration` field. Mirrors the setup in
  // `tests/pubnubClient.test.ts`.
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

  beforeEach(() => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    delete process.env.LOG_LEVEL;
  });
  afterEach(() => {
    delete process.env.BLOCKS_DEBUG_INTERNAL;
    delete process.env.LOG_LEVEL;
    vi.resetModules();
    vi.clearAllMocks();
    vi.unmock('pubnub');
    vi.restoreAllMocks();
  });

  it('passes the silent logger config when BLOCKS_DEBUG_INTERNAL is unset', async () => {
    const { createPubNubClient, instances } = await setup();
    createPubNubClient({
      subscribeKey: 'sub-c-test',
      publishKey: 'pub-c-test',
      userId: 'silence-probe',
    });
    const opts = instances[0];
    expect(opts.logLevel).toBe(5); // None
    const loggers = opts.loggers as unknown[];
    expect(Array.isArray(loggers)).toBe(true);
    expect(loggers).toHaveLength(1);
  });

  it('passes the forwarder logger config when BLOCKS_DEBUG_INTERNAL=forward_transport', async () => {
    process.env.BLOCKS_DEBUG_INTERNAL = 'forward_transport';
    process.env.LOG_LEVEL = 'debug';
    const { createPubNubClient, instances } = await setup();
    createPubNubClient({
      subscribeKey: 'sub-c-test',
      publishKey: 'pub-c-test',
      userId: 'compose-probe',
    });
    const opts = instances[0];
    expect(opts.logLevel).toBe(0); // Trace
    const loggers = opts.loggers as unknown[];
    expect(Array.isArray(loggers)).toBe(true);
    // Exactly one logger: the forwarder. No retry logger composition any more.
    expect(loggers).toHaveLength(1);
  });
});
