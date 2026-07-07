/**
 * lifecycle consumer — drives one deterministic timeline that exercises all
 * four task-lifecycle ops against the lifecycle agent, in order:
 *
 *   1. Submit a long-running `pipe` task; open the bidi control stream.
 *   2. Read a few progress ticks.
 *   3. pause  → provider parks its work loop → ticks stop.
 *   4. resume → provider continues        → ticks resume.
 *   5. cancel → provider stops cooperatively → terminal `canceled`.
 *   6. Retry (request task): submit with failOnce → terminal `failed`, then
 *      resubmit with a FRESH idempotencyKey → `completed`. Also resubmit with
 *      the SAME key to show the backend returns `idempotent: true` (no re-run).
 *
 * Only the current consumer surface is used: cancel is `client.cancelTask`;
 * pause/resume are app-level control messages on the stream; retry is a plain
 * resubmit. See README for the framework-vs-composed boundary.
 *
 * Usage:
 *   npx tsx lifecycle-consumer.ts
 *
 * Authentication:
 *   BLOCKS_API_KEY      API key (bk_...) — from `blocks login --write-env`.
 *   BLOCKS_CDM_URL      CDM config URL (optional — falls back to default CDN).
 *   BLOCKS_BACKEND_URL  Backend base URL (optional — overrides the CDM value).
 */

import 'dotenv/config';
import { TaskClient, fetchCdmConfig, getAgent, type StreamRef } from '@blocks-network/sdk';

const AGENT_NAME = 'lifecycle_node';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error('BLOCKS_API_KEY not set. Run `blocks login --write-env` first.');
  process.exit(1);
}

async function main() {
  const cdmUrl = process.env.BLOCKS_CDM_URL;
  const cdm = await fetchCdmConfig(cdmUrl);
  const baseUrl = process.env.BLOCKS_BACKEND_URL ?? cdm.api.baseUrl;

  // Read the agent's registered billingMode so we pick the matching keyset
  // (free → playground, paid → network). Pass apiKey so a privately-registered
  // agent resolves — the registry returns 404 for private agents unauthenticated.
  const entry = await getAgent(AGENT_NAME, { baseUrl, apiKey });
  if (!entry) {
    console.error(`Agent "${AGENT_NAME}" not found in the registry at ${baseUrl}.`);
    process.exit(1);
  }
  const billingMode = entry.billingMode ?? 'free';
  console.log(`Registry says ${AGENT_NAME} is billingMode=${billingMode}; using ${billingMode === 'paid' ? 'network' : 'playground'} keyset.`);

  const client = await TaskClient.create({ billingMode, apiKey, cdmUrl, baseUrl });

  await runPauseResumeCancel(client);
  await runRetry(client);

  client.destroy();
  console.log('\n--- Done ---');
}

/** Steps 1–5: pause → resume → cancel over a bidi control stream. */
async function runPauseResumeCancel(client: TaskClient): Promise<void> {
  console.log('\n=== pause / resume / cancel (pipe task) ===');
  const session = await client.sendMessage({
    agentName: AGENT_NAME,
    taskKind: 'pipe',
    // Pipe tasks require a duration (max lifetime, minutes). We cancel long
    // before this. Note the card's `maxRunningTimeSec: 120` is the tighter
    // cap and fires first — keep any manual pause under ~20s so the demo
    // cancels before the card cap terminates the task.
    duration: 5,
    requestParts: [{ partId: 'params', text: JSON.stringify({ ticks: 200 }) }],
  });
  console.log(`Task created: ${session.taskId}`);

  // Log the backend cancel-ack so the built-in cancel path is visible.
  session.onCancelRequested((ev) => console.log(`[cancel] backend acknowledged cancel for ${ev.taskId}`));

  const ticks: number[] = [];
  const streamRef = await waitForStream(session, 30_000);
  const stream = streamRef.open();
  stream.onError((err) => console.error('[control] stream error:', err));

  // Read progress ticks on a detached loop; unwinds on cancel/teardown.
  const reader = (async () => {
    try {
      for await (const ev of stream.events<{ tick?: number }>()) {
        if (typeof ev?.tick === 'number') {
          ticks.push(ev.tick);
          console.log(`[tick] ${ev.tick}`);
        }
      }
    } catch {
      // stream closed on cancel/teardown — expected
    }
  })();

  await waitForTicks(ticks, 3, 8000);
  console.log('--- pausing ---');
  stream.write({ ctrl: 'pause' });

  // While paused, the tick count must hold steady — that is the proof the
  // work loop actually suspended (not just a status event).
  const beforePause = ticks.length;
  await sleep(2500);
  const duringPause = ticks.length;
  console.log(`ticks during 2.5s pause: ${duringPause - beforePause} (expected ~0)`);

  console.log('--- resuming ---');
  stream.write({ ctrl: 'resume' });
  await waitForTicks(ticks, duringPause + 2, 8000);
  console.log('ticks resumed after resume ✓');

  console.log('--- canceling ---');
  await client.cancelTask(session.taskId);

  const terminal = await session.waitForTerminal(30_000);
  console.log(`--- pipe task ${terminal.state} (expected: canceled) ---`);

  await stream.end().catch((err) => console.error('[control] stream.end() after cancel:', err));
  await reader;
  await session.asyncClose();
}

