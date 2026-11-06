import { performance } from 'node:perf_hooks';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import {
  type BackendCall,
  createConsumer,
  deleteAgent,
  isSessionEndpoint,
  liveGuard,
  openSessionWhenRouted,
  recordBackendCalls,
  routeRequest,
  sessionsFlagOn,
  sleep,
  startTestProvider,
  type TestProvider,
  uniqueAgentName,
} from '../session-harness.js';
import { pendingUntil } from '../pending.js';
import { type BlocksClient, loadSessionSdk, type SessionSdk } from '../session-sdk.js';
import { answered, type Credential, type Keys, readVerdict, report, sessionKeys } from './gate-harness.js';

// PAM refuses a revoked token within a minute, as it caches a token's non-revoked state that long.
// Each tolerance adds what runs before the revocation call: the provider's checkpoint reports the
// close (one 2 s flush), the backend's revocation pass runs (every 5 s), and a stopped route is
// lost after at most 15 s. Deleting an agent ends its scope at once.
const PAM_REVOCATION_MS = 60_000;
const CHECKPOINT_FLUSH_MS = 2_000;
const REVOCATION_PASS_MS = 5_000;
const ROUTE_LOSS_MS = 15_000;
const PROBE_INTERVAL_MS = 1_000;
// After the first refusal, the SDK's reaction is watched this long. Its route registration backs
// off from 1 s, doubling, so a storm shows as many failed backend calls inside the window.
const STORM_WINDOW_MS = 30_000;
const STORM_MAX_ERRORS = 10;

interface Drill {
  tokenClass: 'consumer' | 'route';
  trigger: string;
  credential: Credential;
  boundMs: number;
}

interface Measured {
  /** From the trigger to PAM's first refusal; null if it never refused within the bound. */
  firstDeniedMs: number | null;
  deniedAt: number;
}

async function firstRefusal(keys: Keys, drill: Drill, triggeredAt: number): Promise<Measured> {
  const deadline = triggeredAt + drill.boundMs;
  for (;;) {
    if ((await readVerdict(keys, drill.credential)) === 'denied') {
      const deniedAt = performance.now();
      return { firstDeniedMs: deniedAt - triggeredAt, deniedAt };
    }
    const remaining = deadline - performance.now();
    if (remaining <= 0) return { firstDeniedMs: null, deniedAt: Number.NaN };
    // The last wait ends at the deadline, so a refusal just before it is still probed.
    await sleep(Math.min(PROBE_INTERVAL_MS, remaining));
  }
}

/** Failed backend calls the SDK made in the storm window after the first refusal. */
const failedAfter = (calls: BackendCall[], from: number) =>
  calls.filter((c) => c.at >= from && c.at <= from + STORM_WINDOW_MS && c.status >= 400);

// The storm limit holds the session runtime's reaction to a revoked token. Other failed calls,
// such as the instance re-registering a deleted agent, are reported beside it.
const sessionErrorsAfter = (calls: BackendCall[], from: number) =>
  failedAfter(calls, from).filter(isSessionEndpoint).length;

const REFRESH_PATH: Record<Drill['tokenClass'], RegExp> = {
  consumer: /^\/api\/v1\/sessions\/[^/]+\/token$/,
  route: /^\/api\/v1\/provider\/routes\/[^/]+\/token$/,
};

const refreshesAfter = (calls: BackendCall[], from: number, tokenClass: Drill['tokenClass']) =>
  calls.filter((c) => c.at >= from && c.method === 'POST' && REFRESH_PATH[tokenClass].test(c.path))
    .length;

const failedCalls: string[] = [];

