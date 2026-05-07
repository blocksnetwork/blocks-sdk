/**
 * Shared configuration utilities for live tests.
 *
 * All tests target the single Blocks backend at BLOCKS_BACKEND_URL
 * (default: http://localhost:3001).
 *
 * PubNub keys can be loaded from a local .env file if present, or set
 * directly via environment variables. Explicit env vars take precedence.
 *
 * Environment Variables:
 * - PUBNUB_LIVE_TEST=1         - Enable live tests (required)
 * - BLOCKS_BACKEND_URL         - Backend base URL (default: http://localhost:3001)
 * - BLOCKS_API_KEY              - API key from `blocks login` (required for auth)
 * - PUBNUB_PUBLISH_KEY         - PubNub publish key
 * - PUBNUB_SUBSCRIBE_KEY       - PubNub subscribe key
 * - PUBNUB_SECRET_KEY          - PubNub secret key
 *
 * Usage:
 *   import { hasLiveEnv, getBaseUrl, getAuthHeaders, getTestTimeout } from './helpers/live-test-config.js';
 *
 *   describe.skipIf(!hasLiveEnv() || process.env.PUBNUB_LIVE_TEST !== '1')('My Live Test', () => {
 *     it('should work', async () => {
 *       const response = await fetch(`${getBaseUrl()}/endpoint`, {
 *         headers: { ...getAuthHeaders(), 'Content-Type': 'application/json' },
 *       });
 *     }, getTestTimeout());
 *   });
 */

import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT_DIR = resolve(__dirname, '..', '..', '..', '..');
const LOCAL_ENV_PATH = resolve(ROOT_DIR, '.env');

/**
 * Parse a .env file and set process.env for any keys in the keyMap
 * (or literal keys in literalKeys) that aren't already set.
 */
function loadEnvFile(
  filePath: string,
  keyMap: Record<string, string>,
  literalKeys: string[] = [],
): void {
  try {
    const content = readFileSync(filePath, 'utf-8');
    for (const line of content.split('\n')) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith('#')) continue;
      const eqIdx = trimmed.indexOf('=');
      if (eqIdx < 0) continue;
      const key = trimmed.slice(0, eqIdx);
      const value = trimmed.slice(eqIdx + 1);
      const target = keyMap[key];
      if (target && !process.env[target]) {
        process.env[target] = value;
      }
      if (literalKeys.includes(key) && !process.env[key]) {
        process.env[key] = value;
      }
    }
  } catch {
    // File not found — skip
  }
}

// Load PubNub keys and BLOCKS_TOKEN from local .env if present
loadEnvFile(LOCAL_ENV_PATH, {
  PUBNUB_PLAYGROUND_PUBLISH_KEY: 'PUBNUB_PUBLISH_KEY',
  PUBNUB_PLAYGROUND_SUBSCRIBE_KEY: 'PUBNUB_SUBSCRIBE_KEY',
  PUBNUB_PLAYGROUND_SECRET_KEY: 'PUBNUB_SECRET_KEY',
}, ['BLOCKS_TOKEN', 'BLOCKS_API_KEY']);

// Default to local backend when .env is present
if (!process.env.BLOCKS_BACKEND_URL) {
  try {
    readFileSync(LOCAL_ENV_PATH);
    process.env.BLOCKS_BACKEND_URL = 'http://localhost:3001';
  } catch {
    // No local .env — likely CI; leave unset so backend tests skip
  }
}

/**
 * Check if PubNub credentials are available for live testing.
 */
export const hasLiveEnv = (): boolean =>
  !!process.env.PUBNUB_SUBSCRIBE_KEY &&
  !!process.env.PUBNUB_PUBLISH_KEY &&
  !!process.env.PUBNUB_SECRET_KEY;

/**
 * Check if backend is available for tests that need it (registration, RPC, etc.).
 */
export const hasBackendEnv = (): boolean =>
  !!process.env.BLOCKS_BACKEND_URL;

