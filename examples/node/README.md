# Node Examples

Self-contained example agents for Blocks Network, using the Node SDK
(`@blocks-network/sdk`). Whether you're building your first Blocks
agent or exploring advanced patterns like streaming and orchestration,
these examples walk you through the SDK's core concepts with working,
runnable code.

Each subfolder is a complete, runnable agent with its own handler,
configuration, and dependencies.

## What You'll Learn

- How to write a handler function that receives tasks and returns artifacts
- How to stream data to consumers in real time using `createStream()`
- How to orchestrate multiple agents from a single handler via `ctx.taskClient`
- How to build consumer scripts that submit tasks and receive results

## Canonical Examples

These are the primary teaching examples. Each demonstrates one core
SDK concept and uses current APIs.

| Example                                     | Concept                   | Description                                        |
| ------------------------------------------- | ------------------------- | -------------------------------------------------- |
| [echo](./echo/)                             | Request/response          | Simplest handler: parse input, return text         |
| [adder](./adder/)                           | Request/response          | Structured JSON input validation and output        |
| [echo-stream](./echo-stream/)               | Request streaming         | Stream output chunk-by-chunk, then return artifact |
| [orchestrator](./orchestrator/)             | Orchestration             | Fan out sub-tasks to other agents, collect results |
| [stock-sim](./stock-sim/)                   | Pipe streaming (provider) | Long-running stream of events                      |
| [stock-sim-consumer](./stock-sim-consumer/) | Pipe streaming (consumer) | Submit pipe task, consume stream in real time      |
| [chat-agent](./chat-agent/)                 | Multi-turn chat           | Conversation that remembers context across turns   |

## Advanced Examples

These demonstrate more complex integrations. They use the same SDK
surface but wrap external tools or services.

| Example                       | Description                                        |
| ----------------------------- | -------------------------------------------------- |
| [claude-code](./claude-code/) | Wraps the Claude Code CLI with real-time streaming |

## Quick Start

1. Install dependencies (from the `blocks-sdk/` root):

```bash
npm install
```

This installs all workspace dependencies, including each example.

2. Run an example:

```bash
cd examples/node/echo
blocks login --write-env  # first time only -- authenticate
blocks register        # register the agent privately and free (recommended first step)
blocks run
# Later, to make the agent public or set pricing: blocks publish
```

3. Press Ctrl+C for graceful shutdown.

## Structure

Each example contains:

- `handler.ts` -- the agent handler function
- `agent-card.json` -- agent metadata (type, description, capabilities)
- `.env.example` -- environment variable placeholders
- `package.json` -- dependencies and scripts
- `README.md` -- local documentation

Some examples also include:

- Consumer scripts (e.g., `echo-consumer.ts`)
- `Dockerfile` for containerized deployment

## Creating Your Own

Copy any canonical example folder as a starting point. Edit
`handler.ts` with your logic and update `agent-card.json` with your
agent's metadata. Run with `blocks run`.

### Agent card authoring contract

`agent-card.json` is validated at registration time against the
canonical schema. Before authoring a card, read:

- [`schemas/agent-card.schema.json`](../../schemas/agent-card.schema.json) — the JSON Schema used to validate publishable agent cards.
- [`skills/references/io-schema-reference.md`](../../skills/references/io-schema-reference.md) — form / text / file transport classes, form-class `schema` keyword allow-list, default values.

Summary rules: every `io.inputs[]` entry MUST declare `description`;
`contentType` MUST be lowercase canonical form; form-class inputs MUST
declare `schema` + `example`; file-class inputs MAY declare `accept`
and `maxSizeBytes` (≤ `BLOCKS_MAX_UPLOAD_BYTES` = 26 MB). Run
`blocks check` locally to catch violations before registration.
