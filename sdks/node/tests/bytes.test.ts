import { describe, it, expect } from 'vitest';
import {
  utf8ByteLength,
  utf8Encode,
  utf8Decode,
  base64ToBytes,
  bytesToBase64,
  concatBytes,
} from '../src/stream/bytes.js';

describe('bytes utilities', () => {
  describe('utf8ByteLength', () => {
    it('returns correct length for ASCII', () => {
      expect(utf8ByteLength('hello')).toBe(5);
    });

    it('returns correct length for multi-byte characters', () => {
      // '€' is 3 bytes in UTF-8
      expect(utf8ByteLength('€')).toBe(3);
      // emoji is 4 bytes
      expect(utf8ByteLength('😀')).toBe(4);
    });

    it('returns 0 for empty string', () => {
      expect(utf8ByteLength('')).toBe(0);
    });
  });

  describe('utf8Encode / utf8Decode round-trip', () => {
    it('round-trips ASCII', () => {
      const encoded = utf8Encode('hello world');
      expect(encoded).toBeInstanceOf(Uint8Array);
      expect(utf8Decode(encoded)).toBe('hello world');
    });

    it('round-trips multi-byte characters', () => {
      const str = 'Héllo wörld 🌍';
      expect(utf8Decode(utf8Encode(str))).toBe(str);
    });
  });

  describe('base64ToBytes / bytesToBase64 round-trip', () => {
    it('round-trips ASCII text', () => {
      const original = new Uint8Array([72, 101, 108, 108, 111]); // "Hello"
      const b64 = bytesToBase64(original);
      const decoded = base64ToBytes(b64);
      expect(Array.from(decoded)).toEqual(Array.from(original));
    });

    it('round-trips binary data with all byte values', () => {
      const original = new Uint8Array([0, 1, 127, 128, 254, 255]);
      const b64 = bytesToBase64(original);
      const decoded = base64ToBytes(b64);
      expect(Array.from(decoded)).toEqual(Array.from(original));
    });

    it('decodes standard base64', () => {
      // btoa('hello') = 'aGVsbG8='
      const result = base64ToBytes('aGVsbG8=');
      expect(utf8Decode(result)).toBe('hello');
    });
  });

  describe('concatBytes', () => {
    it('concatenates multiple arrays', () => {
      const a = new Uint8Array([1, 2, 3]);
      const b = new Uint8Array([4, 5]);
      const c = new Uint8Array([6]);
      const result = concatBytes([a, b, c]);
      expect(Array.from(result)).toEqual([1, 2, 3, 4, 5, 6]);
    });

    it('handles empty arrays', () => {
      const result = concatBytes([]);
      expect(result.length).toBe(0);
    });

    it('handles single array', () => {
      const a = new Uint8Array([1, 2, 3]);
      const result = concatBytes([a]);
      expect(Array.from(result)).toEqual([1, 2, 3]);
    });
  });

  describe('multipart round-trip simulation', () => {
    it('split → base64 → reassemble produces original JSON', () => {
      const original = { type: 'stream_data', streamId: 'test', chunks: ['hello', 'world'], seq: 0 };
      const serialized = JSON.stringify(original);
      const bytes = utf8Encode(serialized);

      // Simulate splitting into 3 parts
      const partSize = Math.ceil(bytes.length / 3);
      const parts: Uint8Array[] = [];
      for (let i = 0; i < 3; i++) {
        parts.push(bytes.subarray(i * partSize, (i + 1) * partSize));
      }

      // Encode each part to base64 (simulates publish)
      const b64Parts = parts.map(bytesToBase64);

      // Decode and reassemble (simulates consumer)
      const decodedParts = b64Parts.map(base64ToBytes);
      const reassembled = concatBytes(decodedParts);
      const result = JSON.parse(utf8Decode(reassembled));

      expect(result).toEqual(original);
    });
  });
});