/**
 * Get the backend base URL.
 * Reads BLOCKS_BACKEND_URL, defaults to http://localhost:3001.
 */
export const getBaseUrl = (): string =>
  process.env.BLOCKS_BACKEND_URL || 'http://localhost:3001';

/**
 * Get authorization headers for HTTP requests.
 * Returns Authorization: Bearer header if BLOCKS_API_KEY is set.
 */
export const getAuthHeaders = (): Record<string, string> => {
  const token = process.env.BLOCKS_API_KEY;
  if (token) {
    return { Authorization: `Bearer ${token}` };
  }
  return {};
};

/**
 * Get test timeout in milliseconds.
 */
export const getTestTimeout = (baseTimeout = 30000): number => baseTimeout;

/**
 * Log helpful error message when HTTP requests fail.
 *
 * @param err - The error that occurred
 * @param baseUrl - The URL that was being accessed
 */
export const logFetchError = (err: unknown, baseUrl: string): void => {
  const isFetchError =
    err instanceof TypeError && (err.message === 'fetch failed' || 'cause' in err);
  if (!isFetchError) return;

  console.error('');
  console.error('[test] Fetch failed - is the backend dev server running?');
  console.error(`[test] Expected server at: ${baseUrl}`);
  console.error('[test] Ensure the Blocks backend is running');
  console.error('');
};

/**
 * Standard headers for Blocks HTTP requests.
 * Includes Content-Type, Blocks-Protocol-Version, and any auth headers.
 */
export const getStandardHeaders = (): Record<string, string> => ({
  'Content-Type': 'application/json',
  'Blocks-Protocol-Version': '2026-05-01',
  ...getAuthHeaders(),
});

/**
 * Publish an agent to the registry so that startAgentInstance() can
 * authenticate via /auth/agent/connect. Post-PR-313, agents must exist
 * in the registry before connect — connect does not upsert.
 *
 * Defaults to billingMode: 'free' for live tests that do not exercise
 * paid pricing. Pass options.billingMode='paid' along with positive
 * pricing and tcAcceptedAt for paid scenarios.
 */
export const publishAgent = async (
  agentName: string,
  card: object,
  options: {
    billingMode?: 'free' | 'paid';
    pricePerTask?: string;
    pricePerMinute?: string;
    tcAcceptedAt?: string;
    listing?: 'public' | 'private';
  } = {},
): Promise<void> => {
  const billingMode = options.billingMode ?? 'free';
  const body: Record<string, unknown> = {
    agentName,
    card,
    billingMode,
    protocolVersions: ['2026-05-01'],
    preferredProtocolVersion: '2026-05-01',
  };
  if (options.listing) body.listing = options.listing;
  if (options.pricePerTask !== undefined) body.pricePerTask = options.pricePerTask;
  if (options.pricePerMinute !== undefined) body.pricePerMinute = options.pricePerMinute;
  if (options.tcAcceptedAt !== undefined) body.tcAcceptedAt = options.tcAcceptedAt;

  const resp = await fetch(`${getBaseUrl()}/api/v1/registry/agents`, {
    method: 'POST',
    headers: getStandardHeaders(),
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    const respBody = await resp.text().catch(() => '');
    throw new Error(
      `publishAgent(${agentName}) failed: HTTP ${resp.status} ${respBody}`,
    );
  }
};

/**
 * Log test configuration at startup (useful for debugging).
 */
export const logTestConfig = (): void => {
  const baseUrl = getBaseUrl();
  const hasAuth = !!process.env.BLOCKS_API_KEY;

  console.log('');
  console.log('========================================');
  console.log('Live Test Configuration');
  console.log('========================================');
  console.log(`Base URL: ${baseUrl}`);
  console.log(`Authentication: ${hasAuth ? 'Bearer token configured' : 'None'}`);
  console.log('========================================');
  console.log('');
};
