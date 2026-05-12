# Stock Sim Consumer (Node)

Canonical pipe-streaming consumer example. Includes both an agent handler
and a standalone interactive consumer utility.

**Category:** Canonical -- pipe streaming (consumer)

## Prerequisites

- Node.js 24+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env` (or accept the interactive prompt during `blocks login`).
- The `stock-sim` agent running (same PubNub keyset)

## Install

```bash
cd examples/node/stock-sim-consumer
npm install
```

## Start the agent

```bash
blocks run
```

This starts the stock-sim-consumer as a registered agent. When a task
arrives, the handler dispatches a pipe task to stock-sim and returns a
summary artifact.

## Run the interactive consumer utility

Requires authentication — run `blocks login --write-env` first (writes
`BLOCKS_API_KEY` to `.env`).

The `consume.ts` script is a **separate consumer utility**, not an agent
entrypoint. It is useful for manual testing and interactive exploration
of the stock-sim pipe stream.

```bash
npm run consume
# or equivalently:
npx tsx consume.ts
```

This prompts for symbols, duration, and provider (Node/Python), then
prints streamed quotes in real time.

## SDK concepts demonstrated

- `TaskClient.sendMessage()` with `taskKind: 'pipe'` and `duration`
- `TaskSession` as the per-task lifecycle manager
- `session.waitForStream()` for stream discovery
- `streamRef.open()` to get a `StreamClient`
- `for await (const ev of stream.events<T>())` for decoded event iteration
  (use `stream.bytes()` for `format: bytes` streams; `stream.inbound` is
  the lower-level wire iterator)
- `session.close()` for cleanup
- Auto-drain: `TaskSession` handles terminal-to-stream coordination
  automatically -- no manual `onTerminal` wiring needed for stream
  lifecycle

## Pipe task consumer lifecycle

1. `sendMessage()` submits a pipe task with a requested duration.
2. The server computes `durationExpiresAtMs` and starts the provider.
3. `waitForStream()` resolves when `stream_started` arrives.
4. `streamRef.open()` opens a `StreamClient` for the data channel.
5. The consumer iterates `stream.events<T>()` until `stream_end` or the
   drain window expires.
6. `session.close()` cleans up.

## What to edit

- Change `stock-sim-client.ts` to modify prompt defaults or quote
  display formatting.
- Target a different provider by changing the `PROVIDERS` map.
