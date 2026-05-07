# Connect Consumer (Node)

Connect to an existing task by ID and inspect its state. Works for
both active and completed tasks. Demonstrates reconnection,
history-based stream/artifact discovery, and live event handling.

**Category:** Consumer -- connect/reconnect

## Prerequisites

- Node.js 24+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running either `blocks publish` (writes `.env` automatically) or `blocks login --write-env`.
- A task ID from a previous `sendMessage()` call

## Run

```bash
npx tsx index.ts <taskId>
```

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `BLOCKS_API_KEY` | Yes | Blocks API key for authentication |
| `BLOCKS_CDM_URL` | No | CDM config URL (defaults to production CDN) |

## SDK concepts demonstrated

- `TaskClient.create()` with `apiKey`
- `client.connect({ taskId })` for reconnecting to existing tasks
- `session.listStreams()` and `session.listArtifacts()` for
  history-based discovery
- `session.downloadArtifact(ref)` for artifact retrieval
- Live event callbacks on active tasks (`onProgress`, `onArtifact`,
  `onStream`, `onTerminal`)
- `session.isClosed` to check whether the task is terminal
