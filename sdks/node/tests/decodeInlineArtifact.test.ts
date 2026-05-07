import { describe, it, expect } from 'vitest';
import { decodeInlineArtifact, type ArtifactRef } from '../src/runtime/artifacts.js';

describe('decodeInlineArtifact', () => {
  it('decodes a base64 inline artifact to Uint8Array', () => {
    const ref: ArtifactRef = {
      kind: 'inline',
      mimeType: 'text/plain',
      size: 5,
      data: btoa('hello'),
    };
    const result = decodeInlineArtifact(ref);
    expect(result).toBeInstanceOf(Uint8Array);
    expect(new TextDecoder().decode(result)).toBe('hello');
  });

  it('throws for file artifacts', () => {
    const ref: ArtifactRef = {
      kind: 'file',
      mimeType: 'application/pdf',
      size: 1024,
      fileId: 'f-123',
    };
    expect(() => decodeInlineArtifact(ref)).toThrow('not an inline artifact');
  });

  it('throws for inline artifacts with no data', () => {
    const ref: ArtifactRef = {
      kind: 'inline',
      mimeType: 'text/plain',
      size: 0,
    };
    expect(() => decodeInlineArtifact(ref)).toThrow('not an inline artifact');
  });

  it('handles binary data correctly', () => {
    const bytes = new Uint8Array([0, 1, 127, 128, 255]);
    const b64 = Buffer.from(bytes).toString('base64');
    const ref: ArtifactRef = {
      kind: 'inline',
      mimeType: 'application/octet-stream',
      size: bytes.length,
      data: b64,
    };
    const result = decodeInlineArtifact(ref);
    expect(Array.from(result)).toEqual(Array.from(bytes));
  });
});
