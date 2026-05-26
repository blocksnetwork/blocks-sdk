import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import {
  log,
  _resolveLogLevel,
  _LOG_LEVEL_ORDER,
  isDebugSubsystemEnabled,
  createLogger,
} from '../src/runtime/logger.js';

describe('logger module', () => {
  let logSpy: ReturnType<typeof vi.spyOn>;
  let warnSpy: ReturnType<typeof vi.spyOn>;
  let errorSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
    warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
  });

  afterEach(() => {
    logSpy.mockRestore();
    warnSpy.mockRestore();
    errorSpy.mockRestore();
    delete process.env.LOG_LEVEL;
    delete process.env.BLOCKS_DEBUG_INTERNAL;
  });

  describe('isDebugSubsystemEnabled', () => {
    it('returns false when BLOCKS_DEBUG_INTERNAL is unset', () => {
      expect(isDebugSubsystemEnabled('diagnostics')).toBe(false);
      expect(isDebugSubsystemEnabled('forward_transport')).toBe(false);
    });

    it('returns true for a single matching subsystem', () => {
      process.env.BLOCKS_DEBUG_INTERNAL = 'diagnostics';
      expect(isDebugSubsystemEnabled('diagnostics')).toBe(true);
      expect(isDebugSubsystemEnabled('forward_transport')).toBe(false);
    });

    it('returns true for both when comma-separated', () => {
      process.env.BLOCKS_DEBUG_INTERNAL = 'diagnostics,forward_transport';
      expect(isDebugSubsystemEnabled('diagnostics')).toBe(true);
      expect(isDebugSubsystemEnabled('forward_transport')).toBe(true);
    });

    it('tolerates whitespace around subsystem names', () => {
      process.env.BLOCKS_DEBUG_INTERNAL = ' diagnostics , forward_transport ';
      expect(isDebugSubsystemEnabled('diagnostics')).toBe(true);
      expect(isDebugSubsystemEnabled('forward_transport')).toBe(true);
    });

    it('is NOT implied by LOG_LEVEL=debug', () => {
      process.env.LOG_LEVEL = 'debug';
      expect(isDebugSubsystemEnabled('diagnostics')).toBe(false);
      expect(isDebugSubsystemEnabled('forward_transport')).toBe(false);
    });

    it('returns false for an unknown subsystem name', () => {
      process.env.BLOCKS_DEBUG_INTERNAL = 'typo';
      expect(isDebugSubsystemEnabled('diagnostics')).toBe(false);
      expect(isDebugSubsystemEnabled('forward_transport')).toBe(false);
    });

    it.each([['0'], ['false'], ['1'], ['true']])(
      'treats legacy truthy/falsy value %s as a no-op for all subsystems',
      (value) => {
        process.env.BLOCKS_DEBUG_INTERNAL = value;
        // Pre-this-PR these were special-cased as the on/off switch.
        // Under the comma-tokenized parser they are unrecognized tokens —
        // both subsystems must read as false. (Task 5's warn-once handles
        // the *signal*; this test pins the *boolean* contract.)
        expect(isDebugSubsystemEnabled('diagnostics')).toBe(false);
        expect(isDebugSubsystemEnabled('forward_transport')).toBe(false);
      },
    );
  });

  describe('_LOG_LEVEL_ORDER and _resolveLogLevel', () => {
    it('exposes the same ordering as before', () => {
      expect(_LOG_LEVEL_ORDER.error).toBe(0);
      expect(_LOG_LEVEL_ORDER.warn).toBe(1);
      expect(_LOG_LEVEL_ORDER.info).toBe(2);
      expect(_LOG_LEVEL_ORDER.debug).toBe(3);
    });

    it('defaults to info when LOG_LEVEL unset', () => {
      delete process.env.LOG_LEVEL;
      expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.info);
    });

    it('falls back to info for unrecognized values', () => {
      process.env.LOG_LEVEL = 'verbose';
      expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.info);
    });
  });

  describe('log() filtering by LOG_LEVEL', () => {
    it('routes error → console.error with tag prefix', () => {
      process.env.LOG_LEVEL = 'error';
      log('[Tag]', 'error', 'boom', { code: 1 });
      expect(errorSpy).toHaveBeenCalledTimes(1);
      const [tag, entry] = errorSpy.mock.calls[0];
      expect(tag).toBe('[Tag]');
      expect(entry).toMatchObject({ level: 'error', message: 'boom', code: 1 });
    });

    it('routes warn → console.warn', () => {
      process.env.LOG_LEVEL = 'warn';
      log('[Tag]', 'warn', 'careful');
      expect(warnSpy).toHaveBeenCalledTimes(1);
    });

    it('routes info → console.log', () => {
      process.env.LOG_LEVEL = 'info';
      log('[Tag]', 'info', 'hello');
      expect(logSpy).toHaveBeenCalledTimes(1);
    });

    it('routes debug → console.log when LOG_LEVEL=debug', () => {
      process.env.LOG_LEVEL = 'debug';
      log('[Tag]', 'debug', 'detail');
      expect(logSpy).toHaveBeenCalledTimes(1);
    });

    it('suppresses info when LOG_LEVEL=error', () => {
      process.env.LOG_LEVEL = 'error';
      log('[Tag]', 'info', 'should not print');
      log('[Tag]', 'warn', 'should not print');
      log('[Tag]', 'debug', 'should not print');
      expect(logSpy).not.toHaveBeenCalled();
      expect(warnSpy).not.toHaveBeenCalled();
    });

    it('suppresses debug when LOG_LEVEL=info (default)', () => {
      delete process.env.LOG_LEVEL;
      log('[Tag]', 'debug', 'should not print');
      expect(logSpy).not.toHaveBeenCalled();
    });

    it('attaches ts and meta to the entry', () => {
      process.env.LOG_LEVEL = 'info';
      log('[Tag]', 'info', 'hello', { foo: 'bar' });
      const entry = logSpy.mock.calls[0][1] as Record<string, unknown>;
      expect(entry.message).toBe('hello');
      expect(entry.level).toBe('info');
      expect(entry.foo).toBe('bar');
      expect(typeof entry.ts).toBe('number');
    });
  });

  describe('createLogger', () => {
    it('prefixes console output with the given source tag', () => {
      const spy = vi.spyOn(console, 'log').mockImplementation(() => {});
      const logger = createLogger('PubNub');
      process.env.LOG_LEVEL = 'debug';
      logger('info', 'hello');
      expect(spy).toHaveBeenCalledWith(
        '[PubNub]',
        expect.objectContaining({ level: 'info', message: 'hello' }),
      );
      spy.mockRestore();
    });
  });

  describe('isDebugSubsystemEnabled — warn on unrecognized tokens', () => {
    // Each test must reset the warn-once latch by re-importing the module.
    // beforeEach() above doesn't do this — we need vi.resetModules().
    const freshLogger = async () => {
      vi.resetModules();
      return import('../src/runtime/logger.js');
    };

    it('does not warn when BLOCKS_DEBUG_INTERNAL is unset', async () => {
      const { isDebugSubsystemEnabled } = await freshLogger();
      isDebugSubsystemEnabled('diagnostics');
      expect(warnSpy).not.toHaveBeenCalled();
    });

    it('does not warn for the known subsystems', async () => {
      process.env.BLOCKS_DEBUG_INTERNAL = 'diagnostics,forward_transport';
      const { isDebugSubsystemEnabled } = await freshLogger();
      isDebugSubsystemEnabled('diagnostics');
      isDebugSubsystemEnabled('forward_transport');
      expect(warnSpy).not.toHaveBeenCalled();
    });

    it('warns once for an unrecognized token via log() with [Blocks] tag and structured meta', async () => {
      process.env.BLOCKS_DEBUG_INTERNAL = 'typo';
      process.env.LOG_LEVEL = 'warn'; // ensure warn is above threshold
      const { isDebugSubsystemEnabled } = await freshLogger();
      isDebugSubsystemEnabled('diagnostics');
      isDebugSubsystemEnabled('forward_transport');
      isDebugSubsystemEnabled('diagnostics');
      expect(warnSpy).toHaveBeenCalledTimes(1);
      const [tag, entry] = warnSpy.mock.calls[0] as [string, Record<string, unknown>];
      expect(tag).toBe('[Blocks]');
      expect(entry).toMatchObject({
        level: 'warn',
        event: 'debug_internal_unknown_token',
      });
      expect(Array.isArray(entry.tokens)).toBe(true);
      expect(entry.tokens).toContain('typo');
      // The human message still mentions the env var and the supported values.
      expect(String(entry.message)).toContain('BLOCKS_DEBUG_INTERNAL');
      expect(String(entry.message)).toContain('diagnostics');
      expect(String(entry.message)).toContain('forward_transport');
    });

    it.each([['1'], ['0'], ['true'], ['false']])(
      'warns when value is legacy %s (truthy/falsy form)',
      async (value) => {
        process.env.BLOCKS_DEBUG_INTERNAL = value;
        process.env.LOG_LEVEL = 'warn';
        const { isDebugSubsystemEnabled } = await freshLogger();
        isDebugSubsystemEnabled('diagnostics');
        expect(warnSpy).toHaveBeenCalledTimes(1);
        const [, entry] = warnSpy.mock.calls[0] as [string, Record<string, unknown>];
        expect(entry.tokens).toContain(value);
      },
    );

    it('warns once even with mixed known + unknown tokens', async () => {
      process.env.BLOCKS_DEBUG_INTERNAL = 'diagnostics,typo,forward_transport,bogus';
      process.env.LOG_LEVEL = 'warn';
      const { isDebugSubsystemEnabled } = await freshLogger();
      isDebugSubsystemEnabled('diagnostics');
      isDebugSubsystemEnabled('forward_transport');
      expect(warnSpy).toHaveBeenCalledTimes(1);
      const [, entry] = warnSpy.mock.calls[0] as [string, Record<string, unknown>];
      expect(entry.tokens).toEqual(expect.arrayContaining(['typo', 'bogus']));
      expect(entry.tokens).not.toContain('diagnostics');
      expect(entry.tokens).not.toContain('forward_transport');
      // Known subsystems still resolve correctly.
      expect(isDebugSubsystemEnabled('diagnostics')).toBe(true);
      expect(isDebugSubsystemEnabled('forward_transport')).toBe(true);
    });

    it('ignores empty string and whitespace-only tokens (no warn)', async () => {
      process.env.BLOCKS_DEBUG_INTERNAL = ' , , ';
      const { isDebugSubsystemEnabled } = await freshLogger();
      isDebugSubsystemEnabled('diagnostics');
      expect(warnSpy).not.toHaveBeenCalled();
    });

    it('LOG_LEVEL=error suppresses the warn-once', async () => {
      process.env.BLOCKS_DEBUG_INTERNAL = 'typo';
      process.env.LOG_LEVEL = 'error';
      const { isDebugSubsystemEnabled } = await freshLogger();
      isDebugSubsystemEnabled('diagnostics');
      // The latch still flips (warn was attempted), but log() drops the
      // entry because warn > threshold. AC2 of sdk-log-hygiene relies
      // on this — a typoed env var must not break LOG_LEVEL=error.
      expect(warnSpy).not.toHaveBeenCalled();
    });
  });
});
