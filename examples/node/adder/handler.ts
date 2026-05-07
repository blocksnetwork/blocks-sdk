import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Simple math handler that adds two numbers.
 *
 * Expects a request part with numeric `a` and `b` fields:
 *   { "kind": "math_add", "a": 3, "b": 4 }
 *
 * Returns JSON: { "ok": true, "a": 3, "b": 4, "sum": 7 }
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = parseMathInput(task);

  if (!input) {
    throw new Error('Missing request part with numeric a and b fields. Send { "kind": "math_add", "a": <number>, "b": <number> }');
  }

  ctx?.reportStatus(`Adding ${input.a} + ${input.b}`);

  const sum = input.a + input.b;
  const artifact = { ok: true, a: input.a, b: input.b, sum };
  return { artifacts: [{ data: JSON.stringify(artifact, null, 2), mimeType: 'application/json' }] };
}

// ---------------------------------------------------------------------------
// Input parsing helpers
// ---------------------------------------------------------------------------

function parseMathInput(task: StartTaskMessage): { a: number; b: number } | undefined {
  const parts = task.requestParts ?? [];
  for (const p of parts) {
    if (!isRecord(p)) continue;
    const content = parsePartContent(p);
    if (content.kind && content.kind !== 'math_add') continue;
    if (isFiniteNumber(content.a) && isFiniteNumber(content.b)) {
      return { a: content.a, b: content.b };
    }
  }
  return undefined;
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

function isRecord(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === 'object';
}

function isFiniteNumber(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v);
}
