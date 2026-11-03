import type { ChildProcess } from 'node:child_process';
import { afterAll, afterEach, describe, expect, it } from 'vitest';
import {
  createConsumer,
  decodeEcho,
  liveGuard,
  sessionsFlagOn,
  openSessionWhenRouted,
  publishSessionAgent,
  removeAgents,
  sleep,
} from '../session-harness.js';
import { type ArtifactRef, type BlocksClient, loadSessionSdk } from '../session-sdk.js';
import {
  firstProgress,
  killChild,
  recordStates,
  spawnProvider,
  TICK_COUNT,
  TICK_INTERVAL_MS,
} from './child-provider.js';
import { goDark, installBlackhole, restoreNetwork } from './network-blackhole.js';

// Severs the subscribe long-poll, yet ends while the provider is still ticking.
const BLIP_MS = 4 * TICK_INTERVAL_MS;
const EXCHANGE_BUDGET_MS = TICK_COUNT * TICK_INTERVAL_MS + 30_000;

describe.skipIf(liveGuard || !sessionsFlagOn)('consumer network blip mid-exchange (live)', () => {
  const agents: string[] = [];
  let provider: ChildProcess | undefined;
  let client: BlocksClient | undefined;

  afterEach(async () => {
    restoreNetwork();
    killChild(provider);
    await client?.destroy?.();
  });
  afterAll(() => removeAgents(agents));

  it(
    'recovers on the same session and delivers each progress event and the outcome once',
    async () => {
      installBlackhole();
      const sdk = await loadSessionSdk();
      const agentName = await publishSessionAgent('sess_blip', agents);
      provider = await spawnProvider(agentName, 'ticker');
      client = await createConsumer(sdk);

      const session = await openSessionWhenRouted(client, agentName);
      const { sessionId } = session;
      const states = recordStates(session);
      const exchange = session.send('ride it out');
      const progress: string[] = [];
      const artifacts: ArtifactRef[] = [];
      exchange.onProgress((ev) => progress.push(ev.message ?? ''));
      exchange.onArtifact((ref) => artifacts.push(ref));

      await firstProgress(exchange);
      goDark();
      await sleep(BLIP_MS);
      restoreNetwork();

      const outcome = await exchange.waitForOutcome(EXCHANGE_BUDGET_MS);
      expect(outcome.state).toBe('completed');
      expect(progress).toEqual(Array.from({ length: TICK_COUNT }, (_, i) => `tick-${i + 1}`));
      expect(artifacts).toHaveLength(1);
      expect(outcome.artifacts).toHaveLength(1);
      expect(decodeEcho(outcome.artifacts[0]).exchangeId).toBe(exchange.exchangeId);

      const next = await session.send('still here').waitForOutcome(EXCHANGE_BUDGET_MS);
      expect(next.state).toBe('completed');
      expect(session.sessionId).toBe(sessionId);
      expect(states).not.toContain('failed');
      expect(progress).toHaveLength(TICK_COUNT);
    },
    2 * EXCHANGE_BUDGET_MS + 60_000,
  );
});
