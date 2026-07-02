# Advanced Stream (Python)

Canonical example for **advanced streaming**. A single pipe task opens
three named streams at once, showing how multi-stream, schema-validated
events, and shared-affinity broadcast compose:

| Declared stream | direction | format   | affinity  | Demonstrates                                  |
| --------------- | --------- | -------- | --------- | --------------------------------------------- |
| `events`        | outbound  | `events` | dedicated | Structured JSON events validated by a schema  |
| `raw`           | outbound  | `bytes`  | dedicated | Raw UTF-8 chunks read as `bytes`              |
| `broadcast`     | outbound  | `events` | shared    | Shared channel with no per-task `stream_end`  |

**Category:** Canonical -- advanced streams (multi-stream / events-schema / shared)

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login --write-env` to generate)

## Install

```bash
cd examples/python/advanced-stream
pip install -e .            # agent
pip install -e ".[consumer]"  # + python-dotenv for the consumer
```

## Run the agent

```bash
blocks register   # register privately (recommended first step)
blocks run
```

## Run the consumer

In a second terminal:

```bash
python main.py        # default 5 ticks
python main.py 10     # request 10 ticks
```

You'll see events from all three streams interleaved, each tagged with
its declared stream name.

## SDK concepts demonstrated

**Provider (`handler.py`)**

- Multiple named streams on one task, each selected by `declared_stream`
- `ctx.create_stream(declared_stream=..., format="events" | "bytes")` —
  affinity (`dedicated` / `shared`) is declared on the card, not passed here
- Writing schema-conformant events, raw bytes, and shared broadcast events
- `ctx.is_cancelled` for cooperative cancellation; `stream.end()`

**Consumer (`main.py`)**

- Current consumer surface: `TaskClient.create()`, `send_message()`,
  `session.on_stream()`, `session.wait_for_terminal()`
- Branching on `descriptor.declared_stream` and `descriptor.format`
- Reading `stream.events()` and `stream.bytes()` on daemon threads
  (Python stream iterators are blocking)
- `stream.on_error()` for subscribe-level errors

## Shared streams: no per-task `stream_end`

Dedicated streams emit a `stream_end` marker when the agent calls
`stream.end()`, so iterating them completes naturally. A **shared**-affinity
stream (`broadcast`) suppresses the per-task marker — its channel is meant
to be shared across tasks — so its reader thread only unwinds when the
consumer closes the session. That's why the consumer waits for terminal
first, then calls `session.close()` to tear the readers down.

Because the shared channel is stable across tasks, subscribing replays
its recent in-memory cache. So a second run may print `broadcast` events
from a **previous** run before its own — that's expected shared-stream
behavior, not a bug. The dedicated `events` / `raw` streams are per-task
and always show exactly the ticks the current task produced.

## What to edit

- Change the event shapes in `handler.py` (keep the `events` stream
  conformant to the card schema).
- Add another declared stream to `agent-card.json` and a matching branch
  in the consumer.
