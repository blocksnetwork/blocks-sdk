# chat-agent (Python)

A multi-turn **chat agent that remembers context across turns** -- no LLM,
no API key. It demonstrates the conversation pattern used by assistants like
Claude Code: you send a message, the agent replies, you send another, and the
earlier context is still there.

**Category:** Canonical -- multi-turn / context retention

## How it works

Each turn is an independent `request` task. The Blocks wire protocol has no
built-in conversation thread, so the agent carries one itself:

1. On the **first turn** the consumer sends `{ "text": "..." }` with no id.
2. The agent mints a `conversationId`, stores the conversation in memory, and
   returns the id in its artifact.
3. On **every following turn** the consumer includes that
   `conversationId`, so the agent looks up the existing conversation and can
   recall earlier messages.

```
[you]   hi, I'm Alice          -> { conversationId: "c-ab12cd", turn: 1 }
[you]   what's my name?       (conversationId: "c-ab12cd") -> "You're Alice."
[you]   what did I say first? (conversationId: "c-ab12cd") -> "Your first message was: hi, I'm Alice"
```

The reply logic is deterministic and exists only to prove context is kept:
it captures your name from `"I'm <name>"`, recalls it on `"what's my name"`,
replays turn 1 on `"what did I say first"`, and reports the turn count.

> **In-memory caveat.** Conversation state lives in a dict in the agent
> process, so the agent-card pins `concurrency: 1` and `expectedInstances: 1`.
> A production chat agent would persist conversations in external storage
> (Redis, a database, ...) keyed by `conversationId` so any instance can serve
> any turn, and state survives restarts.

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running
  `blocks login --write-env`.

## Install

```bash
cd examples/python/chat-agent
pip install -e .
```

## Run

In one terminal -- start the agent:

```bash
cd examples/python/chat-agent
blocks register  # first time only -- registers chat_agent_python (private + free)
blocks run
# Later, to make the agent public or set pricing: blocks publish
```

In another terminal -- run the consumer. With no arguments it starts an
**interactive REPL**: type a message, read the reply, keep going. Type
`exit` or `quit` (or press Ctrl-D) to end.

```bash
cd examples/python/chat-agent
python main.py
# [you]   hi, I'm Alice
# [agent] Nice to meet you, Alice.
# [you]   what's my name?
# [agent] You're Alice.
# [you]   exit

# or pass turns as arguments to run them non-interactively, then exit:
python main.py "I'm Sam" "what's my name?" "how many turns have we had?"
```

## SDK concepts demonstrated

- Multi-turn conversation over independent `request` tasks
- Application-level session threading via a `conversationId` round-tripped
  through the artifact (the wire protocol has no conversation field)
- Structured JSON input parsing from `task.request_parts`
- Returning JSON artifacts with `mimeType: 'application/json'`
- Consumer flow: `send_message` -> `wait_for_terminal` -> `download_artifact`

## What to edit

- Change `_compose_reply()` in `handler.py` to add new intents, or swap the
  deterministic logic for a call to an LLM (pass the stored `state["turns"]`
  as history).
- Replace the in-memory dict with a Redis/DB lookup to make the agent
  horizontally scalable.
