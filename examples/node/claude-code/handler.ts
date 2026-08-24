/**
 * Claude Code handler (Node.js port).
 *
 * Runs a Claude Code session for each task by invoking the `claude` CLI as a
 * subprocess with `--output-format stream-json --include-partial-messages`.
 * Token-level text deltas are streamed to the consumer in real time via the
 * Blocks streaming buffer.  A single JSON artifact is returned with the
 * complete text, session metadata, and tool-usage statistics.
 *
 * Supports multi-turn conversations via sessionId. If the consumer includes
 * a sessionId (obtained from the previous task's artifact) the handler
 * resumes that Claude Code session using the CLI's `--resume` flag.
 * On the first turn, `sessionId` is omitted and the CLI generates one;
 * it is returned in the response artifact.
 *
 * Input format (first turn):
 *   { "kind": "input_text", "text": "Fix the bug in auth.ts" }
 *
 * Input format (follow-up turn):
 *   { "kind": "input_text", "text": "Now add tests", "sessionId": "<id>" }
 *
 * Input format (with tools and cwd):
 *   {
 *     "kind": "input_text",
 *     "text": "Fix the bug",
 *     "tools": ["Read", "Grep", "Glob"],
 *     "cwd": "/path/to/repo",
 *     "disableBashSafety": false
 *   }
 *
 * Environment variables for configuration:
 *   CLAUDE_ALLOWED_TOOLS       -- comma-separated tool allowlist (overrides default).
 *                                 When set, request_parts tools are intersected with
 *                                 this list (request tools can only narrow, not expand).
 *   CLAUDE_DISALLOWED_TOOLS    -- comma-separated tool blocklist (applied after allowlist)
 *   CLAUDE_ALLOWED_PATHS       -- comma-separated allowed working directory paths
 *   CLAUDE_BASH_SAFETY         -- "on" (default) or "off" to toggle bash safety.
 *                                 When on, dangerous commands detected in the stream
 *                                 cause the subprocess to be killed.
 *   CLAUDE_BASH_BLOCKLIST      -- comma-separated additional blocked bash patterns
 *   CLAUDE_MAX_BUDGET_USD      -- max dollar amount per task (passed to CLI)
 *   CLAUDE_MODEL               -- model to use (e.g. "sonnet", "opus")
 */

import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { resolve, sep } from 'node:path';
import { existsSync, statSync } from 'node:fs';
import whichSync from 'which';
import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

// ---------------------------------------------------------------------------
// Tool configuration
// ---------------------------------------------------------------------------

/** The default safe set -- filesystem reads plus controlled writes. */
const DEFAULT_TOOLS = ['Read', 'Write', 'Edit', 'Bash', 'Glob', 'Grep'];

/** Extended set that consumers can opt into by passing tools in request_parts. */
const EXTENDED_TOOLS = [
  ...DEFAULT_TOOLS,
  'WebSearch', 'TodoRead', 'TodoWrite', 'NotebookRead', 'NotebookEdit',
];

// ---------------------------------------------------------------------------
// Bash safety configuration
// ---------------------------------------------------------------------------

/**
 * Patterns that are always blocked unless bash safety is disabled.
 * Each entry is a regex string matched against the full command string.
 */
const DEFAULT_BASH_BLOCKLIST_PATTERNS: string[] = [
  String.raw`rm\s+-[^\s]*r[^\s]*f[^\s]*\s+/\s*$`,           // rm -rf /
  String.raw`rm\s+-[^\s]*f[^\s]*r[^\s]*\s+/\s*$`,           // rm -fr /
  String.raw`rm\s+-rf\s+/(?:usr|etc|var|home|boot|sys|proc|dev)\b`, // rm -rf system dirs
  String.raw`\bsudo\b`,                                       // any sudo usage
  String.raw`\bmkfs\b`,                                       // format filesystem
  String.raw`\bdd\s+.*of=/dev/`,                              // dd to devices
  String.raw`>\s*/dev/sd[a-z]`,                               // redirect to block devices
  String.raw`\bshutdown\b`,                                   // shutdown
  String.raw`\breboot\b`,                                     // reboot
  String.raw`\binit\s+[0-6]\b`,                               // init runlevel changes
  String.raw`chmod\s+-R\s+777\s+/`,                           // recursive chmod 777 on root
  String.raw`:\(\)\s*\{\s*:\|\s*:\s*&\s*\}\s*;`,             // fork bomb
  String.raw`\bkill\s+-9\s+-1\b`,                             // kill all processes
  String.raw`\bcurl\b.*\|\s*\bbash\b`,                       // curl pipe to bash
  String.raw`\bwget\b.*\|\s*\bbash\b`,                       // wget pipe to bash
];

