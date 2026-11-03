import type { ChildProcess } from 'node:child_process';
import { afterAll, afterEach, describe, expect, it } from 'vitest';
import {
  createConsumer,
  decodeEcho,
  errorCode,
  liveGuard,
  sessionsFlagOn,
  openSessionWhenRouted,
  publishSessionAgent,
  removeAgents,
} from '../session-harness.js';
import { type BlocksClient, loadSessionSdk } from '../session-sdk.js';
import { killChild, recordStates, spawnProvider, waitForChildMessage } from './child-provider.js';

// route_unhealthy within 120 s of the subscribe socket dying.
const ZOMBIE_DETECTION_BOUND_MS = 120_000;

describe.skipIf(liveGuard || !sessionsFlagOn)(
  'zombie route: heartbeats alive, delivery dead (live)',
  () => {
    const agents: string[] = [];
    let provider: ChildProcess | undefined;
    let client: BlocksClient | undefined;

    afterEach(async () => {
      killChild(provider);
      await client?.destroy?.();
    });
    afterAll(() => removeAgents(agents));

    it(
      'fails open sessions with route_unhealthy and routes no new session to it',
      async () => {
        const sdk = await loadSessionSdk();
        const agentName = await publishSessionAgent('sess_zombie', agents);
        provider = await spawnProvider(agentName, 'zombie');
        client = await createConsumer(sdk);

        const session = await openSessionWhenRouted(client, agentName);
        const states = recordStates(session);
        const healthy = await session.send('before').waitForOutcome(30_000);
        expect(healthy.state).toBe('completed');
        expect(decodeEcho(healthy.artifacts[0]).text).toBe('before');

        const dark = waitForChildMessage(provider, 'dark');
        provider.send('go-dark');
        await dark;
        const darkAt = Date.now();

        const stranded = await session.send('after').waitForOutcome(ZOMBIE_DETECTION_BOUND_MS);
        expect(Date.now() - darkAt).toBeLessThanOrEqual(ZOMBIE_DETECTION_BOUND_MS);
        expect(stranded).toMatchObject({ state: 'failed', error: 'route_unhealthy' });
        expect(session.state).toBe('failed');
        expect(states.filter((s) => s === 'failed')).toHaveLength(1);

        const refused = await client.openSession(agentName).catch((err: unknown) => err);
        expect(errorCode(refused)).toBe('SESSION_NO_ROUTE');
      },
      ZOMBIE_DETECTION_BOUND_MS + 90_000,
    );
  },
);
