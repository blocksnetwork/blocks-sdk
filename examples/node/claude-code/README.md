# Claude Code Agent (Node.js)

Advanced example: wraps the Claude Code CLI as a Blocks agent with
real-time streaming output via Blocks Network.

**Category:** Advanced -- external tool wrapper

Registers as agent type `claude-code-node` and runs side-by-side with the
Python agent (`claude-code-python`).

## Prerequisites

- **Node.js 22+**
- **`claude` CLI** on your PATH -- install with `npm install -g @anthropic/claude-code`
- **Anthropic API key** from <https://console.anthropic.com/>
- **`BLOCKS_API_KEY`** (run `blocks login` to generate)

## Setup

```bash
cd examples/node/claude-code
npm install
cp .env.example .env
# Edit .env with your ANTHROPIC_API_KEY (required) and PubNub keys (optional)
```

## Run

```bash
blocks run
```

This reads `agent-card.json`, validates it, loads `.env`, and starts
the agent.

## Configuration

All configuration is via environment variables (set in `.env` or your shell):

| Variable                  | Default                          | Description                                                |
| ------------------------- | -------------------------------- | ---------------------------------------------------------- |
| `ANTHROPIC_API_KEY`       | (required)                       | Anthropic API key                                          |
| `CLAUDE_MODEL`            | (CLI default)                    | Model to use (e.g. `sonnet`, `opus`)                       |
| `CLAUDE_ALLOWED_TOOLS`    | `Read,Write,Edit,Bash,Glob,Grep` | Comma-separated tool allowlist                             |
| `CLAUDE_DISALLOWED_TOOLS` | (none)                           | Comma-separated tool blocklist                             |
| `CLAUDE_ALLOWED_PATHS`    | (none)                           | Comma-separated allowed working directories                |
| `CLAUDE_BASH_SAFETY`      | `on`                             | `on` or `off` -- blocks dangerous bash commands            |
| `CLAUDE_BASH_BLOCKLIST`   | (none)                           | Additional blocked bash patterns (comma-separated regexes) |
| `CLAUDE_MAX_BUDGET_USD`   | (none)                           | Max dollar amount per task                                 |
| `BLOCKS_API_KEY`          | (required)                       | Blocks authentication token (from `blocks login`)          |

## How It Works

1. The agent registers as `claude-code-node` and listens for tasks on
   `pubnub://agent.claude-code-node.control`.
2. When a task arrives, the handler validates configuration, extracts the
   prompt and options from `requestParts`, and spawns `claude -p --output-format stream-json`.
3. Token-level text deltas are streamed to the consumer in real time via the
   Blocks stream.
4. Tool usage (file writes, bash commands) is tracked. If bash safety is
   enabled, dangerous commands are detected in the stream and the subprocess
   is killed immediately.
5. A JSON artifact is returned with the full response text, session ID (for
   multi-turn follow-ups), files changed, tool call count, and cost metadata.

## Web App

This agent works with the web app at `examples/python/claude-code/web/`.
Select "Node" from the agent selector dropdown to route tasks to this agent
instead of the Python one.
