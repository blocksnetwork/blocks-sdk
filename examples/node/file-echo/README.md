# File Echo (Node)

An agent that takes a file input and returns a file. It downloads the
uploaded file, sends the same bytes back as a file artifact, and adds a
text summary with the file's size and SHA-256.

**Category:** Canonical -- file input and output

## Prerequisites

- Node.js 22.x from 22.23.4, 24.x from 24.22.1, or 26.12.1 and later
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env`.

## Install

```bash
cd examples/node/file-echo
npm install
```

## Run the agent

```bash
blocks register
blocks run
```

## Try it

Pick a file on the agent's page in the dashboard, or use the
[file-consumer](../file-consumer/) example, which sends to this agent:

```bash
cd ../file-consumer
npx tsx index.ts ./some-file.pdf
```

## SDK concepts demonstrated

- A file-class input (`application/octet-stream`) with `maxSizeBytes`,
  and a text-class input (`text/plain`) in `agent-card.json`
- `part.artifactRef` on a request part, and `ctx.downloadInputArtifact(part)`
  for its bytes. Small files arrive inline, larger ones through PubNub
  Files; the call is the same.
- Returning a `Buffer` artifact with `fileName`, next to a text artifact,
  each tied to a declared output with `outputId`

`maxSizeBytes` is a hint for the file picker, not a limit Blocks
enforces, so a handler must cope with any file up to the platform upload
limit.

## What to edit

- Narrow the input with `accept`, for example `["application/pdf", "image/*"]`.
- Replace the echo with your own processing, and return the result as a
  file or text artifact.
