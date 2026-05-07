import { describe, expect, it } from 'vitest';
import { buildArtifactRef, shouldInlineArtifact } from '../src/runtime/artifacts.js';

describe('artifact handling', () => {
  it('inlines small artifacts', () => {
    expect(shouldInlineArtifact(1024, 2048)).toBe(true);
    const ref = buildArtifactRef({
      mimeType: 'text/plain',
      size: 1024,
      inlineLimit: 2048,
      hash: 'sha256:abc',
    });
    expect(ref.kind).toBe('inline');
    expect(ref.hash).toBe('sha256:abc');
  });

  it('uses file reference for large artifacts', () => {
    const ref = buildArtifactRef({
      mimeType: 'application/octet-stream',
      size: 64 * 1024,
      inlineLimit: 2048,
      file: { id: 'file-id', name: 'blob.bin', channel: 'u.org1.task1' },
    });
    expect(ref.kind).toBe('file');
    expect(ref.fileId).toBe('file-id');
    expect(ref.channel).toBe('u.org1.task1');
    expect(ref.fileName).toBe('blob.bin');
  });

  it('preserves fileName on inline artifacts', () => {
    const ref = buildArtifactRef({
      mimeType: 'text/plain',
      size: 10,
      inlineLimit: 2048,
      data: Buffer.from('hello'),
      fileName: 'hello.txt',
    });
    expect(ref.kind).toBe('inline');
    expect(ref.fileName).toBe('hello.txt');
  });
});
