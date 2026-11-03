// Run against a backend with the session flag off and SESSIONS_FLAG_ON unset.
import { describe, expect } from 'vitest';
import { getBaseUrl, getStandardHeaders, getTestTimeout } from '../../helpers/live-test-config.js';
import { pendingUntil } from './pending.js';
import {
  type BackendCall,
  createConsumer,
  isSessionEndpoint,
  liveGuard,
  recordBackendCalls,
  routeRequest,
  sessionsFlagOn,
  startTestProvider,
  uniqueAgentName,
} from './session-harness.js';
import { type BlocksClient, loadSessionSdk, type SessionSdk } from './session-sdk.js';

// Unknown IDs, so a flag-on backend answers SESSION_NOT_FOUND instead of passing.
const UNKNOWN_SESSION_ID = 'sessionsdisabledprobe';
const UNKNOWN_ROUTE_ID = 'routesdisabledprobe';

const CONSUMER_ENDPOINTS: Array<[method: string, path: string, body?: object]> = [
  ['GET', `/api/v1/sessions/${UNKNOWN_SESSION_ID}`],
  ['POST', `/api/v1/sessions/${UNKNOWN_SESSION_ID}/token`, {}],
  ['POST', `/api/v1/sessions/${UNKNOWN_SESSION_ID}/beacons`, { beacon: 'opened' }],
];

const PROVIDER_ENDPOINTS: Array<[method: string, path: string, body?: object]> = [
  ['POST', `/api/v1/provider/routes/${UNKNOWN_ROUTE_ID}/token`, {}],
  [
    'POST',
    '/api/v1/provider/checkpoints',
    {
      routeId: UNKNOWN_ROUTE_ID,
      idempotencyKey: 'sessions-disabled-probe',
      openSessions: [],
      events: [],
      probeEchoes: [],
    },
  ],
  ['GET', `/api/v1/provider/sessions/${UNKNOWN_SESSION_ID}`],
];

function authorizationFor(calls: BackendCall[], method: string, path: string): string {
  const auth = calls.find((c) => c.method === method && c.path === path)?.authorization;
  if (!auth) throw new Error(`the SDK sent no authorized ${method} ${path}`);
  return auth;
}

async function callDirect(
  authorization: string,
  [method, path, body]: [string, string, object?],
): Promise<void> {
  await fetch(`${getBaseUrl()}${path}`, {
    method,
    headers: { ...getStandardHeaders(), Authorization: authorization },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
}

async function expectOpenRefused(sdk: SessionSdk, consumer: BlocksClient, agentName: string) {
  for (const [name, attempt] of [
    ['openSession', () => consumer.openSession(agentName)],
    ['call', () => consumer.call(agentName, 'hello')],
  ] as const) {
    const err = await attempt().then(
      () => new Error(`${name} resolved with sessions disabled`),
      (e: unknown) => e,
    );
    expect(err, name).toBeInstanceOf(sdk.SessionsDisabledError);
    expect(err, name).toBeInstanceOf(sdk.SessionError);
    expect((err as { code?: string }).code, name).toBe('SESSIONS_DISABLED');
  }
}

// Every endpoint was hit, and each answered 404 SESSIONS_DISABLED without minting credentials.
function expectEveryEndpointRefused(sessionCalls: BackendCall[]) {
  const expectedEndpoints = [
    ['POST', '/api/v1/provider/routes'],
    ['POST', '/api/v1/sessions'],
    ...CONSUMER_ENDPOINTS.map(([method, path]) => [method, path]),
    ...PROVIDER_ENDPOINTS.map(([method, path]) => [method, path]),
  ];
  for (const [method, path] of expectedEndpoints) {
    expect(
      sessionCalls.some((c) => c.method === method && c.path === path),
      `${method} ${path} was exercised`,
    ).toBe(true);
  }
  for (const call of sessionCalls) {
    const label = `${call.method} ${call.path}`;
    expect(call.status, label).toBe(404);
    expect(call.body, label).toMatchObject({ code: 'SESSIONS_DISABLED' });
    expect(JSON.stringify(call.body ?? ''), label).not.toMatch(/pamToken|sessionChannel|routeChannel/);
  }
}

describe.skipIf(liveGuard || sessionsFlagOn)('sessions disabled (live, flag off)', () => {
  pendingUntil('session flag')(
    'every session endpoint refuses with SESSIONS_DISABLED and mints no session token',
    async () => {
      const sdk = await loadSessionSdk();
      const recorder = recordBackendCalls();
      let provider: Awaited<ReturnType<typeof startTestProvider>> | undefined;
      let client: BlocksClient | undefined;
      try {
        provider = await startTestProvider(sdk, { agentName: uniqueAgentName('sess_off') });
        await routeRequest(recorder.calls);
        client = await createConsumer(sdk);
        await expectOpenRefused(sdk, client, provider.agentName);

        const consumerAuth = authorizationFor(recorder.calls, 'POST', '/api/v1/sessions');
        const providerAuth = authorizationFor(recorder.calls, 'POST', '/api/v1/provider/routes');
        for (const endpoint of CONSUMER_ENDPOINTS) await callDirect(consumerAuth, endpoint);
        for (const endpoint of PROVIDER_ENDPOINTS) await callDirect(providerAuth, endpoint);

        expectEveryEndpointRefused(recorder.calls.filter(isSessionEndpoint));
        expect(provider.sessionsOpened).toEqual([]);
      } finally {
        recorder.restore();
        client?.destroy();
        await provider?.stop();
      }
    },
    getTestTimeout(90_000),
  );
});
