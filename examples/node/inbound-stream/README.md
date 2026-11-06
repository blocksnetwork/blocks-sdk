# Inbound Stream (Node)

The consumer streams data to the agent. The agent declares two inbound
streams, one of bytes and one of events, reads each until the consumer
ends it, and returns a report of what arrived.

**Category:** Canonical -- inbound streaming

## Prerequisites

- Node.js 22.x from 22.23.4, 24.x from 24.22.1, or 26.12.1 and later
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env`.

## Install

```bash
cd examples/node/inbound-stream
npm install
```

## Run the agent

```bash
blocks register
blocks run
```

## Run the consumer

In a separate terminal:

```bash
npx tsx inbound-stream-consumer.ts
```

The consumer sends 64 KB of random bytes and three events, then ends both
streams. It prints the SHA-256 of what it sent; the agent's report shows the
byte count and SHA-256 of what arrived, and the events:

```text
Report: {"bytes":{"totalBytes":65536,"sha256":"…"},"events":[{"text":"first"},…]}
```

## SDK concepts demonstrated

- Declared streams with `direction: "inbound"` in `agent-card.json`,
  with a `schema` on the events stream
- `ctx.createStream({ declaredStream, direction: 'inbound', format })`
  on the agent side
- `stream.bytes()` and `stream.events()` on the agent side; each loop
  ends when the consumer calls `end()`
- `session.onStream(ref)`, `ref.open()`, `write()` and `end()` on the
  consumer side
- 64 KB of bytes is above one message, so the SDK splits it into parts
  and puts them back together in order

For a stream that carries data both ways, see [bidi-chat](../bidi-chat/).
For agent-to-consumer streams, see [stock-sim](../stock-sim/) and
[advanced-stream](../advanced-stream/).

## What to edit

- Rename or add streams under `streams` in `agent-card.json`, and open
  each with its `declaredStream` key in `handler.ts`.
- Replace the report with your own processing of the inbound data.
