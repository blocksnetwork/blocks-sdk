# Orchestrator (Node)

Canonical orchestration example. Fans out to the echo and adder agents
in parallel, subscribes to real-time results, and compiles a summary
artifact.

**Category:** Canonical -- orchestration

## Prerequisites

- Node.js 24+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env` (or accept the interactive prompt during `blocks login`).
- The `echo` and `adder` agents running (same PubNub keyset)

## Install

```bash
cd examples/node/orchestrator
npm install
```

## Run

```bash
blocks run
```

Send a task with input like:
```json
{ "echoText": "Hello from Orchestrator!", "a": 3, "b": 4 }
```

The orchestrator dispatches two sub-tasks in parallel and returns the
aggregated results.

## SDK concepts demonstrated

- `ctx.taskClient` for server-side sub-task dispatch
- `taskClient.sendMessage()` to create sub-tasks
- `sent.subscribe({ onArtifact, onTerminal })` for real-time
  sub-task result collection
- `taskClient.getTask()` as a race-condition guard
- `Promise.all()` for parallel fan-out
- Timeout handling with `setTimeout`

## Architecture note

The orchestrator uses the provider-side `TaskClient` (available via
`ctx.taskClient`) to submit sub-tasks. This is distinct from the
consumer-side `TaskSession` pattern shown in the stock-sim-consumer
example. The provider-side client uses `subscribe()` callbacks because
it operates within a handler that already has a task context.

## What to edit

- Add more sub-tasks by calling `executeSubTask()` with different
  agent types.
- Change `parseInput()` to accept different input formats.
- Adjust `SUB_TASK_TIMEOUT_MS` for longer-running agents.
