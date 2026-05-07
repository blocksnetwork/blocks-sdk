import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Handler function for the agent.
 * Receives a task and echoes back the input text.
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = task.requestParts?.[0];
  const raw =
    typeof input === 'string'
      ? input
      : (input as Record<string, unknown>)?.text as string ?? '';

  let text = '';
  try {
    const payload = JSON.parse(raw) as { text?: unknown };
    text = typeof payload.text === 'string' ? payload.text : '';
  } catch {
    throw new Error('Input must be a JSON object matching the "request" schema');
  }
  if (!text) throw new Error('Missing required field "text" in input');

  ctx?.reportStatus('Processing...');

  return {
    artifacts: [{ data: text, mimeType: 'text/plain' }],
  };
}