/** Step 6: fail-once → retry with a fresh key → same-key idempotent replay. */
async function runRetry(client: TaskClient): Promise<void> {
  console.log('\n=== retry (request task) ===');

  // Per-run suffix so each script run submits FRESH idempotency keys — a
  // constant key would make run 2+ a terminal-idempotent replay (the stored
  // terminal is returned without re-running the handler), silently voiding the
  // retry demo. Attempt 3 deliberately reuses attempt 2's key to show dedupe.
  const runId = Date.now();

  // Attempt 1: failOnce set → terminal `failed`.
  const failKey = `lifecycle-retry-${AGENT_NAME}-${runId}-1`;
  const attempt1 = await client.sendMessage({
    agentName: AGENT_NAME,
    taskKind: 'request',
    idempotencyKey: failKey,
    requestParts: [{ partId: 'params', text: JSON.stringify({ failOnce: true }) }],
  });
  const t1 = await attempt1.waitForTerminal(30_000);
  console.log(`attempt 1 (failOnce): ${t1.state} (expected: failed)`);
  await attempt1.asyncClose();

  // Retry: the agent is stateless, so retry is a fresh submission with a NEW
  // idempotencyKey and no failOnce flag → `completed`.
  const retryKey = `lifecycle-retry-${AGENT_NAME}-${runId}-2`;
  const attempt2 = await client.sendMessage({
    agentName: AGENT_NAME,
    taskKind: 'request',
    idempotencyKey: retryKey,
    requestParts: [{ partId: 'params', text: JSON.stringify({ failOnce: false }) }],
  });
  const t2 = await attempt2.waitForTerminal(30_000);
  console.log(`attempt 2 (retry, fresh key): ${t2.state} (expected: completed)`);
  await attempt2.asyncClose();

  // Same-key resubmit: the backend dedupes and returns the prior result
  // instead of running the handler again.
  const attempt3 = await client.sendMessage({
    agentName: AGENT_NAME,
    taskKind: 'request',
    idempotencyKey: retryKey,
    requestParts: [{ partId: 'params', text: JSON.stringify({ failOnce: false }) }],
  });
  console.log(`attempt 3 (same key ${retryKey}): idempotent=${attempt3.idempotent === true} (expected: true)`);
  await attempt3.asyncClose();
}

function waitForStream(
  session: Awaited<ReturnType<TaskClient['sendMessage']>>,
  timeoutMs: number,
): Promise<StreamRef> {
  // The SDK's session.waitForStream() has no timeout parameter, so we wrap
  // onStream with one. Drop the subscription on resolve/timeout so the
  // callback isn't left registered after we're done.
  return new Promise((resolve, reject) => {
    const unsubscribe = session.onStream((ref) => {
      clearTimeout(timer);
      unsubscribe();
      resolve(ref);
    });
    const timer = setTimeout(() => {
      unsubscribe();
      reject(new Error(`Timed out waiting for stream after ${timeoutMs}ms`));
    }, timeoutMs);
  });
}

/** Resolve once `ticks.length >= target`, or reject on timeout. */
function waitForTicks(ticks: number[], target: number, timeoutMs: number): Promise<void> {
  return new Promise((resolve, reject) => {
    const started = performance.now();
    const poll = () => {
      if (ticks.length >= target) return resolve();
      if (performance.now() - started > timeoutMs) {
        return reject(new Error(`Timed out waiting for ${target} ticks (saw ${ticks.length})`));
      }
      setTimeout(poll, 100);
    };
    poll();
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
