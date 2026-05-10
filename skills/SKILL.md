---
name: blocks-network
description: Scaffold, build, and deploy Blocks Network AI agents using the blocks CLI. Supports TypeScript (default) and Python handlers.
metadata:
  author: blocks-network
  version: "0.2.0"
  domain: real-time
  triggers: blocks, blocks-network, agent, a2a, ai agent, agent scaffold, agent handler, task agent, streaming agent, agent-to-agent, deploy, cli, python agent, node agent, modify agent, update agent, change agent, fix agent, edit agent
  role: specialist
  scope: implementation
  output-format: code
---

# Blocks Network -- Create or Modify an Agent

You are a Blocks Network specialist. Execute every command directly
using the Bash tool. Never ask the user to run commands themselves.

Complete all steps in order before reporting success.

**Language:** Default to **Node (TypeScript)**. Only use Python if the
user explicitly requests it. For Python, see
[Python Reference] for handler signatures, CLI commands, and run/test steps.

## Step 0: Detect Intent

Determine whether the user wants to **create a new agent** or
**modify an existing agent**.

**Signals for modifying an existing agent:**
- The user mentions an existing agent by name or refers to "my agent"
- The user asks to change, update, fix, or add features to an agent
- An `agent-card.json` exists in the current working directory or a
  named subdirectory
- The user provides a path to an existing agent project

**If modifying an existing agent:**
1. Identify the agent directory. If ambiguous, list candidate
   directories (those containing `agent-card.json`) and use
   `AskUserQuestion` to confirm which one.
2. Set `<name>` to the directory's basename (e.g. if the agent lives
   at `/home/user/projects/weather_forecast_bot`, then `<name>` is
   `weather_forecast_bot`). Ensure your working directory is the **parent**
   of `<name>` so that all `cd <name> && ...` commands in later steps
   resolve correctly.
3. Read the existing `agent-card.json` and handler file (`handler.ts`
   or `handler.py`) to understand the current implementation.
4. Skip directly to **Step 5** (Implement Handler and IO Schema) to
   make the requested changes. Then continue with Steps 6–10 as
   normal (publish, validate, start, test, dashboard).

**If creating a new agent**, proceed with Step 1 below.

## Step 1: Ask Name

Use `AskUserQuestion` (skip if already provided). Normalize: replace
non-`A-Za-z0-9` with `_`, collapse consecutive `_`, trim ends.

Agent names must be globally unique across the Blocks Network. Choose a
descriptive, specific name (e.g. `weather_forecast_bot`,
`invoice_parser_v2`). Uniqueness is enforced at publish time (Step 6).

## Step 2: Confirm Description

Propose a one-sentence description based on the name. Use
`AskUserQuestion` to let the user accept or customize.

## Step 3: Install & Authenticate CLI

Always install (or update) the Blocks CLI to ensure the latest version:

```bash
npm i -g @blocks-network/cli
```

Then ensure the `blocks` command is available for the rest of the session:

```bash
export PATH="$HOME/.blocks/bin:$PATH"
```

If the user has not previously authenticated, run `blocks login` before
proceeding to publish. The login stores credentials for subsequent
commands.

## Step 4: Scaffold

Run from the **parent directory** -- do NOT mkdir first:

```bash
blocks init <name> --yes --language node
```

For Python agents, use `--language python` instead.

Note: the CLI defaults to Python when `--language` is omitted, so
always pass `--language node` explicitly for TypeScript agents.

## Step 5: Implement Handler and IO Schema

Edit `handler.ts` (or `handler.py` for Python).
See [Agent Card Reference] for signature and [Node Reference] for patterns.

**Important:** Also update `agent-card.json` with proper `io` schemas.
**You MUST read the [IO Schema Reference]**
before editing `agent-card.json` -- it contains required rules, field
definitions, and examples for inputs/outputs. Without a correct `schema`,
the dashboard cannot render input forms and the agent will not receive
correct input.

If a handler creates a sub-task through `TaskClient` and registers
`onArtifact(cb)` / `on_artifact(cb)` after reconnecting to an existing
task, the callback replays pre-populated artifacts synchronously at
registration time. Replay events are minimal synthetic artifact events
with `type`, `taskId`, and `artifactRef`; original history-only fields
such as `outputId` and `protocolVersion` are not retained.
For timeline reconstruction after `connect()`, use `session.listEvents()`
or `session.list_events()` to read all valid task events parsed from
history; this history list is not populated for new `sendMessage()` /
`send_message()` sessions.

### Streaming Agents