/** Measures every drill sharing one trigger, then holds each to its bound and the storm limit. */
async function runDrills(
  keys: Keys,
  calls: BackendCall[],
  drills: Drill[],
  trigger: () => Promise<void>,
  holdStormLimit = true,
): Promise<void> {
  for (const drill of drills) expect(await readVerdict(keys, drill.credential)).toBe('granted');
  const triggeredAt = performance.now();
  await trigger();
  const measured = await Promise.all(drills.map((d) => firstRefusal(keys, d, triggeredAt)));
  const lastDenial = Math.max(...measured.map((m) => m.deniedAt).filter(Number.isFinite));
  if (Number.isFinite(lastDenial)) await sleep(STORM_WINDOW_MS);

  drills.forEach((drill, i) => {
    const { firstDeniedMs, deniedAt } = measured[i];
    const failed = Number.isFinite(deniedAt) ? failedAfter(calls, deniedAt) : [];
    const session = failed.filter(isSessionEndpoint).length;
    const seconds = (ms: number | null) => (ms === null ? 'never' : (ms / 1000).toFixed(1));
    report('token-revocation',
      `| ${drill.tokenClass} | ${drill.trigger} | ${seconds(firstDeniedMs)} | ${seconds(drill.boundMs)} | ${session} | ${failed.length - session} | ${refreshesAfter(calls, triggeredAt, drill.tokenClass)} |`,
    );
  });
  const failures = new Map<string, number>();
  // With no refusal, every deniedAt is NaN; the trigger then starts the window.
  const firstDenial = Math.min(...measured.map((m) => m.deniedAt).filter(Number.isFinite));
  const windowStart = Number.isFinite(firstDenial) ? firstDenial : triggeredAt;
  for (const call of failedAfter(calls, windowStart)) {
    const key = `${call.method} ${call.path.replace(/\/[A-Za-z0-9_-]{16,}(?=\/|$)/g, '/{id}')} ${call.status}`;
    failures.set(key, (failures.get(key) ?? 0) + 1);
  }
  for (const [key, count] of failures) failedCalls.push(`${drills[0].trigger}: \`${key}\` x${count}`);
  // The SDK refreshes a denied token once per connection (refresh-then-fail).
  for (const drill of drills) {
    expect(refreshesAfter(calls, triggeredAt, drill.tokenClass), drill.trigger).toBeLessThanOrEqual(1);
  }
  measured.forEach(({ firstDeniedMs, deniedAt }, i) => {
    const label = `${drills[i].tokenClass} after ${drills[i].trigger}`;
    expect(firstDeniedMs, label).not.toBeNull();
    // The last probe can answer after the deadline; a refusal that late is outside the tolerance.
    expect(firstDeniedMs ?? Number.POSITIVE_INFINITY, label).toBeLessThanOrEqual(drills[i].boundMs);
    if (holdStormLimit) {
      expect(sessionErrorsAfter(calls, deniedAt)).toBeLessThanOrEqual(STORM_MAX_ERRORS);
    }
  });
}

function credentials(calls: BackendCall[]): { consumer: Credential; route: Credential } {
  const opened = answered(calls, 'POST', '/api/v1/sessions')[0].body as Record<string, string>;
  const route = answered(calls, 'POST', '/api/v1/provider/routes')[0].body as Record<string, string>;
  return {
    consumer: {
      pamToken: opened.pamToken,
      uuid: opened.consumerTransportUuid,
      channel: opened.sessionChannel,
    },
    // Reads the route's own session channel, which its pattern covers.
    route: {
      pamToken: route.pamToken,
      uuid: route.providerTransportUuid,
      channel: opened.sessionChannel,
    },
  };
}

async function withOpenSession(
  sdk: SessionSdk,
  prefix: string,
  run: (env: {
    keys: Keys;
    calls: BackendCall[];
    tokens: { consumer: Credential; route: Credential };
    close: () => Promise<void>;
    stopProvider: () => Promise<void>;
    deleteAgent: () => Promise<void>;
  }) => Promise<void>,
): Promise<void> {
  const recorder = recordBackendCalls();
  let provider: TestProvider | undefined;
  let client: BlocksClient | undefined;
  try {
    provider = await startTestProvider(sdk, { agentName: uniqueAgentName(prefix) });
    client = await createConsumer(sdk);
    const started = provider;
    const session = await openSessionWhenRouted(client, started.agentName);
    await routeRequest(recorder.calls);
    await run({
      keys: await sessionKeys(),
      calls: recorder.calls,
      tokens: credentials(recorder.calls),
      close: () => session.close(),
      stopProvider: async () => started.halt(),
      // Throws, unlike cleanup: a failed delete must not read as a token PAM never refused.
      deleteAgent: async () => {
        await deleteAgent(started.agentName);
      },
    });
  } finally {
    recorder.restore();
    client?.destroy();
    await provider?.stop();
  }
}

