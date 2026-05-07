# Request Consumer (Python)

Submit a request task to an agent, wait for the result, and download
artifacts. The simplest possible consumer flow.

**Category:** Consumer -- request/response

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login` to generate)
- An echo agent running on the same keyset
- Optional: `python-dotenv` if you want `.env` file loading

## Install

```bash
cd examples/python/request-consumer
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

- `TaskClient.create()` with `api_key` for consumer authentication
- `send_message()` with explicit `owner_id`
- `session.on_progress()`, `session.on_artifact()`
- `session.wait_for_terminal()` for blocking wait
- `session.list_artifacts()` and `session.download_artifact()`
- `session.close()` and `client.destroy()` for cleanup
