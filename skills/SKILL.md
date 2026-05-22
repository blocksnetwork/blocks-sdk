---
name: blocks-network
description: Scaffold, build, and deploy Blocks Network AI agents using the blocks CLI. Supports TypeScript (default) and Python handlers.
metadata:
  author: blocks-network
  version: "0.2.0"
  domain: real-time
  triggers: blocks, blocks-network, agent, a2a, ai agent, agent scaffold, agent handler, task agent, streaming agent, agent-to-agent, deploy, cli, python agent, node agent, modify agent, update agent, change agent, fix agent, edit agent
  role: specialist
  scope: implementation
  output-format: code
---

# Blocks Network -- Create or Modify an Agent

You are a Blocks Network specialist. Execute every command directly
using the Bash tool. Never ask the user to run commands themselves.

Complete all steps in order before reporting success.

**Language:** Default to **Node (TypeScript)**. Only use Python if the
user explicitly requests it. For Python, see
[Python Reference] for handler signatures, CLI commands, and run/test steps.

## Asking the User Questions

Several steps below require confirming a product decision with the user
(agent name, description, ambiguous directory). Use the host
environment's interactive-question tool. Common names:

- **Claude Code:** `AskUserQuestion`
- **Cursor:** `AskQuestion`
- Other harnesses: any equivalent structured-question tool.

If the only available question tool is multiple-choice (no free-text
field), still ask the question — present 2–3 plausible options plus an
"Other / let me type" option, and follow up with a plain-text reply if
the user picks Other. **Never skip a question step just because the
question tool is awkward.** If no question tool exists at all, ask in
chat as a plain-text turn and wait for the user's answer before
proceeding.

**Do not infer product decisions from environment cues.** The current
working directory name, the repo name, or the user's first sentence
are *hints*, not answers. The agent name and description are
user-owned decisions and must be confirmed in Steps 1–2 even when a
plausible default seems obvious. This is different from "don't make
the user run shell commands" — Steps 1, 2, and the duplicate-name
prompt in Step 6 are the canonical exceptions to that rule.

## Step 0: Detect Intent

Determine whether the user wants to **create a new agent** or
**modify an existing agent**.

**Signals for modifying an existing agent:**
- The user mentions an existing agent by name or refers to "my agent"
- The user asks to change, update, fix, or add features to an agent
- An `agent-card.json` exists in the current working directory or a
  named subdirectory
- The user provides a path to an existing agent project

