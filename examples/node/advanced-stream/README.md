# Advanced Stream (Node)

Canonical example for **advanced streaming**. A single pipe task opens
three named streams at once, showing how multi-stream, schema-validated
events, and shared-affinity broadcast compose:

| Declared stream | direction | format   | affinity  | Demonstrates                                  |
| --------------- | --------- | -------- | --------- | --------------------------------------------- |
| `events`        | outbound  | `events` | dedicated | Structured JSON events validated by a schema  |
| `raw`           | outbound  | `bytes`  | dedicated | Raw UTF-8 chunks read as `Uint8Array`         |
| `broadcast`     | outbound  | `events` | shared    | Shared channel with no per-task `stream_end`  |

**Category:** Canonical -- advanced streams (multi-stream / events-schema / shared)

## Prerequisites

- Node.js 22+
- `BLOCKS_API_KEY` (run `blocks login --write-env` to generate)

## Install

```bash
cd examples/node/advanced-stream
npm install
```

## Run the agent

```bash
blocks register   # register privately (recommended first step)
blocks run
```

## Run the consumer

In a second terminal:

```bash
npx tsx advanced-stream-consumer.ts        # default 5 ticks
npx tsx advanced-stream-consumer.ts 10     # request 10 ticks
```

You'll see events from all three streams interleaved, each tagged with
its declared stream name.

## SDK concepts demonstrated

**Provider (`handler.ts`)**

- Multiple named streams on one task, each selected by `declaredStream`
- `ctx.createStream({ declaredStream, format: 'events' | 'bytes' })` —
  affinity (`dedicated` / `shared`) is declared on the card, not passed here
- Writing schema-conformant events, raw bytes, and shared broadcast events
- `ctx.cancelSignal` for cooperative cancellation; `await stream.end()`

**Consumer (`advanced-stream-consumer.ts`)**

- Current consumer surface: `TaskClient.create()`, `sendMessage()`,
  `session.onStream()`, `session.waitForTerminal()`
- Branching on `descriptor.declaredStream` and `descriptor.format`
- Reading `stream.events<T>()` and `stream.bytes()` (`Uint8Array` chunks)
- `stream.onError()` for subscribe-level errors

## Shared streams: no per-task `stream_end`

Dedicated streams emit a `stream_end` marker when the agent calls
`stream.end()`, so a `for await` over them completes naturally. A
**shared**-affinity stream (`broadcast`) suppresses the per-task marker —
its channel is meant to be shared across tasks — so its reader only
unwinds when the consumer closes the session. That's why the consumer
does **not** await the stream readers before `waitForTerminal()`; it
tears them down via `session.asyncClose()` afterward.

Because the shared channel is stable across tasks, subscribing replays
its recent in-memory cache. So a second run may print `broadcast` events
from a **previous** run before its own — that's expected shared-stream
behavior, not a bug. The dedicated `events` / `raw` streams are per-task
and always show exactly the ticks the current task produced.

## What to edit

- Change the event shapes in `handler.ts` (keep the `events` stream
  conformant to the card schema).
- Add another declared stream to `agent-card.json` and a matching branch
  in the consumer.
