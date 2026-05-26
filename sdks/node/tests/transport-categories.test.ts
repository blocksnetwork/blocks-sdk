import { describe, it, expect } from 'vitest';
import {
  mapTransportCategory,
  mapTransportOperation,
  DEGRADED_TRANSPORT_CATEGORIES,
  RESTORED_TRANSPORT_CATEGORIES,
  FATAL_TRANSPORT_CATEGORIES,
  type TransportCategory,
} from '../src/runtime/transport-categories.js';

describe('mapTransportCategory', () => {
  it.each<[string, TransportCategory]>([
    ['PNConnectedCategory', 'connected'],
    ['PNReconnectedCategory', 'reconnected'],
    ['PNNetworkDownCategory', 'network_down'],
    ['PNNetworkIssuesCategory', 'network_issues'],
    ['PNTimeoutCategory', 'timeout'],
    ['PNMalformedResponseCategory', 'malformed_response'],
    ['PNAccessDeniedCategory', 'access_denied'],
    ['PNBadRequestCategory', 'bad_request'],
  ])('maps %s -> %s', (raw, expected) => {
    expect(mapTransportCategory(raw)).toBe(expected);
  });

  it.each(['', 'PNUnknownCategory', 'PNCancelledCategory', 'garbage'])(
    'falls back to "other" for unknown input %s',
    (raw) => {
      expect(mapTransportCategory(raw)).toBe('other');
    },
  );
});

describe('category sets', () => {
  it('DEGRADED_TRANSPORT_CATEGORIES covers the four warn-level categories', () => {
    expect([...DEGRADED_TRANSPORT_CATEGORIES].sort()).toEqual(
      ['malformed_response', 'network_down', 'network_issues', 'timeout'].sort(),
    );
  });

  it('RESTORED_TRANSPORT_CATEGORIES contains only "reconnected"', () => {
    expect([...RESTORED_TRANSPORT_CATEGORIES]).toEqual(['reconnected']);
  });

  it('FATAL_TRANSPORT_CATEGORIES contains access_denied and bad_request', () => {
    expect([...FATAL_TRANSPORT_CATEGORIES].sort()).toEqual(
      ['access_denied', 'bad_request'].sort(),
    );
  });
});

describe('mapTransportOperation', () => {
  it.each([
    ['PNSubscribeOperation', 'subscribe'],
    ['PNHeartbeatOperation', 'heartbeat'],
    ['PNPublishOperation', 'publish'],
    ['PNSignalOperation', 'publish'],
    ['PNHistoryOperation', 'history'],
    ['PNFetchMessagesOperation', 'history'],
    ['PNMessageCountsOperation', 'history'],
    ['PNHereNowOperation', 'presence'],
    ['PNWhereNowOperation', 'presence'],
    ['PNGetStateOperation', 'presence'],
    ['PNSetStateOperation', 'presence'],
  ])('maps %s to %s', (raw, expected) => {
    expect(mapTransportOperation(raw)).toBe(expected);
  });

  it('falls back to "other" for unknown operations', () => {
    expect(mapTransportOperation('PNCompletelyMadeUpOperation')).toBe('other');
    expect(mapTransportOperation('')).toBe('other');
  });

  it('is undefined-tolerant — returns "other" when raw is undefined', () => {
    expect(mapTransportOperation(undefined)).toBe('other');
  });
});

describe('mapTransportCategory — Event Engine wrapper unwrap', () => {
  it.each<[{ category: string; error?: unknown; statusCode?: unknown }, TransportCategory]>([
    // PNConnectionErrorCategory wrapper — string leaf in `error`
    [{ category: 'PNConnectionErrorCategory', error: 'PNAccessDeniedCategory' }, 'access_denied'],
    [{ category: 'PNConnectionErrorCategory', error: 'PNNetworkIssuesCategory' }, 'network_issues'],
    [{ category: 'PNConnectionErrorCategory', error: 'PNTimeoutCategory' }, 'timeout'],
    [{ category: 'PNConnectionErrorCategory', error: 'PNBadRequestCategory' }, 'bad_request'],
    [{ category: 'PNConnectionErrorCategory', error: 'PNMalformedResponseCategory' }, 'malformed_response'],
    // PNDisconnectedUnexpectedlyCategory wrapper — same shape
    [{ category: 'PNDisconnectedUnexpectedlyCategory', error: 'PNTimeoutCategory' }, 'timeout'],
    [{ category: 'PNDisconnectedUnexpectedlyCategory', error: 'PNNetworkIssuesCategory' }, 'network_issues'],
    [{ category: 'PNDisconnectedUnexpectedlyCategory', error: 'PNAccessDeniedCategory' }, 'access_denied'],
  ])('unwraps %j -> %s', (payload, expected) => {
    expect(mapTransportCategory(payload)).toBe(expected);
  });

  it('returns "other" when the wrapper has no nested error', () => {
    expect(mapTransportCategory({ category: 'PNConnectionErrorCategory' })).toBe('other');
    expect(mapTransportCategory({ category: 'PNDisconnectedUnexpectedlyCategory', error: undefined })).toBe('other');
  });

  it('returns "other" when the wrapper has a non-string nested error (boolean, object, null)', () => {
    expect(mapTransportCategory({ category: 'PNConnectionErrorCategory', error: true })).toBe('other');
    expect(mapTransportCategory({ category: 'PNConnectionErrorCategory', error: {} })).toBe('other');
    expect(mapTransportCategory({ category: 'PNConnectionErrorCategory', error: null })).toBe('other');
  });

  it('returns "other" when the wrapper nests an unknown leaf string', () => {
    expect(mapTransportCategory({ category: 'PNConnectionErrorCategory', error: 'PNFutureCategory' })).toBe('other');
  });

  it('does NOT unwrap non-wrapper outer categories — leaf wins', () => {
    // If the outer category is itself a known leaf, the nested `error` is ignored.
    expect(mapTransportCategory({ category: 'PNAccessDeniedCategory', error: 'PNTimeoutCategory' })).toBe('access_denied');
    expect(mapTransportCategory({ category: 'PNTimeoutCategory', error: 'PNAccessDeniedCategory' })).toBe('timeout');
  });

  it('treats statusCode 403 as access_denied even with an unmapped category', () => {
    expect(mapTransportCategory({ category: 'PNFutureUnknownCategory', statusCode: 403 })).toBe('access_denied');
    expect(mapTransportCategory({ category: 'PNConnectionErrorCategory', statusCode: 403 })).toBe('access_denied');
  });

  it('treats statusCode 400 as bad_request even with an unmapped category', () => {
    expect(mapTransportCategory({ category: 'PNFutureUnknownCategory', statusCode: 400 })).toBe('bad_request');
  });
});

describe('mapTransportCategory — backwards-compatible string form', () => {
  // String form is the old call shape; the rewrite must keep accepting it.
  it('accepts a bare string and behaves as before', () => {
    expect(mapTransportCategory('PNAccessDeniedCategory')).toBe('access_denied');
    expect(mapTransportCategory('PNUnknownLeaf')).toBe('other');
    expect(mapTransportCategory('')).toBe('other');
  });
});
