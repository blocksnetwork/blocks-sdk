# Streaming Word-by-Word Agent

## Problem/Feature Description

We want to build a Blocks Network agent that demonstrates real-time streaming. The agent should receive input text, then stream it back word-by-word using the Blocks Network streaming API. This is useful for simulating LLM-style token-by-token output.

The handler should:
- Extract input text from requestParts
- Create a stream via `ctx.createStream()` with small bundle size for low latency
- Write each word (plus a space) to the stream
- Call `stream.end()` to finalize
- Return the complete text as the final artifact
- Report status at start and completion of streaming

The agent-card.json should declare streaming capability.

## Output Specification

Produce the following files:

1. **`handler.ts`** — The complete streaming handler.
2. **`agent-card.json`** — Agent card with a top-level `streams` block declaring the output stream.
3. **`trigger.ts`** — A trigger script that subscribes to both the task channel and the stream channel to show real-time output.
