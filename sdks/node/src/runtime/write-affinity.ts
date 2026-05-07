/**
 * Write-affinity header tracking for non-browser clients.
 *
 * The backend returns X-Write-Affinity (Unix timestamp) on successful
 * mutations. Echoing it on subsequent requests forces reads from
 * primary during the affinity window, avoiding stale replica reads
 * after a write.
 *
 * Module-level state — affinity is per-process, not per-user.
 *
 * Concurrency: JS is single-threaded, so function bodies run atomically and
 * no lock is needed. However, async response ordering means `capture(newer)`
 * followed by `capture(older)` could otherwise shorten the affinity window.
 * Capture is therefore monotonic — an older expiry never overwrites a newer
 * one — so out-of-order response completions are safe.
 */

const HEADER = 'x-write-affinity';

let storedExpiry: string | null = null;
let storedExpiryValue = 0;

/** Capture the affinity header from a fetch Response. Monotonic — newer wins. */
export function captureAffinity(headers: Headers | undefined): void {
  if (!headers || typeof headers.get !== 'function') return;
  const value = headers.get(HEADER);
  if (!value) return;
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return;
  if (parsed > storedExpiryValue) {
    storedExpiry = value;
    storedExpiryValue = parsed;
  }
}

/** Inject, or strip, the affinity header on an outgoing request. */
export function injectAffinity(headers: Record<string, string>): void {
  if (storedExpiry && storedExpiryValue > Date.now() / 1000) {
    headers[HEADER] = storedExpiry;
    return;
  }
  if (storedExpiry) {
    storedExpiry = null;
    storedExpiryValue = 0;
  }
  delete headers[HEADER];
}

/** Reset stored affinity (for tests). */
export function resetAffinity(): void {
  storedExpiry = null;
  storedExpiryValue = 0;
}
