import { describe, expect, it } from 'vitest';
import { getBaseUrl } from '../../../helpers/live-test-config.js';
import {
  type BackendCall,
  createConsumer,
  liveGuard,
  openSessionWhenRouted,
  recordBackendCalls,
  routeRequest,
  sessionsFlagOn,
  startTestProvider,
  type TestProvider,
  uniqueAgentName,
} from '../session-harness.js';
import { type BlocksClient, loadSessionSdk, type Session } from '../session-sdk.js';
import {
  answered,
  type Credential,
  type Keys,
  parseToken,
  publishVerdict,
  readVerdict,
  replay,
  report,
  sessionKeys,
  subscribeVerdict,
} from './gate-harness.js';

// Every permission bit parseToken reports; a session token holds read and write and nothing else.
const READ_WRITE = {
  read: true,
  write: true,
  manage: false,
  delete: false,
  get: false,
  update: false,
  join: false,
};
const CONSUMER_TTL_MAX = 60;
const ROUTE_TTL_MAX = 720;
// Backend-minted ids are base64url: no dot, so no channel segment, and no regex metacharacter.
const OPAQUE_ID = /^[A-Za-z0-9_-]+$/;

interface RouteToken extends Credential {
  routeId: string;
}

interface ConsumerToken extends Credential {
  routeId: string;
  sessionId: string;
}

const escapeRegex = (literal: string) => literal.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

function routeTokens(calls: BackendCall[]): RouteToken[] {
  return answered(calls, 'POST', '/api/v1/provider/routes').map((call) => {
    const body = call.body as Record<string, string>;
    return {
      routeId: body.routeId,
      pamToken: body.pamToken,
      uuid: body.providerTransportUuid,
      channel: body.routeChannel,
    };
  });
}

function consumerTokens(calls: BackendCall[]): ConsumerToken[] {
  return answered(calls, 'POST', '/api/v1/sessions').map((call) => {
    const body = call.body as Record<string, string>;
    const [, routeId, sessionId] = body.sessionChannel.split('.');
    return {
      routeId,
      sessionId,
      pamToken: body.pamToken,
      uuid: body.consumerTransportUuid,
      channel: body.sessionChannel,
    };
  });
}

/** The refresh endpoints mint through the same issuers; their tokens are held to the same shape. */
async function reissued(calls: BackendCall[], route: RouteToken, consumer: ConsumerToken) {
  const callFor = (path: string, matches: (body: Record<string, string>) => boolean) => {
    const call = answered(calls, 'POST', path).find((c) => matches(c.body as Record<string, string>));
    if (!call) throw new Error(`no recorded POST ${path} for this token`);
    return call;
  };
  const routeCall = callFor('/api/v1/provider/routes', (b) => b.routeId === route.routeId);
  const openCall = callFor('/api/v1/sessions', (b) => b.sessionChannel === consumer.channel);
  const routeBody = (await replay(
    getBaseUrl(),
    routeCall,
    `/api/v1/provider/routes/${encodeURIComponent(route.routeId)}/token`,
  )) as { pamToken: string };
  const consumerBody = (await replay(
    getBaseUrl(),
    openCall,
    `/api/v1/sessions/${encodeURIComponent(consumer.sessionId)}/token`,
  )) as { pamToken: string };
  return {
    route: { ...route, pamToken: routeBody.pamToken },
    consumer: { ...consumer, pamToken: consumerBody.pamToken },
  };
}

function assertConsumerGrant(keys: Keys, token: ConsumerToken): void {
  const parsed = parseToken(keys, token.pamToken);
  expect(parsed.authorized_uuid).toBe(token.uuid);
  expect(parsed.patterns).toBeUndefined();
  expect(parsed.resources).toEqual({ channels: { [token.channel]: READ_WRITE } });
  expect(parsed.ttl).toBeLessThanOrEqual(CONSUMER_TTL_MAX);
}

function assertRouteGrant(keys: Keys, token: RouteToken, sibling: RouteToken): void {
  const parsed = parseToken(keys, token.pamToken);
  expect(token.routeId).toMatch(OPAQUE_ID);
  expect(parsed.authorized_uuid).toBe(token.uuid);
  expect(parsed.resources).toBeUndefined();
  const pattern = `^sess\\.${escapeRegex(token.routeId)}\\..*$`;
  expect(parsed.patterns).toEqual({ channels: { [pattern]: READ_WRITE } });
  expect(parsed.ttl).toBeLessThanOrEqual(ROUTE_TTL_MAX);

  const reach = new RegExp(pattern);
  const r = token.routeId;
  expect(reach.test(`sess.${r}.s1`)).toBe(true);
  expect(reach.test(`sess.${r}.*`)).toBe(true);
  for (const outside of [
    `sess.${sibling.routeId}.s1`,
    `sess.${r}x.s1`,
    `sess.x${r}.s1`,
    `xsess.${r}.s1`,
    `sess.${r}`,
    'u.org.task',
  ]) {
    expect(reach.test(outside), outside).toBe(false);
  }
}

