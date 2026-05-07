import { describe, expect, it } from 'vitest';

import { DEFAULTS } from '../src/defaults.js';
import { BLOCKS_MAX_UPLOAD_BYTES } from '../src/index.js';

describe('DEFAULTS.maxUploadBytes', () => {
  it('equals 25 MiB (26_214_400) — must match afui_mvp_backend MAX_FILE_SIZE_BYTES', () => {
    expect(DEFAULTS.maxUploadBytes).toBe(26_214_400);
    expect(DEFAULTS.maxUploadBytes).toBe(25 * 1024 * 1024);
  });
});

describe('BLOCKS_MAX_UPLOAD_BYTES public export', () => {
  it('is exported from the package root and equals DEFAULTS.maxUploadBytes', () => {
    expect(BLOCKS_MAX_UPLOAD_BYTES).toBe(26_214_400);
    expect(BLOCKS_MAX_UPLOAD_BYTES).toBe(DEFAULTS.maxUploadBytes);
  });
});
