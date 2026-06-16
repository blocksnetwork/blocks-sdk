/**
 * `resolveBackendBaseUrl` — single source of truth for the Blocks backend
 * origin used by the widget.
 *
 * Resolution order (impl_03 §R4.2):
 *   1. `opts.backendBaseUrl` (explicit override).
 *   2. `globalThis.__BLOCKS_EMBED_DEV__?.backendBaseUrl` (set by the
 *      `blocks dev` injected `/__blocks_embed_dev.js` script — local-dev
 *      override surface).
 *   3. The compiled-in default (`BACKEND_BASE_URL_DEFAULT` from
 *      `constants.ts`, which Rollup replaces with `https://blocks.ai`
 *      at bundle time; on-prem builds inject a different URL via the
 *      `BLOCKS_BACKEND_BASE_URL` env var seen by `rollup.config.js`).
 *
 * Trailing slashes are trimmed. The resolved URL must parse and use
 * `https:` — or `http:` only when the host is one of the three loopback
 * hostnames (`localhost`, `127.0.0.1`, `[::1]`/`::1`). Cleartext to any
 * other host would leak refresh tokens. Anything else throws
 * `BlocksAuthError('INVALID_INPUT')`.
 */
import { BACKEND_BASE_URL_DEFAULT } from './constants.js';
import { BlocksAuthError } from './types.js';

interface DevOverride {
  backendBaseUrl?: string;
  cdmUrl?: string;
}

function readDevOverride(): DevOverride | undefined {
  // The dev override is window-scoped; jsdom provides `window`, browsers
  // provide `window`, Node-side rendering may not. Fall through silently
  // when absent.
  if (typeof window === 'undefined') return undefined;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const w = window as any;
  const dev = w.__BLOCKS_EMBED_DEV__;
  if (typeof dev === 'object' && dev !== null) return dev as DevOverride;
  return undefined;
}

export function resolveBackendBaseUrl(opts?: { backendBaseUrl?: string }): string {
  const candidate =
    opts?.backendBaseUrl ??
    readDevOverride()?.backendBaseUrl ??
    BACKEND_BASE_URL_DEFAULT;

  if (typeof candidate !== 'string' || candidate.length === 0) {
    throw new BlocksAuthError(
      'INVALID_INPUT',
      'backendBaseUrl must be a non-empty string',
    );
  }

  const trimmed = candidate.replace(/\/+$/, '');

  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    throw new BlocksAuthError(
      'INVALID_INPUT',
      `backendBaseUrl is not a parseable URL: ${candidate}`,
    );
  }

  if (parsed.protocol === 'https:') {
    return trimmed;
  }
  if (parsed.protocol === 'http:' && isLoopbackHost(parsed.hostname)) {
    return trimmed;
  }
  throw new BlocksAuthError(
    'INVALID_INPUT',
    `backendBaseUrl must use https:, or http: with a loopback host; got ${parsed.protocol}//${parsed.hostname}`,
  );
}

/**
 * `resolveCdmUrl` — same precedence shape as `resolveBackendBaseUrl`:
 *   1. `opts.cdmUrl` (explicit caller override).
 *   2. `globalThis.__BLOCKS_EMBED_DEV__?.cdmUrl` (set by the
 *      `blocks dev` injected `/__blocks_embed_dev.js` script — points
 *      at the local backend's `/api/v1/cdm` endpoint so the SDK
 *      resolves PubNub keysets and `api.baseUrl` from the local stack).
 *   3. `undefined` — the SDK falls through to its compiled-in default
 *      (`https://config.blocks.ai/config.json`).
 *
 * Returned as an explicit constructor option to `TaskClient.create`,
 * which is the path BLOCKS-101's `explicit option → CDM → default`
 * resolver chain preserves. We never need an env var here.
 */
export function resolveCdmUrl(opts?: { cdmUrl?: string }): string | undefined {
  const candidate = opts?.cdmUrl ?? readDevOverride()?.cdmUrl;
  if (typeof candidate !== 'string' || candidate.length === 0) return undefined;
  return candidate;
}

function isLoopbackHost(hostname: string): boolean {
  // URL.hostname is lowercased; IPv6 may or may not retain its brackets
  // depending on the runtime's URL implementation, so accept both forms.
  return (
    hostname === 'localhost' ||
    hostname === '127.0.0.1' ||
    hostname === '::1' ||
    hostname === '[::1]'
  );
}
