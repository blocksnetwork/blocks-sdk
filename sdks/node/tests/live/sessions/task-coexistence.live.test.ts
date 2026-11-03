import { describe, expect, it } from 'vitest';
import { TaskClient } from '../../../src/runtime/task-client.js';
import { getBaseUrl, getTestTimeout } from '../../helpers/live-test-config.js';
import {
  apiKey,
  artifactText,
  echoReply,
  liveGuard,
  sessionsFlagOn,
  TURN_TIMEOUT_MS,
  taskReply,
  uniqueAgentName,
  withRoutedAgent,
  openSessionWhenRouted,
} from './session-harness.js';
import { loadSessionSdk } from './session-sdk.js';

const EXCHANGE_INPUT = 'held-exchange';
const TASK_INPUT = 'legacy-task';

describe.skipIf(liveGuard || !sessionsFlagOn)('session and task coexistence (live)', () => {
  it(
    'a request task completes while an exchange on a concurrency-1 instance is in flight',
    async () => {
      const sdk = await loadSessionSdk();
      let taskArrived!: () => void;
      const taskSeen = new Promise<void>((resolve) => {
        taskArrived = resolve;
      });
      let exchangeInFlight = false;
      let taskRanDuringExchange = false;

      await withRoutedAgent(
        sdk,
        {
          agentName: uniqueAgentName('sess_coexist'),
          // One slot: if the open session counted against concurrency, the task could not run.
          concurrency: 1,
          beforeReply: async (text) => {
            if (text !== EXCHANGE_INPUT) return;
            exchangeInFlight = true;
            await Promise.race([taskSeen, new Promise((r) => setTimeout(r, TURN_TIMEOUT_MS / 2))]);
            exchangeInFlight = false;
          },
          onTask: (text) => {
            if (text !== TASK_INPUT) return;
            taskRanDuringExchange = exchangeInFlight;
            taskArrived();
          },
        },
        async ({ provider, client }) => {
          // The harness's probe session holds the one seat until the provider's next checkpoint.
          const session = await openSessionWhenRouted(client, provider.agentName);
          const exchange = session.send(EXCHANGE_INPUT);
          await expect.poll(() => exchangeInFlight, { timeout: TURN_TIMEOUT_MS }).toBe(true);

          const taskClient = await TaskClient.create({
            billingMode: 'free',
            apiKey: apiKey(),
            baseUrl: getBaseUrl(),
          });
          try {
            const task = await taskClient.sendMessage({
              agentName: provider.agentName,
              requestParts: [{ partId: 'request', text: TASK_INPUT }],
            });
            try {
              const terminal = await task.waitForTerminal(TURN_TIMEOUT_MS);
              expect(terminal.state).toBe('completed');
              expect(task.listArtifacts().map(artifactText)).toEqual([taskReply(TASK_INPUT)]);
            } finally {
              task.close();
            }
          } finally {
            taskClient.destroy();
          }

          const outcome = await exchange.waitForOutcome(TURN_TIMEOUT_MS);
          await session.close();

          expect(taskRanDuringExchange).toBe(true);
          expect(outcome.state).toBe('completed');
          expect(outcome.artifacts.map(artifactText)).toEqual([echoReply(EXCHANGE_INPUT)]);
          expect(provider.tasks.map((t) => t.text)).toEqual([TASK_INPUT]);
        },
      );
    },
    getTestTimeout(120_000),
  );
});
