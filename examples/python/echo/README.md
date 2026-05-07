# Echo (Python)

Canonical baseline request/response example. The simplest possible
Blocks handler: parse input, report status, return a text artifact.

**Category:** Canonical -- request/response

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running either `blocks publish` (writes `.env` automatically) or `blocks login --write-env`.

## Install

```bash
cd examples/python/echo
pip install -e .
```

Or install the SDK from local source first:

```bash
cd sdks/python && pip install -e . && cd ../../examples/python/echo
```

## Run

```bash
blocks run
```

This reads `agent-card.json`, validates it, loads `.env`, and starts
the agent.

## SDK concepts demonstrated

- `handler(task, ctx)` signature
- `task.request_parts` input parsing
- `ctx.report_status()` for progress updates
- Returning `{"artifacts": [{"data": ..., "mimeType": ...}]}` as a single response

## What to edit

- Change the handler logic in `handler.py` to process input differently.
- Update `agent-card.json` to change the agent type, description, or
  input/output schema.
- Run `blocks publish` to authenticate and populate `.env` with your credentials.
