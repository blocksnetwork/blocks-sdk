# Stock Sim (Python)

Canonical pipe-streaming provider example. Streams random stock price
updates once per second for the requested symbols until the duration
expires or the task is cancelled.

**Category:** Canonical -- pipe streaming (provider)

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login` to generate)

## Install

```bash
cd examples/python/stock-sim
pip install -e .
```

## Run

```bash
blocks run
```

The agent registers as `stock-sim-python` and waits for pipe tasks.

## SDK concepts demonstrated

- Pipe task handling (`task.task_kind == "pipe"`)
- `ctx.create_stream(format="events")` for event streams
- `stream.write()` for continuous data emission
- `ctx.is_cancelled` for cooperative cancellation
- `ctx.is_expired` for duration-based completion
- `stream.end()` for clean stream shutdown
- `agent-card.json` pipe-task configuration:
  - `capabilities.taskKinds: ["pipe"]` (pipe-only agent)
  - `runtime.maxRunningTimeSec: 3600` (long-running task)
  - `streams.prices` declared stream with `format: "events"`

## What to edit

- Change `_build_quote()` in `handler.py` for different data shapes.
- Add more complex simulation logic.
- Adjust the sleep interval (currently 1 second).
