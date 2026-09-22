import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const pkg = JSON.parse(
  readFileSync(join(__dirname, '..', 'package.json'), 'utf8'),
) as { dependencies: Record<string, string> };

// tsx ships in dependencies, so what it resolves reaches every consumer; below 4.22 that is a bundler build with an advisory.
const TSX_FLOOR: [number, number, number] = [4, 23, 0];

/**
 * True only for a single `~x.y.z` or `^x.y.z` whose floor is at or above `floor`.
 *
 * Any other range shape is rejected rather than parsed. A compound or open range
 * can permit the old line while reading as current — `~4.23.0 || ~4.21.0` and
 * `>=4.21.0` both do — so the safe answer for anything this cannot reason about
 * is no. Widening the range deliberately means updating this guard with it.
 */
export function floorIsAtLeast(
  range: string,
  floor: [number, number, number],
): boolean {
  const m = /^[~^](\d+)\.(\d+)\.(\d+)$/.exec(range.trim());
  if (!m) return false;
  const got: [number, number, number] = [+m[1], +m[2], +m[3]];
  for (let i = 0; i < 3; i++) {
    if (got[i] !== floor[i]) return got[i] > floor[i];
  }
  return true;
}

/**
 * The version the workspace lockfile pins for this package's tsx.
 *
 * Read by key rather than by resolving the module: these tests run from the repo
 * root as well, whose own older tsx would answer a `require`. npm nests this
 * package's copy while the two differ, and hoists a single one when they agree,
 * so both keys are accepted.
 */
function lockedTsxVersion(): string {
  const lock = JSON.parse(
    readFileSync(join(__dirname, '..', '..', '..', 'package-lock.json'), 'utf8'),
  ) as { packages: Record<string, { version?: string }> };
  const entry =
    lock.packages['sdks/node/node_modules/tsx'] ??
    lock.packages['node_modules/tsx'];
  if (!entry?.version) throw new Error('no tsx entry in the workspace lockfile');
  return entry.version;
}

describe('tsx runtime dependency floor', () => {
  it('stays in dependencies, where blocks-run needs it at runtime', () => {
    expect(pkg.dependencies.tsx).toBeDefined();
  });

  it('declares a floor at or above the line carrying the patched bundler', () => {
    expect(floorIsAtLeast(pkg.dependencies.tsx, TSX_FLOOR)).toBe(true);
  });

  it('pins a resolved version at or above that floor, so a stale lock cannot pass', () => {
    expect(floorIsAtLeast(`~${lockedTsxVersion()}`, TSX_FLOOR)).toBe(true);
  });
});

describe('floorIsAtLeast', () => {
  it('accepts a simple pin at or above the floor', () => {
    expect(floorIsAtLeast('~4.23.0', TSX_FLOOR)).toBe(true);
    expect(floorIsAtLeast('^4.24.1', TSX_FLOOR)).toBe(true);
    expect(floorIsAtLeast('~5.0.0', TSX_FLOOR)).toBe(true);
  });

  it('rejects a simple pin below the floor', () => {
    expect(floorIsAtLeast('~4.21.0', TSX_FLOOR)).toBe(false);
    expect(floorIsAtLeast('^4.22.9', TSX_FLOOR)).toBe(false);
    expect(floorIsAtLeast('~3.99.99', TSX_FLOOR)).toBe(false);
  });

  it('rejects a range that permits the old line while reading as current', () => {
    expect(floorIsAtLeast('~4.23.0 || ~4.21.0', TSX_FLOOR)).toBe(false);
    expect(floorIsAtLeast('4.21.x || 4.23.x', TSX_FLOOR)).toBe(false);
    expect(floorIsAtLeast('>=4.21.0', TSX_FLOOR)).toBe(false);
    expect(floorIsAtLeast('*', TSX_FLOOR)).toBe(false);
  });
});
