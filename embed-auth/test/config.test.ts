/**
 * `resolveBackendBaseUrl` precedence chain:
 *   1. `opts.backendBaseUrl`
 *   2. `window.__BLOCKS_EMBED_DEV__.backendBaseUrl`
 *   3. compiled-in `BACKEND_BASE_URL_DEFAULT`
 */
import { afterEach, describe, expect, it } from 'vitest';

import { BACKEND_BASE_URL_DEFAULT } from '../src/constants.js';
import { resolveBackendBaseUrl, resolveCdmUrl } from '../src/config.js';

afterEach(() => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  delete (window as any).__BLOCKS_EMBED_DEV__;
});

describe('resolveBackendBaseUrl precedence', () => {
  it('uses opts.backendBaseUrl when supplied (overrides dev + default)', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__BLOCKS_EMBED_DEV__ = { backendBaseUrl: 'https://dev.example' };
    expect(resolveBackendBaseUrl({ backendBaseUrl: 'https://opts.example' }))
      .toBe('https://opts.example');
  });

  it('falls back to window.__BLOCKS_EMBED_DEV__.backendBaseUrl when opts is absent', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__BLOCKS_EMBED_DEV__ = { backendBaseUrl: 'https://dev.example' };
    expect(resolveBackendBaseUrl({})).toBe('https://dev.example');
    expect(resolveBackendBaseUrl()).toBe('https://dev.example');
  });

  it('falls back to compiled-in default when no override is present', () => {
    expect(resolveBackendBaseUrl()).toBe(BACKEND_BASE_URL_DEFAULT);
  });

  it('trims trailing slashes', () => {
    expect(resolveBackendBaseUrl({ backendBaseUrl: 'https://x.example/' }))
      .toBe('https://x.example');
    expect(resolveBackendBaseUrl({ backendBaseUrl: 'https://x.example///' }))
      .toBe('https://x.example');
  });

  it('throws BlocksAuthError(INVALID_INPUT) on a non-parseable URL', () => {
    expect(() => resolveBackendBaseUrl({ backendBaseUrl: 'not a url' }))
      .toThrowError(/INVALID_INPUT|not a parseable/);
  });

  it('throws BlocksAuthError(INVALID_INPUT) on a non-http(s) scheme', () => {
    expect(() => resolveBackendBaseUrl({ backendBaseUrl: 'ftp://x.example' }))
      .toThrowError(/INVALID_INPUT|loopback/);
  });
});

describe('resolveBackendBaseUrl scheme/host policy', () => {
  it('accepts https:// for any host', () => {
    expect(resolveBackendBaseUrl({ backendBaseUrl: 'https://blocks.ai' }))
      .toBe('https://blocks.ai');
    expect(resolveBackendBaseUrl({ backendBaseUrl: 'https://staging.blocks.ai' }))
      .toBe('https://staging.blocks.ai');
  });

  it('accepts http:// only for the three loopback hostnames', () => {
    expect(resolveBackendBaseUrl({ backendBaseUrl: 'http://localhost:3000' }))
      .toBe('http://localhost:3000');
    expect(resolveBackendBaseUrl({ backendBaseUrl: 'http://127.0.0.1:5173' }))
      .toBe('http://127.0.0.1:5173');
    expect(resolveBackendBaseUrl({ backendBaseUrl: 'http://[::1]:8080' }))
      .toBe('http://[::1]:8080');
  });

  it('rejects http:// for any non-loopback host', () => {
    // Cleartext to a non-loopback host would leak refresh tokens.
    expect(() => resolveBackendBaseUrl({ backendBaseUrl: 'http://evil.example' }))
      .toThrowError(/INVALID_INPUT|loopback/);
    expect(() => resolveBackendBaseUrl({ backendBaseUrl: 'http://blocks.ai' }))
      .toThrowError(/INVALID_INPUT|loopback/);
    expect(() => resolveBackendBaseUrl({ backendBaseUrl: 'http://192.168.1.5' }))
      .toThrowError(/INVALID_INPUT|loopback/);
    expect(() => resolveBackendBaseUrl({ backendBaseUrl: 'http://10.0.0.1' }))
      .toThrowError(/INVALID_INPUT|loopback/);
  });
});

describe('resolveCdmUrl precedence', () => {
  it('uses opts.cdmUrl when supplied (overrides dev shim)', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__BLOCKS_EMBED_DEV__ = {
      cdmUrl: 'http://localhost:3001/api/v1/cdm',
    };
    expect(resolveCdmUrl({ cdmUrl: 'https://staging.blocks.ai/api/v1/cdm' }))
      .toBe('https://staging.blocks.ai/api/v1/cdm');
  });

  it('falls back to __BLOCKS_EMBED_DEV__.cdmUrl when opts is absent', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__BLOCKS_EMBED_DEV__ = {
      cdmUrl: 'http://localhost:3001/api/v1/cdm',
    };
    expect(resolveCdmUrl({})).toBe('http://localhost:3001/api/v1/cdm');
    expect(resolveCdmUrl()).toBe('http://localhost:3001/api/v1/cdm');
  });

  it('returns undefined when no override is present (SDK uses its baked-in default)', () => {
    expect(resolveCdmUrl()).toBeUndefined();
    expect(resolveCdmUrl({})).toBeUndefined();
  });

  it('treats empty-string and non-string as absent', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__BLOCKS_EMBED_DEV__ = { cdmUrl: '' };
    expect(resolveCdmUrl()).toBeUndefined();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any).__BLOCKS_EMBED_DEV__ = { cdmUrl: 42 };
    expect(resolveCdmUrl()).toBeUndefined();
  });
});
