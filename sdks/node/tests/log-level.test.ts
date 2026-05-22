import { describe, it, expect, afterEach } from 'vitest';
import { _resolveLogLevel, _LOG_LEVEL_ORDER } from '../src/runtime/logger.js';

describe('LOG_LEVEL threshold', () => {
  afterEach(() => {
    delete process.env.LOG_LEVEL;
  });

  it('defaults to info when LOG_LEVEL is unset', () => {
    delete process.env.LOG_LEVEL;
    expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.info);
  });

  it('resolves LOG_LEVEL=error to error threshold', () => {
    process.env.LOG_LEVEL = 'error';
    expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.error);
  });

  it('resolves LOG_LEVEL=warn to warn threshold', () => {
    process.env.LOG_LEVEL = 'warn';
    expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.warn);
  });

  it('resolves LOG_LEVEL=info to info threshold', () => {
    process.env.LOG_LEVEL = 'info';
    expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.info);
  });

  it('resolves LOG_LEVEL=debug to debug threshold', () => {
    process.env.LOG_LEVEL = 'debug';
    expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.debug);
  });

  it('is case-insensitive', () => {
    process.env.LOG_LEVEL = 'DEBUG';
    expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.debug);

    process.env.LOG_LEVEL = 'Error';
    expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.error);
  });

  it('falls back to info for unrecognized values', () => {
    process.env.LOG_LEVEL = 'verbose';
    expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.info);
  });

  it('error threshold suppresses info and debug but allows error', () => {
    // With error threshold (0), only level <= 0 should pass
    process.env.LOG_LEVEL = 'error';
    const threshold = _resolveLogLevel();
    expect(_LOG_LEVEL_ORDER.error).toBeLessThanOrEqual(threshold);
    expect(_LOG_LEVEL_ORDER.warn).toBeGreaterThan(threshold);
    expect(_LOG_LEVEL_ORDER.info).toBeGreaterThan(threshold);
    expect(_LOG_LEVEL_ORDER.debug).toBeGreaterThan(threshold);
  });

  it('debug threshold allows all levels', () => {
    process.env.LOG_LEVEL = 'debug';
    const threshold = _resolveLogLevel();
    expect(_LOG_LEVEL_ORDER.error).toBeLessThanOrEqual(threshold);
    expect(_LOG_LEVEL_ORDER.warn).toBeLessThanOrEqual(threshold);
    expect(_LOG_LEVEL_ORDER.info).toBeLessThanOrEqual(threshold);
    expect(_LOG_LEVEL_ORDER.debug).toBeLessThanOrEqual(threshold);
  });

  it('does not reference NODE_ENV', () => {
    // Confirm NODE_ENV is not consulted
    process.env.NODE_ENV = 'production';
    delete process.env.LOG_LEVEL;
    // Default is info, which allows info/warn/error output
    expect(_resolveLogLevel()).toBe(_LOG_LEVEL_ORDER.info);
    delete process.env.NODE_ENV;
  });
});
