/**
 * Tests for validateStreamId.
 *
 * Covers:
 * - Valid IDs accepted
 * - Dots rejected
 * - Oversized (>92 bytes) rejected
 * - Empty rejected
 * - Invalid characters rejected
 */

import { describe, it, expect } from 'vitest';
import { validateStreamId } from '../src/stream/validate.js';

describe('validateStreamId', () => {
  it('accepts a simple alphanumeric ID', () => {
    expect(() => validateStreamId('myStream123')).not.toThrow();
  });

  it('accepts hyphens and underscores', () => {
    expect(() => validateStreamId('my-stream_id')).not.toThrow();
  });

  it('accepts a 92-byte ID', () => {
    const id = 'a'.repeat(92);
    expect(() => validateStreamId(id)).not.toThrow();
  });

  it('accepts single character IDs', () => {
    expect(() => validateStreamId('a')).not.toThrow();
    expect(() => validateStreamId('Z')).not.toThrow();
    expect(() => validateStreamId('0')).not.toThrow();
    expect(() => validateStreamId('-')).not.toThrow();
    expect(() => validateStreamId('_')).not.toThrow();
  });

  it('rejects empty string', () => {
    expect(() => validateStreamId('')).toThrow('Stream ID cannot be empty');
  });

  it('rejects dots (channel hierarchy separator)', () => {
    expect(() => validateStreamId('my.stream')).toThrow(
      'Stream ID contains invalid characters',
    );
  });

  it('rejects dots even as only character', () => {
    expect(() => validateStreamId('.')).toThrow(
      'Stream ID contains invalid characters',
    );
  });

  it('rejects oversized IDs (>92 bytes)', () => {
    const id = 'a'.repeat(93);
    expect(() => validateStreamId(id)).toThrow('Stream ID exceeds 92 byte limit');
  });

  it('rejects multibyte characters that exceed 92 bytes', () => {
    // Each emoji is 4 bytes in UTF-8. 24 emojis = 96 bytes > 92
    const id = '\u{1F600}'.repeat(24);
    expect(() => validateStreamId(id)).toThrow();
  });

  it('rejects spaces', () => {
    expect(() => validateStreamId('my stream')).toThrow(
      'Stream ID contains invalid characters',
    );
  });

  it('rejects special characters', () => {
    expect(() => validateStreamId('my@stream')).toThrow(
      'Stream ID contains invalid characters',
    );
    expect(() => validateStreamId('my/stream')).toThrow(
      'Stream ID contains invalid characters',
    );
    expect(() => validateStreamId('my:stream')).toThrow(
      'Stream ID contains invalid characters',
    );
    expect(() => validateStreamId('my#stream')).toThrow(
      'Stream ID contains invalid characters',
    );
  });
});
