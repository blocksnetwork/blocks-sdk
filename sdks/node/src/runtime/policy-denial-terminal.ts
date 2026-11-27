import type { TerminalEvent } from './task-session.js';

const POLICY_REJECTED_CODE = 'policy_rejected';
const POLICY_UNAVAILABLE_CODE = 'policy_unavailable';
const STAGES = ['input', 'response'] as const;

// A denial is read off a terminal an agent may have written, so every detail is bounded.
const MAX_REASON_LENGTH = 200;
const MAX_POLICY_IDS = 16;
const MAX_POLICY_ID_LENGTH = 64;

/**
 * Why the policy gate stopped a task: `policy_rejected` when a policy said no, `policy_unavailable` when the check itself broke.
 * `stage` is `input` (the task, before the agent got it) or `response` (an answer, before the caller got it).
 *
 * @experimental Part of the policy gate preview; the shape may change before general availability.
 */
export interface PolicyDenial {
  code: 'policy_rejected' | 'policy_unavailable';
  stage: 'input' | 'response';
  /** The policy's stated reason; never set on `policy_unavailable`, and absent when the gate gave none. */
  reason?: string;
  /** Ids of the policy versions in force for the check; empty when the gate lost the check. */
  policyIds: string[];
}

/** Length is counted in code points, as the policy gate and the Python SDK count it. */
const isShortText = (value: unknown, max: number): value is string => {
  if (typeof value !== 'string' || value.length === 0) return false;
  return value.length <= max || [...value].length <= max;
};

function policyIdsOf(value: unknown): string[] | null {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > MAX_POLICY_IDS) return null;
  return value.every((id) => isShortText(id, MAX_POLICY_ID_LENGTH)) ? [...value] : null;
}

/**
 * The policy denial in a failed terminal's `error` and `errorDetails`, else null (or on malformed details).
 * Unlike `waitForTerminal`'s auth refusal, this never throws: `waitForTerminal` still resolves on a denial.
 * A denial is trustworthy only where the policy gate's response check is deployed, since it strips `errorDetails` from agent terminals that carry a policy code.
 *
 * @experimental Part of the policy gate preview; the shape may change before general availability.
 */
export function policyDenialFromTerminal(event: TerminalEvent): PolicyDenial | null {
  const { state, error: code, errorDetails } = event;
  if (state !== 'failed') return null;
  if (code !== POLICY_REJECTED_CODE && code !== POLICY_UNAVAILABLE_CODE) return null;
  if (!errorDetails || typeof errorDetails !== 'object' || Array.isArray(errorDetails)) return null;
  const { stage, reason, policyIds } = errorDetails;
  if (!STAGES.includes(stage as PolicyDenial['stage'])) return null;
  const ids = policyIdsOf(policyIds);
  if (!ids) return null;
  const denial: PolicyDenial = { code, stage: stage as PolicyDenial['stage'], policyIds: ids };
  if (code === POLICY_UNAVAILABLE_CODE || reason === undefined) return denial;
  if (!isShortText(reason, MAX_REASON_LENGTH)) return null;
  return { ...denial, reason };
}
