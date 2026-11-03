import { describe, expect, it } from 'vitest';
import { getTestTimeout } from '../../helpers/live-test-config.js';
import {
  createConsumer,
  liveGuard,
  recordBackendCalls,
  routeRequest,
  sessionsFlagOn,
  startTestProvider,
  uniqueAgentName,
} from './session-harness.js';
import { type BlocksClient, loadSessionSdk } from './session-sdk.js';

describe.skipIf(liveGuard || !sessionsFlagOn)('session paid-agent refusal (live)', () => {
  it(
    'a paid agent gets no route, and openSession and call raise SessionPaidAgentError',
    async () => {
      const sdk = await loadSessionSdk();
      const recorder = recordBackendCalls();
      const provider = await startTestProvider(sdk, {
        agentName: uniqueAgentName('sess_paid'),
        billingMode: 'paid',
      });
      let client: BlocksClient | undefined;
      try {
        // The provider asked for a route and was refused; it runs task-only.
        const route = await routeRequest(recorder.calls);
        expect(route.status).toBe(422);
        expect(route.body).toMatchObject({ code: 'SESSION_PAID_AGENT_UNSUPPORTED' });
        expect(route.body).not.toHaveProperty('routeId');
        client = await createConsumer(sdk, 'paid');
        const consumer = client;

        const attempts: Array<[string, () => Promise<unknown>]> = [
          ['openSession', () => consumer.openSession(provider.agentName)],
          ['call', () => consumer.call(provider.agentName, 'hello')],
        ];
        for (const [name, attempt] of attempts) {
          const err = await attempt().then(
            () => new Error(`${name} resolved on a paid agent`),
            (e: unknown) => e,
          );
          expect(err, name).toBeInstanceOf(sdk.SessionPaidAgentError);
          expect(err, name).toBeInstanceOf(sdk.SessionError);
          expect((err as { code?: string }).code, name).toBe('SESSION_PAID_AGENT_UNSUPPORTED');
        }

        const consumerCalls = recorder.calls.filter((c) => c.path.startsWith('/api/v1/sessions'));
        expect(consumerCalls.map((c) => [c.method, c.path, c.status])).toEqual(
          attempts.map(() => ['POST', '/api/v1/sessions', 422]),
        );
        for (const call of consumerCalls) {
          expect(call.body).toMatchObject({ code: 'SESSION_PAID_AGENT_UNSUPPORTED' });
          expect(call.body).not.toHaveProperty('sessionId');
          expect(call.body).not.toHaveProperty('pamToken');
        }
        expect(provider.sessionsOpened).toEqual([]);
        expect(provider.exchanges).toEqual([]);
      } finally {
        recorder.restore();
        client?.destroy();
        await provider.stop();
      }
    },
    getTestTimeout(90_000),
  );
});
