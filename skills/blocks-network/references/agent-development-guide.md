---
name: blocks-network
description: Scaffold, build, and deploy Blocks Network AI agents using the blocks CLI and TypeScript/Python handlers. Use when creating A2A-compliant agents with real-time task processing, streaming, and agent-to-agent communication.
metadata:
  author: blocks-network
  version: "0.2.0"
  domain: real-time
  triggers: blocks, blocks-network, agent, a2a, ai agent, agent scaffold, agent handler, task agent, streaming agent, agent-to-agent, deploy, cli
  role: specialist
  scope: implementation
  output-format: code
---

# Blocks Network -- Agent Development Guide

You are a Blocks Network specialist. Help developers scaffold, build,
and deploy AI agents using the Blocks Network framework -- a real-time,
A2A-protocol-compliant agent runtime powered by real-time pub/sub.

## When to Use

- Scaffolding a new agent project
- Implementing a handler (TypeScript or Python)
- Configuring agent-card.json
- Writing trigger or consumer scripts
- Deploying locally
- Implementing streaming or agent-to-agent communication

---

## Before You Build -- Gather Requirements First

**IMPORTANT:** When a user asks you to create a new agent, do NOT start
scaffolding or writing code immediately. Follow this two-step flow:

### Step 1: Ask for the Agent Name

Ask the user what the agent should be called. Skip this if the user
already provided a name.

**Name normalization:** Convert the user's input to a valid agent name
by replacing any character that is not `A-Za-z0-9` with an underscore
(`_`). Collapse consecutive underscores into one and trim
leading/trailing underscores. Example: `"My Cool Agent!"` becomes
`My_Cool_Agent`.

### Step 2: Generate a Description and Let the User Edit

Based on the normalized name, generate a concise one-sentence
description of what the agent does. Present it to the user with the
option to accept or provide a custom description. Once name and
description are confirmed, summarize the plan before proceeding.

After scaffolding completes (`blocks init`), proceed to **Authentication**
(Steps 3-4). After the handler is implemented, proceed to **Run & Test**
(Steps 5-6).

---

## Publish -- Authenticate and Register After Scaffolding

After running `blocks init`, the user must publish their agent before
they can run it against the backend.

### Step 3: Authenticate

```bash
blocks login --network --write-env
```

This opens browser-based OAuth, stores credentials, and writes
`BLOCKS_API_KEY` to the project `.env`. Use `--dir <path>` to target a
different directory (e.g. `blocks login --network --write-env --dir
./my_agent`).

**Name the deployment, and pass `--write-env` (or `--no-write-env`), when
invoking `blocks login` from a coding-agent / non-interactive session.**
In an interactive terminal `blocks login` asks which deployment to target
unless the question is already settled — by any completed login, a Blocks
Network one included, since that stores no deployment URL of its own; or by a
deployment this invocation resolves (the active profile, `BLOCKS_BACKEND_URL`,
or a project `.env` pinning a deployment the user has a profile for — a bare
`blocks login` there returns to it without asking). Only a genuine first run is
asked. Then — if the answer is
Enterprise — for that instance's URL or short name, and finally
`Write credentials to project .env? (Y/n):`. With no `--no-input`, the CLI
auto-detects non-TTY stdin and silently skips them -- so bare `blocks login`
does not hang, but it also does not populate `.env`, which is rarely what an
agent flow wants. Under `--no-input` those two questions are errors instead
of silent defaults. Be explicit so the outcome is the same regardless of how
the harness wires stdin:

- `--network` targets Blocks Network with no deployment prompt. For an
  Enterprise deployment, pass its URL or short name as an argument
  instead (`blocks login https://blocks.acme.com`, or `blocks login acme`
  for `https://acme.blocks.ai`); the two are mutually exclusive. A URL must
  be `https://` unless its host is loopback, its authority must be a host
  with an optional port in 1–65535 and not `user@host`, it may carry a
  path prefix but no query string and no fragment, and a short name must be
  a single DNS label expanding inside the 253-character DNS limit. An
  argument matching an existing profile name is not an address and is not
  re-checked: it resolves to that profile's saved deployment and is used as
  stored. It is still held to the origin
  rule, though: a stored URL that is not `https` (or `http` to a loopback host), or
  that carries userinfo, a query or a fragment, is refused by profile name rather than
  dialled.
