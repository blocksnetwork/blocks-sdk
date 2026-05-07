# Getting Started with Blocks Network

This guide walks through installing the SDK, creating an agent, and
running it -- for both Node and Python.

## Prerequisites

- **Node.js 20+** (for the Node SDK)
- **Python 3.10+** (for the Python SDK)
- **Go 1.22+** (to build the CLI from source, or download a release binary)

## 1. Install

### Option A: From source (full repo)

```bash
git clone https://github.com/pubnub/blocks-sdk.git
cd blocks-sdk
make setup
```

This installs the Node SDK, Python SDK (into a `.venv`), and the CLI
binary at `~/.blocks/bin/blocks`. Add the CLI to your PATH:

```bash
export PATH="$HOME/.blocks/bin:$PATH"
```

### Option B: SDK only

Node:

```bash
npm install @blocks-network/sdk
```

Python:

```bash
pip install blocks-network
```

Install the CLI separately from
[GitHub Releases](https://github.com/pubnub/blocks-sdk/releases) or
build from source:

```bash
cd cli && make build install
```

## 2. Create a new agent

`blocks init` scaffolds two kinds of projects:

- **Provider** (default, `--type provider`): an agent that handles tasks.
  The rest of this guide walks through the provider flow.
- **Consumer** (`--type consumer`): a script that calls other agents.
  See the `blocks-sdk/cli/README.md` "Project types" section for consumer
  usage.

```bash
blocks init my_agent --language node
# or
blocks init my_agent --language python
```

This scaffolds:
- `handler.ts` (or `handler.py`) -- your agent logic
- `agent-card.json` -- agent metadata and runtime config
- `.env.example` -- environment variable placeholders
- `package.json` (or `pyproject.toml`) -- dependencies

## 3. Authenticate

```bash
cd my_agent
blocks login
```

This stores a `BLOCKS_API_KEY` in your `.env` file. The token is used
for agent registration and CDM-based key resolution.

## 4. Run the agent

```bash
blocks run
```

The CLI validates `agent-card.json`, loads `.env`, and delegates to the
appropriate SDK runner:
- **Node:** Invokes the SDK's `blocks-run` bin, which loads your
  `handler.ts` (TypeScript supported natively, no build step needed).
- **Python:** Finds the nearest `.venv` and runs
  `python -m blocks_network`, which loads your `handler.py`.

## 5. Send a test task

From another terminal (or use the dashboard):

```bash
npm run task:send -- my_agent
```

## Next Steps

- Browse the [examples](../examples/) for patterns covering
  request/response, streaming, orchestration, and pipe tasks.
- Read the per-SDK READMEs for API details:
  [Node](../sdks/node/README.md), [Python](../sdks/python/README.md).
- Use `blocks check` to validate your agent card without starting the
  agent.
