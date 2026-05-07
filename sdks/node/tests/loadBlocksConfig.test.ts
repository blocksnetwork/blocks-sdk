import { describe, it, expect, vi, afterEach } from 'vitest';
import { loadBlocksConfig } from '../src/config-loader.js';

describe('loadBlocksConfig', () => {
  const originalFetch = globalThis.fetch;

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it('returns config from a valid JSON response', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        publishKey: 'pub-c-test',
        subscribeKey: 'sub-c-test',
        blocksBackendUrl: 'https://api.example.com',
      }),
    });

    const config = await loadBlocksConfig('https://cdn.example.com/config.json');
    expect(config).toEqual({
      publishKey: 'pub-c-test',
      subscribeKey: 'sub-c-test',
      blocksBackendUrl: 'https://api.example.com',
    });
    expect(globalThis.fetch).toHaveBeenCalledWith('https://cdn.example.com/config.json');
  });

  it('defaults publishKey and blocksBackendUrl to empty string when missing', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ subscribeKey: 'sub-c-minimal' }),
    });

    const config = await loadBlocksConfig('https://cdn.example.com/config.json');
    expect(config).toEqual({
      publishKey: '',
      subscribeKey: 'sub-c-minimal',
      blocksBackendUrl: '',
    });
  });

  it('throws when subscribeKey is missing', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ publishKey: 'pub-c-test' }),
    });

    await expect(
      loadBlocksConfig('https://cdn.example.com/config.json'),
    ).rejects.toThrow('missing subscribeKey');
  });

  it('throws on non-OK HTTP response', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 404,
    });

    await expect(
      loadBlocksConfig('https://cdn.example.com/missing.json'),
    ).rejects.toThrow('Failed to load Blocks config');
  });
});
