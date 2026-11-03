import { describe, expect, it } from 'vitest';
import { getTestTimeout } from '../../helpers/live-test-config.js';
import {
  artifactText,
  echoReply,
  liveGuard,
  recordBackendCalls,
  sessionsFlagOn,
  TURN_TIMEOUT_MS,
  uniqueAgentName,
  withRoutedAgent,
} from './session-harness.js';
import { loadSessionSdk } from './session-sdk.js';

const INPUTS = ['first', 'second', 'third'];

describe.skipIf(liveGuard || !sessionsFlagOn)('session multi-exchange (live)', () => {
  it(
    'three sends settle three correlated outcomes in order over one session open',
    async () => {
      const sdk = await loadSessionSdk();
      await withRoutedAgent(
        sdk,
        { agentName: uniqueAgentName('sess_multi') },
        async ({ provider, client }) => {
          const probeExchanges = provider.exchanges.length;
          const recorder = recordBackendCalls();
          try {
            const session = await client.openSession(provider.agentName);
            const exchanges = INPUTS.map((input) => session.send(input));
            const settledOrder: string[] = [];
            const outcomes = await Promise.all(
              exchanges.map(async (ex) => {
                const outcome = await ex.waitForOutcome(TURN_TIMEOUT_MS);
                settledOrder.push(outcome.exchangeId);
                return outcome;
              }),
            );
            await session.close();

            const sentIds = exchanges.map((ex) => ex.exchangeId);
            expect(new Set(sentIds).size).toBe(INPUTS.length);
            expect(outcomes.map((o) => o.exchangeId)).toEqual(sentIds);
            expect(outcomes.map((o) => o.state)).toEqual(INPUTS.map(() => 'completed'));
            expect(outcomes.map((o) => o.artifacts.map(artifactText))).toEqual(
              INPUTS.map((input) => [echoReply(input)]),
            );
            expect(settledOrder).toEqual(sentIds);

            const served = provider.exchanges.slice(probeExchanges);
            expect(served.map((e) => e.exchangeId)).toEqual(sentIds);
            expect(new Set(served.map((e) => e.sessionId))).toEqual(new Set([session.sessionId]));
            expect(session.state).toBe('closed');

            const opens = recorder.calls.filter(
              (c) => c.method === 'POST' && c.path === '/api/v1/sessions',
            );
            expect(opens).toHaveLength(1);
          } finally {
            recorder.restore();
          }
        },
      );
    },
    getTestTimeout(90_000),
  );
});
