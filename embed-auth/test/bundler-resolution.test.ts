/**
 * Bundler-resolution tests (C345-3-2 / impl_03 §R5.1).
 *
 * Asserts that `import { signInAndGetClient } from '@blocks-network/embed-auth'`
 * resolves to `dist/index.mjs` — NOT to `dist/blocks-auth.iife.min.js` —
 * under each tool's default browser-target settings. Guards against
 * accidentally re-introducing a top-level `"browser"` field or an
 * `exports."."` `browser` condition.
 *
 * **Simplification per the prompt's allowance.** Spinning up three full
 * bundler projects (Webpack, Vite, Rollup) inside vitest's jsdom + Vite-
 * transformed runtime is fragile (jsdom's TextEncoder fails esbuild's
 * invariant; vitest doesn't expose `import.meta.resolve`; plugin
 * internals shifted shape across versions). Instead, each test spawns a
 * fresh Node subprocess that exercises the real resolver under realistic
 * browser-target conditions. This still genuinely tests resolution —
 * it doesn't just read `package.json`.
 *
 *   1. **Webpack equivalent** — Node's `exports`-map algorithm via
 *      `import.meta.resolve` with `--conditions=import,browser`. Webpack
 *      uses the same algorithm with `conditionNames: ['import', 'browser',
 *      'default']` for ESM browser builds.
 *   2. **Vite equivalent** — Node's `exports`-map algorithm with
 *      `--conditions=import,browser` (Vite's default for browser-target
 *      builds). The same algorithm with the same conditions Vite applies.
 *   3. **Rollup** — `@rollup/plugin-node-resolve` with `browser: true`,
 *      which is the exact plugin used by `rollup.config.js`.
 */
import { execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, it, expect, beforeAll } from 'vitest';

const PACKAGE_NAME = '@blocks-network/embed-auth';
const PACKAGE_ROOT = resolve(__dirname, '..');
const DIST_MJS = resolve(PACKAGE_ROOT, 'dist/index.mjs');
const DIST_IIFE = resolve(PACKAGE_ROOT, 'dist/blocks-auth.iife.min.js');

beforeAll(() => {
  if (!existsSync(DIST_MJS)) {
    throw new Error(
      `dist/index.mjs missing — run \`npx rollup -c\` before bundler-resolution tests.`,
    );
  }
  if (!existsSync(DIST_IIFE)) {
    throw new Error(
      `dist/blocks-auth.iife.min.js missing — run \`npx rollup -c\`.`,
    );
  }
});

function endsWithMjs(p: string): boolean {
  return /[\\/]dist[\\/]index\.mjs$/.test(p);
}

function endsWithIife(p: string): boolean {
  return /blocks-auth\.iife\.min\.js$/.test(p);
}

/** Spawn Node with the requested conditions and print the resolved path. */
function nodeResolve(conditions: string[]): string {
  const args = [
    ...conditions.flatMap((c) => ['--conditions', c]),
    '--input-type=module',
    '-e',
    `const u = await import.meta.resolve(${JSON.stringify(PACKAGE_NAME)}); console.log(new URL(u).pathname);`,
  ];
  const out = execFileSync(process.execPath, args, {
    cwd: PACKAGE_ROOT,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  return out.trim();
}

describe('Webpack-equivalent resolution (Node exports-map under [import, browser])', () => {
  it('resolves to dist/index.mjs', () => {
    const filePath = nodeResolve(['import', 'browser']);
    expect(filePath, `resolved to ${filePath}`).toSatisfy(endsWithMjs);
    expect(filePath).not.toSatisfy(endsWithIife);
  });
});

describe('Vite-equivalent resolution (Node exports-map under [import, browser])', () => {
  it('resolves to dist/index.mjs', () => {
    const filePath = nodeResolve(['import', 'browser']);
    expect(filePath, `resolved to ${filePath}`).toSatisfy(endsWithMjs);
    expect(filePath).not.toSatisfy(endsWithIife);
  });
});

describe('Pure ESM bundler resolution (Node exports-map under [import])', () => {
  it('resolves to dist/index.mjs', () => {
    const filePath = nodeResolve(['import']);
    expect(filePath, `resolved to ${filePath}`).toSatisfy(endsWithMjs);
    expect(filePath).not.toSatisfy(endsWithIife);
  });
});

describe('Rollup resolution (@rollup/plugin-node-resolve, browser: true)', () => {
  it('resolves to dist/index.mjs', async () => {
    // The plugin's `resolveId` is exposed as `{ handler, order }` in
    // newer plugin versions — call `.handler` if present, else call the
    // function directly.
    const mod = await import('@rollup/plugin-node-resolve');
    const nodeResolvePlugin =
      (mod as unknown as { default?: typeof mod.nodeResolve }).default ?? mod.nodeResolve;
    const plugin = nodeResolvePlugin({ browser: true, preferBuiltins: false });
    const raw = (plugin as unknown as { resolveId: unknown }).resolveId;
    const handler =
      typeof raw === 'function'
        ? (raw as (...a: unknown[]) => unknown)
        : ((raw as { handler: (...a: unknown[]) => unknown }).handler);
    const ctx = {
      meta: { rollupVersion: '4.0.0' },
      warn: () => {},
      error: (msg: unknown): never => {
        throw msg instanceof Error ? msg : new Error(String(msg));
      },
      resolve: async () => null,
      getModuleInfo: () => null,
    };
    const result = await handler.call(
      ctx,
      PACKAGE_NAME,
      resolve(PACKAGE_ROOT, 'src/index.ts'),
      { isEntry: false } as unknown,
    );
    expect(result, 'Rollup nodeResolve returned no resolution').not.toBeNull();
    const id =
      typeof result === 'string'
        ? result
        : (result as { id: string }).id;
    expect(id, `Rollup resolved to ${id}`).toSatisfy(endsWithMjs);
    expect(id).not.toSatisfy(endsWithIife);
  });
});
