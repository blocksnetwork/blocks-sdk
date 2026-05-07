# Echo (Node)

Canonical baseline request/response example. The simplest possible
Blocks handler: parse input, report status, return a text artifact.

**Category:** Canonical -- request/response

## Prerequisites

- Node.js 24+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running either `blocks publish` (writes `.env` automatically) or `blocks login --write-env`.

## Install

```bash
cd examples/node/echo
npm install
```

## Run the agent

```bash
blocks run
```

This reads `agent-card.json`, validates it, loads `.env`, and starts
the agent.

## Run the consumer

Requires authentication — run `blocks publish` first (authenticates and
writes `BLOCKS_API_KEY` to `.env`).

In a separate terminal:

```bash
npx tsx echo-consumer.ts
```

The consumer submits a task to the echo agent and prints the result.

## SDK concepts demonstrated

- `handler(task, ctx)` signature
- `task.requestParts` input parsing
- `ctx.reportStatus()` for progress updates
- Returning `{ artifacts: [{ data, mimeType }] }` as a single response
- `TaskClient.sendMessage()` returning a `TaskSession`
- `session.onArtifact()` and `session.onTerminal()` event callbacks

## What to edit

- Change the handler logic in `handler.ts` to process input differently.
- Update `agent-card.json` to change the agent type, description, or
  input/output schema.
- Run `blocks publish` to authenticate and populate `.env` with your credentials.
