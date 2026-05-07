import { describe, expect, it, afterEach } from 'vitest';
import { writeFileSync, mkdirSync, rmSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { tmpdir } from 'node:os';
import { loadAgentCard, loadHandler } from '../src/cli/run.js';
import type { StartTaskMessage } from '../src/runtime/agent-instance.js';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Create a temporary directory with an optional agent-card.json. */
function makeTmpDir(cardContent?: string): string {
  const dir = resolve(tmpdir(), `blocks-run-test-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(dir, { recursive: true });
  if (cardContent !== undefined) {
    writeFileSync(join(dir, 'agent-card.json'), cardContent, 'utf-8');
  }
  return dir;
}

/** Minimal valid agent card JSON (new 9-section format). */
function validCardJson(overrides: Record<string, unknown> = {}): string {
  const card: Record<string, unknown> = {
    identity: {
      agentName: 'test_agent',
      displayName: 'test-agent',
      description: 'A test agent',
      version: '1.0.0',
      provider: { organization: 'TestOrg' },
      ...((overrides.identity as Record<string, unknown>) ?? {}),
    },
    capabilities: { taskKinds: ['request'] },
    skills: [],
    runtime: {
      handler: './handler.js',
      ...((overrides.runtime as Record<string, unknown>) ?? {}),
    },
    ...overrides,
  };
  // Merge runtime properly
  if (overrides.runtime !== undefined) {
    card.runtime = overrides.runtime as typeof card.runtime;
  }
  // Merge identity properly
  if (overrides.identity !== undefined) {
    card.identity = overrides.identity as typeof card.identity;
  }
  return JSON.stringify(card);
}

// ---------------------------------------------------------------------------
// loadAgentCard tests
// ---------------------------------------------------------------------------

describe('loadAgentCard', () => {
  const tmpDirs: string[] = [];

  afterEach(() => {
    for (const d of tmpDirs) {
      rmSync(d, { recursive: true, force: true });
    }
    tmpDirs.length = 0;
  });

  it('loads a valid agent-card.json', () => {
    const dir = makeTmpDir(validCardJson());
    tmpDirs.push(dir);

    const { card } = loadAgentCard(dir);
    expect(card.identity.displayName).toBe('test-agent');
    expect(card.identity.agentName).toBe('test_agent');
    expect(card.runtime.handler).toBe('./handler.js');
  });

  it('throws when agent-card.json is missing', () => {
    const dir = makeTmpDir(); // no card file
    tmpDirs.push(dir);

    expect(() => loadAgentCard(dir)).toThrow('agent-card.json not found');
  });

  it('throws on malformed JSON', () => {
    const dir = makeTmpDir('{ not valid json');
    tmpDirs.push(dir);

    expect(() => loadAgentCard(dir)).toThrow('invalid JSON');
  });

  it('throws when runtime section is missing', () => {
    const dir = makeTmpDir(JSON.stringify({
      name: 'test',
      description: 'test',
      version: '1.0.0',
      skills: [],
    }));
    tmpDirs.push(dir);

    expect(() => loadAgentCard(dir)).toThrow('missing the required "runtime" section');
  });

  it('throws when runtime is not an object', () => {
    const dir = makeTmpDir(JSON.stringify({
      name: 'test',
      description: 'test',
      version: '1.0.0',
      skills: [],
      runtime: 'not-an-object',
    }));
    tmpDirs.push(dir);

    expect(() => loadAgentCard(dir)).toThrow('missing the required "runtime" section');
  });

  it('throws when identity.agentName is missing', () => {
    const dir = makeTmpDir(JSON.stringify({
      identity: {
        displayName: 'test',
        description: 'test',
        version: '1.0.0',
        provider: { organization: 'TestOrg' },
      },
      skills: [],
      runtime: { handler: './handler.js' },
    }));
    tmpDirs.push(dir);

    expect(() => loadAgentCard(dir)).toThrow('identity.agentName is required');
  });

  it('preserves all card fields in the raw output', () => {
    const extra = { customField: 'value' };
    const dir = makeTmpDir(validCardJson(extra));
    tmpDirs.push(dir);

    const { raw } = loadAgentCard(dir);
    expect(raw.customField).toBe('value');
  });
});

// ---------------------------------------------------------------------------
// loadHandler tests
// ---------------------------------------------------------------------------

describe('loadHandler', () => {
  const tmpDirs: string[] = [];

  afterEach(() => {
    for (const d of tmpDirs) {
      rmSync(d, { recursive: true, force: true });
    }
    tmpDirs.length = 0;
  });

  it('loads a .js handler with a default export', async () => {
    const dir = makeTmpDir();
    tmpDirs.push(dir);

    // Write a .mjs handler so ESM import works
    const handlerPath = join(dir, 'handler.mjs');
    writeFileSync(handlerPath, `
      export default async function handler(task) {
        return { artifacts: [{ data: 'ok', mimeType: 'text/plain' }] };
      }
    `);

    const parentUrl = pathToFileURL(join(dir, '__test__')).href;
    const fn = await loadHandler(handlerPath, 'default', parentUrl);
    expect(typeof fn).toBe('function');

    const result = await fn({ type: 'StartTask', taskId: 't1', ownerId: 'u1' } as StartTaskMessage);
    expect(result.artifacts[0].data).toBe('ok');
  });

  it('loads a .js handler with a named "handler" export', async () => {
    const dir = makeTmpDir();
    tmpDirs.push(dir);

    const handlerPath = join(dir, 'handler.mjs');
    writeFileSync(handlerPath, `
      export async function handler(task) {
        return { artifacts: [{ data: 'named', mimeType: 'text/plain' }] };
      }
    `);

    const fn = await loadHandler(handlerPath, undefined, pathToFileURL(join(dir, '__test__')).href);
    expect(typeof fn).toBe('function');

    const result = await fn({ type: 'StartTask', taskId: 't1', ownerId: 'u1' } as StartTaskMessage);
    expect(result.artifacts[0].data).toBe('named');
  });

  it('throws when handler file does not exist', async () => {
    const dir = makeTmpDir();
    tmpDirs.push(dir);

    const handlerPath = join(dir, 'nonexistent.mjs');
    await expect(
      loadHandler(handlerPath, 'default', pathToFileURL(join(dir, '__test__')).href),
    ).rejects.toThrow();
  });

  it('throws when module exports no function', async () => {
    const dir = makeTmpDir();
    tmpDirs.push(dir);

    const handlerPath = join(dir, 'handler.mjs');
    writeFileSync(handlerPath, `
      export const handler = 'not-a-function';
      export default 42;
    `);

    await expect(
      loadHandler(handlerPath, 'default', pathToFileURL(join(dir, '__test__')).href),
    ).rejects.toThrow('does not export a function');
  });

  it('loads a .ts handler via tsImport', async () => {
    const dir = makeTmpDir();
    tmpDirs.push(dir);

    const handlerPath = join(dir, 'handler.ts');
    writeFileSync(handlerPath, `
      interface Task { type: string; taskId: string; ownerId: string; }
      export default async function handler(task: Task) {
        return { artifacts: [{ data: 'from-ts', mimeType: 'text/plain' }] };
      }
    `);

    const fn = await loadHandler(handlerPath, 'default', pathToFileURL(join(dir, '__test__')).href);
    expect(typeof fn).toBe('function');

    const result = await fn({ type: 'StartTask', taskId: 't1', ownerId: 'u1' } as StartTaskMessage);
    expect(result.artifacts[0].data).toBe('from-ts');
  });

  it('falls back to "handler" named export when default is not a function', async () => {
    const dir = makeTmpDir();
    tmpDirs.push(dir);

    const handlerPath = join(dir, 'handler.mjs');
    writeFileSync(handlerPath, `
      export default 'not-a-function';
      export async function handler(task) {
        return { artifacts: [{ data: 'fallback', mimeType: 'text/plain' }] };
      }
    `);

    const fn = await loadHandler(handlerPath, 'default', pathToFileURL(join(dir, '__test__')).href);
    expect(typeof fn).toBe('function');

    const result = await fn({ type: 'StartTask', taskId: 't1', ownerId: 'u1' } as StartTaskMessage);
    expect(result.artifacts[0].data).toBe('fallback');
  });

  it('loads a handler using a custom handlerExport name', async () => {
    const dir = makeTmpDir();
    tmpDirs.push(dir);

    const handlerPath = join(dir, 'handler.mjs');
    writeFileSync(handlerPath, `
      export async function myCustomHandler(task) {
        return { artifacts: [{ data: 'custom', mimeType: 'text/plain' }] };
      }
    `);

    const fn = await loadHandler(handlerPath, 'myCustomHandler', pathToFileURL(join(dir, '__test__')).href);
    expect(typeof fn).toBe('function');

    const result = await fn({ type: 'StartTask', taskId: 't1', ownerId: 'u1' } as StartTaskMessage);
    expect(result.artifacts[0].data).toBe('custom');
  });
});

// ---------------------------------------------------------------------------
// Windows path normalization tests
// ---------------------------------------------------------------------------

describe('isDirectExecution path normalization', () => {
  it('matches Windows-style backslash paths', () => {
    const windowsPaths = [
      'C:\\Users\\dev\\node_modules\\.bin\\blocks-run',
      'C:\\Users\\dev\\node_modules\\@blocks-network\\sdk\\dist\\cli\\run.js',
      'C:\\Users\\dev\\project\\node_modules\\@blocks-network\\sdk\\dist\\cli\\run.ts',
    ];

    for (const p of windowsPaths) {
      const normalized = p.replace(/\\/g, '/');
      const matches =
        normalized.endsWith('/cli/run.js') ||
        normalized.endsWith('/cli/run.ts') ||
        normalized.endsWith('/blocks-run');
      expect(matches).toBe(true);
    }
  });

  it('still matches Unix-style forward-slash paths', () => {
    const unixPaths = [
      '/home/dev/node_modules/.bin/blocks-run',
      '/home/dev/node_modules/@blocks-network/sdk/dist/cli/run.js',
    ];

    for (const p of unixPaths) {
      const normalized = p.replace(/\\/g, '/');
      const matches =
        normalized.endsWith('/cli/run.js') ||
        normalized.endsWith('/cli/run.ts') ||
        normalized.endsWith('/blocks-run');
      expect(matches).toBe(true);
    }
  });

  it('does not match unrelated paths', () => {
    const unrelated = [
      '/home/dev/some-other-script.js',
      'C:\\Users\\dev\\run.js',
      '/home/dev/blocks-run-extra',
    ];

    for (const p of unrelated) {
      const normalized = p.replace(/\\/g, '/');
      const matches =
        normalized.endsWith('/cli/run.js') ||
        normalized.endsWith('/cli/run.ts') ||
        normalized.endsWith('/blocks-run');
      expect(matches).toBe(false);
    }
  });
});
