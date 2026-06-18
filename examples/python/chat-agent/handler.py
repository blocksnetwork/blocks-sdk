"""Multi-turn chat handler with context retention -- no LLM, no API key.

Mirrors the Node chat-agent example handler.

Each turn is an independent ``request`` task. Conversation state is kept in an
in-process dict keyed by a ``conversationId`` that the agent mints on the first
turn and returns in its artifact. The consumer threads that id back into every
following turn, so context is preserved across tasks even though the wire
protocol has no built-in notion of a conversation.

Input (first turn):
    { "text": "hi, I'm Alice" }
Input (follow-up turn):
    { "text": "what's my name?", "conversationId": "c-ab12cd" }

Output artifact (application/json):
    { "ok": true, "reply": "...", "conversationId": "c-ab12cd", "turn": 2, "remembered": 1 }

The replies are deterministic and exist only to *prove* that earlier turns are
remembered:
    - "I'm X" / "my name is X"   -> stores the name
    - "what's my name"           -> recalls the stored name
    - "what did I say first"     -> replays turn 1
    - anything else              -> acknowledges and reports turn/history counts

NOTE: state lives in memory, so the agent-card pins concurrency: 1 and
expectedInstances: 1. A production chat agent would persist conversations in
external storage (Redis, a database, ...) keyed by ``conversationId`` so any
instance can serve any turn.
"""

from __future__ import annotations

import json
import re
from typing import Any, Dict, List, Optional, Tuple

from blocks_network.types import StartTaskMessage, TaskContext

# conversationId -> {"turns": [...], "name": Optional[str]}
_conversations: Dict[str, Dict[str, Any]] = {}

_NAME_RE = re.compile(r"\b(?:i'm|i am|my name is|call me)\s+([a-z][a-z'.-]*)", re.IGNORECASE)
# Demo-grade name capture: greedy enough that phrasings like "I'm fine" or
# "call me later" would otherwise be read as names. A small stop-word set guards
# the common false positives; a real agent would use an NLU model.
_NAME_STOP_WORDS = frozenset(
    {"fine", "done", "sorry", "back", "here", "ok", "okay", "good", "great", "later", "now", "sure", "right"}
)
_ASK_NAME_RE = re.compile(r"\b(what(?:'s| is| was)? my name|who am i)\b")
_FIRST_RE = re.compile(r"\bwhat did i say first\b|\bmy first (?:message|line)\b")
_COUNT_RE = re.compile(r"\bhow many (?:messages|turns)\b")


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    text, incoming_id = _extract_input(task.request_parts or [])

    if text is None:
        raise ValueError(
            'Missing request part with a "text" field. '
            'Send { "text": "<message>", "conversationId": "<optional id>" }'
        )

    # Resume an existing conversation, or start a fresh one. An unknown id is
    # treated as a new conversation (state may have been lost on restart).
    if incoming_id and incoming_id in _conversations:
        conversation_id = incoming_id
    else:
        conversation_id = _new_conversation_id(task.task_id)

    state = _conversations.setdefault(conversation_id, {"turns": [], "name": None})

    state["turns"].append(text)
    captured = _extract_name(text)
    if captured:
        state["name"] = captured

    if ctx:
        ctx.report_status(f"Turn {len(state['turns'])} of conversation {conversation_id}")

    reply = _compose_reply(text, state)

    payload = {
        "ok": True,
        "reply": reply,
        "conversationId": conversation_id,
        "turn": len(state["turns"]),
        # prior messages remembered from earlier turns (excludes the current one)
        "remembered": len(state["turns"]) - 1,
    }

    return {
        "artifacts": [{"data": json.dumps(payload, indent=2), "mimeType": "application/json"}],
    }


# ---------------------------------------------------------------------------
# Deterministic reply engine
# ---------------------------------------------------------------------------


def _compose_reply(text: str, state: Dict[str, Any]) -> str:
    normalized = text.strip().lower()
    turns: List[str] = state["turns"]
    name: Optional[str] = state["name"]

    if _ASK_NAME_RE.search(normalized):
        if name:
            return f"You're {name}."
        return "I don't know your name yet -- tell me with \"I'm <name>\"."

    if _FIRST_RE.search(normalized):
        return f'Your first message was: "{turns[0]}"'

    if _COUNT_RE.search(normalized):
        return f"We're on turn {len(turns)}; I remember all {len(turns)} of your messages."

    captured = _extract_name(text)
    if captured:
        return f"Nice to meet you, {captured}! I'll remember that. (turn {len(turns)})"

    prefix = f"{name}, you said" if name else "You said"
    return f'{prefix}: "{text}". That\'s turn {len(turns)} -- I\'ve kept the whole conversation.'


def _extract_name(text: str) -> Optional[str]:
    match = _NAME_RE.search(text)
    if not match:
        return None
    raw = match.group(1)
    if raw.lower() in _NAME_STOP_WORDS:
        return None
    return raw[0].upper() + raw[1:]


# ---------------------------------------------------------------------------
# Input parsing helpers
# ---------------------------------------------------------------------------


def _extract_input(parts: List[Any]) -> Tuple[Optional[str], Optional[str]]:
    text: Optional[str] = None
    conversation_id: Optional[str] = None

    for part in parts:
        if isinstance(part, str):
            text = part
            continue
        content = _parse_part_content(part)
        if isinstance(content.get("text"), str):
            text = content["text"]
        if isinstance(content.get("conversationId"), str):
            conversation_id = content["conversationId"]

    return text, conversation_id


def _parse_part_content(part: Any) -> Dict[str, Any]:
    """Extract structured content from a RequestPart or raw dict.

    The consumer serializes the message + conversationId into the ``text``
    field as JSON. This helper parses it back into a dict, falling back to a
    plain-text wrapper or the part's own ``extra``/dict form.
    """
    text = getattr(part, "text", None) if not isinstance(part, dict) else part.get("text")
    if isinstance(text, str):
        try:
            parsed = json.loads(text)
            if isinstance(parsed, dict):
                return parsed
        except (json.JSONDecodeError, ValueError):
            pass
        return {"text": text}
    if hasattr(part, "extra") and isinstance(part.extra, dict):
        return part.extra
    if isinstance(part, dict):
        return part
    return {}


def _new_conversation_id(task_id: Optional[str]) -> str:
    """A short, human-readable id derived from the task id.

    Deterministic so the example needs no randomness; collisions across
    conversations are vanishingly unlikely because each first turn has a
    distinct task_id (only the first 6 hex chars are kept, so uniqueness is
    probabilistic, not exact).
    """
    suffix = re.sub(r"[^a-z0-9]", "", (task_id or "conv"), flags=re.IGNORECASE)[:6].lower()
    return f"c-{suffix or 'start'}"