/** Lazy-initialized compiled blocklist cache. */
let bashBlocklistCache: RegExp[] | null = null;

function compileBashBlocklist(): RegExp[] {
  const patterns = [...DEFAULT_BASH_BLOCKLIST_PATTERNS];
  const envExtra = (process.env.CLAUDE_BASH_BLOCKLIST ?? '').trim();
  if (envExtra) {
    for (const p of envExtra.split(',')) {
      const trimmed = p.trim();
      if (trimmed) patterns.push(trimmed);
    }
  }
  const compiled: RegExp[] = [];
  for (const p of patterns) {
    try {
      compiled.push(new RegExp(p, 'i'));
    } catch (err) {
      console.warn(`Invalid bash blocklist pattern ${JSON.stringify(p)}: ${err}`);
    }
  }
  return compiled;
}

function getBashBlocklist(): RegExp[] {
  if (bashBlocklistCache === null) {
    bashBlocklistCache = compileBashBlocklist();
  }
  return bashBlocklistCache;
}

/**
 * Check a bash command against the blocklist.
 * Returns the matched pattern source if blocked, or null if allowed.
 */
function checkBashCommand(command: string): string | null {
  for (const pattern of getBashBlocklist()) {
    if (pattern.test(command)) {
      return pattern.source;
    }
  }
  return null;
}

// ---------------------------------------------------------------------------
// Tool configuration helpers
// ---------------------------------------------------------------------------

/**
 * Resolve the final tool list from request_parts and environment.
 *
 * Priority:
 * 1. Determine the base allowlist from `CLAUDE_ALLOWED_TOOLS` env var
 *    or `DEFAULT_TOOLS`.
 * 2. If `requestTools` is provided AND an env allowlist is set, the
 *    result is the intersection (request tools can only narrow the
 *    env allowlist, not expand it).  If no env allowlist is set, request
 *    tools are used as-is (for development flexibility).
 * 3. `CLAUDE_DISALLOWED_TOOLS` env var entries are removed last.
 */
function resolveTools(requestTools: string[] | null): string[] {
  // Step 1: Determine the base allowlist from environment (or default)
  const envAllowed = (process.env.CLAUDE_ALLOWED_TOOLS ?? '').trim();
  let baseTools: string[];
  if (envAllowed) {
    baseTools = envAllowed.split(',').map(t => t.trim()).filter(Boolean);
  } else {
    baseTools = [...DEFAULT_TOOLS];
  }

  // Step 2: Apply request_tools
  let tools: string[];
  if (requestTools && requestTools.length > 0) {
    if (envAllowed) {
      // Intersection: request can only narrow the env allowlist
      const baseSet = new Set(baseTools);
      tools = requestTools.filter(t => baseSet.has(t));
    } else {
      // No env allowlist -- use request tools as-is (dev flexibility)
      tools = [...requestTools];
    }
  } else {
    tools = baseTools;
  }

  // Step 3: Apply disallowed list from environment
  const envDisallowed = (process.env.CLAUDE_DISALLOWED_TOOLS ?? '').trim();
  if (envDisallowed) {
    const blocklist = new Set(
      envDisallowed.split(',').map(t => t.trim()).filter(Boolean),
    );
    tools = tools.filter(t => !blocklist.has(t));
  }

  // Step 4: Warn about unrecognized tool names
  const knownTools = new Set(EXTENDED_TOOLS);
  for (const t of tools) {
    if (!knownTools.has(t)) {
      console.warn(
        `Unrecognized tool name ${JSON.stringify(t)} -- not in DEFAULT_TOOLS or EXTENDED_TOOLS. ` +
        'It may be valid in a newer CLI version.',
      );
    }
  }

  return tools;
}

