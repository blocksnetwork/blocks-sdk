import type { HandlerResult, StartTaskMessage, TaskContext } from '@blocks-network/sdk';
import {
  finalizeStockRequest,
  parseStockRequest,
  promptForStockRequest,
  runStockSimTask,
} from './stock-sim-client.js';

export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx?.taskClient) {
    return {
      artifacts: [{ data: JSON.stringify({ error: 'TaskClient not available — handler requires TaskContext' }), mimeType: 'application/json' }],
    };
  }

  const initialRequest = parseStockRequest(task.requestParts);
  // Only prompt interactively if no input was provided AND running in a terminal.
  const hasInput = !!(initialRequest.symbolsInput || initialRequest.durationMinutes || initialRequest.provider);
  const request = !hasInput && process.stdin.isTTY
    ? await promptForStockRequest(initialRequest)
    : finalizeStockRequest(initialRequest);

  ctx.reportStatus(`Requesting ${request.symbols.join(', ')} from stock-sim...`);

  try {
    const result = await runStockSimTask({
      taskClient: ctx.taskClient,
      ownerId: task.ownerId,
      request,
      log: (line) => console.log(`[stock-sim-consumer] ${line}`),
    });

    ctx.reportStatus('Stock simulation finished');

    return {
      artifacts: [{ data: JSON.stringify({ ok: true, ...result }, null, 2), mimeType: 'application/json' }],
    };
  } catch (err) {
    return {
      artifacts: [{ data: JSON.stringify({
        ok: false,
        error: err instanceof Error ? err.message : String(err),
        symbols: request.symbols,
        provider: request.provider,
      }, null, 2), mimeType: 'application/json' }],
    };
  }
}
