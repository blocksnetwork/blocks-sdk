import { describe, it, expect } from 'vitest';
import { build } from 'vite';
import path from 'node:path';

describe('browser bundle', () => {
  it('bundles SDK and pubnub without Node builtin errors', async () => {
    const result = await build({
      root: path.resolve(__dirname, 'fixtures/browser-smoke'),
      build: {
        write: false,
        lib: {
          entry: path.resolve(__dirname, 'fixtures/browser-smoke/index.ts'),
          formats: ['es'],
        },
        rollupOptions: {
          // Do NOT externalize pubnub — we want to verify the full
          // dependency tree bundles for browsers without Node builtins.
        },
      },
      resolve: {
        alias: {
          '@blocks-network/sdk': path.resolve(__dirname, '../src/index.ts'),
        },
      },
      logLevel: 'silent',
    });
    expect(result).toBeTruthy();
  }, 30_000);
});
