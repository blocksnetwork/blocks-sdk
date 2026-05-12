import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Echo handler -- returns the input text back.
 *
 * This is the simplest possible Blocks handler: parse the input,
 * optionally report status, and return a single artifact.
 */
const TOTAL_PROCESSING_MS = 15_000;
const STATUS_INTERVAL_MS = 2_000;

export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  // requestParts is the SDK's structured input — each part carries text or file data
  const input = task.requestParts?.[0];
  const text = extractText(input);

  const start = Date.now();
  let elapsed = 0;
  while (elapsed + STATUS_INTERVAL_MS < TOTAL_PROCESSING_MS) {
    await sleep(STATUS_INTERVAL_MS);
    elapsed = Date.now() - start;
    const remaining = Math.max(0, Math.ceil((TOTAL_PROCESSING_MS - elapsed) / 1000));
    // reportStatus publishes a real-time progress event to task subscribers
    ctx?.reportStatus(`Processing... ${remaining}s remaining`);
  }
  await sleep(Math.max(0, TOTAL_PROCESSING_MS - (Date.now() - start)));

  const result = `Processed: ${text}`;

  // artifacts is the handler's final output, delivered to the consumer as artifact events
  return {
    artifacts: [{ data: result, mimeType: 'text/plain' }],
  };
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function extractText(input: unknown): string {
  if (input !== null && typeof input === 'object') {
    const part = input as Record<string, unknown>;
    if (typeof part.text === 'string') return part.text;
  }
  if (typeof input === 'string') return input;
  return JSON.stringify(input ?? 'Hello from Blocks!');
}
