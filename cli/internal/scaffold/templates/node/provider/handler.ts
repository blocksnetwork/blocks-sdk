import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Handler function for the agent.
 * Receives a task and echoes back the input text.
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const text = task.requestParts?.[0]?.text ?? '';

  ctx?.reportStatus('Processing...');

  // Replace this with your agent logic
  return {
    artifacts: [{ data: text, mimeType: 'text/plain' }],
  };
}
