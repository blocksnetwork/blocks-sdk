# File Consumer (Node)

Submit a task with a file attachment, receive artifact events, and
download the output. Demonstrates the consumer-side file exchange
flow.

**Category:** Consumer -- file exchange

## Prerequisites

- Node.js 24+
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env` (or accept the interactive prompt during `blocks login`).
- An agent running on the same keyset that accepts file input

## Run

```bash
# With a file attachment
npx tsx index.ts /path/to/input-file.txt

# Without a file (uses a small inline sample)
npx tsx index.ts
```

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `BLOCKS_API_KEY` | Yes | Blocks API key for authentication |
| `BLOCKS_CDM_URL` | No | CDM config URL (defaults to production CDN) |

## SDK concepts demonstrated

- `TaskClient.create()` with `apiKey`
- `sendMessage()` with file data in `requestParts` (SDK handles
  inline vs. pre-signed URL upload automatically based on size)
- `session.onArtifact()` for artifact event callbacks
- `session.listArtifacts()` to enumerate all artifacts
- `session.downloadArtifact(ref)` to download inline and file artifacts
