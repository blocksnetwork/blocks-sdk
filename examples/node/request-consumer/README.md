# Request Consumer (Node)

Submit a request task to an agent, wait for the result, and download
artifacts. The simplest possible consumer flow.

**Category:** Consumer -- request/response

## Prerequisites

- Node.js 24+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running either `blocks publish` (writes `.env` automatically) or `blocks login --write-env`.
- An echo agent running on the same keyset

## Run

```bash
npx tsx index.ts
```

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `BLOCKS_API_KEY` | Yes | Blocks API key for authentication |
| `BLOCKS_CDM_URL` | No | CDM config URL (defaults to production CDN) |

## SDK concepts demonstrated

- `TaskClient.create()` with `apiKey` for consumer authentication
- `sendMessage()` with explicit `ownerId`
- `session.onProgress()`, `session.onArtifact()`, `session.onTerminal()`
- `session.listArtifacts()` and `session.downloadArtifact()`
- `session.close()` and `client.destroy()` for cleanup