- `--write-env` opts in and `--no-write-env` opts out, and each does so whatever
  else is on the command line, because these two flags are the first things
  the decision consults.
- `--no-input` (global) asks the CLI not to read stdin: where it is
  honoured, a prompt that is still required fails with an error naming the
  flag — or the environment variable — that answers it, whether or not stdin
  is a terminal. It covers the three questions above, so pair it with the
  flags in this list — a key in `BLOCKS_API_KEY` does not answer them, while
  `--api-key` / `--api-key-stdin` does. It does **not** cover the
  organization picker the browser login shows when the account belongs to
  more than one organization — pass `--api-key` / `--api-key-stdin` to skip
  the browser flow and that picker — nor the token paste prompt of
  `blocks login --provider cloudflare|vercel|netlify`. Those two are the only
  gaps: `blocks deploy` reads no stdin under the flag. Not every read there
  becomes an error, though — a missing deploy target falls back to the
  positional argument or `deployTarget` in `blocks.config.json`, and the
  post-deploy agent-card question is skipped with a note on stderr while the
  deploy still succeeds. The flag's own help text
  (`blocks --help`) is the current list.

`--write-env` writes `BLOCKS_API_KEY`, plus the deployment's own
`BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` when the login targeted a specific
deployment rather than stock Blocks Network. Both matter to a script you
launch yourself (`npx tsx trigger.ts`, `python main.py`): without the
backend URL it resolves the REST origin from remote config and reaches
Blocks Network, and without the CDM URL it resolves its real-time keysets
from the public default and subscribes on Blocks Network keysets while
calling your deployment's API. A `--network` login needs neither, writes
neither, and clears stale values. All three land in one edit, so an
interrupted login does not leave a new key beside another deployment's URLs
-- see [Environment Variables (.env)](#environment-variables-env).

The key itself is stored in the deployment's profile in
`~/.config/blocks/contexts.json`, independently of `--write-env`.
`blocks logout` clears that profile's cached org keys and strips
`BLOCKS_API_KEY` from the project `.env`, but keeps the deployment target
so a later `blocks login` returns to it; `blocks profile remove <name>`
forgets the deployment, along with whatever the project `.env` held for it —
unless another profile still describes the same deployment, in which case it
forgets only that name and leaves the file alone.

### Step 4: Register the Agent (or Publish)

```bash
cd <agent-name> && blocks register
```

`blocks register` is the recommended first step: it validates
`agent-card.json` and registers the agent as **private and free**
(usable by the owner and invited organizations only). Registering is not
publishing — nothing is listed publicly until `blocks publish`. It has no
visibility/billing/terms prompts or flags, so non-interactive and CI
invocations succeed with no required flags. (Interactive runs may still prompt for an organization name on the first agent an org
registers or publishes — the same prompt `blocks publish` shows, and Blocks Network
only: an Enterprise deployment pre-seeds organizations, so it skips both that prompt
and `--org-name`.) Requires prior `blocks login`.

Run `blocks publish` when you want to make the agent **public** or set
**pricing**; it can also promote an already-registered agent. The flags
below apply to `blocks publish`.

#### Non-interactive publish flags

In a TTY, `blocks publish` walks the user through listing visibility,
billing mode, pricing, and terms acceptance. In CI or coding-agent
sessions it does not prompt at all: a missing required value is an
immediate error naming the flag that supplies it. Provide flags up front:

| Flag | Purpose |
|---|---|
| `--billing-mode {free\|paid}` | Required (mirrors `agent-card.json`). On a deployment with no marketplace, billing is off and `paid` is **rejected**; publish free there (omit the flag, or pass `free`). |
| `--listing {public\|private}` | Visibility in the registry. |
| `--price <usd>` | Single-kind agent price (auto-mapped to per-task or per-minute). |
| `--price-per-task <usd>` / `--price-per-minute <usd>` | Per-kind pricing for dual-kind (request + pipe) agents. |
| `--free-units <n>` | Free trial units per consumer org (auto-mapped from `taskKinds`). |
| `--free-tasks <n>` / `--free-minutes <n>` | Per-kind free trial counts for dual-kind agents. |
| `--accept-terms` | Accept legal attestations non-interactively. |
| `--org-name <name>` | Set the organization name on first publish. **Blocks Network only** — inert on an Enterprise deployment, whose organizations are pre-seeded by its admin. |
| `--api-key <key>` / `--api-key-stdin` | Authenticate inline without `blocks login`. |

Two recipes:

```bash
# Free public agent
blocks publish --billing-mode free --listing public --accept-terms

# Paid private agent
blocks publish --billing-mode paid --listing private \
  --price-per-task 0.05 --accept-terms
```

`blocks publish` re-runs the same schema validation as `blocks check`,
so you don't need to run `check` first -- but it's still useful as a
fast pre-flight.

### Remove the Agent (`blocks unregister`)

`blocks unregister` is the inverse of `blocks register` -- it removes the
agent from the deployment you are currently targeting.

```bash
blocks unregister                     # name read from ./agent-card.json
blocks unregister <agentName>         # remove an agent from anywhere
blocks unregister <agentName> --yes   # skip the confirmation (required in CI)
```

With no argument the name comes from `identity.agentName` in
`agent-card.json` in the current directory. The deployment is printed
before the removal, which **cannot be undone**; no organization is named,
because the deployment authorizes the removal by your rights over this
agent rather than by the organization the key belongs to, and the
confirmation prompt names the agent one line later.
A non-interactive session refuses to remove anything without `--yes`, and
`--no-input` turns the confirmation into an error naming `--yes`.
`--api-key` / `--api-key-stdin` work here as they do on `blocks register`.

### Verify Identity

```bash
blocks whoami
blocks whoami --json   # structured output: org_name, org_id, key_id, expires_at, days_remaining, expired
```

### Manage Private Agents

When `--listing private` is used, grants are managed via `blocks
invite`:

```bash
blocks invite send <agentName> --email user@example.com   # invite a user
blocks invite send <agentName> --org consumer-org-slug    # invite an org
blocks invite list <agentName>                            # list unaccepted invitations, including expired
blocks invite grants <agentName>                          # list active grants
blocks invite revoke <agentName> --email user@example.com # revoke a user grant
blocks invite revoke <agentName> --org consumer-org-slug  # revoke an org grant
blocks invite accept <token>                              # consumer-side: accept an invitation
```

`--email` and `--org` are mutually exclusive on `send` and `revoke`.

### CI/CD Auth (Reference)

```bash
blocks login --api-key "$KEY" --write-env     # non-interactive login + .env write
blocks login --network --write-env            # target Blocks Network, no deployment prompt
blocks login acme --write-env                 # target an Enterprise instance (short name or full URL)
blocks publish --api-key "$KEY"               # use a pre-obtained API key
echo "$KEY" | blocks publish --api-key-stdin  # read from stdin
blocks unregister <agentName> --yes           # remove an agent (confirmation skipped)
blocks logout                                 # clear stored credentials
blocks version                                # print CLI version
blocks upgrade                                # self-update (POSIX installer flow)
```

Prefix any command with `--no-input` (`blocks --no-input register`) to ask
it not to read stdin: where it is honoured, a prompt that is still required
fails with an error naming the flag — or, for a hosting partner's API token,
the environment variable — that answers it, instead of hanging. It is not
universal, so the flag's own help text (`blocks --help`) — kept in step with
the code — is the current list rather than a formality. In the recipes above,
the one gap is the organization picker the browser login shows when the
account belongs to more than one organization, and it has no answer flag at
all; the `--api-key` / `--api-key-stdin` forms skip the browser flow and that
picker.

`blocks invite send` and `blocks invite revoke` print
`[deployment / agent <name> → <grantee>]` before acting;
`blocks invite accept` prints the deployment alone. As with `unregister`,
no organization is named — these operations turn on ownership of the named
agent (or on being the invitation's recipient), not on the organization the
key belongs to. `register` and `publish` do name the organization, since the
agent is created under one.

When an orchestrator reconnects to an existing sub-task, `session.onArtifact(...)`
replays any pre-populated artifacts synchronously at registration time. Those
replay events are minimal synthetic artifact events with `type`, `taskId`, and
`artifactRef`; original history-only wire fields such as `outputId` and
`protocolVersion` are not retained.
For full timeline reconstruction after reconnecting, use
`session.listEvents()` / `session.list_events()` to read all valid task events
parsed from history.

---

## Run & Test -- Start the Agent and Send a Task

After the handler is implemented, run the agent and send a test task
directly.

### Step 5: Start the Agent

```bash
cd <agent-name> && npm install && npm start
```

This runs the agent in the foreground. Leave it running.

### Step 6: Send a Test Task

In a **separate terminal**:

```bash
cd <agent-name> && npx tsx trigger.ts
```

Report the result back to the user.

### Consumer Auth Note

When writing **consumer scripts** with `TaskClient`, there are multiple
auth modes:

- `agentAuth` -- an `AgentAuth` instance (API key exchanged for JWT).
  Used in trigger scripts and server-side agent code.
- `apiKey` -- pass to `TaskClient.create()` for server-side code that
  exchanges a Blocks API key for a short-lived consumer JWT
- `tokenEndpoint` -- a **customer-owned backend proxy** returns a
  short-lived consumer JWT to browser/mobile code
- `tokenProvider` -- custom callback for advanced auth flows

Important:
- `tokenEndpoint` refers to the developer/customer backend proxy, **not**
  the Dashboard's session-based path.
- `POST /api/v1/auth/agent/consumer-token` bootstraps the consumer JWT.
- `POST /api/v1/auth/task-read-token` is a later JWT -> PAM step
  for task-channel access.

---

## CLI Commands

**Always use `--language node` when scaffolding new agents.**

**IMPORTANT:** Do NOT manually create the agent directory before running
`blocks init`. The command creates the directory itself.

```bash
# Correct:
cd /path/to/parent-directory
blocks init my-agent --yes --language node

# WRONG -- do NOT do this:
# mkdir -p my-agent && cd my-agent && blocks init --yes --language node
```

```bash
blocks init <name> --yes --language node                  # Provider scaffold (default --mode provider)
blocks init <name> --yes --language node --mode consumer  # Consumer script (calls agents via TaskClient)
blocks check                                              # Validate agent-card.json + handler existence
blocks run                                                # Start agent (Go CLI delegates to language runner)
```

`blocks init` defaults to `--mode provider` -- it scaffolds a handler
agent (`handler.{ts,py}`, `trigger.{ts,py}`, `agent-card.json`).
Passing `--mode consumer` scaffolds a script (`index.ts` or `main.py`)
that calls other agents via `TaskClient`; consumer projects have no
`agent-card.json` and no handler, and they don't publish.

`blocks check` validates the card JSON **and** verifies that the file
referenced by `runtime.handler` exists on disk; missing handlers
produce a `[FAIL]` even when the JSON is valid.

The default run path after scaffolding is `npm start` (which runs
`blocks run`). This loads `agent-card.json`, resolves the handler
module, and starts the agent instance.

### Scaffolded Project Structure (Node.js)

```
my-agent/
  agent-card.json
  handler.ts
  trigger.ts
  package.json
  .env
  .npmrc
  Dockerfile
```

---

## Agent Card (agent-card.json)

### Minimal

```json
{
  "identity": {
    "agentName": "my_agent",
    "displayName": "my-agent",
    "description": "Agent description",
    "version": "1.0.0",
    "provider": { "organization": "my_agent" }
  },
  "capabilities": {
    "taskKinds": ["request"]
  },
  "tags": [
    { "id": "main", "name": "Main Tag", "description": "Primary tag" }
  ],
  "runtime": {
    "handler": "./handler.ts",
    "handlerExport": "default",
    "concurrency": 1,
    "expectedInstances": 1
  }
}
```

### Key Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `identity.agentName` | string | Yes | Agent type slug (alphanumeric + underscores) |
| `identity.displayName` | string | Yes | Agent display name |
| `identity.description` | string | Yes | Agent description |
| `identity.version` | string | Yes | Semantic version |
| `identity.provider.organization` | string | Yes | Organization name |
| `capabilities.taskKinds` | string[] | Yes | `"request"`, `"pipe"`, or both |
| `tags` | array | Yes | `{ id, name, description?, examples? }` |
| `runtime.handler` | string | Yes | Path to handler module |
| `runtime.handlerExport` | string | No | Named export (default: `"default"`) |
| `runtime.concurrency` | number | No | Max concurrent tasks (default: 1, 0 = unlimited) |
| `runtime.expectedInstances` | number | No | Instance count (default: 1, 0 = broadcast) |

### Streaming Capabilities

To declare streaming support, add a `streams` section:

```json
{
  "capabilities": {
    "taskKinds": ["request", "pipe"]
  },
  "streams": {
    "_default": {
      "direction": "outbound",
      "format": "bytes",
      "description": "Main output stream"
    }
  }
}
```

---

## Handler Function

### Signature (TypeScript)

```typescript
import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult>
```

Must be a **default async export**.

### StartTaskMessage

```typescript
interface StartTaskMessage {
  type: 'StartTask';
  taskId: string;
  agentName?: string;
  ownerId: string;
  orgId?: string;
  taskKind?: string;             // 'request' | 'pipe'
  duration?: number;             // minutes (pipe tasks)
  durationExpiresAtMs?: number;  // epoch ms deadline (server-computed)
  requestParts?: RequestPart[];
  callerClaims?: Record<string, JsonValue>;
  requestSummary?: Record<string, JsonValue>;
  consumerPublicKey?: string;
  hasStream?: boolean;
  writeToken?: string;
  controlToken?: string;
  protocolVersion?: string;
}

interface RequestPart {
  partId?: string;
  text?: string;
  contentType?: string;
  artifactRef?: ArtifactRef;
  [key: string]: JsonValue | ArtifactRef | undefined;
}
```

**Accessing input** -- always check for undefined:

```typescript
const input = task.requestParts?.[0];
const text = typeof input === 'string' ? input : JSON.stringify(input ?? 'default');
```

### TaskContext

```typescript
interface TaskContext {
  reportStatus: (message: string) => void;
  taskId: string;
  readonly requestParts: RequestPart[];
  createStream: (options?: CreateStreamOptions) => Promise<StreamObject>;
  taskClient: TaskClient;
  cancelSignal: AbortSignal;
  readonly isCancelled: boolean;
  readonly isExpired: boolean;
  readonly hasStream: boolean;
  readonly consumerPublicKey: string | undefined;
  downloadInputArtifact: (part: RequestPart) => Promise<Buffer>;
  publishArtifact: (
    data: Buffer | string,
    options?: { mimeType?: string; fileName?: string; outputId?: string },
  ) => Promise<void>;
}
```

Always use optional chaining (`ctx?.reportStatus()`) since ctx may be
undefined in testing.

### HandlerResult

```typescript
type HandlerResult = {
  artifacts?: ArtifactEntry[];
};

type ArtifactEntry = {
  data: Buffer | string;
  mimeType: string;
  fileName?: string;
  outputId?: string;  // References io.outputs[].id from agent card
};
```

---

## Handler Patterns

All examples use the same import:

```typescript
import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';
```

### Simple Text Processor

```typescript
export default async function handler(
  task: StartTaskMessage, ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = task.requestParts?.[0];
  const text = typeof input === 'string' ? input : JSON.stringify(input ?? '');
  ctx?.reportStatus('Processing...');
  return { artifacts: [{ data: text.toUpperCase(), mimeType: 'text/plain' }] };
}
```

### Streaming Handler

```typescript
export default async function handler(
  task: StartTaskMessage, ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = task.requestParts?.[0];
  const text = typeof input === 'string' ? input : 'Hello from streaming agent!';

  // Guard on hasStream: request-task streaming is consumer opt-in, so
  // createStream() throws when the consumer didn't opt in.
  // Degrade to an artifact-only response in that case.
  if (ctx?.hasStream) {
    ctx.reportStatus('Streaming...');
    const stream = await ctx.createStream({
      declaredStream: 'main-stream',
      bundleSizeBytes: 2048,
      maxLatencyMs: 50,
    });
    for (const word of text.split(' ')) stream.write(word + ' ');
    await stream.end();
  }

  return { artifacts: [{ data: text, mimeType: 'text/plain' }] };
}
```

### Agent-to-Agent (Orchestrator)

```typescript
export default async function handler(
  task: StartTaskMessage, ctx?: TaskContext,
): Promise<HandlerResult> {
  const text = typeof task.requestParts?.[0] === 'string' ? task.requestParts[0] : '';
  ctx?.reportStatus('Delegating to sub-agent...');

  const session = await ctx!.taskClient.sendMessage({
    agentName: 'summarizer',
    ownerId: task.ownerId,
    requestParts: [{ partId: 'request', text }],
  });

  return new Promise((resolve) => {
    session.onArtifact((event) =>
      resolve({ artifacts: [{ data: JSON.stringify(event, null, 2), mimeType: 'application/json' }] }),
    );
    session.onTerminal(() =>
      resolve({ artifacts: [{ data: 'Sub-agent completed', mimeType: 'text/plain' }] }),
    );
  });
}
```

---

## Trigger Script

`trigger.ts` is auto-generated by `blocks init` with the agent name
wired in. To send a task:

```bash
npx tsx trigger.ts
```

The generated script uses `TaskClient` from `@blocks-network/sdk`.
Edit the `requestParts` array in `trigger.ts` to change the input.

---

## Environment Variables (.env)

SDKs resolve real-time keys at runtime via CDM (Config Delivery
Mechanism), not from env vars. `BLOCKS_CDM_URL` points them at a specific
deployment's CDM endpoint instead of the default one.

```bash
BLOCKS_CDM_URL=https://blocks.acme.com/api/v1/cdm  # SDK fetches keysets from here
BLOCKS_API_KEY=                                    # API key
BLOCKS_BACKEND_URL=https://blocks.acme.com         # REST origin pin
```

All three are written by `blocks login --write-env` when the login targeted
a specific deployment.

Every Blocks deployment serves dual keyset config (playground + network)
at `GET /api/v1/cdm`. With `BLOCKS_CDM_URL` unset, SDKs fetch from a
hardcoded default endpoint, which resolves Blocks Network keysets.

The two settings are independent: `BLOCKS_BACKEND_URL` overrides only the
REST API origin and does not change where keysets come from.

`blocks run` sets both for the delegated SDK process whenever the
invocation targets a named deployment, so an agent started that way needs
neither in `.env` (a value already present in the environment always
wins); stock Blocks Network needs no injection, since the SDK default
already resolves it. A process you launch directly -- a trigger or consumer script run
with `npx tsx` / `python`, which `blocks run` never touches -- gets no
injection and reads them from `.env`, where `blocks login --write-env` puts
both alongside the key. Missing the CDM URL is the failure that is easy to
overlook: the script calls the enterprise REST API while subscribing on
Blocks Network keysets.

If you set either value by hand, know that a value supplied by a project
`.env` -- rather than exported in your shell, which wins at both of these
gates, the one input above even an export being a target named on the
`blocks login` command line, below -- is checked before it is followed, and
the two follow related but distinct rules:

- `BLOCKS_BACKEND_URL` is honoured when **any** saved profile describes the
  deployment it names, i.e. you have logged in there at some point. It does
  not have to be the profile that is currently active.
- `BLOCKS_CDM_URL` is honoured only when it is the CDM endpoint of the
  deployment this invocation actually targets. That is an identity check
  against the target rather than a question about logging in, so on stock
  Blocks Network, which has no such origin of its own, there is nothing to
  compare against and a file-sourced value is declined there.

The CLI drops anything else and prints what it dropped, so a `.env` that
travels with a cloned repository does not redirect a command or a credential
at a deployment you never named. Export the value in your shell, or log in
to that deployment (or pin the deployment that serves that CDM endpoint), to
make it authoritative. A project `blocks login --write-env` was run in is
not affected: that login creates the profile and writes the matching URLs.

A login that names its target explicitly additionally unsets an ambient
`BLOCKS_CDM_URL` for that login, because the payload behind it names both an
API origin and an OAuth client id and would otherwise redefine the deployment
just named. `--network` (or picking Blocks Network at the prompt) drops any
such value; an instance argument drops any value that is not that instance's
own CDM endpoint. Those drops are the one place an exported value does not
win: they do not weigh provenance, because a choice made on this invocation
outranks ambient configuration. They affect that login process only.

A project `.env` configures your **agent**, and can also retarget the CLI through
a fixed list: the CLI imports the seven settings it consumes itself —
`BLOCKS_API_KEY`, `BLOCKS_BACKEND_URL`, `BLOCKS_CDM_URL`, `BLOCKS_PROFILE`,
`BLOCKS_APP_BASE_URL`, `BLOCKS_DASHBOARD_URL`, `BLOCKS_CLI_CLIENT_ID`. Everything
else stays out of the CLI's own process and is still merged into the environment of
the agent process `blocks run` starts, so your handler's own configuration arrives
from `.env` exactly as before, and nothing a cloned repository ships can change the
transport the CLI uses, which certificates it trusts, where it keeps credentials, or
what it executes. Being a `BLOCKS_*` name is not the test — `BLOCKS_INSTALL_DIR`
is not on the list.

Four of the seven do change where the CLI points, so a cloned `.env` is not inert.
A pin in one naming a deployment no saved profile describes is dropped rather than
obeyed, with a note naming the file, the variable and the deployment used instead;
the withdrawal covers the delegated agent as well as the CLI.

Names commonly found in a `.env` that would be puzzling to see ignored get an
explanatory note on stderr naming the variable and the file: the proxy and
certificate settings (`HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY`,
`SSL_CERT_FILE`, `SSL_CERT_DIR`, `GODEBUG`), the state-location ones
(`XDG_CONFIG_HOME`, `HOME`, `USERPROFILE`), the hosting-provider tokens
(`CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`, `VERCEL_TOKEN`,
`VERCEL_TEAM_ID`, `NETLIFY_AUTH_TOKEN`) and `BLOCKS_INSTALL_DIR`. The note
decides nothing and the remedy is to export the variable in your shell. Matching
is case-insensitive. The one with a day-to-day consequence is the token group:
`blocks deploy` reads those from the environment, so a token in a project `.env`
is not used — export it, which is what the deploy docs have always said.

---

## Deployment

```bash
# Local (single instance)
cd my-agent && npm install && npm start   # Terminal 1: run agent
npx tsx trigger.ts                         # Terminal 2: send task

# Multi-instance (set expectedInstances in agent-card.json)
npm start  # Terminal 1
npm start  # Terminal 2
```

---

## SDK Dependency

The `@blocks-network/sdk` package is published on npm
(<https://www.npmjs.com/package/@blocks-network/sdk>). Reference it in
your `package.json` (`blocks init` scaffolds this for you):

```json
{
  "name": "my-agent",
  "version": "1.0.0",
  "type": "module",
  "private": true,
  "scripts": {
    "start": "tsx main.ts",
    "check": "blocks check"
  },
  "dependencies": {
    "@blocks-network/sdk": "latest",
    "dotenv": "^16.4.5",
    "tsx": "^4.15.7"
  }
}
```

```bash
cd my-agent && npm install
npm start
```
