// Measured the scripts/latency/ way (one process, one clock, serial turns) to compare with tasks.
import { performance } from 'node:perf_hooks';
import { describe, expect, it } from 'vitest';
import { getBaseUrl, getTestTimeout } from '../../helpers/live-test-config.js';
import {
  liveGuard,
  sessionsFlagOn,
  TURN_TIMEOUT_MS,
  uniqueAgentName,
  withRoutedAgent,
} from './session-harness.js';
import { loadSessionSdk } from './session-sdk.js';

// Target warm-turn p50 for a direct publish between the two parties.
const WARM_TURN_P50_BUDGET_MS = 200;
const DEFAULT_TURNS = 10;

interface TurnSample {
  turn: number;
  state: string;
  agentReceivedMs: number | null;
  turnMs: number | null;
}

function turnCount(): number {
  const raw = process.env.LATENCY_TURNS;
  const turns = raw ? Number(raw) : DEFAULT_TURNS;
  if (!Number.isInteger(turns) || turns < 2) {
    throw new Error(`LATENCY_TURNS must be an integer >= 2, got ${JSON.stringify(raw)}`);
  }
  return turns;
}

function nearestRank(sorted: readonly number[], p: number): number | null {
  if (sorted.length === 0) return null;
  return sorted[Math.max(1, Math.ceil((p / 100) * sorted.length)) - 1];
}

function summarize(values: ReadonlyArray<number | null>) {
  const samples = values.filter((v): v is number => v !== null).sort((a, b) => a - b);
  return {
    n: samples.length,
    failures: values.length - samples.length,
    p50: nearestRank(samples, 50),
    p95: nearestRank(samples, 95),
    max: nearestRank(samples, 100),
  };
}

describe.skipIf(liveGuard || !sessionsFlagOn)('session turn latency (live)', () => {
  it(
    'reports warm per-turn latency for turns 2+ of one session',
    async () => {
      const sdk = await loadSessionSdk();
      const turns = turnCount();
      await withRoutedAgent(
        sdk,
        { agentName: uniqueAgentName('sess_latency') },
        async ({ provider, client }) => {
          const session = await client.openSession(provider.agentName);
          const samples: TurnSample[] = [];
          try {
            for (let turn = 1; turn <= turns; turn++) {
              const submitAt = performance.now();
              const exchange = session.send(`turn ${turn}`);
              const outcome = await exchange.waitForOutcome(TURN_TIMEOUT_MS);
              const settledAt = performance.now();
              const received = provider.exchanges.find((e) => e.exchangeId === exchange.exchangeId);
              samples.push({
                turn,
                state: outcome.state,
                agentReceivedMs: received ? received.receivedAt - submitAt : null,
                turnMs: outcome.state === 'completed' ? settledAt - submitAt : null,
              });
            }
          } finally {
            await session.close();
          }

          const warm = samples.filter((s) => s.turn > 1);
          const summary = {
            agentReceivedMs: summarize(warm.map((s) => s.agentReceivedMs)),
            turnMs: summarize(warm.map((s) => s.turnMs)),
          };
          console.table(summary);
          console.log(
            `SESSION_TURN_LATENCY ${JSON.stringify({
              backendUrl: getBaseUrl(),
              turns,
              cohort: 'warm',
              budgetP50Ms: WARM_TURN_P50_BUDGET_MS,
              withinBudget:
                summary.turnMs.p50 !== null && summary.turnMs.p50 <= WARM_TURN_P50_BUDGET_MS,
              summary,
              samples,
            })}`,
          );

          expect(samples.map((s) => s.state)).toEqual(samples.map(() => 'completed'));
          for (const { turnMs, agentReceivedMs } of warm) {
            expect(Number.isFinite(turnMs) && (turnMs as number) > 0).toBe(true);
            expect(Number.isFinite(agentReceivedMs) && (agentReceivedMs as number) > 0).toBe(true);
          }
        },
      );
    },
    getTestTimeout(180_000),
  );
});
