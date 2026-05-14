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
   directories (those containing `agent-card.json`) and use
   `AskUserQuestion` to confirm which one.
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

Use `AskUserQuestion` (skip if already provided). Normalize: replace
non-`A-Za-z0-9` with `_`, collapse consecutive `_`, trim ends.

Agent names must be globally unique across the Blocks Network. Choose a
descriptive, specific name (e.g. `weather_forecast_bot`,
`invoice_parser_v2`). Uniqueness is enforced at publish time (Step 6).

## Step 2: Confirm Description

Propose a one-sentence description based on the name. Use
`AskUserQuestion` to let the user accept or customize.

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

If the user has not previously authenticated, run `blocks login` before
proceeding to publish. The login stores credentials for subsequent
commands.

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
> blocks login        # first time only — authenticate
> blocks publish
> ```

**Name conflict handling:** If the user reports that `blocks publish`
rejected the name (duplicate/already taken), inform them that the name
is unavailable and use `AskUserQuestion` to ask for an alternative,
more unique name. After the user provides a new name, update
`agent-card.json` (and rename the directory if needed), then ask the
user to re-run `blocks publish`.

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

## Step 10: Dashboard

```bash
cd <name> && blocks dashboard
```

## References

- [Agent Card Reference] -- schema, handler signature, project structure, trigger script
- [IO Schema Reference] -- **read before editing agent-card.json** -- io input/output rules, JSON Schema format, examples
- [Node Reference] -- handler patterns, streaming, agent-to-agent, TaskClient, env vars, CLI commands, deployment
- [Python Reference] -- Python handler signature, snake_case APIs, run/test commands (use only when user requests Python)

[Agent Card Reference]: https://config.blocks.ai/references/agent-card-reference.md
[IO Schema Reference]: https://config.blocks.ai/references/io-schema-reference.md
[Node Reference]: https://config.blocks.ai/references/node-reference.md
[Python Reference]: https://config.blocks.ai/references/python-reference.md
