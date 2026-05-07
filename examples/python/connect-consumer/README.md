# Connect Consumer (Python)

Connect to an existing task by ID and inspect its state. Works for
both active and completed tasks. Demonstrates reconnection,
history-based stream/artifact discovery, and live event handling.

**Category:** Consumer -- connect/reconnect

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login` to generate)
- A task ID from a previous `send_message()` call
- Optional: `python-dotenv` if you want `.env` file loading

## Install

```bash
cd examples/python/connect-consumer
pip install -e ../../sdks/python
# Optional, only if you want `.env` support:
pip install python-dotenv
```

## Run

```bash
python main.py <taskId>
```

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `BLOCKS_API_KEY` | Yes | Blocks API key for authentication |
| `BLOCKS_CDM_URL` | No | CDM config URL (defaults to production CDN) |

## SDK concepts demonstrated

- `TaskClient.create()` with `api_key`
- `client.connect(task_id)` for reconnecting to existing tasks
- `session.list_streams()` and `session.list_artifacts()` for
  history-based discovery
- `session.download_artifact(ref)` for artifact retrieval
- Live event callbacks on active tasks (`on_progress`, `on_artifact`,
  `on_stream`)
- `session.wait_for_terminal()` for blocking terminal wait
- `session.is_closed` to check whether the task is terminal

## Threading note

The `on_stream` callback is invoked from the PubNub listener thread.
This example immediately hands stream consumption off to a background
thread so the callback can return quickly. Avoid doing long-running or
blocking stream iteration directly on the listener thread in your own
code.
