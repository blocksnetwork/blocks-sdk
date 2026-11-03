import { describe, expect, it } from 'vitest';
import { ProviderSessionApi } from '../src/runtime/sessions/provider-api.js';
import { manifest, routeGrant } from './helpers/session-provider-fakes.js';

const lookup = (body: unknown) =>
  new ProviderSessionApi('http://backend.test', async () =>
    new Response(JSON.stringify(body), { status: 200 }),
  ).getSession('s1');

describe('provider session lookup', () => {
  it.each([
    ['an org', 'org-1'],
    ['no org', null],
  ])('accepts a session opened by a caller with %s', async (_label, orgId) => {
    await expect(lookup({ ...manifest('s1'), orgId })).resolves.toMatchObject({ orgId });
  });

  it.each([
    ['missing', undefined],
    ['empty', ''],
    ['not a string', 42],
  ])('rejects a manifest whose orgId is %s', async (_label, orgId) => {
    await expect(lookup({ ...manifest('s1'), orgId })).rejects.toThrow('incomplete manifest');
  });

  it('joins a baseUrl with a trailing slash without doubling it', async () => {
    const urls: string[] = [];
    const api = new ProviderSessionApi('http://backend.test/', async (url) => {
      urls.push(url);
      return new Response(JSON.stringify(manifest('s1')), { status: 200 });
    });
    await api.getSession('s1');
    expect(urls).toEqual(['http://backend.test/api/v1/provider/sessions/s1']);
  });
});

const answering = (body: unknown) =>
  new ProviderSessionApi('http://backend.test', async () =>
    new Response(JSON.stringify(body), { status: 200 }),
  );

describe('provider route registration', () => {
  it('refuses a route without the backend UUID that backend notices come from', async () => {
    const { backendTransportUuid: _omitted, ...grant } = routeGrant();
    await expect(answering(grant).registerRoute('i-1', 1)).rejects.toThrow(
      'route is missing backendTransportUuid',
    );
  });
});

describe('provider checkpoint acknowledgement', () => {
  const batch = {
    routeId: 'route1',
    idempotencyKey: 'k1',
    openSessions: [],
    events: [],
    probeEchoes: [],
  };

  it('resolves on the acknowledgement of this batch', async () => {
    await expect(
      answering({ routeId: 'route1', idempotencyKey: 'k1' }).postCheckpoint(batch),
    ).resolves.toBeUndefined();
  });

  it.each([
    ['another batch', { routeId: 'route1', idempotencyKey: 'k2' }],
    ['another route', { routeId: 'route2', idempotencyKey: 'k1' }],
    ['no body', ''],
  ])('rejects a 200 that acknowledges %s, so the batch is retried', async (_label, body) => {
    await expect(answering(body).postCheckpoint(batch)).rejects.toThrow(
      'not the acknowledgement of this batch',
    );
  });
});

