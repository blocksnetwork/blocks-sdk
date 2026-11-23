import type {
  HandlerResult,
  StartTaskMessage,
  TaskContext,
} from '@blocks-network/sdk';

const CALLED_AGENT = 'connections_salesforce_node';
const CALL_TIMEOUT_MS = 45_000;

/** Calls the Salesforce agent as this task's user. A refusal is not caught: it fails this task, and the user's client offers Connect. */
export default async function handler(
  _task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx?.taskClient) throw new Error('TaskContext is required');

  ctx.reportStatus(`Calling ${CALLED_AGENT}...`);
  const text = await callAgent(ctx, CALLED_AGENT);
  return {
    artifacts: [{ data: text, mimeType: 'text/plain', outputId: 'result' }],
  };
}

async function callAgent(ctx: TaskContext, agentName: string): Promise<string> {
  const session = await ctx.taskClient.sendMessage({
    agentName,
    requestParts: [{ partId: 'request', contentType: 'application/json', text: '{}' }],
  });
  try {
    const terminal = await session.waitForTerminal(CALL_TIMEOUT_MS);
    if (terminal.state !== 'completed') {
      throw new Error(`${agentName} ${terminal.state}: ${terminal.error ?? terminal.reason ?? ''}`);
    }
    const [artifact] = session.listArtifacts();
    if (!artifact) return `${agentName} returned no result`;
    const { data } = await session.downloadArtifact(artifact);
    return new TextDecoder().decode(data);
  } finally {
    session.close();
  }
}
