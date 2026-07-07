# Blocks CLI

The Blocks CLI is the single canonical CLI for the Blocks Network
project. It is a standalone Go binary -- it does not depend on the
Node SDK or npm.

## Commands

| Command | Description |
|---------|-------------|
| `blocks init` | Scaffold a new agent project (Node or Python) |
| `blocks check` | Validate `agent-card.json` and handler file |
| `blocks login [instanceUrl]` | Authenticate and store credentials for future commands. An optional instance URL targets a specific deployment (the CLI auto-discovers whether it is enterprise) |
| `blocks register` | Register an agent privately and free — the recommended first step (requires prior `blocks login` or `--api-key`) |
| `blocks publish` | Publish an agent to the network, choosing public/private and free/paid (requires prior `blocks login` or `--api-key`) |
| `blocks run` | Start an agent (delegates to `npm exec --no blocks-run` for Node, venv Python `-m blocks_network` for Python) |
| `blocks logout` | Remove stored credentials for the selected profile |
| `blocks profile` | Manage deployment profiles — `list`, `use <name>`, `rename <old> <new>`, `remove <name>` |
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
  Use `blocks register` (private + free, the recommended first step) or
  `blocks publish` (to choose public/paid) to deploy, and `blocks run` to run.
- `--mode consumer`: a script that calls other agents via `TaskClient`.
  Produces `index.ts` / `main.py`. Run with `npm run start` or
  `python main.py`.
- `--mode webapp`: a static page pre-wired with the Blocks embed-auth
  widget for one or more named agents. Pass `--agent <name>` (repeatable)
  to select which agents the page talks to. Scaffolded projects carry a
  required `backendBaseUrl` in `blocks.config.json` — the backend API
  origin the deployed page calls at runtime. It is resolved (highest
  precedence first) from `--backend-url`, the `BLOCKS_BACKEND_URL` env
  var, your active profile's backend (`blocks profile use <name>`), the
  build-time default baked into packaged/enterprise builds, and finally
  the asset host (`--blocks-base-url`). When `--blocks-base-url` is unset,
  the asset host mirrors the resolved backend origin above; it only defaults
  to `https://app.blocks.ai` for stock users.

Examples:

```bash
blocks init my_agent                         # provider (default)
blocks init my_consumer --mode consumer      # consumer, prompt for language
blocks init my_consumer --mode consumer --language python --yes
blocks init my_ui --mode webapp --agent echo # webapp wired to the echo agent
```

## Profiles & deployments

A **profile** is a named deployment target (Blocks Network or an Enterprise
instance) with its own base URL, branding, and per-org API-key cache. The
stock `blocks-network` profile always exists and is the default.

```bash
blocks profile list                       # show profiles (active one marked)
blocks profile use acme                    # switch the active profile
blocks profile rename localhost:3001 dev   # rename a profile (data preserved)
blocks profile remove acme                 # delete a profile
```

Logging in to an enterprise deployment creates/updates a profile. By default
the profile is named after the instance host (e.g. `blocks.acme.com`); pass
`--profile <name>` to store it under a custom name instead:

```bash
blocks login https://blocks.acme.com                   # profile "blocks.acme.com"
blocks login https://blocks.acme.com --profile acme    # profile "acme"
```

### Selecting a profile per command

Every command resolves which profile to use in this order:

1. `--profile <name>` — a persistent flag accepted by any command
   (e.g. `blocks --profile acme publish`, `blocks --profile acme logout`).
2. `BLOCKS_PROFILE` environment variable.
3. The saved active profile (set by `blocks profile use`).
4. The default `blocks-network` profile.

### Credential storage

Profiles are stored in `~/.config/blocks/contexts.json`
(`$XDG_CONFIG_HOME/blocks/contexts.json` when `XDG_CONFIG_HOME` is set), written
with `0600` permissions. On first run, a legacy `credentials.json` from an older
CLI is automatically migrated into the default profile.

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
