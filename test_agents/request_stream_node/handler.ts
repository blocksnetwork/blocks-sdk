import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * request_stream_node handler.
 *
 * Accepts JSON input with "message" (string) and "seconds" (integer).
 * Streams one event per second for N seconds, then returns an artifact
 * summarising the run.
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = task.requestParts?.[0];
  const raw = typeof input === 'string' ? input : (input as Record<string, unknown>)?.text as string ?? '';

  let message = '';
  let seconds = 0;
  try {
    const payload = JSON.parse(raw);
    message = String(payload.message ?? '');
    seconds = Number(payload.seconds ?? 0);
  } catch {
    message = raw;
  }

  if (!message) throw new Error('Missing "message" in input');
  if (!seconds || seconds <= 0) throw new Error('"seconds" must be a positive integer');

  if (ctx) {
    ctx.reportStatus('Starting stream...');
    const stream = await ctx.createStream();

    for (let i = 1; i <= seconds; i++) {
      if (ctx.isCancelled) break;
      await sleep(1000);
      stream.write(`${i} seconds`);
      ctx.reportStatus(`${i}/${seconds} seconds`);
    }

    await stream.end();
    ctx.reportStatus('Done');
  }

  return {
    artifacts: [{ data: `I ran for ${seconds} seconds and your message to me was ${message}`, mimeType: 'text/plain' }],
  };
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
