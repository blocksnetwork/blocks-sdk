/**
 * Neutral category labels surfaced through user-facing logs and the public
 * `StreamError.category` field. The SDK maps the underlying realtime
 * transport's internal category strings into this small enum so the
 * user-visible surface stays implementation-agnostic.
 *
 * "other" is the catch-all for transport categories the SDK does not
 * specifically classify. Devs investigating an unrecognised category
 * should opt into BLOCKS_DEBUG_INTERNAL=forward_transport to see the
 * underlying entries directly.
 */
export type TransportCategory =
  | 'connected'
  | 'reconnected'
  | 'network_down'
  | 'network_issues'
  | 'timeout'
  | 'malformed_response'
  | 'access_denied'
  | 'bad_request'
  | 'other';

const CATEGORY_MAP: Readonly<Record<string, TransportCategory>> = {
  PNConnectedCategory: 'connected',
  PNReconnectedCategory: 'reconnected',
  PNNetworkDownCategory: 'network_down',
  PNNetworkIssuesCategory: 'network_issues',
  PNTimeoutCategory: 'timeout',
  PNMalformedResponseCategory: 'malformed_response',
  PNAccessDeniedCategory: 'access_denied',
  PNBadRequestCategory: 'bad_request',
};

// Event Engine wrapper categories. The PubNub JS Event Engine wraps
// subscribe-time errors as `{ category: '<wrapper>', error: '<leaf>' }`
// (see node_modules/pubnub/dist/web/pubnub.js — emitStatus calls in
// HandshakingState and ReceivingState). The leaf string carries the real
// cause; we unwrap so user-facing surfaces (StreamError.category, the
// access-denied handler, the connectivity warn line, the diag listener)
// see the cause instead of "other".
const WRAPPER_CATEGORIES: ReadonlySet<string> = new Set([
  'PNConnectionErrorCategory',
  'PNDisconnectedUnexpectedlyCategory',
]);

/** Status payload shape consumed by the mapper. Mirrors the fields the
 * PubNub JS SDK populates on `Status` / `StatusEvent`. */
export interface TransportStatusPayload {
  category?: unknown;
  error?: unknown;
  statusCode?: unknown;
}

/**
 * Map the underlying transport's status payload to a neutral category.
 *
 * Resolution order:
 *   1. `statusCode === 403` -> `access_denied` (REST-grade override; wins
 *      even over an unknown wrapper category, because PAM revocation is
 *      the load-bearing case for `accessDeniedHandler`).
 *   2. `statusCode === 400` -> `bad_request` (same rationale).
 *   3. If the outer category is one of the Event Engine wrappers
 *      (`PNConnectionErrorCategory` / `PNDisconnectedUnexpectedlyCategory`),
 *      look up the nested `error` string in CATEGORY_MAP.
 *   4. Look up the outer category in CATEGORY_MAP.
 *   5. Fallback: `'other'`.
 *
 * Accepts either the full payload `{ category, error, statusCode }` or
 * a bare string (the legacy form). String form is preserved so test
 * fixtures and pure-classifier callers don't have to wrap.
 */
export function mapTransportCategory(
  input: TransportStatusPayload | string | null | undefined,
): TransportCategory {
  if (input === null || input === undefined) return 'other';
  if (typeof input === 'string') return CATEGORY_MAP[input] ?? 'other';
  const { category, error, statusCode } = input;
  if (statusCode === 403) return 'access_denied';
  if (statusCode === 400) return 'bad_request';
  const outer = typeof category === 'string' ? category : '';
  if (outer && WRAPPER_CATEGORIES.has(outer)) {
    const leaf = typeof error === 'string' ? error : '';
    return CATEGORY_MAP[leaf] ?? 'other';
  }
  return CATEGORY_MAP[outer] ?? 'other';
}

/**
 * Convenience predicate for the access-denied path. True when the
 * payload resolves to `access_denied` via `mapTransportCategory` (which
 * already honors statusCode 403, the wrapper unwrap, and the leaf
 * category).
 */
export function isAccessDeniedStatus(input: TransportStatusPayload): boolean {
  return mapTransportCategory(input) === 'access_denied';
}

/** Categories that warrant a "connectivity degraded" warn-level log line. */
export const DEGRADED_TRANSPORT_CATEGORIES: ReadonlySet<TransportCategory> = new Set([
  'network_down',
  'network_issues',
  'timeout',
  'malformed_response',
]);

/** Categories that warrant a "connectivity restored" info-level log line. */
export const RESTORED_TRANSPORT_CATEGORIES: ReadonlySet<TransportCategory> = new Set([
  'reconnected',
]);

/** Categories that force-terminate a stream subscription. */
export const FATAL_TRANSPORT_CATEGORIES: ReadonlySet<TransportCategory> = new Set([
  'access_denied',
  'bad_request',
]);

/**
 * Neutral operation labels surfaced through user-facing logs. The SDK
 * maps the underlying realtime transport's internal operation strings
 * (e.g. `PNSubscribeOperation`) into this small enum so the user-visible
 * surface stays implementation-agnostic. "other" is the catch-all for
 * operations the SDK does not specifically classify.
 */
export type TransportOperation =
  | 'subscribe'
  | 'heartbeat'
  | 'publish'
  | 'history'
  | 'presence'
  | 'other';

const OPERATION_MAP: Readonly<Record<string, TransportOperation>> = {
  PNSubscribeOperation: 'subscribe',
  PNHeartbeatOperation: 'heartbeat',
  PNPublishOperation: 'publish',
  PNSignalOperation: 'publish',
  PNHistoryOperation: 'history',
  PNFetchMessagesOperation: 'history',
  PNMessageCountsOperation: 'history',
  PNHereNowOperation: 'presence',
  PNWhereNowOperation: 'presence',
  PNGetStateOperation: 'presence',
  PNSetStateOperation: 'presence',
};

export function mapTransportOperation(raw: string | undefined): TransportOperation {
  if (raw === undefined) return 'other';
  return OPERATION_MAP[raw] ?? 'other';
}
