/**
 * Schema parity guard. The widget keeps copies of the postMessage envelope
 * schemas under `src/__schemas__/` so Rollup's TS plugin doesn't have to
 * follow imports outside the package's `rootDir`. This test reads the
 * canonical files at `schemas/embedded-auth/` and asserts byte-identity
 * with the in-package copies — any drift in the source schemas trips CI
 * before the widget can ship a stale envelope shape.
 */
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, it, expect } from 'vitest';

describe('postMessage envelope schemas — source-vs-copy parity', () => {
  const packageRoot = resolve(__dirname, '..');
  // Resolve from this file: blocks-sdk/embed-auth/test/ → repo root via
  // blocks-sdk/embed-auth/../../../schemas/embedded-auth/.
  const repoSchemaDir = resolve(packageRoot, '../../schemas/embedded-auth');
  const localSchemaDir = resolve(packageRoot, 'src/__schemas__');

  const files = [
    'postmessage-envelope.success.schema.json',
    'postmessage-envelope.error.schema.json',
  ] as const;

  for (const file of files) {
    it(`${file} matches schemas/embedded-auth byte-for-byte`, () => {
      const sourcePath = resolve(repoSchemaDir, file);
      const copyPath = resolve(localSchemaDir, file);
      const source = readFileSync(sourcePath, 'utf8');
      const copy = readFileSync(copyPath, 'utf8');
      expect(copy, `${file} drift: re-copy from ${sourcePath}`).toBe(source);
    });
  }
});
