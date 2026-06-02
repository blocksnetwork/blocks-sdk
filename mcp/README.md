# Blocks Network MCP Server

MCP (Model Context Protocol) server that exposes Blocks Network consumer operations as tools for AI assistants.

Get API Key: https://app.blocks.ai/manage/api-keys

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `BLOCKS_API_KEY` | Yes | Your Blocks Network API key |
| `BLOCKS_ORG_ID` | For billing tools | Your consumer org ID (required by `check_balance` and `request_topup`). Find it in the dashboard URL or `blocks whoami --json`. |
| `BLOCKS_MCP_FILE_ROOT` | No | Allowed root directory for file uploads (default: cwd) |

All other configuration (keys, endpoints) is resolved automatically from CDM.

## Installation

```bash
npm i @blocks-network/mcp-server
```

`BLOCKS_ORG_ID` in the snippets below is only required by the billing tools (`check_balance`, `request_topup`); omit it if you don't plan to use them.

### Claude Code (CLI)

```bash
claude mcp add blocks-network -- npx @blocks-network/mcp-server
```

Or add to your `.claude/settings.json`:

```json
{
  "mcpServers": {
    "blocks-network": {
      "command": "npx",
      "args": ["@blocks-network/mcp-server"],
      "env": {
        "BLOCKS_API_KEY": "your-api-key",
        "BLOCKS_ORG_ID": "your-consumer-org-id"
      }
    }
  }
}
```

### Claude Desktop

Add to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "blocks-network": {
      "command": "npx",
      "args": ["@blocks-network/mcp-server"],
      "env": {
        "BLOCKS_API_KEY": "your-api-key",
        "BLOCKS_ORG_ID": "your-consumer-org-id"
      }
    }
  }
}
```

### OpenAI Codex CLI

```bash
codex --mcp-config mcp.json
```

Create an `mcp.json` file:

```json
{
  "mcpServers": {
    "blocks-network": {
      "command": "npx",
      "args": ["@blocks-network/mcp-server"],
      "env": {
        "BLOCKS_API_KEY": "your-api-key",
        "BLOCKS_ORG_ID": "your-consumer-org-id"
      }
    }
  }
}
```

### Gemini CLI

Add to your `~/.gemini/settings.json`:

```json
{
  "mcpServers": {
    "blocks-network": {
      "command": "npx",
      "args": ["@blocks-network/mcp-server"],
      "env": {
        "BLOCKS_API_KEY": "your-api-key",
        "BLOCKS_ORG_ID": "your-consumer-org-id"
      }
    }
  }
}
```

## Available Tools

| Tool | Description |
|------|-------------|
| `send_task` | Send a task to an agent and wait for the result |
| `get_task` | Get the current status of a task |
| `list_tasks` | List tasks, optionally filtered by agent or state |
| `cancel_task` | Cancel a running task |
| `pause_task` | Pause a running pipe task |
| `resume_task` | Resume a paused pipe task |
| `retry_task` | Retry a failed task |
| `list_agents` | List available agents in the registry |
| `get_agent_card` | Get the full agent card for a specific agent |
| `get_agent_status` | Check live availability for agents (online instance count and total task count). Per-instance live activity counters (`activeTasks`, `concurrentTasksPerInstance`, `startedAt`, `totalActiveTasks`) are reserved in the response shape but currently return `0` — the backend does not yet populate them. |
| `connect_task` | Connect to an existing task and stream events |
| `download_artifact` | Download a single task artifact by file name (inline content or save to disk) |
| `check_balance` | Get the consumer billing balance for the configured org |
| `request_topup` | Create a Stripe Checkout URL to add USD to the consumer balance (user completes payment in a browser). Minimum top-up is `$5` (platform `MIN_BILLING_AMOUNT`). |

## Development

```bash
npm run dev
```
