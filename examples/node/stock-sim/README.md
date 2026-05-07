# Stock Sim (Node)

Canonical pipe-streaming provider example. Streams random stock price
updates once per second for the requested symbols until the duration
expires or the task is cancelled.

**Category:** Canonical -- pipe streaming (provider)

## Prerequisites

- Node.js 24+
- `BLOCKS_API_KEY` (run `blocks login` to generate)

## Install

```bash
cd examples/node/stock-sim
npm install
```

## Run

```bash
blocks run
```

The agent registers as `stock-sim` and waits for pipe tasks. Use the
stock-sim-consumer example or the dashboard to submit tasks.

## SDK concepts demonstrated

- Pipe task handling (`task.taskKind === 'pipe'`)
- `ctx.createStream({ format: 'events' })` for event streams
- `stream.write()` for continuous data emission
- `ctx.cancelSignal` (AbortSignal) for cooperative cancellation
- `ctx.isExpired` and `ctx.isCancelled` for completion reason
- `await stream.end()` for clean stream shutdown
- `agent-card.json` pipe-task configuration:
  - `heartbeatMs: 0` (presence gating temporarily disabled)
  - `maxRunningTimeSec: 3600` (long-running task)

## Pipe task lifecycle

Pipe tasks differ from request tasks:
- The server computes `durationExpiresAtMs` from `task.duration`
- The SDK runs a local duration timer that fires `cancelSignal` when
  expired
- The handler runs until cancelled, expired, or terminated
- `heartbeatMs: 0` disables presence gating (temporarily disabled
  across the platform)

## What to edit

- Change `buildQuote()` in `handler.ts` for different data shapes.
- Add more complex simulation logic.
- Adjust the tick interval (currently 1 second).
