/**
 * Billing helpers — direct HTTP to the routes that accept Bearer
 * API-key auth (`requireAuth` middleware path on the backend).
 *
 *   GET  /api/v1/billing/:orgId/consumer/balance
 *   POST /api/v1/billing/:orgId/consumer/topup
 *
 * Other billing routes (ledger, usage-summary, dashboard-summary,
 * topup-from-earnings) are session-only on the backend and are not
 * callable from an MCP server holding only an API key.
 */

import {
  PROTOCOL_VERSION_HEADER,
  CURRENT_PROTOCOL_VERSION,
} from './protocol-headers.js';

/**
 * Platform minimum top-up amount in USD. MUST stay in sync with
 * the service's MIN_BILLING_AMOUNT
 * (decimal-dollar string `'5'`). The backend rejects values below this floor
 * via `stripeMoneyAtLeast(MIN_BILLING_AMOUNT, ...)`; we mirror it here so MCP
 * callers get the real contract up front instead of a runtime HTTP 400.
 */
export const MIN_TOPUP_AMOUNT_USD = 5;

export interface ConsumerBalance {
  /** Ledger balance as a decimal-dollar string (e.g. "12.34"). */
  balance: string;
  /** Currently reserved (held for in-flight tasks). */
  reservedBalance: string;
  /** balance - reservedBalance. */
  availableBalance: string;
  /** ISO timestamp of when the balance snapshot was taken. */
  updatedAt: string;
}

export interface TopUpSession {
  /** Stripe Checkout URL the user opens in a browser to complete payment. */
  checkoutUrl: string;
  /** Stripe Checkout session id. */
  sessionId: string;
}

export interface BillingClientBase {
  baseUrl: string;
  orgId: string;
  apiKey: string;
  fetchImpl?: typeof fetch;
}

function buildHeaders(apiKey: string): Record<string, string> {
  return {
    [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
    Authorization: `Bearer ${apiKey}`,
    'Content-Type': 'application/json',
  };
}

function billingUrl(baseUrl: string, orgId: string, suffix: string): string {
  return `${baseUrl.replace(/\/+$/, '')}/api/v1/billing/${encodeURIComponent(orgId)}${suffix}`;
}

export async function getConsumerBalance(
  opts: BillingClientBase,
): Promise<ConsumerBalance> {
  const url = billingUrl(opts.baseUrl, opts.orgId, '/consumer/balance');
  const fetchFn = opts.fetchImpl ?? fetch;
  const response = await fetchFn(url, {
    method: 'GET',
    headers: buildHeaders(opts.apiKey),
  });
  if (!response.ok) {
    throw new Error(`Balance lookup failed: HTTP ${response.status}`);
  }
  return (await response.json()) as ConsumerBalance;
}

export interface CreateTopUpOptions extends BillingClientBase {
  /**
   * Whole-dollar (or whole-cent decimal) USD amount, e.g. 25 or 19.99.
   * Must be at least `MIN_TOPUP_AMOUNT_USD` ($5) — backend rejects below this.
   */
  amountUsd: number;
}

export async function createConsumerTopUp(
  opts: CreateTopUpOptions,
): Promise<TopUpSession> {
  if (!Number.isFinite(opts.amountUsd) || opts.amountUsd <= 0) {
    throw new Error('amountUsd must be a positive finite number');
  }
  if (Math.round(opts.amountUsd * 100) / 100 !== opts.amountUsd) {
    throw new Error('amountUsd must be a whole-cent value (no sub-cent fractions)');
  }
  if (opts.amountUsd < MIN_TOPUP_AMOUNT_USD) {
    throw new Error(
      `amountUsd must be at least $${MIN_TOPUP_AMOUNT_USD}.00 (platform minimum)`,
    );
  }
  const amount = opts.amountUsd.toFixed(2);

  const url = billingUrl(opts.baseUrl, opts.orgId, '/consumer/topup');
  const fetchFn = opts.fetchImpl ?? fetch;
  const response = await fetchFn(url, {
    method: 'POST',
    headers: buildHeaders(opts.apiKey),
    body: JSON.stringify({ amount }),
  });
  if (!response.ok) {
    let detail = '';
    try {
      detail = await response.text();
    } catch {
      // ignore body decode failures
    }
    throw new Error(
      `Top-up failed: HTTP ${response.status}${detail ? ` — ${detail}` : ''}`,
    );
  }
  return (await response.json()) as TopUpSession;
}
