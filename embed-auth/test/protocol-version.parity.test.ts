import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, it, expect } from 'vitest';

import {
  CURRENT_PROTOCOL_VERSION,
  PROTOCOL_VERSION_HEADER,
} from '../src/protocol-version.js';

/**
 * Parity guard. The Consumer SDK's `runtime/protocol-version.ts` is not
 * part of the SDK package's public `exports` map, so the widget keeps its
 * own copy. This test reads the SDK source at test time and asserts
 * byte-identity with the widget's local exports — any SDK bump trips CI
 * before the widget can drift.
 */
describe('protocol-version parity with @blocks-network/sdk', () => {
  // Resolve from this file: blocks-sdk/embed-auth/test/ → blocks-sdk/sdks/node/src/runtime/protocol-version.ts
  const sdkProtocolPath = resolve(
    __dirname,
    '../../sdks/node/src/runtime/protocol-version.ts',
  );
  const source = readFileSync(sdkProtocolPath, 'utf8');

  it('SDK source is readable', () => {
    expect(source.length).toBeGreaterThan(0);
  });

  it('CURRENT_PROTOCOL_VERSION matches the SDK byte-for-byte', () => {
    const match = source.match(
      /export const CURRENT_PROTOCOL_VERSION\s*=\s*['"]([^'"]+)['"]/,
    );
    expect(match, 'failed to extract CURRENT_PROTOCOL_VERSION from SDK source')
      .not.toBeNull();
    const sdkValue = match![1];
    expect(CURRENT_PROTOCOL_VERSION).toBe(sdkValue);
  });

  it('PROTOCOL_VERSION_HEADER matches the SDK byte-for-byte', () => {
    const match = source.match(
      /export const PROTOCOL_VERSION_HEADER\s*=\s*['"]([^'"]+)['"]/,
    );
    expect(match, 'failed to extract PROTOCOL_VERSION_HEADER from SDK source')
      .not.toBeNull();
    const sdkValue = match![1];
    expect(PROTOCOL_VERSION_HEADER).toBe(sdkValue);
  });
});
