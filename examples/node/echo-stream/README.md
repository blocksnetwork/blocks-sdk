# Echo Stream (Node)

Canonical request-streaming example. Streams input text back
chunk-by-chunk (line-by-line or word-by-word), then returns the full
text as a final artifact.

**Category:** Canonical -- request streaming

## Prerequisites

- Node.js 24+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env` (or accept the interactive prompt during `blocks login`).

## Install

```bash
cd examples/node/echo-stream
npm install
```

## Run the agent

```bash
blocks run
```

## Run the consumer

Requires authentication — run `blocks login --write-env` first (writes
`BLOCKS_API_KEY` to `.env`).

In a separate terminal:

```bash
npx tsx echo-stream-consumer.ts
npx tsx echo-stream-consumer.ts "custom text to echo"
```

The consumer submits a task, reads streamed chunks in real time, then
prints the final artifact.

## SDK concepts demonstrated

### Provider side (handler.ts)

- `ctx.createStream()` to open an outbound stream
- `stream.write()` for incremental chunk delivery
- `await stream.end()` to signal `stream_end` to consumers
- Returning a final artifact after streaming completes

### Consumer side (echo-stream-consumer.ts)

- `TaskClient.sendMessage()` returning a `TaskSession`
- `session.onStream()` callback for stream discovery
- `streamRef.open()` to get a `StreamClient`
- `for await (const ev of stream.events<EchoEvent>())` async iteration (decoded events)
- `session.onArtifact()` and `session.onTerminal()` for lifecycle

## Stream lifecycle

1. The handler calls `ctx.createStream()` to create an outbound stream.
2. The consumer receives a `stream_started` event via `session.onStream()`.
3. The consumer opens the stream with `streamRef.open()` and iterates
   over `stream.events<T>()` (decoded events). `stream.inbound` is the
   low-level wire iterator and is only needed when the handler needs raw
   envelope metadata.
4. The handler calls `stream.end()`, which publishes a `stream_end`
   marker. The consumer's async iterator completes.
5. The handler returns a final artifact. The consumer receives it via
   `session.onArtifact()`, and the terminal event via
   `session.onTerminal()`.

## What to edit

- Change the chunking strategy in `handler.ts` (`chunkText` function).
- Adjust stream buffer options (`bundleSizeBytes`, `maxLatencyMs`).
- Update `agent-card.json` to change capabilities or schema.
