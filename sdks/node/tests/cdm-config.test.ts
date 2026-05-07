import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const MOCK_CDM_RESPONSE = {
  playground: {
    publishKey: 'pub-c-playground-key',
    subscribeKey: 'sub-c-playground-key',
  },
  network: {
    publishKey: 'pub-c-network-key',
    subscribeKey: 'sub-c-network-key',
  },
  api: {
    baseUrl: 'http://localhost:3001',
  },
};

describe('fetchCdmConfig', () => {
  let fetchCdmConfig: typeof import('../src/runtime/cdm-config.js').fetchCdmConfig;
  let DEFAULT_CDM_URL: string;
  const originalFetch = globalThis.fetch;

  beforeEach(async () => {
    vi.resetModules();
    delete process.env.BLOCKS_CDM_URL;
    const mod = await import('../src/runtime/cdm-config.js');
    fetchCdmConfig = mod.fetchCdmConfig;
    DEFAULT_CDM_URL = mod.DEFAULT_CDM_URL;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    delete process.env.BLOCKS_CDM_URL;
  });

  it('fetches config from default URL', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(MOCK_CDM_RESPONSE),
    });

    const config = await fetchCdmConfig();

    expect(globalThis.fetch).toHaveBeenCalledWith(DEFAULT_CDM_URL);
    expect(config.playground.publishKey).toBe('pub-c-playground-key');
    expect(config.network.subscribeKey).toBe('sub-c-network-key');
    expect(config.api.baseUrl).toBe('http://localhost:3001');
  });

  it('uses BLOCKS_CDM_URL env var when set', async () => {
    process.env.BLOCKS_CDM_URL = 'https://custom-cdn.example.com/config.json';
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(MOCK_CDM_RESPONSE),
    });

    await fetchCdmConfig();

    expect(globalThis.fetch).toHaveBeenCalledWith('https://custom-cdn.example.com/config.json');
  });

  it('uses explicit url parameter over env var', async () => {
    process.env.BLOCKS_CDM_URL = 'https://env.example.com/config.json';
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(MOCK_CDM_RESPONSE),
    });

    await fetchCdmConfig('https://explicit.example.com/config.json');

    expect(globalThis.fetch).toHaveBeenCalledWith('https://explicit.example.com/config.json');
  });

  it('throws on HTTP error', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 404,
      statusText: 'Not Found',
    });

    await expect(fetchCdmConfig()).rejects.toThrow('CDM config fetch failed: 404 Not Found');
  });

  it('throws on missing playground keys', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ network: MOCK_CDM_RESPONSE.network, api: MOCK_CDM_RESPONSE.api }),
    });

    await expect(fetchCdmConfig()).rejects.toThrow('CDM config missing playground keys');
  });

  it('throws on missing network keys', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ playground: MOCK_CDM_RESPONSE.playground, api: MOCK_CDM_RESPONSE.api }),
    });

    await expect(fetchCdmConfig()).rejects.toThrow('CDM config missing network keys');
  });
});
