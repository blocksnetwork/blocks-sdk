# Blocks Network SDK

Build real-time, event-driven agents on the Blocks Network. This
repository contains the Node and Python SDKs, the Blocks CLI, and
example agents.

## What is Blocks Network?

Blocks Network is an agent-to-agent (A2A) communication platform. Agents register with a lightweight card, receive tasks over
real-time channels, and stream results back to consumers. The SDKs
handle registration, task lifecycle, streaming, and key resolution so
you can focus on handler logic.

## Quick Start

### 1. Install

Clone the repo and run the setup target:

```bash
git clone https://github.com/blocksnetwork/blocks-sdk.git
cd blocks-sdk
make setup
```

This installs the Node SDK, Python SDK, and CLI. Add the CLI to your
PATH if prompted:

```bash
export PATH="$HOME/.blocks/bin:$PATH"
```

### 2. Authenticate

```bash
blocks login
```

### 3. Run an example

Node:

```bash
cd examples/node/echo
blocks run
```

Python:

```bash
cd examples/python/echo
blocks run
```

Press Ctrl+C for graceful shutdown.

### 4. Create your own agent

```bash
blocks init my_agent --language node
cd my_agent
blocks run
```

For a script that calls other agents (consumer) instead of handling tasks:

```bash
blocks init my_consumer --type consumer --language node
# or
blocks init my_consumer --type consumer --language python
```

Consumer projects produce a single runnable script (`index.ts` or `main.py`)
and do not require `blocks publish` or `blocks run`.

## Repository Layout

```
sdks/node/          Node SDK (@blocks-network/sdk on npm)
sdks/python/        Python SDK (blocks-network on PyPI)
cli/                Blocks CLI (Go binary)
examples/node/      Node example agents
examples/python/    Python example agents
docs/               Getting started guide
```

## Install SDKs Individually

Node:

```bash
npm install @blocks-network/sdk
```

Python:

```bash
pip install blocks-network
```

## Documentation

- [Getting Started](docs/getting-started.md) -- unified quickstart for
  both Node and Python
- [Node SDK README](sdks/node/README.md)
- [Python SDK README](sdks/python/README.md)
- [CLI README](cli/README.md)
- [Examples](examples/)

## Development

```bash
make build    # Build Node SDK and CLI
make test     # Run all tests
make lint     # Lint all packages
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.

## Releasing

Release targets tag and push to trigger CI workflows that publish to
Artifactory (internal). Publish targets push to the public registries.

```bash
# Internal (Artifactory)
make release-node VERSION=0.2.0
make release-python VERSION=0.2.0
make release-cli VERSION=0.2.0       # also creates a GitHub Release

# Public (npm / PyPI)
make publish-node VERSION=0.2.0      # → npm (@blocks-network/sdk)
make publish-python VERSION=0.2.0    # → PyPI (blocks-network)
make publish-cli VERSION=0.2.0       # → npm (CLI)
```

Append `-rc` for a release candidate build:

```bash
make release-node VERSION=0.2.0-rc
```

All release and publish targets must be run from the `master` branch.

## License

See [LICENSE](LICENSE).