// ---------------------------------------------------------------------------
// CWD sandboxing
// ---------------------------------------------------------------------------

/**
 * Validate and resolve the working directory.
 * Returns the resolved absolute path, or null to use the process default.
 * Throws if the path is not allowed or does not exist.
 */
function resolveCwd(requestCwd: string | null): string | null {
  if (!requestCwd) return null;

  const resolved = resolve(requestCwd);

  // Check existence
  if (!existsSync(resolved) || !statSync(resolved).isDirectory()) {
    throw new Error(`Working directory does not exist: ${resolved}`);
  }

  // Check against allowed paths (if configured)
  const envAllowed = (process.env.CLAUDE_ALLOWED_PATHS ?? '').trim();
  if (envAllowed) {
    const allowed = envAllowed.split(',')
      .map(p => p.trim())
      .filter(Boolean)
      .map(p => resolve(p));
    const isWithin = allowed.some(
      a => resolved === a || resolved.startsWith(a + sep),
    );
    if (!isWithin) {
      throw new Error(
        `Working directory ${resolved} is not within allowed paths: ${JSON.stringify(allowed)}`,
      );
    }
  }

  return resolved;
}

// ---------------------------------------------------------------------------
// Error / cancel artifact helpers
// ---------------------------------------------------------------------------

function errorArtifact(
  errorType: string,
  message: string,
  details?: Record<string, unknown>,
): HandlerResult {
  const payload: Record<string, unknown> = {
    ok: false,
    errorType,
    error: message,
  };
  if (details) {
    payload.details = details;
  }
  return {
    artifacts: [{ data: JSON.stringify(payload), mimeType: 'application/json' }],
  };
}

function cancelArtifact(
  sessionId: string | null,
  filesChanged: Set<string>,
  toolCallCount: number,
  bashCommandsRun: string[],
): HandlerResult {
  const payload = {
    ok: false,
    text: 'Task was cancelled.',
    sessionId,
    filesChanged: [...filesChanged].sort(),
    toolCallCount,
    bashCommandCount: bashCommandsRun.length,
    cancelled: true,
  };
  return {
    artifacts: [{ data: JSON.stringify(payload), mimeType: 'application/json' }],
  };
}

// ---------------------------------------------------------------------------
// Input extraction
// ---------------------------------------------------------------------------

interface ExtractedInput {
  prompt: string | null;
  sessionId: string | null;
  tools: string[] | null;
  cwd: string | null;
  disableBashSafety: boolean;
  model: string | null;
}

function extractInput(parts: unknown[]): ExtractedInput {
  let prompt: string | null = null;
  let sessionId: string | null = null;
  let tools: string[] | null = null;
  let cwd: string | null = null;
  let disableBashSafety = false;
  let model: string | null = null;

  for (const part of parts) {
    if (typeof part === 'string') {
      prompt = part;
    } else if (isRecord(part)) {
      const content = parsePartContent(part);
      if (typeof content.text === 'string') {
        prompt = content.text;
      }
      if (typeof content.sessionId === 'string') {
        sessionId = content.sessionId;
      }
      if (Array.isArray(content.tools)) {
        tools = content.tools.filter((t): t is string => typeof t === 'string');
      }
      if (typeof content.cwd === 'string') {
        cwd = content.cwd;
      }
      if (content.disableBashSafety === true) {
        disableBashSafety = true;
      }
      if (typeof content.model === 'string') {
        model = content.model;
      }
    }
  }

  return { prompt, sessionId, tools, cwd, disableBashSafety, model };
}