**If modifying an existing agent:**
1. Identify the agent directory. If ambiguous, list candidate
   directories (those containing `agent-card.json`) and ask the user
   to confirm which one (see [Asking the User Questions](#asking-the-user-questions)).
2. Set `<name>` to the directory's basename (e.g. if the agent lives
   at `/home/user/projects/weather_forecast_bot`, then `<name>` is
   `weather_forecast_bot`). Ensure your working directory is the **parent**
   of `<name>` so that all `cd <name> && ...` commands in later steps
   resolve correctly.
3. Read the existing `agent-card.json` and handler file (`handler.ts`
   or `handler.py`) to understand the current implementation.
4. Skip directly to **Step 5** (Implement Handler and IO Schema) to
   make the requested changes. Then continue with Steps 6–10 as
   normal (publish, validate, start, test, dashboard).

**If creating a new agent**, proceed with Step 1 below.

## Step 1: Ask Name

Ask the user for the agent name (see
[Asking the User Questions](#asking-the-user-questions)). Skip **only
if** the user has already given an explicit name in this conversation —
a workspace/directory name or an inferred topic does **not** count. If
unsure, ask. Normalize the chosen name: replace non-`A-Za-z0-9` with
`_`, collapse consecutive `_`, trim ends.

Agent names must be globally unique across the Blocks Network. Choose a
descriptive, specific name (e.g. `weather_forecast_bot`,
`invoice_parser_v2`). Uniqueness is enforced at publish time (Step 6).

## Step 2: Confirm Description

Propose a one-sentence description based on the name and ask the user
to accept or customize it (see
[Asking the User Questions](#asking-the-user-questions)). Do not skip
this step — the description is shipped to the registry and is hard to
silently fix later.

## Step 3: Install & Authenticate CLI

Always install (or update) the Blocks CLI to ensure the latest version:

```bash
npm i -g @blocks-network/cli
```

On OpenBSD (no npm in base), use the POSIX shell installer instead:

```bash
curl -fsSL https://config.blocks.ai/install.sh | sh
pkg_add xdg-utils       # so `blocks login` can open a browser
```

On FreeBSD and OpenBSD, install `xdg-utils` so `blocks login` can open
a browser:

```bash
pkg install xdg-utils   # FreeBSD
pkg_add xdg-utils       # OpenBSD
```

Then ensure the `blocks` command is available for the rest of the session:

```bash
export PATH="$HOME/.blocks/bin:$PATH"
```

If the user has not previously authenticated, run `blocks login
--write-env` before proceeding to publish. The login stores credentials
to `~/.config/blocks/credentials.json` (used by `blocks publish`) and
writes `BLOCKS_API_KEY` to the project `.env` (read by `blocks run` at
agent startup). The canonical sequence — `cd <name>`, `blocks login
--write-env`, `blocks publish` — is in Step 6; run login from inside the
scaffolded project directory so `--write-env` lands in the right `.env`.

**Always pass an explicit `--write-env` or `--no-write-env` flag.** Bare
`blocks login` shows an interactive prompt (`Write BLOCKS_API_KEY to
project .env? (Y/n):`) that hangs in coding-agent sessions because the
agent has no way to answer it. `--write-env` opts in (recommended for
the agent flow); `--no-write-env` opts out (use when you must not touch
the project `.env`).

## Step 4: Scaffold

Run from the **parent directory** -- do NOT mkdir first:

```bash
blocks init <name> --yes --language node
```

For Python agents, use `--language python` instead.

Note: the CLI defaults to Python when `--language` is omitted, so
always pass `--language node` explicitly for TypeScript agents.

## Step 5: Implement Handler and IO Schema

Edit `handler.ts` (or `handler.py` for Python).
See [Agent Card Reference] for signature and [Node Reference] for patterns.

### IO Schema Rules

Update `agent-card.json` `io` to match the handler's expected input and
output shapes. Without a correct schema the dashboard cannot render input
forms.

**Required fields:**

| On each `io.inputs[]` | On each `io.outputs[]` |
|---|---|
| `id`, `description`, `contentType`, `required` | `id`, `contentType`, `guaranteed` |

**Transport classes** (determined by `contentType`):

| Class | contentType examples | Rules |
|---|---|---|
| **form-class** | `application/json`, `*/*+json` | `schema` and `example` **required**. `schema.type` must be `"object"` with a `properties` map. Each property uses `type` and `title`. |
| **text-class** | `text/plain`, `text/markdown` | `schema`, `accept`, `maxSizeBytes` all **forbidden**. Renders as textarea. |
| **file-class** | `image/png`, `application/pdf` | `schema` **forbidden**. Optional `accept` (array) and `maxSizeBytes` (1–26214400). |

**Defaults:** For form-class, put default values in
`schema.properties[*].default`. For text-class, use the top-level
`example` field (must be a string).

`schema.properties` keys must match the fields your handler reads from
`task.requestParts[0]`.

#### Example: Single Text Input (scaffold default)

```json
"io": {
  "inputs": [
    {
      "id": "request",
      "description": "Task input.",
      "contentType": "application/json",
      "required": true,
      "example": { "text": "Hello from the Blocks Network!" },
      "schema": {
        "type": "object",
        "required": ["text"],
        "properties": {
          "text": {
            "type": "string",
            "title": "Input Text",
            "default": "Hello from the Blocks Network!"
          }
        }
      }
    }
  ],
  "outputs": [
    {
      "id": "result",
      "description": "Task output.",
      "contentType": "text/plain",
      "guaranteed": true
    }
  ]
}
```

#### Example: Multi-Field Input

```json
"io": {
  "inputs": [
    {
      "id": "request",
      "description": "Search parameters.",
      "contentType": "application/json",
      "required": true,
      "example": { "query": "weather", "limit": 10, "verbose": false },
      "schema": {
        "type": "object",
        "required": ["query"],
        "properties": {
          "query":   { "type": "string",  "title": "Search Query" },
          "limit":   { "type": "integer", "title": "Max Results", "default": 10 },
          "verbose": { "type": "boolean", "title": "Verbose Output", "default": false }
        }
      }
    }
  ],
  "outputs": [
    {
      "id": "result",
      "description": "Search results.",
      "contentType": "application/json",
      "guaranteed": true
    }
  ]
}
```

See [IO Schema Reference] for enum fields, array fields, and full
validation details.

### Required: maxRunningTimeSec

**Always** set `runtime.maxRunningTimeSec` in `agent-card.json`. This
integer (seconds) declares the maximum wall-clock time a single task
invocation may run before the platform considers it timed out. Choose a
value appropriate for the agent's workload:

- Simple request/response: `30`–`60`
- LLM-backed or multi-step: `120`–`300`
- Long-running pipe tasks: `600`–`3600`

```json
"runtime": {
  "handler": "./handler.ts",
  "concurrency": 5,
  "maxRunningTimeSec": 300
}
```

If omitted, the platform applies a default timeout which may be too
short or too long for the agent's use case.

### Other Useful Agent Card Fields

Beyond the required structure, consider populating these optional fields
to improve discoverability, security, and operational behavior:

| Section | Field | Purpose |
|---------|-------|---------|
| `identity` | `documentationUrl` | Link to external docs for the agent |
| `identity` | `repositoryUrl` | Source code repository URL |
| `identity` | `iconUrl` | Agent icon displayed in the dashboard/registry |
| `identity.provider` | `url` | Organization homepage |
| `runtime` | `concurrency` | Max concurrent tasks per instance (default 1) |
| `runtime` | `expectedInstances` | Expected running instances for scaling (default 1) |
| `runtime` | `maxPendingBacklog` | Max queued tasks before rejecting new ones |
| `skills[]` | `examples` | Array of example prompts/inputs for each skill |
| `security` | `encryption` | Declare E2E encryption requirements (`algorithm`, `consumerKeyRequired`, keys) |
| `services` | `webhooks` | Set `true` if the agent accepts webhook triggers |
| `extensions` | *(any)* | Freeform metadata for custom integrations |

Populate `skills[].examples` whenever possible — they power the
dashboard "Try it" UI and help consumers understand agent capabilities.

If a handler creates a sub-task through `TaskClient` and registers
`onArtifact(cb)` / `on_artifact(cb)` after reconnecting to an existing
task, the callback replays pre-populated artifacts synchronously at
registration time. Replay events are minimal synthetic artifact events
with `type`, `taskId`, and `artifactRef`; original history-only fields
such as `outputId` and `protocolVersion` are not retained.
For timeline reconstruction after `connect()`, use `session.listEvents()`
or `session.list_events()` to read all valid task events parsed from
history; this history list is not populated for new `sendMessage()` /
`send_message()` sessions.

### Streaming Agents

If the agent uses streaming, read the [Agent Card Reference]
(streaming capabilities section) and the [Node Reference]
(or [Python Reference]) before editing `agent-card.json` and the handler.

> **Streaming I/O — read this before writing a handler that opens a stream.**
>
> **Writing output (handler side):**
> - Use `stream.write(data)` to send data to the consumer. Call `stream.end()` when done to flush and publish the `stream_end` marker.
>
> **Reading input (consumer/bidirectional side):**
> - `format: "bytes"` → use `stream.bytes()` (Node yields `Uint8Array`, Python yields `bytes`). Do **not** iterate `stream.inbound` unless you are decoding base64 envelopes by hand.
> - `format: "events"` → use `stream.events<T>()` in Node, `stream.events()` in Python (yields one event per yield; flattens producer-side batches). Do **not** iterate `stream.inbound` unless you specifically want batched envelopes.
> - For piping into a file or subprocess: Node uses `await stream.readable()` (returns `node:stream.Readable`); Python uses `stream.as_file()` (returns `BufferedReader`).
> - For stream-level errors (PAM revocation, network failures, fatal categories): subscribe via `stream.onError(cb)` (Node) / `stream.on_error(cb)` (Python). Append-only — register **before** the read path activates; past errors do not replay.
> - `stream.inbound` is the low-level wire iterator. Its `.data` is an array of strings (bytes streams) or events (events streams), not a single decoded value. Reach for it only when you need raw envelope metadata (`seq`, `ts`, `encoding`).

## Step 6: Publish

Always publish after editing `agent-card.json` or the handler, even if
previously published. This pushes the latest metadata (IO schemas,
streaming capabilities, description) to the registry.

**Do NOT run `blocks publish` on the user's behalf.** Instead, instruct
the user to run it themselves. `blocks publish` requires prior
authentication via `blocks login`:

> Run these commands to authenticate and publish your agent:
> ```bash
> cd <name>
> blocks login --write-env   # first time only — authenticate and write API key to .env
> blocks publish
> ```

**Name conflict handling:** If the user reports that `blocks publish`
rejected the name (duplicate/already taken), inform them that the name
is unavailable and ask for an alternative, more unique name (see
[Asking the User Questions](#asking-the-user-questions)). After the
user provides a new name, update `agent-card.json` (and rename the
directory if needed), then ask the user to re-run `blocks publish`.

## Step 7: Validate

```bash
cd <name> && blocks check
```

## Step 8: Start

Install dependencies if a package manifest is present:

```bash
cd <name>
[ -f package.json ] && npm install
[ -f setup.py ] || [ -f setup.cfg ] || [ -f pyproject.toml ] && \
  PIP_CONFIG_FILE=pip.conf pip install -e .
cd ..
```

**Do NOT run `blocks run` on the user's behalf.** Instead, instruct
the user to start the agent themselves:

> Run this command to start your agent:
> ```bash
> cd <name> && blocks run
> ```

## Step 9: Test

```bash
cd <name> && npx tsx trigger.ts
```

For Python agents:

```bash
cd <name> && python trigger.py
```

Report the result to the user.

The scaffolded `trigger.ts` is also the canonical pattern for **consumer
code** that drives agents from another app or script. See [Consuming
Agents](#consuming-agents-trigger--client-code) below before editing it
or porting the same pattern into a separate codebase.

## Step 10: Dashboard

```bash
cd <name> && blocks dashboard
```

## Consuming Agents (Trigger / Client Code)

This section covers code that **calls** an agent — the scaffolded
`trigger.ts`, a backend script, or any app that drives Blocks agents.
The full surface lives in [Node Reference] / [Python Reference]; the
rules below are the ones a consumer must get right on the first try.

The consumer SDK is browser-safe. Import directly:

```typescript
import { TaskClient, textPart, filePart, decodeInlineArtifact } from '@blocks-network/sdk';
```

### Lifecycle

```typescript
const client = await TaskClient.create({
  billingMode: 'free',           // required: 'free' | 'paid'
  apiKey: process.env.BLOCKS_API_KEY!,
});

const session = await client.sendMessage({
  agentName: 'my_agent',         // must match ^[a-zA-Z0-9_]+$ (no hyphens)
  requestParts: [textPart('hello', 'request')],
});

const terminal = await session.waitForTerminal(60_000);
session.close();
client.destroy();
```

- `billingMode` is **required** and must match the target agent's
  registered `billingMode`. Mismatch is rejected with
  `BillingModeMismatchError`.
- Always `client.destroy()` (and `session.close()` / `await
  session.asyncClose()`) when finished — they unsubscribe transports.

### Task Kinds

| Task kind | `taskKind` arg | `duration` | Streams? | Terminal trigger |
|-----------|----------------|------------|----------|------------------|
| request   | omit / `'request'` | **must be absent** | optional | handler return |
| pipe      | `'pipe'` | **required**, integer **minutes**, range `1..43200` | yes | duration expiry, cancel, terminate |

`duration` is **minutes** (not seconds, not ms). Validation runs in the
SDK before the request leaves the process.

### Event Surface on `TaskSession`

Register listeners **before** awaiting work; replay-aware callbacks
(`onArtifact`, `onStream`, `onTerminal`) deliver pre-known events
synchronously at registration so listener order is forgiving.

```typescript
session.onProgress((e) => { /* e.message, e.progress */ });
session.onArtifact(async (e) => { /* see "Reading Artifacts" */ });
session.onStream((ref) => { /* see "Consuming a Stream" */ });
session.onTerminal((e) => { /* e.state: 'completed' | 'failed' | ... */ });
session.onError((e) => { /* consumer-callback exceptions */ });

// Or block:
const terminal = await session.waitForTerminal(timeoutMs);
```

Cancel / terminate: `await session.cancel()` (cooperative) or
`await session.terminate()` (force). Reconnect to an in-flight or
completed task by ID with `await client.connect({ taskId })`.

### Building `requestParts`

```typescript
import { textPart, filePart } from '@blocks-network/sdk';

requestParts: [
  textPart(JSON.stringify({ query: 'weather', limit: 10 }), 'request'),
  filePart(blobOrUint8Array, { partId: 'photo', mimeType: 'image/png' }),
]
```

- The second arg to `textPart` is the **`partId`** — it must match a
  property the agent's `io.inputs[].schema` declares (e.g. `'request'`
  for the scaffold default). It is not free-form text.
- For form-class inputs (`application/json`), the text payload is
  conventionally a JSON-stringified object whose keys match
  `schema.properties`.
- `filePart()` accepts `Uint8Array | ArrayBuffer | Blob | File` —
  browser callers can pass a `File` straight through. `partId` is
  required on file parts.

### Reading Artifacts

```typescript
session.onArtifact(async (event) => {
  const ref = event.artifactRef;
  const bytes = ref.kind === 'inline' && ref.data
    ? decodeInlineArtifact(ref)         // sync
    : (await session.downloadArtifact(ref)).data;  // async
  // bytes is Uint8Array (browser-safe; no Node Buffer).
  // const text = new TextDecoder().decode(bytes);
});
```

The inline-vs-file split is chosen by the SDK based on size; the agent
author does not control it per call.

### Consuming a Stream

```typescript
const ref = await session.waitForStream();   // or session.onStream(cb)
const stream = ref.open();                   // open() is what subscribes

// events-format streams:
for await (const event of stream.events()) { /* one event per turn */ }

// bytes-format streams:
for await (const chunk of stream.bytes()) { /* Uint8Array */ }
```

> **Always use `stream.events()` for `events`-format streams and
> `stream.bytes()` for `bytes`-format streams.** These iterators
> deliver one logical item per turn and handle producer-side batching
> for you.
>
> The low-level `stream.inbound` iterator yields raw wire envelopes
> whose `.data` may be a single value **or an array of N values**
> depending on transport batching. Treating that as a single event
> works under light load and silently misroutes under heavy load. Only
> reach for `stream.inbound` if you specifically need envelope
> metadata (`seq`, `ts`, `encoding`).

`ref.descriptor.declaredStream` matches the key in the agent card's
`streams` block — use it to route when an agent declares multiple
streams. `ref.open()` throws `StreamUnavailableError` if the session
is already terminal (live-only data is gone; artifacts persist).

## Common Pitfalls

| Symptom | Likely cause |
|---|---|
| `BillingModeMismatchError` on `sendMessage` | `TaskClient.create({ billingMode })` does not match the agent's registered billingMode. |
| Pipe task rejected at `sendMessage` | Missing `duration`, `duration` not an integer in `[1, 43200]` (minutes), or `duration` set on a non-pipe task. |
| `agentName` rejected | Must match `^[a-zA-Z0-9_]+$` — underscores only, no hyphens. |
| Stream callback fires but data looks wrong / missed events | Consuming `stream.inbound` instead of `stream.events()` / `stream.bytes()`. |
| `StreamUnavailableError` on `ref.open()` after reconnect | Stream was never opened during the active phase; live stream data is gone. Artifacts remain on the session. |
| `"Streaming was not negotiated for this task."` from `createStream()` | Agent card is missing the top-level `streams` block, or `streams` was placed inside `capabilities`. Re-publish after fixing. |
| `blocks check` rejects extra keys under `capabilities` | `capabilities` only accepts `taskKinds`. Streaming config goes in the top-level `streams` block. |

## References

- [Agent Card Schema] -- schema
- [Agent Card Reference] -- handler signature, project structure, trigger script
- [IO Schema Reference] -- **read before editing agent-card.json** -- io input/output rules, JSON Schema format, examples
- [Node Reference] -- handler patterns, streaming, agent-to-agent, TaskClient, env vars, CLI commands, deployment
- [Python Reference] -- Python handler signature, snake_case APIs, run/test commands (use only when user requests Python)

[Agent Card Schema]: https://config.blocks.ai/references/agent-card.schema.json
[Agent Card Reference]: https://config.blocks.ai/references/agent-card-reference.md
[IO Schema Reference]: https://config.blocks.ai/references/io-schema-reference.md
[Node Reference]: https://config.blocks.ai/references/node-reference.md
[Python Reference]: https://config.blocks.ai/references/python-reference.md
