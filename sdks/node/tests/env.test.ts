import { describe, it, expect } from 'vitest';
import { getEnv } from '../src/env.js';

describe('getEnv', () => {
  it('returns the env value when process.env is available', () => {
    process.env.TEST_GET_ENV_KEY = 'hello';
    expect(getEnv('TEST_GET_ENV_KEY')).toBe('hello');
    delete process.env.TEST_GET_ENV_KEY;
  });

  it('returns undefined for missing keys', () => {
    expect(getEnv('DEFINITELY_NOT_SET_KEY_XYZ')).toBeUndefined();
  });

  it('returns undefined when process is undefined', () => {
    const originalProcess = globalThis.process;
    // @ts-expect-error — simulating browser environment
    globalThis.process = undefined;
    try {
      expect(getEnv('PATH')).toBeUndefined();
    } finally {
      globalThis.process = originalProcess;
    }
  });
});
