/**
 * Part builder helpers for constructing SendMessageRequestPart objects.
 *
 * These simplify the consumer experience by handling wire format field
 * names and file reading automatically.
 *
 * Browser-safe: no top-level `node:fs` import. The Node-only
 * `filePartFromPath` helper lazy-imports `node:fs` on call so browser
 * bundlers can tree-shake it out of consumer bundles that don't use
 * filesystem paths.
 */

import type { SendMessageRequestPart } from './task-client.js';

/** Extract the base name from a file path (portable, no node:path needed). */
function baseName(filePath: string): string {
  const lastSlash = Math.max(filePath.lastIndexOf('/'), filePath.lastIndexOf('\\'));
  return lastSlash >= 0 ? filePath.slice(lastSlash + 1) : filePath;
}

/**
 * Build a text request part.
 *
 * @param text The text content.
 * @param partId The part ID (defaults to 'text'). Must match a declared
 *               io.inputs[].id in the agent's card.
 */
export function textPart(text: string, partId = 'text'): SendMessageRequestPart {
  return { partId, text };
}

/**
 * Build a file request part from raw file data.
 *
 * Universal: works in both Node and browser. No filesystem access.
 * For Node-only file-path convenience, use `filePartFromPath`.
 *
 * Accepts `Uint8Array`, `ArrayBuffer`, `Blob`, or `File`. Browser
 * consumers can pass a `File` from `<input type="file">` directly.
 *
 * @param data Raw file data.
 * @param options Optional overrides for partId, fileName, and contentType.
 */
export function filePart(
  data: Uint8Array | ArrayBuffer | Blob | File,
  options?: { partId?: string; fileName?: string; contentType?: string },
): SendMessageRequestPart {
  const partId = options?.partId ?? 'file';
  // Prefer an inferred fileName for File instances (browser convention).
  const inferredName =
    typeof File !== 'undefined' && data instanceof File ? data.name : undefined;
  const inferredType =
    typeof Blob !== 'undefined' && data instanceof Blob && data.type
      ? data.type
      : undefined;
  return {
    partId,
    file: data,
    fileName: options?.fileName ?? inferredName ?? 'file',
    contentType:
      options?.contentType ?? inferredType ?? 'application/octet-stream',
  };
}

/**
 * Build a file request part from a filesystem path (Node-only).
 *
 * Lazy-imports `node:fs` on first call so this helper can coexist
 * with browser bundlers without pulling `node:fs` into the bundle.
 * Not available in browsers.
 *
 * @param path Filesystem path.
 * @param options Optional overrides for partId, fileName, and contentType.
 */
export async function filePartFromPath(
  path: string,
  options?: { partId?: string; fileName?: string; contentType?: string },
): Promise<SendMessageRequestPart> {
  const { readFileSync } = await import('node:fs');
  const buffer = readFileSync(path);
  // Re-wrap as a fresh Uint8Array so downstream normalization treats
  // the data uniformly regardless of whether Buffer is defined.
  const data = new Uint8Array(buffer);
  return {
    partId: options?.partId ?? 'file',
    file: data,
    fileName: options?.fileName ?? baseName(path),
    contentType: options?.contentType ?? 'application/octet-stream',
  };
}
