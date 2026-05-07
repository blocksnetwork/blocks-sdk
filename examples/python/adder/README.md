# Adder (Python)

Canonical baseline request/response example. Parses structured JSON
input, validates fields, and returns a computed result as JSON.

**Category:** Canonical -- request/response

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login` to generate)

## Install

```bash
cd examples/python/adder
pip install -e .
```

## Run

```bash
blocks run
```

Send a task with input `{ "kind": "math_add", "a": 5, "b": 3 }` to
receive `{ "ok": true, "a": 5, "b": 3, "sum": 8 }`.

## SDK concepts demonstrated

- Structured JSON input parsing from `task.request_parts`
- Input validation with error raising
- `ctx.report_status()` for progress updates
- Returning JSON artifacts with `mimeType: 'application/json'`

## What to edit

- Change `_parse_math_input()` in `handler.py` to accept different
  input shapes.
- Update `agent-card.json` to change the agent type or schema.