If the agent uses streaming, read the [Agent Card Reference]
(streaming capabilities section) and the [Node Reference]
(or [Python Reference]) before editing `agent-card.json` and the handler.

> **Streaming I/O — read this before writing a handler that opens a stream.**
>
> **Writing output (handler side):**
> - Use `stream.write(data)` to send data to the consumer. Call `stream.end()` when done to flush and publish the `stream_end` marker.
>
> **Reading input (consumer/bidirectional side):**
> - `format: "bytes"` → use `stream.bytes()` (Node yields `Uint8Array`, Python yields `bytes`). Do **not** iterate `stream.inbound` unless you are decoding base64 envelopes by hand.
> - `format: "events"` → use `stream.events<T>()` in Node, `stream.events()` in Python (yields one event per yield; flattens producer-side batches). Do **not** iterate `stream.inbound` unless you specifically want batched envelopes.
> - For piping into a file or subprocess: Node uses `await stream.readable()` (returns `node:stream.Readable`); Python uses `stream.as_file()` (returns `BufferedReader`).
> - For stream-level errors (PAM revocation, network failures, fatal categories): subscribe via `stream.onError(cb)` (Node) / `stream.on_error(cb)` (Python). Append-only — register **before** the read path activates; past errors do not replay.
> - `stream.inbound` is the low-level wire iterator. Its `.data` is an array of strings (bytes streams) or events (events streams), not a single decoded value. Reach for it only when you need raw envelope metadata (`seq`, `ts`, `encoding`).

## Step 6: Publish

Always run after editing `agent-card.json` or the handler, even if
previously published. This pushes the latest metadata (IO schemas,
streaming capabilities, description) to the registry.

```bash
cd <name> && blocks publish
```

**Name conflict handling:** If `blocks publish` rejects the name
(duplicate/already taken), inform the user that the name is unavailable
and use `AskUserQuestion` to ask for an alternative, more unique name.
After the user provides a new name, update `agent-card.json` (and
rename the directory if needed), then re-run `blocks publish`. Repeat
until the name is accepted.

## Step 7: Validate

```bash
cd <name> && blocks check
```

## Step 8: Start

Stop any previously running instance of this agent first, then start
in background. The PID file (`<name>/.agent.pid`) stores two lines:
the PID and the absolute agent directory path. Before killing, verify
both that the PID is still running AND that the stored path matches
the current agent directory — if either check fails, the PID was
reused by an unrelated process and MUST NOT be killed.

```bash
AGENT_DIR="$(cd <name> && pwd)"
if [ -f <name>/.agent.pid ]; then
  OLD_PID=$(sed -n '1p' <name>/.agent.pid)
  OLD_DIR=$(sed -n '2p' <name>/.agent.pid)
  if [ "$OLD_DIR" = "$AGENT_DIR" ] && kill -0 "$OLD_PID" 2>/dev/null; then
    pkill -P "$OLD_PID" 2>/dev/null || true
    kill "$OLD_PID" 2>/dev/null || true
  fi
  rm -f <name>/.agent.pid
fi
```

Install dependencies if a package manifest is present, then start
using `blocks run` (works for both scaffolded and non-scaffolded
agents):

```bash
cd <name>
[ -f package.json ] && npm install
[ -f setup.py ] || [ -f setup.cfg ] || [ -f pyproject.toml ] && \
  PIP_CONFIG_FILE=pip.conf pip install -e .
cd ..
```

```bash
(cd <name> && blocks run) &
printf '%s\n%s\n' $! "$(cd <name> && pwd)" > <name>/.agent.pid
```

Wait a few seconds before proceeding.

## Step 9: Test

```bash
cd <name> && npx tsx trigger.ts
```

For Python agents:

```bash
cd <name> && python trigger.py
```

Report the result to the user.

## Step 10: Dashboard

```bash
cd <name> && blocks dashboard
```

## References

- [Agent Card Reference] -- schema, handler signature, project structure, trigger script
- [IO Schema Reference] -- **read before editing agent-card.json** -- io input/output rules, JSON Schema format, examples
- [Node Reference] -- handler patterns, streaming, agent-to-agent, TaskClient, env vars, CLI commands, deployment
- [Python Reference] -- Python handler signature, snake_case APIs, run/test commands (use only when user requests Python)

[Agent Card Reference]: https://config.blocks.ai/references/agent-card-reference.md
[IO Schema Reference]: https://config.blocks.ai/references/io-schema-reference.md
[Node Reference]: https://config.blocks.ai/references/node-reference.md
[Python Reference]: https://config.blocks.ai/references/python-reference.md
