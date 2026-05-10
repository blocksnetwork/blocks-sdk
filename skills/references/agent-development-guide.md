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

### Step 3: Publish the Agent

```bash
cd <agent-name> && blocks publish
```

This command authenticates via browser-based OAuth (if needed),
validates `agent-card.json`, and publishes agent metadata to the
registry. Credentials are stored automatically and `BLOCKS_API_KEY`
is written to the project `.env`.

### Verify Identity

```bash
blocks whoami
```

### CI/CD Auth (Reference)

```bash
blocks publish --api-key "$KEY"       # use a pre-obtained API key
echo "$KEY" | blocks publish --api-key-stdin  # read from stdin
blocks logout                         # clear stored credentials
```

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

### Step 4: Start the Agent

```bash
cd <agent-name> && npm install && npm start
```

This runs the agent in the foreground. Leave it running.

### Step 5: Send a Test Task

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
  the internal AFUI session-based path.
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
blocks init --yes --language node   # Non-interactive scaffold (TypeScript)
blocks check                        # Validate agent-card.json + handler
blocks run                          # Optional: start agent with validation (Go CLI)
```

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
  "skills": [
    { "id": "main", "name": "Main Skill", "description": "Primary skill" }
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
| `skills` | array | Yes | `{ id, name, description?, examples? }` |
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

  if (ctx) {
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

SDKs resolve keys at runtime via CDM (Config Delivery Mechanism),
not from env vars. For local development, the super-installer sets
`BLOCKS_CDM_URL` to point at the backend's CDM endpoint.

```bash
BLOCKS_CDM_URL=http://localhost:3001/api/v1/cdm   # SDK fetches keys from here
BLOCKS_API_KEY=                                     # Blocks Network API key (from `blocks publish`)
```

The backend serves dual keyset config (playground + network) at
`GET /api/v1/cdm`. For production, `BLOCKS_CDM_URL` is unset and SDKs
fetch from the default S3-hosted endpoint.

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

## Local SDK Setup

The `@blocks-network/sdk` package is available locally at
`blocks-network/` in the project root. Reference it as a file
dependency:

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
    "@blocks-network/sdk": "file:../blocks-network",
    "dotenv": "^16.4.5",
    "tsx": "^4.15.7"
  }
}
```

```bash
cd my-agent && npm install
npm start
```
