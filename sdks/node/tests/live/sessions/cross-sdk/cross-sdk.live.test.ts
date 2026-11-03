// The Python side needs blocks_network importable by $PYTHON (default python3).
import { type ChildProcessWithoutNullStreams, spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { dirname, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { afterAll, afterEach, describe, expect, it } from 'vitest';
import { getBaseUrl } from '../../../helpers/live-test-config.js';
import { makeTestCard } from '../../../helpers/test-card.js';
import {
  createConsumer,
  decodeEcho,
  type EchoPayload,
  echoResult,
  liveGuard,
  sessionsFlagOn,
  openSessionWhenRouted,
  publishSessionAgent,
  removeAgents,
} from '../session-harness.js';
import { type BlocksClient, loadSessionSdk } from '../session-sdk.js';

const HERE = dirname(fileURLToPath(import.meta.url));
const PYTHON = process.env.PYTHON ?? 'python3';
const INPUTS = ['first', 'second', 'third'];
const OUTCOME_TIMEOUT_MS = 30_000;
const CHILD_TIMEOUT_MS = 120_000;
const PROGRESS_MESSAGE = 'working';

// consumer.py needs the Python consumer client; skip until the installed SDK has it.
const pythonHasClient =
  spawnSync(PYTHON, ['-c', 'from blocks_network import BlocksClient']).status === 0;

interface PythonConsumerReport {
  sent: { exchangeId: string; text: string }[];
  outcomes: { exchangeId: string; state: string; echo: EchoPayload; progress: string[] }[];
  finalState: string;
}

function spawnPython(script: string, args: string[]): ChildProcessWithoutNullStreams {
  const child = spawn(PYTHON, [resolve(HERE, script), ...args], { env: process.env });
  child.stderr.pipe(process.stderr);
  return child;
}

async function firstJsonLine<T>(child: ChildProcessWithoutNullStreams): Promise<T> {
  const lines = createInterface({ input: child.stdout });
  const timer = setTimeout(() => child.kill('SIGKILL'), CHILD_TIMEOUT_MS);
  try {
    for await (const line of lines) {
      if (line.startsWith('{')) return JSON.parse(line) as T;
    }
    throw new Error(`${PYTHON} child exited without a JSON line`);
  } finally {
    clearTimeout(timer);
    lines.close();
  }
}

function expectCorrelated(
  sent: { exchangeId: string; text: string }[],
  echoes: { exchangeId: string; state: string; echo: EchoPayload; progress: string[] }[],
  providerSdk: EchoPayload['sdk'],
): void {
  expect(echoes.map((o) => o.exchangeId)).toEqual(sent.map((s) => s.exchangeId));
  for (const [i, outcome] of echoes.entries()) {
    expect(outcome.state).toBe('completed');
    expect(outcome.progress).toContain(PROGRESS_MESSAGE);
    expect(outcome.echo).toEqual({ ...sent[i], sdk: providerSdk });
  }
}

describe.skipIf(liveGuard || !sessionsFlagOn)('cross-SDK sessions (live)', () => {
  const agents: string[] = [];
  const children: ChildProcessWithoutNullStreams[] = [];
  const stops: (() => void)[] = [];
  let client: BlocksClient | undefined;

  afterEach(async () => {
    for (const child of children.splice(0)) child.kill('SIGKILL');
    for (const stop of stops.splice(0)) stop();
    await client?.destroy?.();
  });
  afterAll(() => removeAgents(agents));

  it(
    'Node consumer, Python provider: each outcome answers its own exchange',
    async () => {
      const sdk = await loadSessionSdk();
      const agentName = await publishSessionAgent('sess_xpy', agents);
      const provider = spawnPython('provider.py', [
        agentName,
        JSON.stringify(makeTestCard({ agentName })),
      ]);
      children.push(provider);
      expect(await firstJsonLine<{ event: string }>(provider)).toEqual({ event: 'ready' });
      client = await createConsumer(sdk);

      const session = await openSessionWhenRouted(client, agentName);
      const exchanges = INPUTS.map((text) => {
        const exchange = session.send(text);
        const progress: string[] = [];
        exchange.onProgress((event) => progress.push(event.message ?? ''));
        return { text, exchange, progress };
      });
      const outcomes = await Promise.all(
        exchanges.map(({ exchange }) => exchange.waitForOutcome(OUTCOME_TIMEOUT_MS)),
      );
      await session.close();

      expectCorrelated(
        exchanges.map(({ text, exchange }) => ({ exchangeId: exchange.exchangeId, text })),
        outcomes.map((o, i) => ({
          ...o,
          echo: decodeEcho(o.artifacts[0]),
          progress: exchanges[i].progress,
        })),
        'python',
      );
      expect(session.state).toBe('closed');
    },
    CHILD_TIMEOUT_MS,
  );

  it.skipIf(!pythonHasClient)(
    'Python consumer, Node provider: each outcome answers its own exchange',
    async () => {
      const sdk = await loadSessionSdk();
      const agentName = await publishSessionAgent('sess_xnode', agents);
      const provider = await sdk.startAgentInstance({
        agentName,
        card: makeTestCard({ agentName }),
        baseUrl: getBaseUrl(),
        onExchange: (_ctx, exchange) => {
          exchange.reportStatus(PROGRESS_MESSAGE);
          return echoResult(exchange, 'node');
        },
      });
      stops.push(provider.stop);

      const consumer = spawnPython('consumer.py', [agentName, JSON.stringify(INPUTS)]);
      children.push(consumer);
      const report = await firstJsonLine<PythonConsumerReport>(consumer);
      const exitCode = consumer.exitCode ?? (await once(consumer, 'exit'))[0];

      expect(exitCode).toBe(0);
      expect(report.sent.map((s) => s.text)).toEqual(INPUTS);
      expectCorrelated(report.sent, report.outcomes, 'node');
      expect(report.finalState).toBe('closed');
    },
    CHILD_TIMEOUT_MS,
  );
});
