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

### Project modes

`blocks init` can scaffold three kinds of projects via the `--mode` flag:

- `--mode provider` (default): an agent handler project. Produces
  `handler.{ts,py}`, `trigger.{ts,py}`, and `agent-card.json`.
  Use `blocks publish` and `blocks run` to deploy and run.
- `--mode consumer`: a script that calls other agents via `TaskClient`.
  Produces `index.ts` / `main.py`. Run with `npm run start` or
  `python main.py`.
- `--mode webapp`: a static page pre-wired with the Blocks embed-auth
  widget for one or more named agents. Pass `--agent <name>` (repeatable)
  to select which agents the page talks to.

Examples:

```bash
blocks init my_agent                         # provider (default)
blocks init my_consumer --mode consumer      # consumer, prompt for language
blocks init my_consumer --mode consumer --language python --yes
blocks init my_ui --mode webapp --agent echo # webapp wired to the echo agent
```

## Installation

Install the latest release via npm (Linux, macOS, Windows, FreeBSD):

```sh
npm install -g @blocks-network/cli
```

Or via shell script (works on every supported platform, including
FreeBSD and OpenBSD — the installer is POSIX `sh`-compatible, so no
bash is required):

```sh
curl -fsSL https://config.blocks.ai/install.sh | sh
```

### Upgrading

Run `blocks upgrade` to download and install the latest release. The CLI
checks the npm registry for new versions every 2 hours and prints a
notice to stderr when an update is available. Upgrade behavior by install
method:

- **`~/.blocks/bin` (install.sh, `make install`)** — `blocks upgrade`
  replaces the binary in place.
- **npm global (`npm i -g`)** — `blocks upgrade` detects this and
  directs you to run `npm i -g @blocks-network/cli@latest` instead.
- **OpenBSD** — npm packages are not published; use `install.sh`.

Environment variables:

- `BLOCKS_INSTALL_DIR` — override the install directory for `blocks upgrade`.

Files created:

- `~/.blocks/update-check.json` — caches the latest version to avoid
  hitting the registry on every invocation.

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
