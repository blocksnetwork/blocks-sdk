# Agent Card Reference (agent-card.json)

## Scaffolded Project Structure

```
my-agent/
  agent-card.json
  handler.ts
  trigger.ts
  package.json
  .env
  .npmrc
  .gitignore
  Dockerfile
```

## Minimal Example

```json
{
  "identity": {
    "agentName": "my_agent",
    "displayName": "My Agent",
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

## Key Fields

Required top-level: `identity`, `capabilities`, `skills`, `runtime`.

Required in `identity`: `agentName` (pattern `^[a-zA-Z0-9_]+$`), `displayName`, `description`, `version`, `provider` (`{ organization }`). Optional in `identity`: `documentationUrl`, `repositoryUrl`, `iconUrl`. Optional in `provider`: `url`.

Required in `capabilities`: `taskKinds` (array of `"request"`, `"pipe"`, or both).

Required in `skills`: at least one `{ id, name }`. Optional per skill: `description`, `examples`.

Required in `runtime`: `handler`. Optional: `handlerExport` (default `"default"`), `concurrency` (default 1), `expectedInstances` (default 1), `maxPendingBacklog`, `maxRunningTimeSec`.

Optional `io` section: `inputs`, `outputs` (each an array of typed I/O
descriptors). On every `io.inputs[]` entry, **required**: `id`,
`description`, `contentType`, `required`. **Optional**: `example`,
`schema`, `accept` (file-class only — array of
`contentType`-or-family-glob entries), `maxSizeBytes` (file-class only
— integer in `[1, 26214400]`). On every `io.outputs[]` entry,
**required**: `id`, `contentType`, `guaranteed`. **Optional**:
`description`, `example`, `schema`. Per-class invariants on inputs
(form requires `schema` and `example`; text and file forbid `schema`;
only file allows `accept` and `maxSizeBytes`) are enforced at
registration; see `skills/references/io-schema-reference.md` for the
detailed rules. `contentType` values must be in canonical lowercase
MIME form with no `;` parameters; the schema accepts a curated
catalog plus `text|image|audio|video/*` family wildcards and
`*/*+(json|xml|zip|gzip)` suffix patterns
(see `dev_docs/SUPPORTED_CONTENT_TYPES.md`).

## Streaming Capabilities

<!-- sync: streaming rules duplicated in SKILL.md, node-reference.md, python-reference.md -->
**IMPORTANT:** Streaming is declared via a **top-level `streams`** property --
NOT inside `capabilities`. The `capabilities` object only accepts `taskKinds`
(`additionalProperties: false`). Adding `streaming` or `extensions` to
`capabilities` will fail `blocks check`.

For agents with only `request` in `taskKinds`, streams must contain only
`_default` (at most one entry, schema-enforced). Agents with both `request`
and `pipe` can use other stream names. Both `direction` and `format` are
required on each stream.

**Format constraints** (enforced by the schema):
- **Event streams**: `contentType` is NOT allowed. Unidirectional (`outbound`/`inbound`) use `schema`; bidirectional MUST use `outboundSchema` + `inboundSchema` instead.
- **Byte streams**: `schema`, `outboundSchema`, `inboundSchema` are NOT allowed.

**Affinity** (`affinity: "dedicated" | "shared"`, default `"dedicated"`):
- **Dedicated** (default): each task gets its own per-task channel `stream.{agent}.{taskId}-{counter}`. Independent writer per task. Safe default.
- **Shared**: all tasks that open this declared stream write to and read from one cross-task broadcast channel `stream.{agent}.{declaredKey}`. Ref-counted across tasks; SDK manages a single writer. Four lifecycle rules differ from dedicated streams (see `SDK_CONTRACT.md` §4.4.2, §4.4.3, §8.4.1a, §8.7.3a, §8.7.4):
  - **Pipe-only**: `createStream()` on a shared-affinity declared stream from a request-task handler throws at runtime. Request tasks are single-shot; cross-task broadcast is inherently a pipe-task concept. If your agent card includes `"request"` in `taskKinds`, don't declare a stream with `affinity: "shared"`.
  - **Not combinable with `external: true`**: shared affinity implies one SDK-managed writer with per-task ref-counting; external streams delegate the writer entirely. The SDK rejects the combination at runtime. Use `affinity: "dedicated"` with `external: true`, or `affinity: "shared"` without external.
  - **No per-task `stream_end` marker**: `stream.end()` on a shared stream releases only the current task's refcount; it does NOT publish a `stream_end` marker on the shared channel. Consumers drain via task-terminal / auto-drain rather than the marker.
  - **Per-task discovery**: each task that calls `createStream()` for a shared declared stream receives its own `stream_started` event on its own status channel, regardless of whether the writer is fresh or reused.
  - **Idempotent within a task**: a second `createStream()` call from the same task for the same declared stream returns the same `StreamObject` reference with no registry mutation or extra setup publish.

```json
{
  "streams": {
    "_default": {
      "direction": "outbound",
      "format": "bytes",
      "description": "Main output stream"
    }
  }
}
```

Without the `streams` block, `ctx.createStream()` throws at runtime:
`"Streaming was not negotiated for this task."`

### Full Streaming Agent Card Example

```json
{
  "identity": {
    "agentName": "my_streaming_agent",
    "displayName": "My Streaming Agent",
    "description": "Streams real-time data to subscribers.",
    "version": "1.0.0",
    "provider": { "organization": "my_streaming_agent" }
  },
  "capabilities": {
    "taskKinds": ["request"]
  },
  "streams": {
    "_default": {
      "direction": "outbound",
      "format": "bytes",
      "description": "Real-time data stream"
    }
  },
  "io": {
    "inputs": [
      {
        "id": "request",
        "description": "Stream configuration.",
        "contentType": "application/json",
        "required": true,
        "example": { "query": "data" },
        "schema": {
          "type": "object",
          "required": ["query"],
          "properties": {
            "query": { "type": "string", "title": "Query" }
          }
        }
      }
    ],
    "outputs": [
      {
        "id": "result",
        "description": "Final summary.",
        "contentType": "application/json",
        "guaranteed": true
      }
    ]
  },
  "skills": [
    { "id": "main", "name": "Stream Data", "description": "Streams data" }
  ],
  "runtime": {
    "handler": "./handler.ts",
    "handlerExport": "default",
    "concurrency": 5,
    "expectedInstances": 1
  }
}
```

### Pipe Task Agent Card Examples

Pipe-only agents use a named stream with dedicated affinity:

```json
{
  "identity": {
    "agentName": "my_pipe_agent",
    "displayName": "My Pipe Agent",
    "description": "Streams real-time data.",
    "version": "1.0.0",
    "provider": { "organization": "my_pipe_agent" }
  },
  "capabilities": {
    "taskKinds": ["pipe"]
  },
  "streams": {
    "stream": {
      "direction": "outbound",
      "format": "events",
      "description": "Primary event stream.",
      "affinity": "dedicated"
    }
  },
  "skills": [
    { "id": "main", "name": "Stream Data", "description": "Streams events" }
  ],
  "runtime": {
    "handler": "./handler.ts",
    "handlerExport": "default",
    "concurrency": 5,
    "expectedInstances": 1
  }
}
```

Mixed agents (request + pipe) use the `_default` stream:

```json
{
  "capabilities": {
    "taskKinds": ["request", "pipe"]
  },
  "streams": {
    "_default": {
      "direction": "outbound",
      "format": "events",
      "description": "Default event stream."
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

Must be a **default async export**. Return `{ artifacts: [{ data, mimeType, fileName?, outputId? }] }`.

**Accessing input** -- always check for undefined:

```typescript
const input = task.requestParts?.[0];
const text = typeof input === 'string' ? input : (input as Record<string, unknown>)?.text as string ?? 'default';
```

### Example Handler

```typescript
import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

export default async function handler(
  task: StartTaskMessage, ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = task.requestParts?.[0];
  const text = typeof input === 'string' ? input : JSON.stringify(input ?? '');
  ctx?.reportStatus('Processing...');
  return { artifacts: [{ data: text.toUpperCase(), mimeType: 'text/plain' }] };
}
```

---

## Trigger Script

`trigger.ts` is auto-generated by `blocks init` with the agent name wired in. To send a task:

```bash
npx tsx trigger.ts
```

Edit the `requestParts` array in `trigger.ts` to change the input.