async function agentDeletedDrill(holdStormLimit: boolean): Promise<void> {
  const sdk = await loadSessionSdk();
  await withOpenSession(sdk, 'sess_revoke_del', async ({ keys, calls, tokens, deleteAgent }) => {
    const boundMs = REVOCATION_PASS_MS + PAM_REVOCATION_MS;
    await runDrills(
      keys,
      calls,
      [
        { tokenClass: 'consumer', trigger: 'agent deleted (open session)', credential: tokens.consumer, boundMs },
        { tokenClass: 'route', trigger: 'agent deleted (provider running)', credential: tokens.route, boundMs },
      ],
      deleteAgent,
      holdStormLimit,
    );
  });
}

describe.skipIf(liveGuard || !sessionsFlagOn)('session token revocation drill (live)', () => {
  beforeAll(() => {
    report('token-revocation', '\n### Revocation drill\n');
    report('token-revocation',
      '| Class | Trigger | First 403 after trigger (s) | Test tolerance (s) | Failed session calls, 30 s after it | Other failed backend calls, same window | Token refreshes after trigger |',
    );
    report('token-revocation', '|---|---|---|---|---|---|---|');
  });

  afterAll(() => {
    if (failedCalls.length === 0) return;
    report('token-revocation', '\nFailed backend calls after the first refusal:\n');
    for (const line of failedCalls) report('token-revocation', `- ${line}`);
  });

  it(
    'a consumer token is refused once its session closes, and its route token is not',
    async () => {
      const sdk = await loadSessionSdk();
      await withOpenSession(sdk, 'sess_revoke_close', async ({ keys, calls, tokens, close }) => {
        await runDrills(
          keys,
          calls,
          [
            {
              tokenClass: 'consumer',
              trigger: 'session closed',
              credential: tokens.consumer,
              boundMs: CHECKPOINT_FLUSH_MS + REVOCATION_PASS_MS + PAM_REVOCATION_MS,
            },
          ],
          close,
        );
        expect(await readVerdict(keys, tokens.route)).toBe('granted');
      });
    },
    3 * 60_000,
  );

  it(
    'consumer and route tokens are refused once their provider stops',
    async () => {
      const sdk = await loadSessionSdk();
      await withOpenSession(sdk, 'sess_revoke_stop', async ({ keys, calls, tokens, stopProvider }) => {
        const boundMs = ROUTE_LOSS_MS + REVOCATION_PASS_MS + PAM_REVOCATION_MS;
        await runDrills(
          keys,
          calls,
          [
            { tokenClass: 'consumer', trigger: 'route lost (open session)', credential: tokens.consumer, boundMs },
            { tokenClass: 'route', trigger: 'provider stopped', credential: tokens.route, boundMs },
          ],
          stopProvider,
        );
      });
    },
    3 * 60_000,
  );

  it(
    'consumer and route tokens are refused once their agent is deleted under a running provider',
    () => agentDeletedDrill(false),
    3 * 60_000,
  );

  // A provider whose agent was deleted posts a checkpoint every flush tick, each refused 401, with
  // no backoff. Un-pend once the Node and Python runtimes end the route on that 401, or back off.
  pendingUntil('checkpoint backoff after the agent is deleted')(
    'an agent deleted under a running provider makes no error storm',
    () => agentDeletedDrill(true),
    3 * 60_000,
  );
});
