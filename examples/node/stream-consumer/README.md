# Stream Consumer (Node)

Submit a pipe task to an agent and consume the resulting data stream
in real time. Demonstrates stream discovery, opening, and async
iteration.

**Category:** Consumer -- pipe streaming

## Prerequisites

- Node.js 22+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env` (or accept the interactive prompt during `blocks login`).
- A streaming agent running on the same keyset (e.g., echo-stream)

## Run

```bash
npx tsx index.ts
```

## Environment variables

| Variable         | Required | Description                                 |
| ---------------- | -------- | ------------------------------------------- |
| `BLOCKS_API_KEY` | Yes      | Blocks API key for authentication           |
| `BLOCKS_CDM_URL` | No       | CDM config URL (defaults to production CDN) |

## SDK concepts demonstrated

- `TaskClient.create()` with `apiKey`
- `sendMessage()` with `taskKind: 'pipe'` and `duration`
- `session.onStream()` for stream discovery
- `streamRef.open()` to get a `StreamClient`
- `for await (const ev of stream.events<T>())` for decoded event iteration
  (use `stream.bytes()` for `format: bytes` streams; `stream.inbound` is
  the lower-level wire iterator)
- Terminal handling with `session.onTerminal()`
