# Python Examples

Self-contained example agents for Blocks Network, using the Python SDK
(`blocks-network`). Whether you're building your first Blocks agent or
exploring advanced patterns like streaming and orchestration, these
examples walk you through the SDK's core concepts with working,
runnable code.

Each subfolder is a complete, runnable agent with its own handler,
configuration, and dependencies.

## What You'll Learn

- How to write a handler function that receives tasks and returns artifacts
- How to stream data to consumers in real time using `create_stream()`
- How to orchestrate multiple agents from a single handler via `ctx.task_client`
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

1. From the `blocks-sdk/` root, run setup (installs the Python SDK
   into a local venv and installs Node workspace dependencies):

```bash
make setup
```

2. Run an example:

```bash
cd examples/python/echo
blocks login --write-env  # first time only -- authenticate
blocks publish         # register agent with the registry
blocks run
```

3. Press Ctrl+C to stop. The agent's `stop()` runs best-effort cleanup
   in a daemon thread, then the runtime hard-exits — **in-flight tasks
   are interrupted, not drained**. Proper graceful task-drain on
   SIGINT/SIGTERM is tracked under the agent SDK graceful-shutdown
   initiative; today the Python and Node runtimes behave the same way.

## Optional .env support

Some Python consumer examples call `load_dotenv()` so they can read
configuration from a local `.env` file. For that behavior, install:

```bash
pip install python-dotenv
```

If you prefer, you can skip `python-dotenv` and provide the same values
through normal shell environment variables instead.

## Structure

Each example contains:

- `handler.py` -- the agent handler function
- `agent-card.json` -- agent metadata (type, description, capabilities)
- `.env.example` -- environment variable placeholders
- `pyproject.toml` -- Python project metadata and dependencies
- `README.md` -- local documentation

Some examples also include:

- Consumer scripts (e.g., `main.py`, `stock_sim_client.py`)
- `HOW_TO.md` for complex setup instructions

## Creating Your Own

Copy any canonical example folder as a starting point. Edit
`handler.py` with your logic and update `agent-card.json` with your
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
