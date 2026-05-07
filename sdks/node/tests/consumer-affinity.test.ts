import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { ConsumerAuth } from '../src/runtime/consumer-auth.js';
import { resetAffinity } from '../src/runtime/write-affinity.js';

const BASE_URL = 'http://localhost:3001';

function jsonResponse(
  body: Record<string, unknown>,
  affinity?: string,
): { ok: boolean; status: number; headers: Headers; json: () => Promise<unknown> } {
  const headers = new Headers();
  if (affinity) headers.set('x-write-affinity', affinity);
  return {
    ok: true,
    status: 200,
    headers,
    json: async () => body,
  };
}

describe('ConsumerAuth write-affinity wiring', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
    resetAffinity();
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    resetAffinity();
  });

  it('captures X-Write-Affinity from consumer-token response and echoes on refresh', async () => {
    const future = String(Math.floor(Date.now() / 1000) + 60);

    fetchSpy
      .mockResolvedValueOnce(
        jsonResponse(
          {
            accessToken: 'jwt-1',
            refreshToken: 'rt-1',
            expiresIn: 60,
            userId: 'u-1',
          },
          future,
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse({ accessToken: 'jwt-2', refreshToken: 'rt-2', expiresIn: 60 }),
      );

    const auth = new ConsumerAuth({ apiKey: 'ck_test', baseUrl: BASE_URL });
    await auth.init();
    await auth.onAuthFailure();

    expect(fetchSpy).toHaveBeenCalledTimes(2);

    const firstHeaders = fetchSpy.mock.calls[0][1].headers as Record<string, string>;
    expect(firstHeaders['x-write-affinity']).toBeUndefined();

    const secondHeaders = fetchSpy.mock.calls[1][1].headers as Record<string, string>;
    expect(secondHeaders['x-write-affinity']).toBe(future);
  });

  it('does not inject an expired affinity header on refresh', async () => {
    const past = String(Math.floor(Date.now() / 1000) - 10);

    fetchSpy
      .mockResolvedValueOnce(
        jsonResponse(
          {
            accessToken: 'jwt-1',
            refreshToken: 'rt-1',
            expiresIn: 60,
            userId: 'u-1',
          },
          past,
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse({ accessToken: 'jwt-2', refreshToken: 'rt-2', expiresIn: 60 }),
      );

    const auth = new ConsumerAuth({ apiKey: 'ck_test', baseUrl: BASE_URL });
    await auth.init();
    await auth.onAuthFailure();

    const secondHeaders = fetchSpy.mock.calls[1][1].headers as Record<string, string>;
    expect(secondHeaders['x-write-affinity']).toBeUndefined();
  });

  it('ignores missing affinity header in responses', async () => {
    fetchSpy
      .mockResolvedValueOnce(
        jsonResponse({
          accessToken: 'jwt-1',
          refreshToken: 'rt-1',
          expiresIn: 60,
          userId: 'u-1',
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({ accessToken: 'jwt-2', refreshToken: 'rt-2', expiresIn: 60 }),
      );

    const auth = new ConsumerAuth({ apiKey: 'ck_test', baseUrl: BASE_URL });
    await auth.init();
    await auth.onAuthFailure();

    const secondHeaders = fetchSpy.mock.calls[1][1].headers as Record<string, string>;
    expect(secondHeaders['x-write-affinity']).toBeUndefined();
  });
});
