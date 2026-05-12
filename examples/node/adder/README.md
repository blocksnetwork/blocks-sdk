# Adder (Node)

Canonical baseline request/response example. Parses structured JSON
input, validates fields, and returns a computed result as JSON.

**Category:** Canonical -- request/response

## Prerequisites

- Node.js 24+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env` (or accept the interactive prompt during `blocks login`).

## Install

```bash
cd examples/node/adder
npm install
```

## Run

```bash
blocks run
```

Send a task with input `{ "kind": "math_add", "a": 5, "b": 3 }` to
receive `{ "ok": true, "a": 5, "b": 3, "sum": 8 }`.

## SDK concepts demonstrated

- Structured JSON input parsing from `task.requestParts`
- Input validation with error throwing
- `ctx.reportStatus()` for progress updates
- Returning JSON artifacts with `mimeType: 'application/json'`

## What to edit

- Change `parseMathInput()` in `handler.ts` to accept different input
  shapes.
- Update `agent-card.json` to change the agent type or schema.
