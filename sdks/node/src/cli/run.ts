#!/usr/bin/env node

/**
 * blocks-run -- Node SDK bin entry for starting an agent from agent-card.json.
 *
 * Reads `agent-card.json` from cwd, resolves the handler module (supporting
 * both .ts and .js), and calls `startAgentInstance()`.
 *
 * TypeScript handlers are loaded via tsx's scoped `tsImport()` API so that
 * no global loader registration, shebang wrapper, or build step is needed.
 */

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { startAgentInstance } from '../runtime/agent-instance.js';
import type { AgentCard } from '../runtime/agent-registry.js';
import type { HandlerFn, AgentInstanceHandle } from '../runtime/agent-instance.js';

// ---------------------------------------------------------------------------
// Agent-card loading
// ---------------------------------------------------------------------------

export interface LoadedCard {
  card: AgentCard & { runtime: NonNullable<AgentCard['runtime']> };
  raw: Record<string, unknown>;
}

/**
 * Load and validate `agent-card.json` from the given directory.
 * Throws with a human-readable message on missing file, bad JSON,
 * or missing `runtime` section.
 */
export function loadAgentCard(cwd: string): LoadedCard {
  const cardPath = resolve(cwd, 'agent-card.json');

  let raw: string;
  try {
    raw = readFileSync(cardPath, 'utf-8');
  } catch (err: unknown) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code === 'ENOENT') {
      throw new Error(`agent-card.json not found in ${cwd}`);
    }
    throw new Error(`Failed to read agent-card.json: ${(err as Error).message}`);
  }

  let parsed: Record<string, unknown>;
  try {
    parsed = JSON.parse(raw) as Record<string, unknown>;
  } catch {
    throw new Error('agent-card.json contains invalid JSON');
  }

  if (!parsed.runtime || typeof parsed.runtime !== 'object') {
    throw new Error('agent-card.json is missing the required "runtime" section');
  }

  const identity = parsed.identity as Record<string, unknown> | undefined;
  if (!identity || typeof identity !== 'object') {
    throw new Error('agent-card.json is missing the required "identity" section');
  }
  if (!identity.agentName || typeof identity.agentName !== 'string') {
    throw new Error('agent-card.json identity.agentName is required and must be a string');
  }

  return {
    card: parsed as unknown as LoadedCard['card'],
    raw: parsed,
  };
}

// ---------------------------------------------------------------------------
// Handler loading
// ---------------------------------------------------------------------------

/**
 * Resolve and load the handler module from the path specified in the
 * agent card's `runtime.handler` field. TypeScript files (.ts) are
 * loaded via `tsImport()` from tsx; JavaScript files use native import.
 *
 * Returns the handler function, checking `mod.default` first, then
 * `mod[handlerExport]`, then `mod.handler`.
 */
export async function loadHandler(
  handlerRelativePath: string,
  handlerExport: string | undefined,
  parentUrl: string,
): Promise<HandlerFn> {
  const handlerPath = resolve(handlerRelativePath);

  const handlerUrl = pathToFileURL(handlerPath).href;

  let mod: Record<string, unknown>;
  if (handlerPath.endsWith('.ts')) {
    const { tsImport } = await import('tsx/esm/api');
    mod = await tsImport(handlerUrl, parentUrl) as Record<string, unknown>;
  } else {
    mod = await import(handlerUrl) as Record<string, unknown>;
  }

  // tsImport can double-wrap ESM modules: mod.default may itself be a
  // module namespace object (with __esModule: true and its own .default).
  // Unwrap one level if that is the case.
  if (
    mod.default != null &&
    typeof mod.default === 'object' &&
    (mod.default as Record<string, unknown>).__esModule === true
  ) {
    mod = mod.default as Record<string, unknown>;
  }

  // Determine export name to look up
  const exportName = handlerExport ?? 'default';

  // Try the specified export first
  let fn = mod[exportName];

  // Fall back to `handler` if the specified export was not found
  if (typeof fn !== 'function' && exportName !== 'handler') {
    fn = mod.handler;
  }

  // Fall back to `default` if still not found (handles named export case)
  if (typeof fn !== 'function' && exportName !== 'default') {
    fn = mod.default;
  }

  if (typeof fn !== 'function') {
    throw new Error(
      `Handler module at ${handlerPath} does not export a function. ` +
      `Checked exports: "${exportName}", "handler", "default".`,
    );
  }

  return fn as HandlerFn;
}

// ---------------------------------------------------------------------------
// Main entry
// ---------------------------------------------------------------------------

/**
 * Run the agent: load card, resolve handler, start instance, register
 * signal handlers for graceful shutdown. This function is the core
 * logic, separated from the top-level execution for testability.
 */
export async function run(cwd?: string): Promise<AgentInstanceHandle> {
  const dir = cwd ?? process.cwd();

  // Load .env from the working directory before any config resolution,
  // matching the Python runner's behavior.
  const dotenv = await import('dotenv');
  dotenv.config({ path: resolve(dir, '.env') });

  const { card } = loadAgentCard(dir);
  const runtime = card.runtime;

  const handlerPath = resolve(dir, runtime.handler ?? './handler.ts');
  const handler = await loadHandler(
    handlerPath,
    runtime.handlerExport,
    pathToFileURL(resolve(dir, '__blocks_run_entrypoint__')).href,
  );

  console.log(`[blocks-run] starting "${card.identity.displayName}" (${card.identity.agentName})`);

  const instance = await startAgentInstance({
    handler,
    agentName: card.identity.agentName,
    description: card.identity.description,
    concurrency: runtime.concurrency ?? 1,
    expectedInstances: runtime.expectedInstances ?? 1,
    maxPendingBacklog: runtime.maxPendingBacklog,
    maxRunningTimeSec: runtime.maxRunningTimeSec,
    card,
  });

  console.log(`[blocks-run] instance ${instance.instanceId} running`);
  console.log('[blocks-run] press Ctrl+C to stop');

  const shutdown = () => {
    console.log('\n[blocks-run] shutting down...');
    instance.stop();
    process.exit(0);
  };

  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);

  return instance;
}

// ---------------------------------------------------------------------------
// Top-level execution (when invoked as a bin script)
// ---------------------------------------------------------------------------

const normalizedArgv1 = process.argv[1]?.replace(/\\/g, '/');
const isDirectExecution = normalizedArgv1 &&
  (
    normalizedArgv1.endsWith('/cli/run.js') ||
    normalizedArgv1.endsWith('/cli/run.ts') ||
    normalizedArgv1.endsWith('/blocks-run')
  );

if (isDirectExecution) {
  run().catch((err: Error) => {
    console.error(`[blocks-run] fatal: ${err.message}`);
    process.exit(1);
  });
}
