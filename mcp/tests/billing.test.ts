import { describe, it, expect, vi } from 'vitest';
import { checkBalance, requestTopup } from '../src/tools.js';
import { getConsumerBalance, createConsumerTopUp } from '../src/billing.js';
import {
  PROTOCOL_VERSION_HEADER,
  CURRENT_PROTOCOL_VERSION,
} from '../src/protocol-headers.js';
import { makeFakeDeps } from './helpers.js';

function mockResponse(body: unknown, ok = true, status = 200): Response {
  return {
    ok,
    status,
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as Response;
}

describe('check_balance (handler)', () => {
  it('returns an error when BLOCKS_API_KEY is missing', async () => {
    const { deps } = makeFakeDeps();

    const res = await checkBalance({}, deps);

    expect(res.isError).toBe(true);
    expect(res.content[0].text).toMatch(/BLOCKS_API_KEY/);
  });

  it('returns an error when BLOCKS_ORG_ID is missing', async () => {
    const { deps } = makeFakeDeps({ apiKey: 'bk_test' });

    const res = await checkBalance({}, deps);

    expect(res.isError).toBe(true);
    expect(res.content[0].text).toMatch(/BLOCKS_ORG_ID/);
  });

  it('forwards baseUrl, orgId, apiKey and emits balance JSON', async () => {
    const balance = {
      balance: '100.00',
      reservedBalance: '5.00',
      availableBalance: '95.00',
      updatedAt: '2026-05-28T00:00:00Z',
    };
    const { deps, mocks } = makeFakeDeps({
      apiKey: 'bk_test',
      orgId: 'org_42',
      balance,
    });

    const res = await checkBalance({}, deps);

    expect(mocks.getConsumerBalance).toHaveBeenCalledWith({
      baseUrl: 'http://api.test',
      apiKey: 'bk_test',
      orgId: 'org_42',
    });
    expect(JSON.parse(res.content[0].text)).toEqual(balance);
  });
});

describe('request_topup (handler)', () => {
  it('returns an error when BLOCKS_ORG_ID is missing', async () => {
    const { deps } = makeFakeDeps({ apiKey: 'bk_test' });

    const res = await requestTopup({ amountUsd: 25 }, deps);

    expect(res.isError).toBe(true);
    expect(res.content[0].text).toMatch(/BLOCKS_ORG_ID/);
  });

  it('returns the Stripe checkout URL with a human-readable label', async () => {
    const { deps, mocks } = makeFakeDeps({
      apiKey: 'bk_test',
      orgId: 'org_42',
      topUpSession: {
        checkoutUrl: 'https://checkout.stripe.test/session/xyz',
        sessionId: 'cs_test_abc',
      },
    });

    const res = await requestTopup({ amountUsd: 25 }, deps);

    expect(mocks.createConsumerTopUp).toHaveBeenCalledWith({
      baseUrl: 'http://api.test',
      apiKey: 'bk_test',
      orgId: 'org_42',
      amountUsd: 25,
    });
    expect(res.content[0].text).toContain('https://checkout.stripe.test/session/xyz');
    expect(res.content[0].text).toContain('$25.00');
  });
});

describe('getConsumerBalance (HTTP helper)', () => {
  it('GETs /api/v1/billing/:orgId/consumer/balance with Bearer auth', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(
        mockResponse({
          balance: '0',
          reservedBalance: '0',
          availableBalance: '0',
          updatedAt: '2026-05-28T00:00:00Z',
        }),
      );

    await getConsumerBalance({
      baseUrl: 'http://api.test',
      orgId: 'org_42',
      apiKey: 'bk_test',
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const [url, init] = fetchImpl.mock.calls[0];
    const parsed = new URL(String(url));
    expect(parsed.pathname).toBe('/api/v1/billing/org_42/consumer/balance');
    expect(init?.method).toBe('GET');
    const headers = init?.headers as Record<string, string>;
    expect(headers.Authorization).toBe('Bearer bk_test');
    expect(headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
  });

  it('throws on non-OK responses', async () => {
    const fetchImpl = vi.fn().mockResolvedValue(mockResponse({}, false, 403));

    await expect(
      getConsumerBalance({
        baseUrl: 'http://api.test',
        orgId: 'org_42',
        apiKey: 'bk_test',
        fetchImpl: fetchImpl as unknown as typeof fetch,
      }),
    ).rejects.toThrow('HTTP 403');
  });
});

describe('createConsumerTopUp (HTTP helper)', () => {
  it('POSTs JSON body with whole-cent amount string', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(
        mockResponse({
          checkoutUrl: 'https://checkout.stripe.test/x',
          sessionId: 'cs_test_x',
        }),
      );

    await createConsumerTopUp({
      baseUrl: 'http://api.test',
      orgId: 'org_42',
      apiKey: 'bk_test',
      amountUsd: 19.99,
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });

    const [url, init] = fetchImpl.mock.calls[0];
    const parsed = new URL(String(url));
    expect(parsed.pathname).toBe('/api/v1/billing/org_42/consumer/topup');
    expect(init?.method).toBe('POST');
    expect(JSON.parse(String(init?.body))).toEqual({ amount: '19.99' });
    const headers = init?.headers as Record<string, string>;
    expect(headers['Content-Type']).toBe('application/json');
  });

  it('rejects non-positive amounts', async () => {
    await expect(
      createConsumerTopUp({
        baseUrl: 'http://api.test',
        orgId: 'org_42',
        apiKey: 'bk_test',
        amountUsd: 0,
      }),
    ).rejects.toThrow(/positive/);
  });

  it('rejects amounts below the $5 platform minimum', async () => {
    await expect(
      createConsumerTopUp({
        baseUrl: 'http://api.test',
        orgId: 'org_42',
        apiKey: 'bk_test',
        amountUsd: 1,
      }),
    ).rejects.toThrow(/at least \$5/);
  });

  it('throws on non-OK responses with body detail', async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(mockResponse({ error: 'amount too small' }, false, 400));

    await expect(
      createConsumerTopUp({
        baseUrl: 'http://api.test',
        orgId: 'org_42',
        apiKey: 'bk_test',
        amountUsd: 25,
        fetchImpl: fetchImpl as unknown as typeof fetch,
      }),
    ).rejects.toThrow(/HTTP 400/);
  });
});