function parsePartContent(part: Record<string, unknown>): Record<string, unknown> {
  if (typeof part.text === 'string') {
    try {
      const parsed = JSON.parse(part.text);
      if (parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // text is plain string, not JSON -- fall through
    }
  }
  return part;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

// ---------------------------------------------------------------------------
// Claude session runner
// ---------------------------------------------------------------------------

async function runClaudeSession(
  prompt: string,
  sessionId: string | null,
  ctx: TaskContext | undefined,
  tools: string[],
  cwd: string | null,
  bashSafetyEnabled: boolean,
  requestModel: string | null,
): Promise<HandlerResult> {
  // Stream only when negotiated (request streaming is consumer
  // opt-in); otherwise createStream() would throw. The code below already
  // treats stream as optional (stream?.write / stream?.end).
  const stream = ctx?.hasStream ? await ctx.createStream() : undefined;

  try {
    let currentSessionId = sessionId;
    let toolCallCount = 0;
    const filesChanged = new Set<string>();
    const bashCommandsRun: string[] = [];
    let resultData: Record<string, unknown> = {};

    // Build CLI command
    const args: string[] = [
      '-p',
      '--output-format', 'stream-json',
      '--verbose',
      '--include-partial-messages',
      '--allow-dangerously-skip-permissions',
      '--dangerously-skip-permissions',
      '--allowedTools', tools.join(','),
    ];

    if (currentSessionId) {
      args.push('--resume', currentSessionId);
    }

    const maxBudget = process.env.CLAUDE_MAX_BUDGET_USD;
    if (maxBudget) {
      args.push('--max-budget-usd', maxBudget);
    }

    const model = requestModel || process.env.CLAUDE_MODEL;
    if (model) {
      args.push('--model', model);
    }

    args.push('--', prompt);

    // Unset CLAUDECODE to allow invocation from within a Claude Code session
    const env = { ...process.env, CLAUDECODE: '' };

    const proc = spawn('claude', args, {
      cwd: cwd ?? undefined,
      env,
      stdio: ['pipe', 'pipe', 'pipe'],
    });

    // Close stdin -- prompt is passed as a CLI argument
    proc.stdin.end();

    const rl = createInterface({ input: proc.stdout! });

    for await (const rawLine of rl) {
      // Cooperative cancellation: check at each stream event
      if (ctx?.isCancelled) {
        proc.kill();
        rl.close();
        await stream?.end();
        return cancelArtifact(
          currentSessionId, filesChanged, toolCallCount, bashCommandsRun,
        );
      }

      const line = rawLine.trim();
      if (!line) continue;

      let event: Record<string, unknown>;
      try {
        event = JSON.parse(line) as Record<string, unknown>;
      } catch {
        continue;
      }

      const etype = event.type as string | undefined;

      if (etype === 'system') {
        // First-turn: capture session_id from init event
        if (!currentSessionId && typeof event.session_id === 'string') {
          currentSessionId = event.session_id;
        }
      } else if (etype === 'stream_event') {
        // Token-level streaming deltas
        const inner = (event.event ?? {}) as Record<string, unknown>;
        const innerType = inner.type as string | undefined;
        if (innerType === 'content_block_delta') {
          const delta = (inner.delta ?? {}) as Record<string, unknown>;
          const deltaType = delta.type as string | undefined;
          if (deltaType === 'text_delta') {
            const text = delta.text as string | undefined;
            if (text && stream) {
              stream.write(text);
            }
          }
          // Skip thinking_delta, input_json_delta
        }
      } else if (etype === 'assistant') {
        // Complete message -- extract tool metadata
        const message = (event.message ?? {}) as Record<string, unknown>;
        const content = (message.content ?? []) as Record<string, unknown>[];
        for (const block of content) {
          const btype = block.type as string | undefined;
          if (btype === 'tool_use') {
            toolCallCount++;
            const name = (block.name ?? '') as string;
            const inp = (block.input ?? {}) as Record<string, unknown>;
            if (name === 'Write' || name === 'Edit') {
              const fp = inp.file_path as string | undefined;
              if (fp) {
                filesChanged.add(fp);
              }
            }
            if (name === 'Bash') {
              const cmdStr = (inp.command ?? '') as string;
              if (cmdStr) {
                bashCommandsRun.push(cmdStr);
                // Stream-based bash safety: kill subprocess if dangerous
                if (bashSafetyEnabled) {
                  const matched = checkBashCommand(cmdStr);
                  if (matched) {
                    console.warn(
                      `BLOCKED bash command: ${cmdStr.slice(0, 200)} (pattern: ${matched})`,
                    );
                    proc.kill();
                    rl.close();
                    await stream?.end();
                    return errorArtifact(
                      'BashSafetyViolation',
                      `Blocked dangerous bash command matching pattern ${JSON.stringify(matched)}`,
                      { command: cmdStr.slice(0, 200), sessionId: currentSessionId },
                    );
                  }
                }
              }
            }
          }
        }
      } else if (etype === 'result') {
        resultData = event;
        if (typeof event.session_id === 'string') {
          currentSessionId = event.session_id;
        }
      }
    }

    // Wait for process to exit
    await new Promise<void>((res) => {
      proc.on('close', () => res());
      // If process already exited, resolve immediately
      if (proc.exitCode !== null) res();
    });

    // Final cancellation check after process exits
    if (ctx?.isCancelled) {
      await stream?.end();
      return cancelArtifact(
        currentSessionId, filesChanged, toolCallCount, bashCommandsRun,
      );
    }

    // Build response from the result event
    const fullText = (resultData.result ?? '') as string;
    const isError = (resultData.is_error ?? false) as boolean;

    const responsePayload: Record<string, unknown> = {
      ok: !isError,
      text: fullText,
      sessionId: currentSessionId,
      filesChanged: [...filesChanged].sort(),
      toolCallCount,
      bashCommandCount: bashCommandsRun.length,
    };

    // Include optional metadata fields only when available
    if (resultData.duration_ms != null) {
      responsePayload.durationMs = resultData.duration_ms;
    }
    if (resultData.num_turns != null) {
      responsePayload.numTurns = resultData.num_turns;
    }
    if (resultData.total_cost_usd != null) {
      responsePayload.totalCostUsd = resultData.total_cost_usd;
    }

    await stream?.end();
    return {
      artifacts: [{ data: JSON.stringify(responsePayload), mimeType: 'application/json' }],
    };
  } finally {
    // Safety net: always close the stream, even if the subprocess fails.
    // stream.end() is idempotent -- calling it after it's already ended is safe.
    if (stream) {
      try {
        await stream.end();
      } catch {
        console.warn('Failed to close stream');
      }
    }
  }
}

// ---------------------------------------------------------------------------
// Main handler
// ---------------------------------------------------------------------------

/**
 * Handle an incoming task by running Claude Code and streaming output.
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  // Check for API key early
  if (!process.env.ANTHROPIC_API_KEY) {
    return errorArtifact(
      'ConfigurationError',
      'ANTHROPIC_API_KEY environment variable is not set. ' +
      'Set it to your Anthropic API key from https://console.anthropic.com/',
    );
  }

  // Check for claude CLI on PATH
  try {
    whichSync.sync('claude');
  } catch {
    return errorArtifact(
      'ConfigurationError',
      'claude CLI not found on PATH. ' +
      'Install it with: npm install -g @anthropic/claude-code',
    );
  }

  const { prompt, sessionId, tools: requestTools, cwd: requestCwd, disableBashSafety, model: requestModel } =
    extractInput(task.requestParts ?? []);

  if (!prompt) {
    return errorArtifact(
      'InputError',
      'Missing text input',
      { example: { kind: 'input_text', text: 'Write a hello world in Python' } },
    );
  }

  // Resolve tools
  const tools = resolveTools(requestTools);

  // Resolve and validate cwd
  let cwd: string | null;
  try {
    cwd = resolveCwd(requestCwd);
  } catch (err) {
    return errorArtifact('SandboxError', (err as Error).message);
  }

  // Determine bash safety setting
  const envBashSafety = (process.env.CLAUDE_BASH_SAFETY ?? 'on').trim().toLowerCase();
  const bashSafetyEnabled = envBashSafety !== 'off' && !disableBashSafety;

  try {
    return await runClaudeSession(
      prompt, sessionId, ctx, tools, cwd, bashSafetyEnabled, requestModel,
    );
  } catch (err) {
    console.error(`Claude Code session failed: session_id=${sessionId}`, err);
    return errorArtifact(
      (err as Error).constructor?.name ?? 'Error',
      (err as Error).message ?? String(err),
      { sessionId },
    );
  }
}
