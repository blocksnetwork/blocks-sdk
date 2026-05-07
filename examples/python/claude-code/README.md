# Claude Code Agent Example

Advanced example: wraps the Claude Code CLI as a Blocks agent with
real-time streaming output via `ctx.create_stream()`.

**Category:** Advanced -- external tool wrapper

Supports multi-turn conversations, configurable tool permissions, filesystem sandboxing, and bash safety hooks.

## Prerequisites

- Python 3.10+
- An Anthropic API key ([console.anthropic.com](https://console.anthropic.com/))
- `BLOCKS_API_KEY` (run `blocks login` to generate)

## Setup

```bash
cd examples/python/claude-code
pip install -e .

# Copy and edit .env
cp .env.example .env
# Set ANTHROPIC_API_KEY to your key
```

## Run

```bash
# Terminal 1: Start the agent
blocks run

# Terminal 2: Start the Blocks backend
# See backend documentation for setup instructions

# Terminal 3: Send a task
npm run task:send -- claude-code-python \
  --message "Write a hello world in Python"
```

## Multi-turn usage

The handler returns a `sessionId` in its metadata artifact. Pass it back in subsequent tasks to resume context:

```
Turn 1 request:
  { "text": "Fix the bug in auth.ts" }

Turn 1 response artifacts:
  [0] text/plain:        "I found a null check issue in auth.ts line 42..."
  [1] application/json:  { "sessionId": "ses-a1b2c3d4e5f6", ... }

Turn 2 request:
  { "text": "Now add tests for that fix", "sessionId": "ses-a1b2c3d4e5f6" }
```

Claude Code maintains full conversational context across turns -- it remembers all previous messages, tool calls, and file edits from the session.

## Configuration

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ANTHROPIC_API_KEY` | (required) | Anthropic API key |
| `CLAUDE_ALLOWED_TOOLS` | `Read,Write,Edit,Bash,Glob,Grep` | Comma-separated tool allowlist |
| `CLAUDE_DISALLOWED_TOOLS` | (unset) | Comma-separated tool blocklist |
| `CLAUDE_ALLOWED_PATHS` | (unset -- any path) | Comma-separated allowed working directories |
| `CLAUDE_BASH_SAFETY` | `on` | `"on"` or `"off"` to toggle bash safety hooks |
| `CLAUDE_BASH_BLOCKLIST` | (unset) | Additional blocked regex patterns |

### Per-task request_parts fields

| Field | Type | Description |
|-------|------|-------------|
| `text` | string | The prompt (required) |
| `sessionId` | string | Resume a previous session |
| `tools` | string[] | Override tool allowlist for this task |
| `cwd` | string | Working directory for Claude Code |
| `disableBashSafety` | boolean | Disable bash safety hooks for this task |

## Security features

**Bash safety hooks** -- A built-in blocklist blocks destructive commands (`rm -rf /`, `sudo`, `mkfs`, fork bombs, etc.). Extend via `CLAUDE_BASH_BLOCKLIST` or disable per-task.

**CWD sandboxing** -- Restrict Claude Code's working directory to paths listed in `CLAUDE_ALLOWED_PATHS`. The handler resolves symlinks and rejects paths outside allowed directories.

**Tool allowlists** -- The default tool set is `Read`, `Write`, `Edit`, `Bash`, `Glob`, `Grep`. Override per-environment or per-task. Apply a blocklist via `CLAUDE_DISALLOWED_TOOLS`.

**bypassPermissions mode** -- The handler runs with `permission_mode="bypassPermissions"` for fully automated execution. All tools in the allowlist execute without confirmation. Restrict the allowlist in production.

## Architecture

```
A2A Task --> handler() --> asyncio.run(_run_claude_session())
                             |
                             +--> ctx.create_stream()    --> Blocks stream channel
                             +--> claude_agent_sdk.query() --> Claude Code engine
                             |     |
                             |     +--> AssistantMessage/TextBlock --> stream.write()
                             |     +--> ToolUseBlock               --> track files/tools
                             |     +--> ResultMessage              --> break
                             |
                             +--> stream.end()           --> flush stream
                             +--> return artifacts        --> text summary + JSON metadata
```

The handler never touches PubNub directly. It uses `ctx.create_stream()` from the Blocks SDK, and `query()` from the Claude Agent SDK. Both are async; the sync handler bridges via `asyncio.run()`.

## Troubleshooting

**`ModuleNotFoundError: No module named 'claude_agent_sdk'`** -- Run `pip install -e .` in the `claude-code` directory. The `claude-agent-sdk` package must be available on PyPI or installed from a local path.

**`ANTHROPIC_API_KEY environment variable is not set`** -- Copy `.env.example` to `.env` and set your key.

**`RuntimeError: cannot be called from a running event loop`** -- The handler uses `asyncio.run()` which cannot nest inside an existing event loop. If the Blocks runtime is async, install `nest_asyncio` or use a thread-based bridge.

**Bash command blocked** -- The bash safety hook denied a command. Check logs for the blocked pattern. Set `CLAUDE_BASH_SAFETY=off` or pass `disableBashSafety: true` to disable.

**CWD rejected** -- The requested `cwd` is outside `CLAUDE_ALLOWED_PATHS`. Either add the path to the allowed list or remove the restriction.

## Web App

A browser-based UI for interacting with the Claude Code agent. Sends prompts, displays real-time streaming output with markdown/code highlighting, and shows structured artifacts on completion. Supports multi-turn sessions, model selection, and cost tracking.

### Prerequisites

- Node.js 18+
- The Claude Code agent running (`blocks run`)
- The Blocks backend running (see backend documentation for setup)

### Setup

```bash
cd web
npm install
cp config.example.json public/config.json
```

Edit `public/config.json` with your PubNub subscribe key and backend URL:

```json
{
  "blocksBackendUrl": "http://localhost:3001",
  "subscribeKey": "sub-c-YOUR-KEY"
}
```

### Run

```bash
npm run dev
```

Open http://localhost:5173 in your browser.

### Features

- Real-time token-level streaming with markdown and syntax highlighting
- Model selection (sonnet, opus, haiku)
- Multi-turn conversations via automatic sessionId carry-forward
- Working directory configuration
- Session cost tracking (accumulated across turns)
- Collapsible artifact panel with decoded JSON and copy-to-clipboard
- Dark theme optimized for code readability

### Build for production

```bash
npm run build
# Output in web/dist/
```

## Known issues

- **Return type**: The handler returns a list of two artifact dicts on success, but other examples return a single dict. Verify that the Blocks runtime accepts list returns before production use.
- **Session persistence**: Claude Code sessions are stored on the local filesystem. They do not survive container restarts.

## Design spec

The design rationale is documented internally.
