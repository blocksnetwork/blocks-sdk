import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Multi-turn chat handler with context retention -- no LLM, no API key.
 *
 * Each turn is an independent `request` task. Conversation state is kept in
 * an in-process Map keyed by a `conversationId` that the agent mints on the
 * first turn and returns in its artifact. The consumer threads that id back
 * into every following turn, so context is preserved across tasks even though
 * the wire protocol has no built-in notion of a conversation.
 *
 * Input (first turn):
 *   { "text": "hi, I'm Alice" }
 * Input (follow-up turn):
 *   { "text": "what's my name?", "conversationId": "c-ab12cd" }
 *
 * Output artifact (application/json):
 *   { "ok": true, "reply": "...", "conversationId": "c-ab12cd", "turn": 2, "remembered": 1 }
 *
 * The replies are deterministic and exist only to *prove* that earlier turns
 * are remembered:
 *   - "I'm X" / "my name is X"   -> stores the name
 *   - "what's my name"           -> recalls the stored name
 *   - "what did I say first"     -> replays turn 1
 *   - anything else              -> acknowledges and reports turn/history counts
 *
 * NOTE: state lives in memory, so the agent-card pins concurrency: 1 and
 * expectedInstances: 1. A production chat agent would persist conversations in
 * external storage (Redis, a database, ...) keyed by `conversationId` so any
 * instance can serve any turn.
 */

interface ConversationState {
  turns: string[];
  name?: string;
}

const conversations = new Map<string, ConversationState>();

export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const { text, conversationId: incomingId } = extractInput(task.requestParts ?? []);

  if (text === null) {
    throw new Error('Missing request part with a "text" field. Send { "text": "<message>", "conversationId": "<optional id>" }');
  }

  // Resume an existing conversation, or start a fresh one. An unknown id is
  // treated as a new conversation (state may have been lost on restart).
  const conversationId =
    incomingId && conversations.has(incomingId) ? incomingId : newConversationId(task.taskId);

  const state = conversations.get(conversationId) ?? { turns: [] };
  conversations.set(conversationId, state);

  state.turns.push(text);
  const capturedName = extractName(text);
  if (capturedName) {
    state.name = capturedName;
  }

  ctx?.reportStatus(`Turn ${state.turns.length} of conversation ${conversationId}`);

  const reply = composeReply(text, state);

  const payload = {
    ok: true,
    reply,
    conversationId,
    turn: state.turns.length,
    // prior messages remembered from earlier turns (excludes the current one)
    remembered: state.turns.length - 1,
  };

  return {
    artifacts: [{ data: JSON.stringify(payload, null, 2), mimeType: 'application/json' }],
  };
}

// ---------------------------------------------------------------------------
// Deterministic reply engine
// ---------------------------------------------------------------------------

function composeReply(text: string, state: ConversationState): string {
  const normalized = text.trim().toLowerCase();

  if (/\b(what(?:'s| is| was)? my name|who am i)\b/.test(normalized)) {
    return state.name
      ? `You're ${state.name}.`
      : "I don't know your name yet -- tell me with \"I'm <name>\".";
  }

  if (/\bwhat did i say first\b/.test(normalized) || /\bmy first (message|line)\b/.test(normalized)) {
    return `Your first message was: "${state.turns[0]}"`;
  }

  if (/\bhow many (messages|turns)\b/.test(normalized)) {
    return `We're on turn ${state.turns.length}; I remember all ${state.turns.length} of your messages.`;
  }

  const capturedName = extractName(text);
  if (capturedName) {
    return `Nice to meet you, ${capturedName}! I'll remember that. (turn ${state.turns.length})`;
  }

  const prefix = state.name ? `${state.name}, you said` : 'You said';
  return `${prefix}: "${text}". That's turn ${state.turns.length} -- I've kept the whole conversation.`;
}

// Demo-grade name capture: greedy enough that phrasings like "I'm fine" or
// "call me later" would otherwise be read as names. A small stop-word set
// guards the common false positives; a real agent would use an NLU model.
const NAME_STOP_WORDS = new Set([
  'fine', 'done', 'sorry', 'back', 'here', 'ok', 'okay', 'good', 'great',
  'later', 'now', 'sure', 'right',
]);

function extractName(text: string): string | undefined {
  const match =
    /\b(?:i'm|i am|my name is|call me)\s+([a-z][a-z'.-]*)/i.exec(text);
  if (!match) return undefined;
  const raw = match[1];
  if (NAME_STOP_WORDS.has(raw.toLowerCase())) return undefined;
  return raw.charAt(0).toUpperCase() + raw.slice(1);
}

// ---------------------------------------------------------------------------
// Input parsing helpers
// ---------------------------------------------------------------------------

interface ExtractedInput {
  text: string | null;
  conversationId: string | null;
}

function extractInput(parts: unknown[]): ExtractedInput {
  let text: string | null = null;
  let conversationId: string | null = null;

  for (const part of parts) {
    if (typeof part === 'string') {
      text = part;
      continue;
    }
    if (!isRecord(part)) continue;
    const content = parsePartContent(part);
    if (typeof content.text === 'string') {
      text = content.text;
    }
    if (typeof content.conversationId === 'string') {
      conversationId = content.conversationId;
    }
  }

  return { text, conversationId };
}

function parsePartContent(part: Record<string, unknown>): Record<string, unknown> {
  if (typeof part.text === 'string') {
    try {
      const parsed = JSON.parse(part.text);
      if (parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // text is a plain string, not JSON -- fall through and use the part as-is
    }
  }
  return part;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

// A short, human-readable id derived from the task id. Deterministic so the
// example needs no Math.random(); collisions across conversations are
// vanishingly unlikely because each first turn has a distinct taskId (only
// the first 6 hex chars are kept, so uniqueness is probabilistic, not exact).
function newConversationId(taskId: string | undefined): string {
  const suffix = (taskId ?? 'conv').replace(/[^a-z0-9]/gi, '').slice(0, 6).toLowerCase();
  return `c-${suffix || 'start'}`;
}
