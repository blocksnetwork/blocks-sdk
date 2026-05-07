# Claude Code Agent — Quickstart

## 1. Install

```bash
# Install the Blocks Network SDK from local source
cd sdks/python
pip install -e .

# Ensure claude CLI is on PATH (v2.1+)
claude --version

# Go to the example directory
cd ../../examples/python/claude-code
```

## 2. Configure

```bash
cp .env.example .env
```

Edit `.env` and set your Anthropic API key:

```
ANTHROPIC_API_KEY=sk-ant-your-key-here
```

Run `blocks login` to generate your `BLOCKS_API_KEY`.

## 3. Run

```bash
blocks-run
```

The agent registers as `claude-code-python` and listens for tasks on `pubnub://agent.claude-code-python.control`.

## 4. Send a task

A task is a JSON message with `request_parts`. Minimal example:

```json
{
  "kind": "input_text",
  "text": "Write a Python function that checks if a number is prime"
}
```

## 5. What you get back

**While running:** Streaming text output in real time (the assistant typing).

**When done:** A single JSON artifact:

```json
{
  "ok": true,
  "text": "Here's a prime checker...",
  "sessionId": "abc123",
  "filesChanged": ["prime.py"],
  "toolCallCount": 3,
  "bashCommandCount": 1,
  "durationMs": 12500,
  "numTurns": 2,
  "totalCostUsd": 0.04
}
```

## 6. Multi-turn conversations

Pass `sessionId` from the previous response to continue the conversation:

```json
{
  "kind": "input_text",
  "text": "Now add unit tests for that",
  "sessionId": "abc123"
}
```

Claude Code resumes with full prior context.

## 7. Optional configuration

**Restrict tools** (via `.env`):

```
CLAUDE_ALLOWED_TOOLS=Read,Grep,Glob
CLAUDE_DISALLOWED_TOOLS=Bash
```

**Sandbox the working directory:**

```
CLAUDE_ALLOWED_PATHS=/home/user/projects,/tmp/scratch
```

**Disable bash safety hooks** (not recommended):

```
CLAUDE_BASH_SAFETY=off
```

**Per-task overrides** in `request_parts`:

```json
{
  "kind": "input_text",
  "text": "Fix the bug",
  "tools": ["Read", "Grep", "Glob"],
  "cwd": "/path/to/repo",
  "disableBashSafety": false
}
```

Note: when `CLAUDE_ALLOWED_TOOLS` is set, per-task `tools` can only narrow that list, not expand it.
