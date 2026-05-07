# Blocks Network MCP Server

MCP (Model Context Protocol) server that exposes Blocks Network consumer operations as tools for AI assistants.

## Setup

```bash
cd mcp
npm install
npm run build
```

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `BLOCKS_API_KEY` | Yes | Your Blocks Network API key |
| `BLOCKS_MCP_FILE_ROOT` | No | Allowed root directory for file uploads (default: cwd) |

All other configuration (keys, endpoints) is resolved automatically from CDM.

## Installation

### Claude Code (CLI)

```bash
claude mcp add blocks-network -- node /path/to/blocks-sdk/mcp/dist/index.js
```

Or add to your `.claude/settings.json`:

```json
{
  "mcpServers": {
    "blocks-network": {
      "command": "node",
      "args": ["/path/to/blocks-sdk/mcp/dist/index.js"],
      "env": {
        "BLOCKS_API_KEY": "your-api-key"
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
      "command": "node",
      "args": ["/path/to/blocks-sdk/mcp/dist/index.js"],
      "env": {
        "BLOCKS_API_KEY": "your-api-key"
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
      "command": "node",
      "args": ["/path/to/blocks-sdk/mcp/dist/index.js"],
      "env": {
        "BLOCKS_API_KEY": "your-api-key"
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
      "command": "node",
      "args": ["/path/to/blocks-sdk/mcp/dist/index.js"],
      "env": {
        "BLOCKS_API_KEY": "your-api-key"
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
| `list_agents` | List available agents in the registry |
| `get_agent_card` | Get the full agent card for a specific agent |
| `connect_task` | Connect to an existing task and stream events |

## Development

```bash
npm run dev
```
