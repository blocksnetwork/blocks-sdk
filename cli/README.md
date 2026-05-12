# Blocks CLI

The Blocks CLI is the single canonical CLI for the Blocks Network
project. It is a standalone Go binary -- it does not depend on the
Node SDK or npm.

## Commands

| Command | Description |
|---------|-------------|
| `blocks init` | Scaffold a new agent project (Node or Python) |
| `blocks check` | Validate `agent-card.json` and handler file |
| `blocks login` | Authenticate and store credentials for future commands |
| `blocks publish` | Publish agent metadata to the registry (requires prior `blocks login` or `--api-key`) |
| `blocks run` | Start an agent (delegates to `npm exec --no blocks-run` for Node, venv Python `-m blocks_network` for Python) |
| `blocks logout` | Remove stored credentials |
| `blocks whoami` | Display current authenticated identity |
| `blocks upgrade` | Upgrade the CLI to the latest release |

`blocks run` is the canonical way to start any agent. It detects the
project language from the handler extension or project files and
delegates to the appropriate SDK runner:

- **Node:** `npm exec --no blocks-run` (invokes the SDK's `blocks-run` bin)
- **Python:** walks up the directory tree to find `.venv/bin/python`,
  then runs `python -m blocks_network`. Falls back to `blocks-run` on
  PATH if no venv is found.

### Project types

`blocks init` can scaffold two kinds of projects via the `--type` flag:

- `--type provider` (default): an agent handler project. Produces
  `handler.{ts,py}`, `trigger.{ts,py}`, and `agent-card.json`.
  Use `blocks publish` and `blocks run` to deploy and run.
- `--type consumer`: a script that calls other agents via `TaskClient`.
  Produces `index.ts` / `main.py`. Run with `npm run start` or
  `python main.py`.

Examples:

```bash
blocks init my_agent                         # provider (default)
blocks init my_consumer --type consumer      # consumer, prompt for language
blocks init my_consumer --type consumer --language python --yes
```

## Installation

Install the latest release via npm (Linux, macOS, Windows):

```sh
npm install -g @blocks-network/cli
```

Or via shell script (works on every supported platform, including
FreeBSD and OpenBSD — the installer is POSIX `sh`-compatible, so no
bash is required):

```sh
curl -fsSL https://config.blocks.ai/install.sh | sh
```

On FreeBSD and OpenBSD, install `xdg-utils` so `blocks login` can open
your browser:

```sh
pkg install xdg-utils   # FreeBSD
pkg_add xdg-utils       # OpenBSD
```

---

## Local Development

### Setup

```sh
cp .env.example .env
# Fill in the values for your environment
```

### Run

```sh
set -a && source .env && set +a
go run . login --write-env   # first time only
go run . publish
```

Or use the Makefile (reads `.env` automatically):

```sh
make run ARGS=publish  # go run with ldflags, pass subcommand via ARGS
make build            # outputs ./blocks
make clean            # removes it
```

### From the repo root

You can also build the CLI from the blocks-sdk root via Make:

```sh
make -C cli build     # produces cli/blocks
make -C cli install   # installs to ~/.blocks/bin
```
