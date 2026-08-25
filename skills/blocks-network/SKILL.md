---
name: blocks-network
description: Non-linear reference for managing Blocks Network agents — features, configuration, CLI, IO schemas, streaming, consumer SDK, publishing, invites, troubleshooting. Use when working with an existing agent, modifying, deploying, calling agents from a script, or looking up a Blocks feature.
metadata:
  author: blocks-network
  version: "0.3.0"
  domain: real-time
  triggers: blocks, blocks-network, agent, a2a, modify agent, update agent, change agent, fix agent, edit agent, deploy agent, connect agent, register agent, publish agent, republish, streaming agent, consumer, consume agent, call agent, task client, taskclient, trigger script, invite, private agent, agent card, io schema, runtime config, blocks cli, troubleshoot, pitfall, blocks login, headless login, docker login, blocks register, blocks publish, blocks check, blocks dashboard
  role: specialist
  scope: implementation
  output-format: code
---

# Blocks Network -- Reference for Managing Agents

You are a Blocks Network specialist. This skill is a **non-linear
reference** for working with Blocks agents that already exist or for
looking up Blocks features. Jump to the section that matches what the
user is asking for.

**Building a brand-new agent from nothing?** Stop and activate the
**`blocks-getstarted`** skill. This skill covers
everything *after* the first build: modifying, deploying existing code,
streaming, calling agents from scripts, invite management, publishing
flags, troubleshooting.

Execute every command directly using the Bash tool. Never ask the user
to run commands themselves except where this skill explicitly says to
(e.g. `blocks register`, `blocks publish`, `blocks run`).

**Language:** Default to **Node (TypeScript)**. Only use Python if the
user explicitly requests it. For Python, see [Python Reference].

**No TTY available / asking the user product questions.** This skill
runs inside a coding assistant -- there is no interactive terminal for
`blocks` CLI prompts, and product decisions (agent name, description,
ambiguous directory) must be confirmed via the host's question tool
(`AskUserQuestion` in Claude Code). The full plumbing rules live in the
`blocks-getstarted` skill → Asking the User Questions / No TTY available --
treat that copy as authoritative and follow it.

## Required Reading: the Agent Card Schema

The **[Agent Card Schema]** is the single source of truth for the
structure of `agent-card.json` — every field, type, and constraint the
platform enforces. `blocks check` validates your card against it, and
`blocks publish` rejects anything that does not conform. **Read it
before authoring or editing any `agent-card.json`.** Do not infer the
card shape from examples alone (including the snippets in this file) —
examples illustrate, the schema decides.

> [Agent Card Schema] — https://config.blocks.ai/references/agent-card.schema.json

When the schema and any prose or example in this skill appear to
disagree, the schema wins. See also [Agent Card Reference] (field
guidance) and [IO Schema Reference] (input/output rules).

## Section Index

