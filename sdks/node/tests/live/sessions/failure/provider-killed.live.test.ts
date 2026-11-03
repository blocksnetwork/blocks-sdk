import type { ChildProcess } from 'node:child_process';
import { afterAll, afterEach, describe, expect, it } from 'vitest';
import {
  createConsumer,
  liveGuard,
  sessionsFlagOn,
  openSessionWhenRouted,
  publishSessionAgent,
  removeAgents,
} from '../session-harness.js';
import { type BlocksClient, loadSessionSdk } from '../session-sdk.js';
import { firstProgress, killChild, recordStates, spawnProvider } from './child-provider.js';

// route_lost within 15 s of the route's last acknowledged batch.
const ROUTE_LOSS_BOUND_MS = 15_000;

describe.skipIf(liveGuard || !sessionsFlagOn)('provider process killed mid-session (live)', () => {
  const agents: string[] = [];
  let provider: ChildProcess | undefined;
  let client: BlocksClient | undefined;

  afterEach(async () => {
    killChild(provider);
    await client?.destroy?.();
  });
  afterAll(() => removeAgents(agents));

  it(
    'a SIGKILLed provider fails the session with route_lost within the route-loss bound',
    async () => {
      const sdk = await loadSessionSdk();
      const agentName = await publishSessionAgent('sess_kill', agents);
      provider = await spawnProvider(agentName, 'hang');
      client = await createConsumer(sdk);

      const session = await openSessionWhenRouted(client, agentName);
      const states = recordStates(session);
      const exchange = session.send('never finishes');
      await firstProgress(exchange);

      provider.kill('SIGKILL');
      const killedAt = Date.now();
      const outcome = await exchange.waitForOutcome(ROUTE_LOSS_BOUND_MS * 2);
      const elapsed = Date.now() - killedAt;

      expect(outcome).toMatchObject({
        exchangeId: exchange.exchangeId,
        state: 'failed',
        error: 'route_lost',
      });
      expect(elapsed).toBeLessThanOrEqual(ROUTE_LOSS_BOUND_MS);
      expect(session.state).toBe('failed');
      expect(states.filter((s) => s === 'failed')).toHaveLength(1);
    },
    ROUTE_LOSS_BOUND_MS * 2 + 60_000,
  );
});
