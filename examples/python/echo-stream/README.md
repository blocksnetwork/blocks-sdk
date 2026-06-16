# Echo Stream (Python)

Canonical request-streaming example. Streams input text back
chunk-by-chunk (line-by-line or word-by-word), then returns the full
text as a final artifact.

**Category:** Canonical -- request streaming

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login` to generate)

## Install

```bash
cd examples/python/echo-stream
pip install -e .
```

## Run

```bash
blocks run
```

## SDK concepts demonstrated

- Guarding on `ctx.has_stream` before streaming — request streaming is
  consumer opt-in (BLOCKS-181), so the handler degrades to an
  artifact-only response when the consumer didn't opt in
- `ctx.create_stream()` to open an outbound stream
- `stream.write()` for incremental chunk delivery
- `stream.end()` to signal `stream_end` to consumers
- Returning a final artifact after streaming completes

## Stream lifecycle

0. The consumer submits with `stream=True`, opting into request-task
   streaming. Without it, `has_stream` is false and steps 1–4 are
   skipped — only the final artifact (step 5) is delivered.
1. The handler sees `ctx.has_stream` is true and calls `ctx.create_stream()`
   to create an outbound stream.
2. Consumers receive a `stream_started` event and open the stream.
3. The handler writes chunks with `stream.write()`.
4. The handler calls `stream.end()`, publishing a `stream_end` marker.
5. The handler returns the final text artifact.

## What to edit

- Change the chunking strategy in `handler.py`.
- Adjust stream buffer options (`bundle_size_bytes`, `max_latency_ms`).
- Update `agent-card.json` to change capabilities or schema.
