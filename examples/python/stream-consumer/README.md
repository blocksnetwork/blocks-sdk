# Stream Consumer (Python)

Submit a pipe task to an agent and consume the resulting data stream
in real time. Demonstrates stream discovery, opening, and iteration.

**Category:** Consumer -- pipe streaming

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running either `blocks publish` (writes `.env` automatically) or `blocks login --write-env`.
- A streaming agent running on the same keyset (e.g., echo-stream)
- Optional: `python-dotenv` if you want `.env` file loading

## Install

```bash
cd examples/python/stream-consumer
pip install -e ../../sdks/python
# Optional, only if you want `.env` support:
pip install python-dotenv
```

## Run

```bash
python main.py
```

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `BLOCKS_API_KEY` | Yes | Blocks API key for authentication |
| `BLOCKS_CDM_URL` | No | CDM config URL (defaults to production CDN) |

## SDK concepts demonstrated

- `TaskClient.create()` with `api_key`
- `send_message()` with `task_kind="pipe"` and `duration`
- `session.wait_for_stream()` for stream discovery
- `stream_ref.open()` to get a `StreamClient`
- `for event in stream.events()` for decoded `format: "events"` iteration (echo-stream emits `events`); use `stream.bytes()` for `format: "bytes"` streams
- `stream.inbound` is the lower-level wire iterator -- reach for it only when you need raw envelope metadata (`seq`, `ts`, `encoding`)
- `session.close()` and `client.destroy()` for cleanup
