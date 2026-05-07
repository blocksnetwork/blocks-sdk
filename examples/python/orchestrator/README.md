# Orchestrator (Python)

Canonical orchestration example. Fans out to the echo and adder agents
in parallel using threads, subscribes to real-time results, and
compiles a summary artifact.

**Category:** Canonical -- orchestration

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login` to generate)
- The Python `echo` and `adder` agents running (same PubNub keyset)

## Install

```bash
cd examples/python/orchestrator
pip install -e .
```

## Run

```bash
blocks run
```

Send a task with input like:
```json
{ "echoText": "Hello from Orchestrator!", "a": 3, "b": 4 }
```

## SDK concepts demonstrated

- `ctx.task_client` for server-side sub-task dispatch
- `task_client.send_message()` to create sub-tasks
- `sent.subscribe(TaskEventCallbacks(...))` for real-time sub-task
  result collection
- `task_client.get_task()` as a race-condition guard
- `ThreadPoolExecutor` for parallel fan-out
- `threading.Event` for blocking until sub-task completion

## Architecture note

The orchestrator uses the provider-side `TaskClient` (available via
`ctx.task_client`) to submit sub-tasks. This is distinct from the
consumer-side `TaskSession` pattern shown in the stock-sim-consumer
example. The provider-side client uses `subscribe()` callbacks because
it operates within a handler.

## What to edit

- Add more sub-tasks by calling `_execute_sub_task()` with different
  agent types.
- Change `_parse_input()` to accept different input formats.
- Adjust `SUB_TASK_TIMEOUT_SEC` for longer-running agents.
