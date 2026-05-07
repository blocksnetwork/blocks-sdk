# Stock Sim Consumer (Python)

Canonical pipe-streaming consumer example. Submits a pipe task to the
stock-sim agent and consumes the resulting stock-price stream in real
time, printing each quote as it arrives.

**Category:** Canonical -- pipe streaming (consumer)

## Prerequisites

- Python 3.10+
- PubNub keys (required for standalone consumer mode)
- The `stock-sim` or `stock-sim-python` agent running (same keyset)

## Install

```bash
cd examples/python/stock-sim-consumer
pip install -e .
```

## Run as an agent

```bash
blocks run
```

When a task arrives, the consumer handler dispatches a pipe task to
stock-sim and returns a summary artifact.

## Run as an interactive consumer

```bash
python main.py
```

Prompts for symbols, duration, and provider (Node/Python), then
prints streamed quotes in real time.

## SDK concepts demonstrated

- `TaskClient.send_message()` with `task_kind="pipe"` and `duration`
- `TaskSession` as the per-task lifecycle manager
- `session.wait_for_stream()` for stream discovery
- `stream_ref.open()` to get a `StreamClient`
- `for event in stream.events()` for decoded `format: "events"` iteration (stock-sim emits `events`); use `stream.bytes()` for `format: "bytes"` streams
- `stream.inbound` is the lower-level wire iterator -- reach for it only when you need raw envelope metadata (`seq`, `ts`, `encoding`)
- `session.close()` for cleanup

## What to edit

- Change `stock_sim_client.py` to modify prompt defaults or quote
  display formatting.
- Target a different provider by changing the `PROVIDERS` map.
