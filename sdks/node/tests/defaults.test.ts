import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import { DEFAULTS } from '../src/defaults.js';
import { BLOCKS_MAX_UPLOAD_BYTES } from '../src/index.js';

const BLOCKS_SDK_ROOT = fileURLToPath(new URL('../../..', import.meta.url));
const PYTHON_CONFIG = 'sdks/python/blocks_network/config.py';
const SCHEMA_COPIES = [
  'schemas/agent-card.schema.json',
  'cli/internal/schema/schemas/agent-card.schema.json',
  'skills/blocks-network/references/agent-card.schema.json',
];

function read(relative: string): string {
  return readFileSync(path.join(BLOCKS_SDK_ROOT, relative), 'utf8');
}

function maxSizeBytesMaxima(node: unknown): number[] {
  if (Array.isArray(node)) return node.flatMap(maxSizeBytesMaxima);
  if (!node || typeof node !== 'object') return [];
  return Object.entries(node).flatMap(([key, value]) => {
    const own =
      key === 'maxSizeBytes' && value && typeof value === 'object' && 'maximum' in value
        ? [(value as { maximum: number }).maximum]
        : [];
    return [...own, ...maxSizeBytesMaxima(value)];
  });
}

describe('BLOCKS_MAX_UPLOAD_BYTES public export', () => {
  it('is exported from the package root and equals DEFAULTS.maxUploadBytes', () => {
    expect(BLOCKS_MAX_UPLOAD_BYTES).toBe(DEFAULTS.maxUploadBytes);
  });
});

describe('upload limit parity across the published SDK', () => {
  it('Python BLOCKS_MAX_UPLOAD_BYTES matches the Node limit', () => {
    const match = read(PYTHON_CONFIG).match(/^BLOCKS_MAX_UPLOAD_BYTES:\s*int\s*=\s*([\d_]+)/m);
    expect(match, `BLOCKS_MAX_UPLOAD_BYTES not found in ${PYTHON_CONFIG}`).not.toBeNull();
    expect(Number(match![1].replaceAll('_', ''))).toBe(DEFAULTS.maxUploadBytes);
  });

  it.each(SCHEMA_COPIES)('%s caps maxSizeBytes at the Node limit', (copy) => {
    const maxima = maxSizeBytesMaxima(JSON.parse(read(copy)));
    expect(maxima.length).toBeGreaterThan(0);
    expect(maxima.every((maximum) => maximum === DEFAULTS.maxUploadBytes)).toBe(true);
  });
});
