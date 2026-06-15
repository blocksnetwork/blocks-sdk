"""
Claude Code handler.

Runs a Claude Code session for each task by invoking the ``claude`` CLI as a
subprocess with ``--output-format stream-json --include-partial-messages``.
Token-level text deltas are streamed to the consumer in real time via the
Blocks streaming buffer.  A single JSON artifact is returned with the
complete text, session metadata, and tool-usage statistics.

Supports multi-turn conversations via sessionId. If the consumer includes
a sessionId (obtained from the previous task's artifact) the handler
resumes that Claude Code session using the CLI's ``--resume`` flag.
On the first turn, ``sessionId`` is omitted and the CLI generates one;
it is returned in the response artifact.

Input format (first turn):
  { "kind": "input_text", "text": "Fix the bug in auth.ts" }

Input format (follow-up turn):
  { "kind": "input_text", "text": "Now add tests for that fix", "sessionId": "<id>" }

Input format (with tools and cwd):
  {
    "kind": "input_text",
    "text": "Fix the bug",
    "tools": ["Read", "Grep", "Glob"],
    "cwd": "/path/to/repo",
    "disableBashSafety": false
  }

Environment variables for configuration:
  CLAUDE_ALLOWED_TOOLS       -- comma-separated tool allowlist (overrides default).
                                When set, request_parts tools are intersected with
                                this list (request tools can only narrow, not expand).
  CLAUDE_DISALLOWED_TOOLS    -- comma-separated tool blocklist (applied after allowlist)
  CLAUDE_ALLOWED_PATHS       -- comma-separated allowed working directory paths
  CLAUDE_BASH_SAFETY         -- "on" (default) or "off" to toggle bash safety.
                                When on, dangerous commands detected in the stream
                                cause the subprocess to be killed.
  CLAUDE_BASH_BLOCKLIST      -- comma-separated additional blocked bash patterns
  CLAUDE_MAX_BUDGET_USD      -- max dollar amount per task (passed to CLI)
  CLAUDE_MODEL               -- model to use (e.g. "sonnet", "opus")
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import re
import shutil
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

from blocks_network.types import StartTaskMessage, TaskContext

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Tool configuration
# ---------------------------------------------------------------------------

# The default safe set -- filesystem reads plus controlled writes
DEFAULT_TOOLS = ["Read", "Write", "Edit", "Bash", "Glob", "Grep"]

# Extended set that consumers can opt into by passing tools in request_parts
EXTENDED_TOOLS = DEFAULT_TOOLS + [
    "WebSearch", "TodoRead", "TodoWrite", "NotebookRead", "NotebookEdit",
]

# ---------------------------------------------------------------------------
# Bash safety configuration
# ---------------------------------------------------------------------------

# Patterns that are always blocked unless bash safety is disabled.
# Each entry is a compiled regex matched against the full command string.
_DEFAULT_BASH_BLOCKLIST_PATTERNS = [
    r"rm\s+-[^\s]*r[^\s]*f[^\s]*\s+/\s*$",  # rm -rf /
    r"rm\s+-[^\s]*f[^\s]*r[^\s]*\s+/\s*$",  # rm -fr /
    r"rm\s+-rf\s+/(?:usr|etc|var|home|boot|sys|proc|dev)\b",  # rm -rf system dirs
    r"\bsudo\b",                               # any sudo usage
    r"\bmkfs\b",                               # format filesystem
    r"\bdd\s+.*of=/dev/",                      # dd to devices
    r">\s*/dev/sd[a-z]",                       # redirect to block devices
    r"\bshutdown\b",                           # shutdown
    r"\breboot\b",                             # reboot
    r"\binit\s+[0-6]\b",                       # init runlevel changes
    r"chmod\s+-R\s+777\s+/",                   # recursive chmod 777 on root
    r":\(\)\s*\{\s*:\|\s*:\s*&\s*\}\s*;",     # fork bomb
    r"\bkill\s+-9\s+-1\b",                     # kill all processes
    r"\bcurl\b.*\|\s*\bbash\b",               # curl pipe to bash
    r"\bwget\b.*\|\s*\bbash\b",               # wget pipe to bash
]


def _compile_bash_blocklist() -> List[re.Pattern]:
    """Compile the default blocklist plus any from the environment."""
    patterns = list(_DEFAULT_BASH_BLOCKLIST_PATTERNS)
    env_extra = os.environ.get("CLAUDE_BASH_BLOCKLIST", "").strip()
    if env_extra:
        patterns.extend(p.strip() for p in env_extra.split(",") if p.strip())
    compiled = []
    for p in patterns:
        try:
            compiled.append(re.compile(p, re.IGNORECASE))
        except re.error as exc:
            logger.warning("Invalid bash blocklist pattern %r: %s", p, exc)
    return compiled


_bash_blocklist: Optional[List[re.Pattern]] = None


def _get_bash_blocklist() -> List[re.Pattern]:
    """Lazy-init and cache the compiled blocklist."""
    global _bash_blocklist
    if _bash_blocklist is None:
        _bash_blocklist = _compile_bash_blocklist()
    return _bash_blocklist


def _check_bash_command(command: str) -> Optional[str]:
    """Check a bash command against the blocklist.

    Returns the matched pattern string if blocked, or None if allowed.
    """
    for pattern in _get_bash_blocklist():
        if pattern.search(command):
            return pattern.pattern
    return None


# ---------------------------------------------------------------------------
# Tool configuration helpers
# ---------------------------------------------------------------------------


def _resolve_tools(
    request_tools: Optional[List[str]],
) -> List[str]:
    """Resolve the final tool list from request_parts and environment.

    Priority:
    1. Determine the base allowlist from ``CLAUDE_ALLOWED_TOOLS`` env var
       or ``DEFAULT_TOOLS``.
    2. If ``request_tools`` is provided AND an env allowlist is set, the
       result is the **intersection** (request tools can only narrow the
       env allowlist, not expand it).  If no env allowlist is set, request
       tools are used as-is (for development flexibility).
    3. ``CLAUDE_DISALLOWED_TOOLS`` env var entries are removed last.

    A warning is logged for any tool name not in ``DEFAULT_TOOLS + EXTENDED_TOOLS``.
    """
    # Step 1: Determine the base allowlist from environment (or default)
    env_allowed = os.environ.get("CLAUDE_ALLOWED_TOOLS", "").strip()
    if env_allowed:
        base_tools = [t.strip() for t in env_allowed.split(",") if t.strip()]
    else:
        base_tools = list(DEFAULT_TOOLS)

    # Step 2: Apply request_tools
    if request_tools:
        if env_allowed:
            # Intersection: request can only narrow the env allowlist
            base_set = set(base_tools)
            tools = [t for t in request_tools if t in base_set]
        else:
            # No env allowlist -- use request tools as-is (dev flexibility)
            tools = list(request_tools)
    else:
        tools = base_tools

    # Step 3: Apply disallowed list from environment
    env_disallowed = os.environ.get("CLAUDE_DISALLOWED_TOOLS", "").strip()
    if env_disallowed:
        blocklist = {t.strip() for t in env_disallowed.split(",") if t.strip()}
        tools = [t for t in tools if t not in blocklist]

    # Step 4: Warn about unrecognized tool names
    known_tools = set(EXTENDED_TOOLS)  # EXTENDED_TOOLS is a superset of DEFAULT_TOOLS
    for t in tools:
        if t not in known_tools:
            logger.warning(
                "Unrecognized tool name %r -- not in DEFAULT_TOOLS or EXTENDED_TOOLS. "
                "It may be valid in a newer CLI version.",
                t,
            )

    return tools


# ---------------------------------------------------------------------------
# CWD sandboxing
# ---------------------------------------------------------------------------


def _resolve_cwd(request_cwd: Optional[str]) -> Optional[str]:
    """Validate and resolve the working directory.

    Returns the resolved absolute path, or ``None`` to use the process default.
    Raises ``ValueError`` if the path is not allowed or does not exist.
    """
    if not request_cwd:
        return None

    resolved = str(Path(request_cwd).resolve())

    # Check existence
    if not Path(resolved).is_dir():
        raise ValueError(
            f"Working directory does not exist: {resolved}"
        )

    # Check against allowed paths (if configured)
    env_allowed = os.environ.get("CLAUDE_ALLOWED_PATHS", "").strip()
    if env_allowed:
        allowed = [str(Path(p.strip()).resolve()) for p in env_allowed.split(",") if p.strip()]
        if not any(resolved == a or resolved.startswith(a + os.sep) for a in allowed):
            raise ValueError(
                f"Working directory {resolved} is not within allowed paths: {allowed}"
            )

    return resolved


# ---------------------------------------------------------------------------
# Error artifact helpers
# ---------------------------------------------------------------------------


def _error_artifact(error_type: str, message: str, details: Optional[Dict] = None) -> Dict[str, Any]:
    """Build a structured error artifact."""
    payload: Dict[str, Any] = {
        "ok": False,
        "errorType": error_type,
        "error": message,
    }
    if details:
        payload["details"] = details
    return {
        "artifacts": [{"data": json.dumps(payload), "mimeType": "application/json"}],
    }


def _cancel_artifact(
    session_id: Optional[str],
    files_changed: set,
    tool_call_count: int,
    bash_commands_run: List[str],
) -> Dict[str, Any]:
    """Build a cancel artifact with whatever metadata we have so far."""
    payload: Dict[str, Any] = {
        "ok": False,
        "text": "Task was cancelled.",
        "sessionId": session_id,
        "filesChanged": sorted(files_changed),
        "toolCallCount": tool_call_count,
        "bashCommandCount": len(bash_commands_run),
        "cancelled": True,
    }
    return {
        "artifacts": [{"data": json.dumps(payload), "mimeType": "application/json"}],
    }


# ---------------------------------------------------------------------------
# Input extraction
# ---------------------------------------------------------------------------


def _extract_input(
    parts: List[Any],
) -> Tuple[Optional[str], Optional[str], Optional[List[str]], Optional[str], bool, Optional[str]]:
    """Extract prompt text, sessionId, tools, cwd, disableBashSafety, and model from request parts.

    Returns:
        (prompt, session_id, tools, cwd, disable_bash_safety, model)
    """
    prompt: Optional[str] = None
    session_id: Optional[str] = None
    tools: Optional[List[str]] = None
    cwd: Optional[str] = None
    disable_bash_safety = False
    model: Optional[str] = None

    for part in parts:
        if isinstance(part, str):
            prompt = part
        else:
            content = _parse_part_content(part)
            if isinstance(content.get("text"), str):
                prompt = content["text"]
            if isinstance(content.get("sessionId"), str):
                session_id = content["sessionId"]
            if isinstance(content.get("tools"), list):
                tools = [str(t) for t in content["tools"] if isinstance(t, str)]
            if isinstance(content.get("cwd"), str):
                cwd = content["cwd"]
            if content.get("disableBashSafety") is True:
                disable_bash_safety = True
            if isinstance(content.get("model"), str):
                model = content["model"]

    return prompt, session_id, tools, cwd, disable_bash_safety, model


def _parse_part_content(part: Any) -> Dict[str, Any]:
    """Extract structured content from a RequestPart or raw dict.

    The frontend serializes structured content into the ``text`` field as
    JSON.  This helper parses it back into a dict.
    """
    text = getattr(part, "text", None) if not isinstance(part, dict) else part.get("text")
    if isinstance(text, str):
        try:
            parsed = json.loads(text)
            if isinstance(parsed, dict):
                return parsed
        except (json.JSONDecodeError, ValueError):
            pass
        return {"text": text}
    if hasattr(part, "extra") and isinstance(part.extra, dict):
        return part.extra
    if isinstance(part, dict):
        return part
    return {}


# ---------------------------------------------------------------------------
# Main handler
# ---------------------------------------------------------------------------


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    """Handle an incoming task by running Claude Code and streaming output."""

    # Check for API key early
    if not os.environ.get("ANTHROPIC_API_KEY"):
        return _error_artifact(
            "ConfigurationError",
            "ANTHROPIC_API_KEY environment variable is not set. "
            "Set it to your Anthropic API key from https://console.anthropic.com/",
        )

    # Check for claude CLI on PATH
    if not shutil.which("claude"):
        return _error_artifact(
            "ConfigurationError",
            "claude CLI not found on PATH. "
            "Install it with: npm install -g @anthropic/claude-code",
        )

    prompt, session_id, request_tools, request_cwd, disable_bash_safety, request_model = _extract_input(
        task.request_parts or []
    )

    if not prompt:
        return _error_artifact(
            "InputError",
            "Missing text input",
            {"example": {"kind": "input_text", "text": "Write a hello world in Python"}},
        )

    # Resolve tools
    tools = _resolve_tools(request_tools)

    # Resolve and validate cwd
    try:
        cwd = _resolve_cwd(request_cwd)
    except ValueError as exc:
        return _error_artifact("SandboxError", str(exc))

    # Determine bash safety setting
    env_bash_safety = os.environ.get("CLAUDE_BASH_SAFETY", "on").strip().lower()
    bash_safety_enabled = (env_bash_safety != "off") and (not disable_bash_safety)

    try:
        result = asyncio.run(
            _run_claude_session(prompt, session_id, ctx, tools, cwd, bash_safety_enabled, request_model)
        )
        return result
    except asyncio.CancelledError:
        logger.warning("Task cancelled: session_id=%s", session_id)
        return _error_artifact(
            "CancelledError",
            "Task was cancelled",
            {"sessionId": session_id},
        )
    except KeyboardInterrupt:
        logger.warning("Task interrupted: session_id=%s", session_id)
        return _error_artifact(
            "InterruptedError",
            "Task was interrupted",
            {"sessionId": session_id},
        )
    except Exception as exc:
        logger.exception("Claude Code session failed: session_id=%s", session_id)
        return _error_artifact(
            type(exc).__name__,
            str(exc),
            {"sessionId": session_id},
        )


def _is_cancelled(ctx: Optional[TaskContext]) -> bool:
    """Check if the task has been cancelled via cooperative cancellation."""
    return ctx is not None and ctx.is_cancelled


async def _run_claude_session(
    prompt: str,
    session_id: Optional[str],
    ctx: Optional[TaskContext],
    tools: List[str],
    cwd: Optional[str],
    bash_safety_enabled: bool,
    request_model: Optional[str] = None,
) -> Dict[str, Any]:
    """Run Claude Code via CLI subprocess and stream output to Blocks.

    Invokes ``claude -p --output-format stream-json --include-partial-messages``
    and reads newline-delimited JSON events from stdout.  Text deltas
    (``content_block_delta`` with ``text_delta``) are written to the Blocks
    stream for token-level progressive output.

    Args:
        prompt: The user prompt text.
        session_id: Session ID from a previous turn (for resumption), or None.
        ctx: Blocks task context for streaming.
        tools: Resolved list of allowed tool names.
        cwd: Working directory, or None for process default.
        bash_safety_enabled: Whether to monitor for dangerous bash commands.
        request_model: Model from request_parts (overrides CLAUDE_MODEL env var).
    """
    # Stream only when negotiated (request streaming is consumer opt-in,
    # BLOCKS-181); otherwise create_stream() would raise. The code below
    # already treats stream as optional.
    stream = ctx.create_stream() if ctx and ctx.has_stream else None

    try:
        tool_call_count = 0
        files_changed: set[str] = set()
        bash_commands_run: List[str] = []
        result_data: Dict[str, Any] = {}

        # Build CLI command
        cmd: List[str] = [
            "claude", "-p",
            "--output-format", "stream-json",
            "--verbose",
            "--include-partial-messages",
            "--allow-dangerously-skip-permissions",
            "--dangerously-skip-permissions",
            "--allowedTools", ",".join(tools),
        ]

        if session_id:
            cmd.extend(["--resume", session_id])

        max_budget = os.environ.get("CLAUDE_MAX_BUDGET_USD")
        if max_budget:
            cmd.extend(["--max-budget-usd", max_budget])

        model = request_model or os.environ.get("CLAUDE_MODEL")
        if model:
            cmd.extend(["--model", model])

        cmd.extend(["--", prompt])

        # Unset CLAUDECODE to allow invocation from within a Claude Code session
        env = {**os.environ, "CLAUDECODE": ""}

        # Claude CLI can emit very large JSON lines (e.g. assistant messages
        # with tool results or long code). The default asyncio StreamReader
        # limit is 64 KB which is too small. Use 16 MB.
        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            limit=16 * 1024 * 1024,
            cwd=cwd,
            env=env,
        )

        async for raw_line in proc.stdout:
            # Cooperative cancellation: check at each stream event
            if _is_cancelled(ctx):
                logger.info("Task cancelled, killing subprocess: session_id=%s", session_id)
                proc.kill()
                await proc.wait()
                return _cancel_artifact(
                    session_id, files_changed, tool_call_count, bash_commands_run,
                )

            line = raw_line.strip()
            if not line:
                continue
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue

            etype = event.get("type")

            if etype == "system":
                # First-turn: capture session_id from init event
                if not session_id:
                    session_id = event.get("session_id")

            elif etype == "stream_event":
                # Token-level streaming deltas
                inner = event.get("event", {})
                inner_type = inner.get("type")
                if inner_type == "content_block_delta":
                    delta = inner.get("delta", {})
                    delta_type = delta.get("type")
                    if delta_type == "text_delta":
                        text = delta.get("text", "")
                        if text and stream:
                            stream.write(text)
                    # Skip thinking_delta, input_json_delta

            elif etype == "assistant":
                # Complete message — extract tool metadata
                content = (event.get("message") or {}).get("content") or []
                for block in content:
                    btype = block.get("type")
                    if btype == "tool_use":
                        tool_call_count += 1
                        name = block.get("name", "")
                        inp = block.get("input") or {}
                        if name in ("Write", "Edit"):
                            fp = inp.get("file_path")
                            if fp:
                                files_changed.add(fp)
                        if name == "Bash":
                            cmd_str = inp.get("command", "")
                            if cmd_str:
                                bash_commands_run.append(cmd_str)
                                logger.debug("Bash tool call #%d: %s", len(bash_commands_run), cmd_str[:200])
                                # Stream-based bash safety: kill subprocess if dangerous
                                if bash_safety_enabled:
                                    matched = _check_bash_command(cmd_str)
                                    if matched:
                                        logger.warning("BLOCKED bash command: %s (pattern: %s)", cmd_str[:200], matched)
                                        proc.kill()
                                        return _error_artifact(
                                            "BashSafetyViolation",
                                            f"Blocked dangerous bash command matching pattern {matched!r}",
                                            {"command": cmd_str[:200], "sessionId": session_id},
                                        )

            elif etype == "result":
                result_data = event
                session_id = event.get("session_id", session_id)

        await proc.wait()

        # Final cancellation check after process exits
        if _is_cancelled(ctx):
            return _cancel_artifact(
                session_id, files_changed, tool_call_count, bash_commands_run,
            )

        # Build response from the result event
        full_text = result_data.get("result", "")
        is_error = result_data.get("is_error", False)

        response_payload: Dict[str, Any] = {
            "ok": not is_error,
            "text": full_text,
            "sessionId": session_id,
            "filesChanged": sorted(files_changed),
            "toolCallCount": tool_call_count,
            "bashCommandCount": len(bash_commands_run),
        }
        # Include optional metadata fields only when available
        if result_data.get("duration_ms") is not None:
            response_payload["durationMs"] = result_data["duration_ms"]
        if result_data.get("num_turns") is not None:
            response_payload["numTurns"] = result_data["num_turns"]
        if result_data.get("total_cost_usd") is not None:
            response_payload["totalCostUsd"] = result_data["total_cost_usd"]

        return {
            "artifacts": [{"data": json.dumps(response_payload), "mimeType": "application/json"}],
        }

    finally:
        # Always close the stream, even if the subprocess fails
        if stream:
            try:
                stream.end()
            except Exception:
                logger.warning("Failed to close stream", exc_info=True)
