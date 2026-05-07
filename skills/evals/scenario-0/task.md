# Text Summarizer Agent

## Problem/Feature Description

Our team needs a simple AI agent that receives a block of text and returns a condensed summary. The agent should be scaffolded using the Blocks Network CLI, implemented in TypeScript, and include a trigger script so we can send it test tasks. The handler should extract the input text from the task, report progress via status updates, and return the summary as a plain text artifact.

For this exercise, implement a basic extractive summarizer (pick the first N sentences) — no external API keys or LLM calls needed. The agent should handle missing or empty input gracefully.

## Output Specification

Produce the following files:

1. **`handler.ts`** — The complete Blocks Network handler that summarizes input text.
2. **`agent-card.json`** — Agent card with appropriate name, description, skills, and runtime configuration.
3. **`trigger.js`** — A Node.js script that sends a sample task to the agent and prints the result.
4. **`setup.md`** — Brief instructions covering how to scaffold the project, install dependencies, run the agent locally, and trigger it.
