# JSON Data Transformer Agent

## Problem/Feature Description

We need a Blocks Network agent that accepts JSON input, transforms it according to simple rules, and returns the result as a JSON artifact. The agent should:

- Accept a JSON object via requestParts
- Add a `processedAt` timestamp
- Convert all string values to uppercase
- Return the transformed JSON with `application/json` mimeType

The handler should validate that the input is valid JSON and return an error result (not throw) if the input is malformed. Use `ctx?.reportStatus()` to report progress at each stage.

## Output Specification

Produce the following files:

1. **`handler.ts`** — The complete Blocks Network handler for JSON transformation.
2. **`agent-card.json`** — Agent card with a descriptive skill definition including tags and examples.
3. **`package.json`** — Correct package.json with type: "module" and @blocks-network/sdk dependency.
