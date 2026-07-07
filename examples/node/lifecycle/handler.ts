import type { HandlerResult, StartTaskMessage, TaskContext } from '@blocks-network/sdk';

/**
 * lifecycle handler — teaches the boundary between task-lifecycle ops the
 * framework backs first-class and ops a provider composes from primitives.
 *
 *   - cancel  (built-in)            cooperative: the SDK aborts ctx.cancelSignal
 *                                   and the loop stops → terminal `canceled`.
 *   - pause   (provider-composed)   NOT a handler hook — the framework's
 *   - resume  (provider-composed)   PauseTask/ResumeTask only publish a status
 *                                   event and never reach handler code. So we
 *                                   build real work-suspension ourselves: read
 *                                   { ctrl: 'pause' | 'resume' } on a bidi
 *                                   control stream and park the work loop.
 *   - retry   (consumer-composed)   NOT done here — task state is in-memory
 *                                   only (SDK_CONTRACT §17), so retry is a
 *                                   consumer resubmit. This handler stays
 *                                   idempotent and honors a `failOnce` flag so
 *                                   the consumer can drive a failed→completed
 *                                   retry against a stateless agent.
 *
 * Task-kind split:
 *   - pipe:    long-running work loop with pause/resume/cancel (no auto-terminal).
 *   - request: single-shot; throws when `failOnce` is set (drives the retry demo).
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const log = (msg: string) => console.log(`[lifecycle] ${msg}`);
  const params = parseParams(task.requestParts);
  const isPipe = !task.taskKind || task.taskKind === 'pipe';

  // --- request path: fail-once retry demo -------------------------------
  // A stateless throw. The consumer sends failOnce on attempt 1 (→ terminal
  // `failed`) and resubmits with a fresh idempotencyKey and no flag (→
  // `completed`). No persisted "have I run before?" state is needed.
  if (!isPipe) {
    if (params.failOnce) {
      log(`Task ${task.taskId}: failOnce set — throwing to produce a failed terminal`);
      throw new Error('lifecycle: simulated first-attempt failure (failOnce)');
    }
    log(`Task ${task.taskId}: request completed`);
    return {
      artifacts: [{
        data: JSON.stringify({ kind: 'request', completionReason: 'completed' }, null, 2),
        mimeType: 'application/json',
      }],
    };
  }

  // --- pipe path: pause / resume / cancel work loop ---------------------
  // Throw (not return) on a missing stream: a pipe handler's voluntary return
  // publishes NO terminal, so returning an error artifact would leave the task
  // lingering until duration expiry and then terminal as `completed`. A throw
  // publishes an immediate, correct `failed` — matching the request path.
  if (!ctx || !ctx.hasStream) {
    throw new Error('lifecycle pipe task requires a negotiated stream');
  }

  log(`Task ${task.taskId}: starting work loop (up to ${params.ticks} ticks)`);
  const control = await ctx.createStream({ declaredStream: 'control', direction: 'bidirectional', format: 'events' });

  // Read control messages on a detached loop so the work loop below is never
  // blocked waiting on input. `paused` is the single source of truth the work
  // loop parks on; `resumed` records that we ever came back from a pause (for
  // the summary). The reader unwinds when the stream is torn down.
  const state = { paused: false, resumed: false };
  const reader = (async () => {
    try {
      for await (const ev of control.events<{ ctrl?: string }>()) {
        const ctrl = typeof ev === 'object' && ev ? ev.ctrl : undefined;
        if (ctrl === 'pause' && !state.paused) {
          state.paused = true;
          log('control: pause — parking work loop');
          ctx.reportStatus('paused');
        } else if (ctrl === 'resume' && state.paused) {
          state.paused = false;
          state.resumed = true;
          log('control: resume — continuing work loop');
          ctx.reportStatus('running');
        }
      }
    } catch {
      // Stream torn down (cancel / completion) — expected.
    }
  })();

  ctx.reportStatus('running');
  let emitted = 0;
  try {
    for (let tick = 1; tick <= params.ticks && !ctx.cancelSignal.aborted; tick += 1) {
      // Park while paused. This is the real suspension the framework's
      // status-only PauseTask does not provide: no progress is emitted and
      // the tick counter does not advance until we resume (or cancel).
      while (state.paused && !ctx.cancelSignal.aborted) {
        await sleepMs(TICK_MS, ctx.cancelSignal);
      }
      if (ctx.cancelSignal.aborted) break;

      try {
        control.write({ tick, state: 'running' });
      } catch (err) {
        // The SDK force-ends the stream on fatal status / PAM revocation / a
        // teardown racing the cancel check; a write then throws. Break rather
        // than rethrow so cancel still converges on `canceled` (mirrors Python).
        if (isEndedStreamError(err)) break;
        throw err;
      }
      emitted += 1;
      await sleepMs(TICK_MS, ctx.cancelSignal);
    }
  } catch (err) {
    if (!isAbortError(err)) throw err;
  }

  await control.end().catch(() => undefined);
  await reader;

  const completionReason = ctx.isCancelled ? 'canceled' : 'completed';
  log(`Work loop ${completionReason} (${emitted} ticks, paused=${state.paused || state.resumed})`);
  ctx.reportStatus(completionReason);

  return {
    artifacts: [{
      data: JSON.stringify(
        { kind: 'pipe', ticks: emitted, paused: state.paused, resumed: state.resumed, completionReason },
        null,
        2,
      ),
      mimeType: 'application/json',
    }],
  };
}

const TICK_MS = 500;
const ABORT_MESSAGE = 'aborted';
const DEFAULT_TICKS = 20;
const MAX_TICKS = 200;

interface Params {
  ticks: number;
  failOnce: boolean;
}

function parseParams(parts: unknown[] | undefined): Params {
  let ticks = DEFAULT_TICKS;
  let failOnce = false;
  for (const part of parts ?? []) {
    const record = coerceRecord(part);
    if (!record) continue;
    if (typeof record.ticks === 'number' && Number.isFinite(record.ticks)) {
      ticks = Math.min(MAX_TICKS, Math.max(1, Math.trunc(record.ticks)));
    }
    if (typeof record.failOnce === 'boolean') {
      failOnce = record.failOnce;
    }
  }
  return { ticks, failOnce };
}

function coerceRecord(part: unknown): Record<string, unknown> | undefined {
  if (part === null || typeof part !== 'object' || Array.isArray(part)) {
    return undefined;
  }
  const record = part as Record<string, unknown>;
  if (typeof record.text === 'string') {
    try {
      const parsed = JSON.parse(record.text);
      if (parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // text is not JSON — fall through to the raw record
    }
  }
  return record;
}

function sleepMs(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);

    const onAbort = () => {
      clearTimeout(timer);
      signal.removeEventListener('abort', onAbort);
      reject(new Error(ABORT_MESSAGE));
    };

    signal.addEventListener('abort', onAbort, { once: true });
  });
}

function isAbortError(err: unknown): boolean {
  return err instanceof Error && err.message === ABORT_MESSAGE;
}

function isEndedStreamError(err: unknown): boolean {
  return err instanceof Error && err.message === 'Cannot write to an ended stream';
}
