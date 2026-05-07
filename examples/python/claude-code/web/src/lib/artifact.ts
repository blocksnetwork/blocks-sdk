import type { ArtifactRef, TaskArtifact } from '../types.ts';

/**
 * Decode a base64 string to a UTF-8 string.
 *
 * atob() alone does not handle multi-byte UTF-8 characters correctly --
 * it decodes base64 to a Latin-1 byte string. This function converts
 * that byte string into a proper Uint8Array and decodes it via TextDecoder
 * to produce the correct UTF-8 output.
 */
function base64ToUtf8(base64: string): string {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return new TextDecoder('utf-8').decode(bytes);
}

/**
 * Decode an artifact reference into a structured TaskArtifact.
 *
 * Handles two artifact kinds:
 * - inline: base64-encoded JSON decoded via base64ToUtf8()
 * - file: fetched from CDN URL via HTTP GET
 *
 * Both cases parse the resulting string as JSON.
 */
export async function decodeArtifact(ref: ArtifactRef): Promise<TaskArtifact> {
  let raw: string;

  if (ref.kind === 'inline' && ref.data) {
    raw = base64ToUtf8(ref.data);
  } else if (ref.kind === 'file' && ref.fileUrl) {
    const resp = await fetch(ref.fileUrl);
    if (!resp.ok) {
      throw new Error(`Failed to fetch artifact: HTTP ${resp.status}`);
    }
    raw = await resp.text();
  } else {
    throw new Error(`Unsupported artifact: kind=${ref.kind}, data=${!!ref.data}, fileUrl=${!!ref.fileUrl}`);
  }

  return JSON.parse(raw) as TaskArtifact;
}