describe.skipIf(liveGuard || !sessionsFlagOn)('session token grants (live)', () => {
  it(
    'every minted session token is exact or anchored to its own resource, and PAM refuses siblings',
    async () => {
      const sdk = await loadSessionSdk();
      const recorder = recordBackendCalls();
      const providers: TestProvider[] = [];
      const sessions: Session[] = [];
      let client: BlocksClient | undefined;
      try {
        client = await createConsumer(sdk);
        providers.push(await startTestProvider(sdk, { agentName: uniqueAgentName('sess_gate_a') }));
        providers.push(await startTestProvider(sdk, { agentName: uniqueAgentName('sess_gate_b') }));
        sessions.push(await openSessionWhenRouted(client, providers[0].agentName));
        sessions.push(await openSessionWhenRouted(client, providers[0].agentName));
        sessions.push(await openSessionWhenRouted(client, providers[1].agentName));
        await routeRequest(recorder.calls);

        const keys = await sessionKeys();
        const routes = routeTokens(recorder.calls);
        const consumers = consumerTokens(recorder.calls);
        const [a1, a2, b1] = consumers;
        const routeA = routes.find((r) => r.routeId === a1.routeId);
        const routeB = routes.find((r) => r.routeId === b1.routeId);
        if (!routeA || !routeB) throw new Error('a session opened on a route that was not recorded');
        expect(a2.routeId).toBe(routeA.routeId);
        expect(routeB.routeId).not.toBe(routeA.routeId);

        const fresh = await reissued(recorder.calls, routeA, a1);
        for (const token of [...consumers, fresh.consumer]) assertConsumerGrant(keys, token);
        assertRouteGrant(keys, routeA, routeB);
        assertRouteGrant(keys, routeB, routeA);
        assertRouteGrant(keys, fresh.route, routeB);

        // Each denial below is paired with a grant to the same token, so a token PAM refuses
        // outright cannot pass them.
        const probes: Array<[string, Promise<string>, string]> = [
          ['consumer reads its own session', readVerdict(keys, a1), 'granted'],
          ['consumer subscribes to its own session', subscribeVerdict(keys, a1, a1.channel), 'granted'],
          ['consumer reads a sibling session on its route', readVerdict(keys, a1, a2.channel), 'denied'],
          ['consumer reads a session on another route', readVerdict(keys, a1, b1.channel), 'denied'],
          ['consumer publishes to a sibling session', publishVerdict(keys, a1, a2.channel), 'denied'],
          ['consumer publishes to another route', publishVerdict(keys, a1, b1.channel), 'denied'],
          ['consumer subscribes to a sibling session', subscribeVerdict(keys, a1, a2.channel), 'denied'],
          ['consumer subscribes to its route wildcard', subscribeVerdict(keys, a1, routeA.channel), 'denied'],
          ['route reads its own sessions', readVerdict(keys, routeA, a2.channel), 'granted'],
          ['route subscribes to its own wildcard', subscribeVerdict(keys, routeA, routeA.channel), 'granted'],
          ['route reads a sibling route session', readVerdict(keys, routeA, b1.channel), 'denied'],
          ['route publishes to a sibling route session', publishVerdict(keys, routeA, b1.channel), 'denied'],
          ['route subscribes to a sibling route wildcard', subscribeVerdict(keys, routeA, routeB.channel), 'denied'],
          ['route publishes to a routeId-prefixed channel', publishVerdict(keys, routeA, `sess.${routeA.routeId}x.gate`), 'denied'],
          ['route publishes to a non-session channel', publishVerdict(keys, routeA, 'u.gate.probe'), 'denied'],
        ];
        const verdicts = await Promise.all(probes.map(([, verdict]) => verdict));
        report('token-grants', '\n### Token grants\n');
        report('token-grants', '| Probe | Expected | Got |');
        report('token-grants', '|---|---|---|');
        probes.forEach(([name, , expected], i) => report('token-grants', `| ${name} | ${expected} | ${verdicts[i]} |`));
        expect(verdicts).toEqual(probes.map(([, , expected]) => expected));
      } finally {
        recorder.restore();
        await Promise.allSettled(sessions.map((s) => s.close()));
        client?.destroy();
        for (const provider of providers) await provider.stop();
      }
    },
    3 * 60_000,
  );
});
