import type {
  StartTaskMessage,
  TaskContext,
  HandlerResult,
  TaskClient,
  TaskEvent,
} from '@blocks-network/sdk';

/**
 * Orchestrator handler — fans out to echo and adder agents in parallel,
 * subscribes to real-time results, and compiles a summary.
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx?.taskClient) {
    throw new Error('TaskClient not available — handler requires TaskContext');
  }
  const taskClient = ctx.taskClient;
  const input = parseInput(task);

  ctx?.reportStatus('Dispatching sub-tasks...');

  const ownerId = task.ownerId;

  const [echoResult, adderResult] = await Promise.all([
    executeSubTask(taskClient, 'echo', [{ partId: 'text', text: input.echoText }], ownerId),
    executeSubTask(taskClient, 'adder', [{ partId: 'numbers', text: JSON.stringify({ kind: 'math_add', a: input.a, b: input.b }) }], ownerId),
  ]);

  ctx?.reportStatus('Compiling results...');

  const output = {
    ok: echoResult.status === 'completed' && adderResult.status === 'completed',
    echo: echoResult,
    adder: adderResult,
    summary: `Echo: ${echoResult.status}, Adder: ${adderResult.status}`,
  };

  return { artifacts: [{ data: JSON.stringify(output, null, 2), mimeType: 'application/json' }] };
}

// ---------------------------------------------------------------------------
// Sub-task execution helper
// ---------------------------------------------------------------------------

interface SubTaskResult {
  status: 'completed' | 'failed' | 'timeout';
  artifact?: unknown;
  error?: string;
}

/**
 * Client-side timeout for each sub-task. Must be less than the orchestrator's
 * own maxRunningTimeSec (60 s in agent-card.json) to leave time for result
 * collection and response assembly. The 2x ratio provides headroom for two
 * sequential dispatches plus overhead.
 */
const SUB_TASK_TIMEOUT_MS = 30_000;

/**
 * Extract actual content from an artifact reference.
 * Inline artifacts carry base64-encoded data; decode it to a string or parsed JSON.
 * File artifacts without downloadable data are returned as-is.
 */
function decodeArtifact(ref: unknown): unknown {
  if (ref === null || typeof ref !== 'object') return ref;
  const obj = ref as Record<string, unknown>;
  if (obj.kind === 'inline' && typeof obj.data === 'string') {
    const text = Buffer.from(obj.data, 'base64').toString('utf-8');
    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }
  return ref;
}

async function executeSubTask(
  taskClient: TaskClient,
  agentName: string,
  requestParts: unknown[],
  ownerId: string,
): Promise<SubTaskResult> {
  try {
    const sent = await taskClient.sendMessage({ agentName, requestParts, ownerId });

    return new Promise<SubTaskResult>((resolve) => {
      let settled = false;
      const result: { artifact?: unknown } = {};

      const finish = (outcome: SubTaskResult) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        sent.close();
        resolve(outcome);
      };

      const timer = setTimeout(() => {
        finish({
          status: 'timeout',
          error: `${agentName} timed out after ${SUB_TASK_TIMEOUT_MS}ms`,
        });
      }, SUB_TASK_TIMEOUT_MS);

      sent.onArtifact((event: TaskEvent) => {
        result.artifact = decodeArtifact(event.artifactRef ?? event.artifact);
      });
      sent.onTerminal((event: TaskEvent) => {
        if (event.state === 'completed') {
          finish({ status: 'completed', artifact: result.artifact });
        } else {
          finish({
            status: 'failed',
            error: (event.error as string) ?? event.state ?? 'unknown',
          });
        }
      });

      // Guard against race: the sub-task may complete before the subscription
      // is active. A single getTask poll right after subscribing covers this gap.
      taskClient
        .getTask(sent.taskId)
        .then((info) => {
          const state = info.state as string | undefined;
          if (state === 'completed' || state === 'failed' || state === 'canceled') {
            // If completed but the artifact event hasn't arrived yet via subscription,
            // try to extract it from the task record returned by getTask.
            let artifact = result.artifact;
            if (state === 'completed' && artifact === undefined) {
              const arts = (info as Record<string, unknown>).artifacts as unknown[] | undefined;
              if (arts && arts.length > 0) {
                artifact = decodeArtifact(arts[arts.length - 1]);
              }
            }
            finish(
              state === 'completed'
                ? { status: 'completed', artifact }
                : { status: 'failed', error: state },
            );
          }
        })
        .catch(() => {
          /* poll failed; rely on real-time subscription */
        });
    });
  } catch (err) {
    return {
      status: 'failed',
      error: (err as Error)?.message ?? 'sendMessage failed',
    };
  }
}

// ---------------------------------------------------------------------------
// Input parsing
// ---------------------------------------------------------------------------

interface OrchestratorInput {
  echoText: string;
  a: number;
  b: number;
}

function parseInput(task: StartTaskMessage): OrchestratorInput {
  const defaults: OrchestratorInput = {
    echoText: 'Hello from Orchestrator!',
    a: 3,
    b: 4,
  };

  const parts = task.requestParts ?? [];
  for (const p of parts) {
    if (p !== null && typeof p === 'object' && !Array.isArray(p)) {
      const obj = parsePartContent(p as Record<string, unknown>);
      if (typeof obj.echoText === 'string') defaults.echoText = obj.echoText;
      if (typeof obj.a === 'number' && Number.isFinite(obj.a)) defaults.a = obj.a;
      if (typeof obj.b === 'number' && Number.isFinite(obj.b)) defaults.b = obj.b;
    }
  }

  return defaults;
}

function parsePartContent(part: Record<string, unknown>): Record<string, unknown> {
  if (typeof part.text === 'string') {
    try {
      const parsed = JSON.parse(part.text);
      if (parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // text is plain string, not JSON -- fall through
    }
  }
  return part;
}
