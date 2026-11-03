import { describe, expect, it } from 'vitest';
import { getTestTimeout } from '../../helpers/live-test-config.js';
import {
  artifactText,
  echoReply,
  liveGuard,
  sessionsFlagOn,
  uniqueAgentName,
  withRoutedAgent,
} from './session-harness.js';
import { loadSessionSdk } from './session-sdk.js';

describe.skipIf(liveGuard || !sessionsFlagOn)('session call() (live)', () => {
  it(
    'one-shot call to a free agent settles exactly one completed outcome and closes its session',
    async () => {
      const sdk = await loadSessionSdk();
      await withRoutedAgent(
        sdk,
        { agentName: uniqueAgentName('sess_call') },
        async ({ provider, client }) => {
          const probeExchanges = provider.exchanges.length;

          const outcome = await client.call(provider.agentName, 'hello');

          expect(outcome.state).toBe('completed');
          expect(outcome.error).toBeUndefined();
          expect(outcome.artifacts.map(artifactText)).toEqual([echoReply('hello')]);

          const served = provider.exchanges.slice(probeExchanges);
          expect(served).toHaveLength(1);
          expect(served[0].exchangeId).toBe(outcome.exchangeId);
          const { sessionId } = served[0];
          // call() resolves on the outcome, which can precede the provider observing session_close.
          await expect
            .poll(() => provider.sessionsClosed.filter((c) => c.sessionId === sessionId), {
              timeout: 10_000,
            })
            .toEqual([{ sessionId, reason: 'consumer_closed' }]);
        },
      );
    },
    getTestTimeout(90_000),
  );
});
