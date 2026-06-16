/**
 * `@blocks-network/embed-auth` — public entry.
 *
 * Public API surface (impl_03 §R4.2):
 *   - `signInAndGetClient(opts)` — single-agent sign-in.
 *   - `signInAndGetClients(opts)` — multi-agent sign-in.
 *   - `signOut()` — best-effort revoke + local clear of every session on this page.
 *   - `BlocksAuthError` — typed error class for every rejection.
 *
 * Rollup builds the IIFE bundle via `output.format: 'iife'` +
 * `output.name: 'BlocksAuth'`, which exposes the named exports below as
 * properties of the `BlocksAuth` global automatically — no separate IIFE
 * entry file needed.
 */

export { signInAndGetClient, signInAndGetClients, signOut } from './api.js';
// `computePartitionKey` is exposed for scaffold templates / advanced
// callers that need to mirror the widget's storage partitioning (for
// example: scaffold auto-resume needs to know whether *this* page's
// exact `(backendBaseUrl, pageOrigin, agents)` partition is the one
// stored, before calling `signInAndGetClient*` — otherwise a stale
// unrelated partition triggers a popup at page load and browsers
// block it.). Treat the function shape as a stable contract.
export { computePartitionKey } from './storage.js';
export {
  BlocksAuthError,
  type BlocksAuthErrorCode,
  type Agent,
  type SignInSingleOptions,
  type SignInMultiOptions,
  type TokenResult,
} from './types.js';
