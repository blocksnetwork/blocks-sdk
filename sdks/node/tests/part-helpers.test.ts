import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { textPart, filePart, filePartFromPath } from '../src/runtime/part-helpers.js';
import { writeFileSync, mkdirSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

describe('part-helpers', () => {
  describe('textPart', () => {
    it('produces correct SendMessageRequestPart with default partId', () => {
      const part = textPart('Hello, world');
      expect(part).toEqual({ partId: 'text', text: 'Hello, world' });
    });

    it('uses custom partId when provided', () => {
      const part = textPart('question', 'prompt');
      expect(part).toEqual({ partId: 'prompt', text: 'question' });
    });

    it('handles empty string', () => {
      const part = textPart('');
      expect(part).toEqual({ partId: 'text', text: '' });
    });
  });

  describe('filePartFromPath (Node-only)', () => {
    let tmpDir: string;

    beforeEach(() => {
      tmpDir = join(tmpdir(), `part-helpers-test-${Date.now()}`);
      mkdirSync(tmpDir, { recursive: true });
    });

    afterEach(() => {
      rmSync(tmpDir, { recursive: true, force: true });
    });

    it('reads file from path and infers fileName', async () => {
      const filePath = join(tmpDir, 'data.csv');
      writeFileSync(filePath, 'col1,col2\na,b');
      const part = await filePartFromPath(filePath);
      expect(part.partId).toBe('file');
      expect(part.fileName).toBe('data.csv');
      expect(part.contentType).toBe('application/octet-stream');
      expect(part.file).toBeInstanceOf(Uint8Array);
      expect(new TextDecoder().decode(part.file as Uint8Array)).toBe('col1,col2\na,b');
    });

    it('allows custom partId, fileName, and contentType for path', async () => {
      const filePath = join(tmpDir, 'doc.txt');
      writeFileSync(filePath, 'content');
      const part = await filePartFromPath(filePath, {
        partId: 'document',
        fileName: 'renamed.txt',
        contentType: 'text/plain',
      });
      expect(part.partId).toBe('document');
      expect(part.fileName).toBe('renamed.txt');
      expect(part.contentType).toBe('text/plain');
    });

    it('handles nested path for fileName extraction', async () => {
      const filePath = join(tmpDir, 'nested.txt');
      writeFileSync(filePath, 'nested');
      const part = await filePartFromPath(filePath);
      expect(part.fileName).toBe('nested.txt');
    });
  });

  describe('filePart (universal, data-only)', () => {
    it('works with raw Buffer data (Buffer extends Uint8Array)', () => {
      const data = Buffer.from('raw bytes');
      const part = filePart(data);
      expect(part.partId).toBe('file');
      expect(part.fileName).toBe('file');
      expect(part.contentType).toBe('application/octet-stream');
      expect(part.file).toBe(data);
    });

    it('works with Uint8Array data', () => {
      const data = new Uint8Array([72, 101, 108, 108, 111]);
      const part = filePart(data, { fileName: 'hello.bin' });
      expect(part.partId).toBe('file');
      expect(part.fileName).toBe('hello.bin');
      expect(part.file).toBe(data);
    });

    it('works with ArrayBuffer data', () => {
      const ab = new Uint8Array([1, 2, 3]).buffer;
      const part = filePart(ab, { fileName: 'ab.bin' });
      expect(part.partId).toBe('file');
      expect(part.file).toBe(ab);
    });

    it('works with Blob data and infers contentType when provided', () => {
      const blob = new Blob([new Uint8Array([4, 5, 6])], { type: 'text/plain' });
      const part = filePart(blob);
      expect(part.partId).toBe('file');
      expect(part.file).toBe(blob);
      expect(part.contentType).toBe('text/plain');
    });

    it('works with File data and infers fileName', () => {
      const file = new File([new Uint8Array([7, 8, 9])], 'input.bin', {
        type: 'application/octet-stream',
      });
      const part = filePart(file);
      expect(part.partId).toBe('file');
      expect(part.fileName).toBe('input.bin');
      expect(part.contentType).toBe('application/octet-stream');
      expect(part.file).toBe(file);
    });

    it('allows custom options for raw data', () => {
      const data = Buffer.from('{}');
      const part = filePart(data, {
        partId: 'config',
        fileName: 'config.json',
        contentType: 'application/json',
      });
      expect(part.partId).toBe('config');
      expect(part.fileName).toBe('config.json');
      expect(part.contentType).toBe('application/json');
    });
  });

  describe('package surface', () => {
    // Guard: the SDK contract documents textPart / filePart /
    // filePartFromPath as exported from the package root. These must
    // be reachable through the barrel, not just the internal module.
    it('re-exports textPart, filePart, and filePartFromPath from the package entrypoint', async () => {
      const sdk = await import('../src/index.js');
      expect(typeof sdk.textPart).toBe('function');
      expect(typeof sdk.filePart).toBe('function');
      expect(typeof sdk.filePartFromPath).toBe('function');
    });
  });
});
