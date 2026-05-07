import { getConfig } from '../config.ts';
import type { SendMessageResult, ModelOption } from '../types.ts';

export interface SendTaskParams {
  prompt: string;
  ownerId: string;
  agentName: string;
  sessionId?: string;
  cwd?: string;
  model?: ModelOption;
}

/**
 * Send a task to the A2A facade via HTTP POST.
 *
 * Posts a JSON-RPC SendMessage request and returns the parsed result,
 * which includes the taskId and PubNub stream channel names.
 */
export async function sendTask(params: SendTaskParams): Promise<SendMessageResult> {
  const config = getConfig();

  const requestPart: Record<string, unknown> = {
    kind: 'input_text',
    text: params.prompt,
  };
  if (params.sessionId) requestPart.sessionId = params.sessionId;
  if (params.cwd) requestPart.cwd = params.cwd;
  if (params.model) requestPart.model = params.model;

  const body = {
    jsonrpc: '2.0',
    id: Date.now(),
    method: 'SendMessage',
    params: {
      agentName: params.agentName,
      ownerId: params.ownerId,
      requestParts: [requestPart],
      retryPolicy: { maxRetries: 3, expiresAfterSec: 300 },
    },
  };

  const resp = await fetch(`${config.blocksBackendUrl}/api/v1/rpc`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'A2A-Version': '2025-01-01',
    },
    body: JSON.stringify(body),
  });

  if (!resp.ok) {
    throw new Error(`HTTP ${resp.status}: ${resp.statusText}`);
  }

  const json = await resp.json();

  if (json.error) {
    throw new Error(json.error.message || JSON.stringify(json.error));
  }

  return json.result as SendMessageResult;
}

/**
 * Cancel a running task via the A2A facade JSON-RPC endpoint.
 *
 * Sends a CancelTask JSON-RPC request. Returns the result which
 * includes the task state after cancellation.
 */
export async function cancelTask(taskId: string): Promise<{ taskId: string; state: string }> {
  const config = getConfig();

  const body = {
    jsonrpc: '2.0',
    id: Date.now(),
    method: 'CancelTask',
    params: { taskId },
  };

  const resp = await fetch(`${config.blocksBackendUrl}/api/v1/rpc`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'A2A-Version': '2025-01-01',
    },
    body: JSON.stringify(body),
  });

  const json = await resp.json();

  if (json.error) {
    throw new Error(json.error.data?.message || json.error.message || JSON.stringify(json.error));
  }

  return json.result as { taskId: string; state: string };
}
