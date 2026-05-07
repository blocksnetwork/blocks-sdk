import { describe, it, expect } from 'vitest';
import { validateStreamId } from '../src/stream/validate.js';

describe('validateStreamId (browser-safe)', () => {
  it('validates a normal stream ID without Buffer', () => {
    const originalBuffer = globalThis.Buffer;
    // @ts-expect-error — simulating browser environment
    globalThis.Buffer = undefined;
    try {
      expect(() => validateStreamId('my-stream-1')).not.toThrow();
    } finally {
      globalThis.Buffer = originalBuffer;
    }
  });

  it('rejects oversized stream ID without Buffer', () => {
    const originalBuffer = globalThis.Buffer;
    // @ts-expect-error — simulating browser environment
    globalThis.Buffer = undefined;
    try {
      const longId = 'a'.repeat(93);
      expect(() => validateStreamId(longId)).toThrow('92 byte limit');
    } finally {
      globalThis.Buffer = originalBuffer;
    }
  });

  it('handles multi-byte input without Buffer (rejected by regex, not crash)', () => {
    const originalBuffer = globalThis.Buffer;
    // @ts-expect-error — simulating browser environment
    globalThis.Buffer = undefined;
    try {
      // Stream IDs only allow [a-zA-Z0-9-_], so multi-byte chars
      // will be rejected by the regex. The key assertion is that the
      // byte-length check runs without crashing before the regex fires.
      expect(() => validateStreamId('héllo-wörld')).toThrow('invalid characters');
    } finally {
      globalThis.Buffer = originalBuffer;
    }
  });
});
