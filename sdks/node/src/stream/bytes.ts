/**
 * Browser-safe byte utilities for the Stream SDK.
 *
 * Replaces Node.js Buffer usage with Uint8Array, TextEncoder, and
 * TextDecoder, which work identically in browsers and Node.js.
 */

const encoder = new TextEncoder();
const decoder = new TextDecoder();

export function utf8ByteLength(str: string): number {
  return encoder.encode(str).length;
}

export function utf8Encode(str: string): Uint8Array {
  return encoder.encode(str);
}

export function utf8Decode(bytes: Uint8Array): string {
  return decoder.decode(bytes);
}

export function base64ToBytes(b64: string): Uint8Array {
  const binStr = atob(b64);
  const bytes = new Uint8Array(binStr.length);
  for (let i = 0; i < binStr.length; i++) bytes[i] = binStr.charCodeAt(i);
  return bytes;
}

/** Convert bytes to base64. Intended for bounded stream chunks and
 *  multipart parts (typically <16KB), not arbitrary large blobs. */
export function bytesToBase64(bytes: Uint8Array): string {
  let binStr = '';
  for (const b of bytes) binStr += String.fromCharCode(b);
  return btoa(binStr);
}

export function concatBytes(parts: Uint8Array[]): Uint8Array {
  const totalLen = parts.reduce((sum, p) => sum + p.length, 0);
  const result = new Uint8Array(totalLen);
  let offset = 0;
  for (const p of parts) { result.set(p, offset); offset += p.length; }
  return result;
}
