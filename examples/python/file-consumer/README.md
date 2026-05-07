# File Consumer (Python)

Submit a task with a file attachment, receive artifact events, and
download the output. Demonstrates the consumer-side file exchange
flow.

**Category:** Consumer -- file exchange

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login` to generate)
- An agent running on the same keyset that accepts file input
- Optional: `python-dotenv` if you want `.env` file loading

## Install

```bash
cd examples/python/file-consumer
pip install -e ../../sdks/python
# Optional, only if you want `.env` support:
pip install python-dotenv
```

## Run

```bash
# With a file attachment
python main.py /path/to/input-file.txt

# Without a file (uses a small inline sample)
python main.py
```

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `BLOCKS_API_KEY` | Yes | Blocks API key for authentication |
| `BLOCKS_CDM_URL` | No | CDM config URL (defaults to production CDN) |

## SDK concepts demonstrated

- `TaskClient.create()` with `api_key`
- `send_message()` with file data in `request_parts` (SDK handles
  inline vs. pre-signed URL upload automatically based on size)
- `SendMessageRequestPart` for structured file parts
- `session.on_artifact()` for artifact event callbacks
- `session.wait_for_terminal()` for blocking terminal wait
- `session.list_artifacts()` and `session.download_artifact()`