- [Required Reading: the Agent Card Schema](#required-reading-the-agent-card-schema) -- **read first** -- canonical, enforced `agent-card.json` structure
- [CLI Reference](#cli-reference) -- install, login (+ headless), whoami, run, check, dashboard, env overrides
- [Agent Card Reference](#agent-card-reference) -- runtime config, optional fields
- [IO Schema Rules](#io-schema-rules) -- transport classes, examples, drafting from a handler
- [Streaming Agents](#streaming-agents) -- direction/format matrix, handler I/O, consumer I/O
- [Registering & Publishing](#registering--publishing) -- register private/free first, promote to public/paid later, non-interactive flags, name conflicts, invite management
- [Modifying an Existing Agent](#modifying-an-existing-agent) -- edit-and-republish recipe
- [Deploying Code You've Already Written](#deploying-code-youve-already-written) -- locate, draft missing card, publish
- [Consumer Projects & Trigger / Client Code](#consumer-projects--trigger--client-code) -- calling agents from scripts/apps
- [Common Pitfalls](#common-pitfalls) -- error → cause lookup
- [References](#references) -- bundled reference index

## CLI Reference

### Install / upgrade

```bash
npm i -g @blocks-network/cli
```

On OpenBSD (no npm in base), use the POSIX shell installer:

```bash
curl -fsSL https://config.blocks.ai/install.sh | sh
pkg_add xdg-utils       # so `blocks login` can open a browser
```

On FreeBSD, install `xdg-utils` so `blocks login` can open a browser:

```bash
pkg install xdg-utils
```

Make `blocks` available for the rest of the session:

```bash
export PATH="$HOME/.blocks/bin:$PATH"
```

Self-update for users who installed via `install.sh` (no global npm):

```bash
blocks upgrade
```

### `blocks login`

Always pass an explicit `--write-env` or `--no-write-env`. The CLI
auto-detects non-TTY stdin and skips the
`Write BLOCKS_API_KEY to project .env? (Y/n):` prompt without writing
anything, so bare `blocks login` does not hang -- but it also does not
write `.env`. Pass the flag explicitly so the outcome is deterministic:

- `--write-env` opts in (recommended for the agent flow). Stores creds
  in `~/.config/blocks/credentials.json` and writes `BLOCKS_API_KEY` to
  the project `.env`.
- `--no-write-env` opts out (use when you must not touch the project
  `.env`).
- `--dir <name>` points `--write-env` at the named project's `.env`
  when invoking from a parent directory.

### Login in containerized / headless environments

`blocks login` waits for the OAuth callback on
`http://127.0.0.1:8787/callback`. In Docker, SSH, a cloud VM, or an
agent sandbox, that address is the *container's* loopback --
unreachable from the user's browser, so login hangs, then times out.

**Path A -- browser relay** (interactive user who has a browser). The
authorize URL is printed *before* the browser-open attempt, so it is
usable even where no browser exists.

1. Inside the container: `blocks login --write-env --dir <project> &`
   (`blocks: command not found` after `npm i -g` means the install dir
   is off `PATH` -- `export PATH="$HOME/.blocks/bin:$PATH"` first.)
2. Open the printed `.../api/auth/oauth2/authorize?...` URL (default
   deployment: `https://app.blocks.ai/...`) and authenticate.
3. The redirect to `http://127.0.0.1:8787/callback?code=...` fails to
   load -- **expected**. Copy it from the address bar and run, *inside
   the container*: `curl -s "<callback-url>"`
4. The CLI finishes the exchange and writes `BLOCKS_API_KEY`. Verify
   with `blocks whoami`.

Path A fails if: more than 5 minutes elapse (`authorization timed out
— no callback received within 5 minutes`); the `curl` runs outside the
container (the listener binds `127.0.0.1` only); or the URL is from an
earlier attempt (`state mismatch`).

**Path B -- pre-obtained API key** (CI / scripted / no user present):

```bash
echo "<key>" | blocks login --api-key-stdin --write-env --dir <project>
```

Mint the key at `https://app.blocks.ai/manage/api-keys`. The same flags
work on `blocks register` / `blocks publish`.

### Verify or revoke credentials

| Command | Purpose |
|---|---|
| `blocks whoami` | Print the current org, key id, and expiry. Errors with `not logged in` if no creds. |
| `blocks whoami --json` | Same, structured for programmatic checks (`org_name`, `org_id`, `key_id`, `expires_at`, `days_remaining`, `expired`). |
| `blocks logout` | Delete `~/.config/blocks/credentials.json` and remove `BLOCKS_API_KEY` from the project `.env`. |
| `blocks version` | Print the installed CLI version. |

### `blocks check`

```bash
cd <your-agent-name> && blocks check
```

Validates `agent-card.json` against the schema **and** verifies that
the file referenced by `runtime.handler` exists on disk. A missing
handler file causes `[FAIL]` even when the JSON itself is valid.
`blocks publish` re-runs the same validation, so `blocks check` is a
fast pre-flight, not a gate the user must clear before publishing.

### `blocks run`

Starts the agent locally. Reads `agent-card.json`, imports the handler,
and uses `BLOCKS_API_KEY` from `.env`. Don't run on the user's behalf
-- instruct the user to run `cd <your-agent-name> && blocks run`
themselves so they own the live process.

Install deps first if a manifest is present:

```bash
cd <your-agent-name>
[ -f package.json ] && npm install
[ -f setup.py ] || [ -f setup.cfg ] || [ -f pyproject.toml ] && \
  pip install -e . && pip install blocks-network --upgrade
cd ..
```

### `blocks dashboard`

```bash
cd <your-agent-name> && blocks dashboard
```

Reads the dashboard URL from `BLOCKS_APP_BASE_URL` /
`BLOCKS_DASHBOARD_URL` (or the active profile's dashboard origin) if
set, otherwise from the active deployment — `BLOCKS_BACKEND_URL`, the
active profile's backend, or the CDM config — so it opens the agent's
page on the deployment you're targeting rather than always stock Blocks
Network. When `BLOCKS_BACKEND_URL` points at a different backend than
the active profile was logged into, the profile's cached dashboard
origin is skipped so the link follows `BLOCKS_BACKEND_URL`. Override for
staging / a worktree / a self-hosted deploy:

```bash
BLOCKS_APP_BASE_URL=https://staging.blocks.ai blocks dashboard
```

## Agent Card Reference

The authoritative structure of `agent-card.json` is the
**[Agent Card Schema]** (see [Required Reading](#required-reading-the-agent-card-schema)).
This section gives field-level guidance; the schema is what
`blocks check` and `blocks publish` enforce.

### Recommended: `runtime.maxRunningTimeSec`

**Strongly recommended:** set `runtime.maxRunningTimeSec` in
`agent-card.json`. It is not enforced -- `blocks check` passes without
it and `blocks init` does not scaffold it -- but omitting it means the
platform applies a default that is often wrong for the workload. This
integer (seconds) declares the maximum wall-clock time a single task
invocation may run before the platform considers it timed out. Choose
a value appropriate for the workload:

- Simple request/response: `30`-`60`
- LLM-backed or multi-step: `120`-`300`
- Long-running pipe tasks: `600`-`3600`

```json
"runtime": {
  "handler": "./handler.ts",
  "concurrency": 5,
  "maxRunningTimeSec": 300
}
```

If omitted, the platform applies a default that may be too short or
too long for the agent's use case.

### Other useful fields

Beyond the required structure, populate these to improve
discoverability, security, and operational behavior:

| Section | Field | Purpose |
|---------|-------|---------|
| `identity` | `documentationUrl` | Link to external docs for the agent |
| `identity` | `repositoryUrl` | Source code repository URL |
| `identity` | `iconUrl` | Agent icon displayed in the dashboard/registry |
| `identity.provider` | `url` | Organization homepage |
| `runtime` | `concurrency` | Max concurrent tasks per instance (default 1) |
| `runtime` | `expectedInstances` | Expected running instances for scaling (default 1) |
| `runtime` | `maxPendingBacklog` | Max queued tasks before rejecting new ones |
| `tags[]` | `examples` | Array of example prompts/inputs for each tag |
| `security` | `encryption` | Declare E2E encryption requirements (`algorithm`, `consumerKeyRequired`, keys) |
| `services` | `webhooks` | Set `true` if the agent accepts webhook triggers |
| `extensions` | *(any)* | Freeform metadata for custom integrations |

Populate `tags[].examples` whenever possible — they power the dashboard
"Try it" UI and help consumers understand agent capabilities.

For full handler signatures, project structure, and trigger-script
shape, see the bundled [Agent Card Reference].

## IO Schema Rules

Update `agent-card.json` `io` to match the handler's expected input
and output shapes. Without a correct schema, the dashboard cannot
render input forms.

**Required fields:**

| On each `io.inputs[]` | On each `io.outputs[]` |
|---|---|
| `id`, `description`, `contentType`, `required` | `id`, `contentType`, `guaranteed` |

**Transport classes** (determined by `contentType`):

| Class | contentType examples | Rules |
|---|---|---|
| **form-class** | `application/json`, `*/*+json` | `schema` and `example` **required**. `schema.type` must be `"object"` with a `properties` map. Each property uses `type` and `title`. |
| **text-class** | `text/plain`, `text/markdown` | `schema`, `accept`, `maxSizeBytes` all **forbidden**. Renders as textarea. |
| **file-class** | `image/png`, `application/pdf` | `schema` **forbidden**. Optional `accept` (array) and `maxSizeBytes` (1-26214400). |

**Defaults:** For form-class, put default values in
`schema.properties[*].default`. For text-class, use the top-level
`example` field (must be a string).

`schema.properties` keys must match the fields the handler reads from
`task.requestParts[0]`.

### Example: Single text input (scaffold default)

> **Example only -- replace every string value before publishing.**
> The literal text below (`"Input Text"`, the default string, the
> `example` payload) is illustrative. Substitute values that match
> the user's actual agent inputs and outputs. Do not paste this
> block verbatim into a real `agent-card.json`.

```json
"io": {
  "inputs": [
    {
      "id": "request",
      "description": "Task input.",
      "contentType": "application/json",
      "required": true,
      "example": { "text": "<your example input here>" },
      "schema": {
        "type": "object",
        "required": ["text"],
        "properties": {
          "text": {
            "type": "string",
            "title": "Input Text",
            "default": "<your default input here>"
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

### Example: Multi-field input

> **Example only -- replace every string value before publishing.**
> Substitute names and titles that match the user's actual handler
> fields. Do not paste this block verbatim.

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

### Drafting an IO schema from an existing handler

When deploying code that has no `agent-card.json`, infer the schema by
reading the handler:

1. Identify the keys the handler reads from `task.requestParts[0]` -- these become `schema.properties`.
2. Note required keys (the ones the handler can't run without) -- these become `schema.required`.
3. Pick a `contentType` based on what the handler produces in its return / artifacts.
4. Choose `runtime.maxRunningTimeSec` per the workload guidance above.
5. **Show the drafted card to the user via the host question tool** ("Accept and write to disk" / "Let me edit it first") **before writing the file.** Production-grade providers must not be silently surprised by auto-generated metadata.

## Streaming Agents

If the agent uses streaming, read [Agent Card Reference] (streaming
capabilities section) and [Node Reference] / [Python Reference] before
editing `agent-card.json` and the handler.

### Declaring a stream in `agent-card.json`

Each entry in the top-level `streams` block requires `direction` and
`format`. The schema field set is **not** the same for all three
patterns -- the publisher's validator enforces this conditionally and
rejects mismatches:

| `direction` | `format` | Schema fields | Forbidden fields |
|---|---|---|---|
| `outbound` or `inbound` | `events` | single `schema` | `outboundSchema`, `inboundSchema`, `contentType` |
| `bidirectional` | `events` | **both** `outboundSchema` **and** `inboundSchema` (events flowing each way may have different shapes) | `schema`, `contentType` |
| any | `bytes` | `contentType` (e.g. `"application/octet-stream"`) | `schema`, `outboundSchema`, `inboundSchema` |

Example -- bidirectional events stream (a bare
`direction`/`format`/`description` entry will be rejected at publish
time):

```json
"streams": {
  "_default": {
    "direction": "bidirectional",
    "format": "events",
    "description": "Two-way event channel.",
    "outboundSchema": { "type": "object", "properties": { "kind": { "type": "string" } } },
    "inboundSchema":  { "type": "object", "properties": { "kind": { "type": "string" } } }
  }
}
```

### Streaming I/O -- read this before writing a handler that opens a stream

**Writing output (handler side):**
- Use `stream.write(data)` to send data to the consumer. Call `stream.end()` when done to flush and publish the `stream_end` marker.

**Reading input (consumer/bidirectional side):**
- `format: "bytes"` -> use `stream.bytes()` (Node yields `Uint8Array`, Python yields `bytes`). Do **not** iterate `stream.inbound` unless decoding base64 envelopes by hand.
- `format: "events"` -> use `stream.events<T>()` in Node, `stream.events()` in Python (yields one event per yield; flattens producer-side batches). Do **not** iterate `stream.inbound` unless you specifically want batched envelopes.
- For piping into a file or subprocess: Node uses `await stream.readable()` (returns `node:stream.Readable`); Python uses `stream.as_file()` (returns `BufferedReader`).
- For stream-level errors (PAM revocation, network failures, fatal categories): subscribe via `stream.onError(cb)` (Node) / `stream.on_error(cb)` (Python). Append-only -- register **before** the read path activates; past errors do not replay.
- `stream.inbound` is the low-level wire iterator. Its `.data` is an array of strings (bytes streams) or events (events streams), not a single decoded value. Reach for it only when you need raw envelope metadata (`seq`, `ts`, `encoding`).

### Sub-task replay & history reconstruction

If a handler creates a sub-task through `TaskClient` and registers
`onArtifact(cb)` / `on_artifact(cb)` after reconnecting to an existing
task, the callback replays pre-populated artifacts synchronously at
registration time. Replay events are minimal synthetic artifact events
with `type`, `taskId`, and `artifactRef`; original history-only fields
such as `outputId` and `protocolVersion` are not retained. For timeline
reconstruction after `connect()`, use `session.listEvents()` /
`session.list_events()` to read all valid task events parsed from
history; this history list is not populated for new `sendMessage()` /
`send_message()` sessions.

## Registering & Publishing

Registering / publishing pushes the latest IO schemas, streaming
capabilities, and description to the registry. Re-register (or
republish) whenever the agent card or handler shape changes — running
`blocks register` again on the same agent updates the card, IO schema,
description, and tags the same way re-running `blocks publish` does.

**Caveat once you've promoted with `blocks publish`:** `blocks register`
always sends `listing=private` + `billingMode=free`, so re-running it
on an agent that was promoted to public/paid will reset the visibility
and pricing back to private+free (card content still updates
correctly). After the first promotion, prefer `blocks publish` for
subsequent updates so the listing isn't silently demoted.

The recommended first step is `blocks register`, which registers the
agent **privately and free** (usable by the owner, other members of the
organization that owns it, and the users and organizations they invite;
no public listing, no pricing). It has no
listing/billing/terms prompts or flags, so non-interactive and CI
invocations succeed with no required flags. (Interactive runs may still
prompt for an organization name on the first agent an org publishes —
the same prompt `blocks publish` shows.) `blocks publish` is the path to
go **public** and/or **paid**; it can also promote an already-registered
agent — running it later on the same agent updates the listing.

**Do NOT run `blocks register` or `blocks publish` on the user's
behalf.** Instruct the user to run it themselves:

> ```bash
> cd <your-agent-name>
> blocks login --write-env   # first time only
> blocks register            # private + free, the recommended first step
> # ...or, to go public / set pricing:
> blocks publish
> ```

### Non-interactive publish flags

Bare `blocks publish` is fine in a TTY -- the CLI walks the user
through listing visibility, billing mode, pricing, and terms
acceptance. In a non-interactive shell (CI, headless containers,
agent-driven sessions), the same prompts surface as a hang. Always
include the relevant flags in the recipe you give the user, derived
from the agent card's `billingMode`:

| Flag | Purpose |
|---|---|
| `--billing-mode {free\|paid}` | Required (mirrors `agent-card.json` billing mode). |
| `--listing {public\|private}` | Visibility in the registry. |
| `--price <usd>` | Price for single-kind agents (auto-mapped to per-task or per-minute). |
| `--price-per-task <usd>` | Per-task price for dual-kind (request + pipe) agents. |
| `--price-per-minute <usd>` | Per-minute price for dual-kind (request + pipe) agents. |
| `--free-units <n>` | Free trial units per consumer org (auto-mapped from taskKinds). |
| `--free-tasks <n>` / `--free-minutes <n>` | Per-kind free trial counts for dual-kind agents. |
| `--accept-terms` | Accept legal attestations non-interactively. |
| `--org-name <name>` | Set the organization name on first publish (otherwise prompted). |
| `--api-key <key>` / `--api-key-stdin` | Skip `blocks login` and authenticate inline. |

Two common recipes:

```bash
# Free public agent
blocks publish --billing-mode free --listing public --accept-terms

# Paid private agent
blocks publish --billing-mode paid --listing private \
  --price-per-task 0.05 --accept-terms
```

### Name conflicts

Agent names are globally unique across the Blocks Network. Since
`blocks register` is the recommended first step, name collisions
typically surface there; `blocks publish` enforces the same check. If
either command rejected the name as duplicate / already taken, inform
the user that the name is unavailable and ask for a more unique
alternative via the host question tool. Update `agent-card.json`
`identity.agentName` (and rename the project directory if needed),
then ask the user to re-run the same command (`blocks register` or
`blocks publish`).

**Reserved names (`409 AgentNameReserved`).** A collision is not always
permanent. When an agent is deleted, its name is held for a reservation
window (30 days on Blocks Network). During the hold the name is
reclaimable **only by the original owner or another member of the
agent's org** — re-registering as that owner reclaims it and proceeds.
Any other caller is rejected with `409 AgentNameReserved` (distinct from
an ordinary "already taken" active-name conflict). If the user *is* the
prior owner and hit this error, they can retry with the same credentials
to reclaim; otherwise the name is unavailable until the hold expires, so
pick a different one.

### Manage access for private agents

When `--listing private` is set, the agent is unlisted and reachable by
its owner and by other members of the organization that owns it. Anyone
outside that organization needs an invitation. Use the `blocks invite`
subcommand family to grant or revoke access:

| Command | Purpose |
|---|---|
| `blocks invite send <agentName> --email <email>` | Invite a specific user by email. |
| `blocks invite send <agentName> --org <slug>` | Invite an entire consumer organization by slug. `--email` and `--org` are mutually exclusive. |
| `blocks invite list <agentName>` | List unaccepted invitations for the agent, including expired invitations. |
| `blocks invite grants <agentName>` | List active grants (users + orgs that already have access). |
| `blocks invite revoke <agentName> --email <email>` | Revoke a user grant. |
| `blocks invite revoke <agentName> --org <slug-or-id>` | Revoke an org grant. |
| `blocks invite accept <token>` | (Consumer-side) Accept an invitation token to gain access. |

All commands require `blocks login` first. They are safe to run on the
user's behalf when the agent already exists in the registry.

## Modifying an Existing Agent

The user has a working agent and wants to edit handler / IO schema and
republish. Reference checklist:

1. **Identify the agent directory.** If ambiguous, list candidates
   (those containing `agent-card.json`) and ask the user via the host
   question tool.
2. **Read `agent-card.json` and the handler** (`handler.ts` /
   `handler.py`) to understand the current implementation.
3. **Make the requested changes.** When modifying input shape or
   output shape, also update `io.inputs[]` / `io.outputs[]` per [IO
   Schema Rules](#io-schema-rules). When adding streaming, see
   [Streaming Agents](#streaming-agents).
4. **Validate** with `cd <agent-dir> && blocks check`.
5. **Re-register (or republish)** per [Registering & Publishing](#registering--publishing).
   The published metadata is what consumers see -- don't rely on
   `blocks run` alone to confirm the change is live.
6. **Restart** the running agent so the new handler is loaded -- ask
   the user to re-run `cd <agent-dir> && blocks run`.
7. **Re-test** with the trigger script (`npx tsx trigger.ts` /
   `python trigger.py`).

## Deploying Code You've Already Written

The user has handler code and wants it on the network. The card may or
may not exist. **Production-grade providers want to publish as-is** --
don't assume they want to edit the handler.

1. **Locate the project directory.** If the cwd contains exactly one
   `handler.ts` / `handler.py` (and optionally `agent-card.json`), use
   cwd. Otherwise list candidate subdirectories and ask the user to
   pick one. Never pick based on directory name alone.
2. **Detect the language.** `handler.ts` → node, `handler.py` →
   python. If both, ask.
3. **Check for `agent-card.json`:**
   - **Card present:** Read it. Verify it has the required fields per
     [Agent Card Reference] minimal example (`identity.{agentName,
     displayName, description, version, provider.organization}`,
     `capabilities.taskKinds`, `tags[]`, `runtime.handler`). If
     anything required is missing, treat as "card missing" below.
     `runtime.maxRunningTimeSec` is recommended but optional -- its
     absence alone does not make a card "missing"; just add it.
   - **Card missing:** Read the handler to infer input/output shape,
     then draft a minimal `agent-card.json` per [IO Schema Rules →
     Drafting an IO schema from an existing
     handler](#drafting-an-io-schema-from-an-existing-handler). **Show
     the drafted card to the user before writing the file.**
4. **Ask the user: deploy as-is, or make changes first?**
   - **As-is:** Skip handler edits. Authenticate (`blocks login
     --write-env` if needed), then `blocks register` (private + free,
     the recommended first step) per
     [Registering & Publishing](#registering--publishing). Use
     `blocks publish` instead if the user explicitly wants public/paid.
   - **Changes first:** Edit handler / IO schema, then register (or
     publish).
5. **Validate** (`blocks check`), **start** (`blocks run`), **test**
   (`npx tsx trigger.ts` / `python trigger.py`), **dashboard**
   (`blocks dashboard`).

For CLI invocations, set `<agent-name>` from the card's
`identity.agentName` (NOT the directory basename) when they differ.
Use the actual directory path for `cd`, the agentName for `blocks
invite send`, etc.

> Run this command to start your agent:
> ```bash
> cd <your-agent-name> && blocks run
> ```

## Step 9: Test

```bash
cd <your-agent-name> && npx tsx trigger.ts
```

For Python agents:

```bash
cd <your-agent-name> && python trigger.py
```

Report the result to the user.

The scaffolded `trigger.ts` is also the canonical pattern for **consumer
code** that drives agents from another app or script. See [Consumer
Projects](#consumer-projects--trigger--client-code) below before editing it
or porting the same pattern into a separate codebase.

## Step 10: Dashboard

```bash
cd <your-agent-name> && blocks dashboard
```

`blocks dashboard` reads the dashboard URL from `BLOCKS_APP_BASE_URL` /
`BLOCKS_DASHBOARD_URL` (or the active profile's dashboard origin) if
set, otherwise from the active deployment — `BLOCKS_BACKEND_URL`, the
active profile's backend, or the CDM config — then opens the agent's
page on the deployment you're targeting. When `BLOCKS_BACKEND_URL`
points at a different backend than the active profile was logged into,
the profile's cached dashboard origin is skipped so the link follows
`BLOCKS_BACKEND_URL`. To target a non-prod environment (staging, a
worktree, or a self-hosted deployment), export the env var before
invoking the command, for example:

```bash
BLOCKS_APP_BASE_URL=https://staging.blocks.ai blocks dashboard
```

## Step 11: Ship a Web UI (Optional)

If the user wants a browser UI in front of the agent (chat box, form,
streaming preview) without standing up a backend, scaffold a static page
using the embedded-auth template:

```bash
blocks init my-ui --mode webapp --agent <agent-name>   # repeat --agent for multiple agents (max 25)
blocks dev                 # serves ./web locally against prod Blocks auth
blocks deploy cloudflare  # or vercel, netlify
```

The generated page drops in a "Sign in with Blocks" widget — the page calls
`BlocksAuth.signInAndGetClient({ agent })` (one agent) or
`BlocksAuth.signInAndGetClients({ agents })` (several) — that mints
short-lived per-agent JWTs via a popup handshake — no API key in the
browser, no provider-hosted backend required. End users authenticate to
Blocks; paid agent usage is billed to those end users, not the page
author.

The scaffolder writes **`blocks.config.json`** (`templateVersion`, `agents`,
required `backendBaseUrl` — the backend API origin the deployed page calls at
runtime, distinct from the `--blocks-base-url` asset host — plus optional
`deployTarget` / `lastDeployedUrl`) plus a generated `web/`
(`index.html`, `app.js`, `styles.css`) wired from each agent's card. For any
agent that declares the **pipe** task kind, the page includes a duration
control (minutes, range 1..43200): pipe-only agents always send it; mixed
request+pipe agents send it only when the "run as a pipe session" box is
checked.

**Deploy credentials (non-interactive).** `blocks deploy <partner>` needs a
partner API token. This skill runs with no TTY, so export the matching env
var BEFORE deploying (the CLI's interactive `blocks login --provider <partner>`
paste flow cannot be used here):

- Cloudflare Pages: `CLOUDFLARE_API_TOKEN`
- Vercel: `VERCEL_TOKEN`
- Netlify: `NETLIFY_AUTH_TOKEN`

See `blocks-sdk/embed-auth/README.md` for the widget API and the wire-level
pattern (popup flow, refresh, sign-out, error envelopes),
`docs/embed-getting-started.md` for a step-by-step partner-page walkthrough, and
`blocks-sdk/docs/embed-multi-agent.md` for the multi-agent composition pattern.

For full-fledged apps that already have a backend, use the existing
`backend_jwt_proxy` (server-mints-JWT) or `browser_sdk` (dashboard-style
session) patterns documented in the [Node Reference] — those are still
the right call when the developer is already operating server-side code.

## Consumer Projects & Trigger / Client Code

This section covers code that **calls** an agent -- the scaffolded
`trigger.ts`, a backend script, or any app that drives Blocks agents.
The full surface lives in [Node Reference] / [Python Reference]; the
rules below are the ones a consumer must get right on the first try.

The consumer SDK is browser-safe.

### Scaffolding a consumer project

If the user wants to **call** other Blocks agents from a script or app
rather than build a new agent, scaffold a consumer project with
`--mode consumer`:

```bash
blocks init <your-script-name> --yes --language node --mode consumer
# or
blocks init <your-script-name> --yes --language python --mode consumer
```

A consumer project produces:

- Node: `index.ts` plus a `package.json` with a `start` script. No
  `agent-card.json`, no `handler.ts`.
- Python: `main.py` plus `pyproject.toml`. No `agent-card.json`, no
  `handler.py`.

After scaffolding:

1. Set `BLOCKS_API_KEY` in `.env` (or run `blocks login --write-env`
   from the consumer directory).
2. Edit the script and set the target agent name on `sendMessage` /
   `send_message`.
3. Run with `npm run start` (Node) or `python main.py` (Python).

Consumer projects don't publish, don't register a handler, and don't
need a dashboard entry. The patterns below apply equally to the
scaffolded `index.ts`/`main.py`, the scaffolded `trigger.ts`/
`trigger.py` shipped with provider agents, or any other script that
calls a Blocks agent.

### Import

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
- `sendMessage({ stream })` / `send_message(stream=)` is an optional
  request-task streaming opt-in: `true` requests live token
  output, `false` suppresses it (status + final result only), omitted
  leaves the server default — which is now **no streaming** for request
  tasks, so pass `stream: true` explicitly if you rely on a stream.
  Resolved streaming still requires agent capability, so `stream: true`
  against a non-streaming agent yields no stream (soft hint, no error).
  Ignored for pipe tasks (pipe streaming is capability-driven).
- Always `client.destroy()` (and `session.close()` / `await
  session.asyncClose()`) when finished -- they unsubscribe transports.
- If background token refresh permanently fails (3 retries exhausted),
  the next authenticated `TaskClient` call (`sendMessage`, `connect`,
  `getTask`, `listTasks`, `cancelTask`, file-upload helpers, etc.)
  runs through a shared preflight that first attempts one
  reactive-recovery refresh; if that recovery succeeds the call
  proceeds normally, and if it fails the typed
  `AuthRefreshFailedError` is thrown/raised instead of an opaque 401.
  Register `onAuthError` (Node) / `on_auth_error` (Python) on
  `TaskClient.create(...)` for a proactive hook; the preflight is the
  safety net for callers who don't and the recovery path for transient
  outages. See [Node Reference] / [Python Reference] for re-auth
  patterns.

### Task kinds

| Task kind | `taskKind` arg | `duration` | Streams? | Terminal trigger |
|-----------|----------------|------------|----------|------------------|
| request   | omit / `'request'` | **must be absent** | optional | handler return |
| pipe      | `'pipe'` | **required**, integer **minutes**, range `1..43200` | yes | duration expiry, cancel, terminate |

`duration` is **minutes** (not seconds, not ms). Validation runs in
the SDK before the request leaves the process.

### Event surface on `TaskSession`

Register listeners **before** awaiting work; replay-aware callbacks
(`onArtifact`, `onStream`, `onTerminal`) deliver pre-known events
synchronously at registration so listener order is forgiving.

```typescript
session.onProgress((e) => { /* e.message, e.progress */ });
session.onArtifact(async (e) => { /* see "Reading artifacts" */ });
session.onStream((ref) => { /* see "Consuming a stream" */ });
session.onTerminal((e) => { /* e.state: 'completed' | 'failed' | ... */ });
session.onCancelRequested((e) => { /* e.ts (Date.now() in ms) */ });
session.onError((e) => { /* consumer-callback exceptions */ });

// Or block:
const terminal = await session.waitForTerminal(timeoutMs);
```

**At-most-once `onTerminal`.** The SDK guarantees that
`session.onTerminal`, `session.waitForTerminal()`, and
`TaskClient.subscribeToTask`'s `onTerminal` each fire at most once per
task — even when the wire delivers two terminals (e.g. a
scanner force-cancel followed by the agent's own delayed
terminal). The first terminal wins; subsequent terminals are silently
dropped. This holds across the synthetic re-emit when registering a
callback against an already-terminal session as well.

**`onCancelRequested`.** Backend acknowledgment of a cooperative
cancel, published on `u.{orgId}.{taskId}` after the authoritative
writes. Fires zero or once per session — suppressed once a terminal
has been delivered. Carries `{ taskId, ts }` (no actor identity; the
obs.* channel records ownerId for ops/admin audit). Use it to render
an in-flight "cancel requested" UI signal before any terminal
arrives. **Late registration:** callbacks registered after the wire
`cancel_requested` arrived still receive a synthetic replay of the
first event, mirroring `onTerminal`'s sticky behavior — but only while
no terminal has been delivered; a post-terminal registration receives
nothing (causality).

Cancel / terminate: `await session.cancel()` (cooperative) or `await
session.terminate()` (force). Reconnect to an in-flight or completed
task by ID with `await client.connect({ taskId })`.

### Out-of-band lifecycle (no live session needed)

`client.getTask(id)`, `client.listTasks({ ownerId?, agentName?,
state?, limit?, cursor? })`, `client.cancelTask(id)`,
`client.pauseTask(id)`, `client.resumeTask(id)`, `client.retryTask(id)`,
`client.terminateTask(id)`. Python equivalents are snake_case
(`client.get_task` / `client.list_tasks` / `client.cancel_task` /
...). Use these from a backend or CLI script when you only have a
task ID and don't want to subscribe to the live channel.

### Building `requestParts`

```typescript
import { textPart, filePart } from '@blocks-network/sdk';

requestParts: [
  textPart(JSON.stringify({ query: 'weather', limit: 10 }), 'request'),
  filePart(blobOrUint8Array, { partId: 'photo', contentType: 'image/png' }),
]
```

- The second arg to `textPart` is the **`partId`** -- it must match a
  property the agent's `io.inputs[].schema` declares (e.g. `'request'`
  for the scaffold default). It is not free-form text.
- For form-class inputs (`application/json`), the text payload is
  conventionally a JSON-stringified object whose keys match
  `schema.properties`.
- `filePart()` accepts `Uint8Array | ArrayBuffer | Blob | File` --
  browser callers can pass a `File` straight through. `partId` is
  required on file parts. The options object is
  `{ partId, fileName?, contentType? }` -- use `contentType` (not
  `mimeType`) to set the part's MIME type.

### Reading artifacts

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

For bulk export at terminal time, `await session.saveArtifacts(dir)`
(Node) / `session.save_artifacts(directory)` (Python) writes every
artifact on the session to disk and returns the resulting file paths.
Useful in trigger / script flows that just need the artifacts on local
disk without iterating `onArtifact`.

### Consuming a stream

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
> whose `.data` is **always an array** (`string[]` for bytes streams,
> `T[]` for events streams) or a raw passthrough dict (`raw` format).
> A single producer `write()` already yields a 1-element array — the
> bundler coalesces writes by size/latency. Treating `.data` as a
> single value works under light load (1-element array, JS
> auto-coerces) and silently misroutes once batching kicks in. Only
> reach for `stream.inbound` if you specifically need envelope
> metadata (`seq`, `ts`, `encoding`); the Node SDK now enforces the
> per-format shape with a discriminated union.

`ref.descriptor.declaredStream` matches the key in the agent card's
`streams` block -- use it to route when an agent declares multiple
streams. `ref.open()` throws `StreamUnavailableError` if the session
is already terminal (live-only data is gone; artifacts persist).

## Common Pitfalls

| Symptom | Likely cause |
|---|---|
| User says "I want to build my first Blocks agent" | Wrong skill -- this one is for managing existing agents. Switch to the `blocks-getstarted` skill. |
| `BillingModeMismatchError` on `sendMessage` | `TaskClient.create({ billingMode })` does not match the agent's registered billingMode. Read it from the registry: `(await getAgent(name)).billingMode`. |
| `AuthRefreshFailedError` on the next `TaskClient` call | Background token refresh failed 3 times AND the per-call preflight's reactive-recovery attempt also failed (expired/revoked API key, broken token endpoint, persistent outage). Re-create the `TaskClient` with valid credentials, or register `onAuthError` for proactive re-auth UX. A transient outage that recovers before the preflight runs is handled silently and the call proceeds. |
| Pipe task rejected at `sendMessage` | Missing `duration`, `duration` not an integer in `[1, 43200]` (minutes), or `duration` set on a non-pipe task. |
| `agentName` rejected | Must match `^[a-zA-Z0-9_]+$` -- underscores only, no hyphens. |
| Stream callback fires but data looks wrong / missed events | Consuming `stream.inbound` instead of `stream.events()` / `stream.bytes()`. |
| `StreamUnavailableError` on `ref.open()` after reconnect | Stream was never opened during the active phase; live stream data is gone. Artifacts remain on the session. |
| `"Streaming was not negotiated for this task."` from `createStream()` | `hasStream` is false. Either the agent card is missing the top-level `streams` block (or it was placed inside `capabilities`) — re-publish after fixing — or, for a request task, the consumer didn't opt in via `extensions.blocks.stream`. Guard handler code on `ctx.hasStream` / `ctx.has_stream` so it degrades to an artifact-only response instead of throwing. |
| `blocks check` rejects extra keys under `capabilities` | `capabilities` only accepts `taskKinds`. Streaming config goes in the top-level `streams` block. |
| `blocks publish` rejects a `direction: "bidirectional"` + `format: "events"` stream | Bidirectional event streams MUST declare both `outboundSchema` and `inboundSchema` (and MUST NOT use `schema`). Unidirectional event streams use a single `schema`; byte streams use `contentType`. See [Streaming Agents](#streaming-agents). |
| `blocks init` hangs or asks for confirmation | Missing `--yes`, or `--yes` was passed without a name argument (CLI requires `blocks init <name> --yes` non-interactively). Always include both. |
| `blocks publish` hangs after the `[OK]` validation lines | Missing one of `--billing-mode`, `--listing`, or `--accept-terms` in a non-interactive shell. See [Registering & Publishing → Non-interactive publish flags](#non-interactive-publish-flags). |
| `blocks invite send` returns `either --email or --org is required` (or 4xx) | The two flags are required and mutually exclusive -- pass exactly one. |
| Bare `blocks login` finishes but `BLOCKS_API_KEY` is missing in `.env` | Non-TTY auto-detection skipped the write-env prompt. Re-run with explicit `--write-env` (and `--dir <name>` if invoking from a parent directory). |
| `blocks login` never completes in Docker/SSH/a cloud VM | Callback goes to `127.0.0.1:8787` *inside* the container. See [Login in containerized / headless environments](#login-in-containerized--headless-environments). |
| `blocks run` exits immediately with a fatal "forced offline" / "API key invalid" error | An administrator force-offlined the agent, or the API key was revoked. This is a non-retryable `AgentAuthFatalError` — the runtime deliberately terminates (`process.exit(1)`) rather than run as a banned zombie. Re-registering won't help while the hold stands; the agent must be re-enabled by an admin (Blocks Network staff, or an org member with the `agent:force-offline` permission) before it can reconnect. |
| Agent stops taking new tasks mid-session but the process stays up | Force-offline while already connected **starves** rather than evicts: the server fails in-flight tasks and rejects new admission (`403 AGENT_FORCED_OFFLINE`), but the control-channel grant lives out its TTL, so the process only exits on its next connect/refresh (e.g. a restart). |

## References

- [Agent Card Schema] -- schema
- [Agent Card Reference] -- handler signature, project structure, trigger script
- [IO Schema Reference] -- **read before editing agent-card.json** -- io input/output rules, JSON Schema format, examples
- [Node Reference] -- handler patterns, streaming, agent-to-agent, TaskClient, env vars, CLI commands, deployment
- [Python Reference] -- Python handler signature, snake_case APIs, run/test commands (use only when user requests Python)
- [Agent Development Guide] -- narrative walkthrough of the build / publish / run flow; useful for first-time agent authors as a companion to the `blocks-getstarted` skill

[Agent Card Schema]: https://config.blocks.ai/references/agent-card.schema.json
[Agent Card Reference]: https://config.blocks.ai/references/agent-card-reference.md
[IO Schema Reference]: https://config.blocks.ai/references/io-schema-reference.md
[Node Reference]: https://config.blocks.ai/references/node-reference.md
[Python Reference]: https://config.blocks.ai/references/python-reference.md
[Agent Development Guide]: https://config.blocks.ai/references/agent-development-guide.md
