import type { HandlerResult, StartTaskMessage, TaskContext } from '@blocks-network/sdk';

/**
 * advanced-stream handler.
 *
 * Demonstrates three streaming patterns on a single pipe task, each
 * selected by its `declaredStream` name from the agent card:
 *
 *   - `events`    outbound, format: 'events'  — structured JSON events that
 *                 conform to the schema declared on the card.
 *   - `raw`       outbound, format: 'bytes'   — raw UTF-8 chunks the consumer
 *                 reads as Uint8Array.
 *   - `broadcast` outbound, format: 'events', affinity: 'shared' — a shared
 *                 channel with no per-task stream_end marker.
 *
 * Affinity ('dedicated' vs 'shared') is declared on the card, not passed to
 * createStream(). The handler only names the stream via `declaredStream`.
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const log = (msg: string) => console.log(`[advanced-stream] ${msg}`);

  if (!ctx) {
    return {
      artifacts: [{ data: JSON.stringify({ error: 'TaskContext is required for streaming' }), mimeType: 'application/json' }],
    };
  }

  if (task.taskKind && task.taskKind !== 'pipe') {
    throw new Error('advanced-stream only supports pipe tasks');
  }

  const ticks = parseTicks(task.requestParts);
  log(`Task ${task.taskId}: emitting ${ticks} tick(s) on each stream`);

  // Open all three streams up front. Each is selected by its card-declared
  // name; there are no invented stream IDs and no post-create sleeps.
  const events = await ctx.createStream({ declaredStream: 'events', format: 'events' });
  const raw = await ctx.createStream({ declaredStream: 'raw', format: 'bytes' });
  const broadcast = await ctx.createStream({ declaredStream: 'broadcast', format: 'events' });
  log(`Streams open: events=${events.channel} raw=${raw.channel} broadcast=${broadcast.channel}`);

  ctx.reportStatus(`Streaming ${ticks} ticks across events/raw/broadcast...`);

  const encoder = new TextEncoder();
  let emitted = 0;

  try {
    for (let tick = 1; tick <= ticks && !ctx.cancelSignal.aborted; tick += 1) {
      // Schema-validated event: { tick, label, at } matches the card schema.
      events.write({ tick, label: `event #${tick}`, at: new Date().toISOString() });

      // Raw bytes: consumer decodes each chunk from Uint8Array.
      raw.write(encoder.encode(`chunk ${tick}\n`));

      // Shared broadcast: fans in to a channel shared across tasks.
      broadcast.write({ tick, kind: 'broadcast' });

      emitted += 1;
      await sleepMs(500, ctx.cancelSignal);
    }
  } catch (err) {
    if (!isAbortError(err)) {
      throw err;
    }
  }

  // end() publishes a stream_end marker on dedicated streams so consumers
  // know they are complete. Shared streams suppress the per-task marker.
  await Promise.all([events.end(), raw.end(), broadcast.end()]);
  log(`Streams ended (${emitted} ticks emitted)`);

  const completionReason = ctx.isCancelled ? 'canceled' : 'completed';
  ctx.reportStatus(`Streaming ${completionReason} (${emitted} ticks)`);

  return {
    artifacts: [{
      data: JSON.stringify({ ticksRequested: ticks, ticksEmitted: emitted, completionReason }, null, 2),
      mimeType: 'application/json',
    }],
  };
}

const DEFAULT_TICKS = 5;
const MAX_TICKS = 50;

function parseTicks(parts: unknown[] | undefined): number {
  for (const part of parts ?? []) {
    const record = coerceRecord(part);
    if (record && typeof record.ticks === 'number' && Number.isFinite(record.ticks)) {
      return Math.min(MAX_TICKS, Math.max(1, Math.trunc(record.ticks)));
    }
  }
  return DEFAULT_TICKS;
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
      reject(new Error('aborted'));
    };

    signal.addEventListener('abort', onAbort, { once: true });
  });
}

function isAbortError(err: unknown): boolean {
  return err instanceof Error && err.message === 'aborted';
}
