import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import {
  log,
  _resolveLogLevel,
  _LOG_LEVEL_ORDER,
  isInternalDebugEnabled,
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

  describe('isInternalDebugEnabled', () => {
    it('returns false when neither flag is set', () => {
      delete process.env.BLOCKS_DEBUG_INTERNAL;
      delete process.env.LOG_LEVEL;
      expect(isInternalDebugEnabled()).toBe(false);
    });

    it('returns true when BLOCKS_DEBUG_INTERNAL=1', () => {
      process.env.BLOCKS_DEBUG_INTERNAL = '1';
      expect(isInternalDebugEnabled()).toBe(true);
    });

    it('returns true for any non-empty BLOCKS_DEBUG_INTERNAL value except "0"/"false"', () => {
      process.env.BLOCKS_DEBUG_INTERNAL = 'true';
      expect(isInternalDebugEnabled()).toBe(true);
      process.env.BLOCKS_DEBUG_INTERNAL = 'yes';
      expect(isInternalDebugEnabled()).toBe(true);
    });

    it('treats BLOCKS_DEBUG_INTERNAL=0 as disabled', () => {
      process.env.BLOCKS_DEBUG_INTERNAL = '0';
      delete process.env.LOG_LEVEL;
      expect(isInternalDebugEnabled()).toBe(false);
    });

    it('treats BLOCKS_DEBUG_INTERNAL=false as disabled', () => {
      process.env.BLOCKS_DEBUG_INTERNAL = 'false';
      delete process.env.LOG_LEVEL;
      expect(isInternalDebugEnabled()).toBe(false);
    });

    it('returns true when LOG_LEVEL=debug even if BLOCKS_DEBUG_INTERNAL is unset', () => {
      delete process.env.BLOCKS_DEBUG_INTERNAL;
      process.env.LOG_LEVEL = 'debug';
      expect(isInternalDebugEnabled()).toBe(true);
    });
  });
});
