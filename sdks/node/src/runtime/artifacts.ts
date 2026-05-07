import type PubNub from 'pubnub';
import { DEFAULTS } from '../defaults.js';
import { bytesToBase64 } from '../stream/bytes.js';

export interface DownloadedArtifact {
  data: Uint8Array;
  mimeType: string;
  fileName?: string;
}

export interface ArtifactRef {
  kind: 'inline' | 'file';
  mimeType: string;
  size: number;
  hash?: string;
  /** Base64-encoded data for inline artifacts (kind: 'inline' only) */
  data?: string;
  /** PubNub Files identifier (kind: 'file' only) */
  fileId?: string;
  /** File name (both variants; required for kind: 'file') */
  fileName?: string;
  /** PubNub channel where the file is stored (kind: 'file' only, required).
   *  Used with fileId and fileName for pubnub.downloadFile() under PAM. */
  channel?: string;
  expiresAt?: string;
}

export const shouldInlineArtifact = (
  sizeBytes: number,
  inlineLimit: number = DEFAULTS.inlineLimitBytes,
): boolean => sizeBytes <= inlineLimit;

export interface BuildArtifactInput {
  mimeType: string;
  /** Size in bytes - required if data is not provided */
  size?: number;
  hash?: string;
  /** Raw data for inline artifacts - will be base64 encoded. Accepts
   *  `Uint8Array` (includes Node `Buffer` at runtime). */
  data?: Uint8Array;
  /** File reference for uploaded artifacts. Channel is the PubNub channel
   *  where the file is stored (required for downloadFile under PAM). */
  file?: { id: string; name: string; channel: string; expiresAt?: string };
  /** Original file name (preserved on inline artifacts too). */
  fileName?: string;
  inlineLimit?: number;
}

export const buildArtifactRef = (input: BuildArtifactInput): ArtifactRef => {
  const inlineLimit = input.inlineLimit ?? DEFAULTS.inlineLimitBytes;
  const size = input.size ?? input.data?.length ?? 0;
  const inline = shouldInlineArtifact(size, inlineLimit) || !input.file;

  if (inline) {
    return {
      kind: 'inline',
      mimeType: input.mimeType,
      size,
      hash: input.hash,
      // bytesToBase64 is Buffer-independent and works in both Node and
      // browsers. Uint8Array.toString('base64') returns a comma-joined
      // decimal string on runtimes where Buffer is not defined --
      // silently corrupting artifact data.
      data: input.data !== undefined ? bytesToBase64(input.data) : undefined,
      fileName: input.fileName,
    };
  }

  // file is guaranteed to be defined here since inline is false only when file exists
  const file = input.file!;
  return {
    kind: 'file',
    mimeType: input.mimeType,
    size,
    hash: input.hash,
    fileId: file.id,
    fileName: file.name,
    channel: file.channel,
    expiresAt: file.expiresAt,
  };
};

/**
 * Decode an inline base64 artifact ref into raw bytes.
 * Browser-safe: uses atob() + Uint8Array (no Buffer dependency).
 *
 * The ArtifactRef shape uses top-level `kind: 'inline'` discriminator
 * with `data` as a top-level base64 string field (see ArtifactRef
 * interface and artifact-ref.schema.json).
 */
export function decodeInlineArtifact(ref: ArtifactRef): Uint8Array {
  if (ref.kind !== 'inline' || !ref.data) {
    throw new Error('ArtifactRef is not an inline artifact or has no data');
  }
  const binaryString = atob(ref.data);
  const bytes = new Uint8Array(binaryString.length);
  for (let i = 0; i < binaryString.length; i++) {
    bytes[i] = binaryString.charCodeAt(i);
  }
  return bytes;
}

/**
 * Download an artifact from an ArtifactRef.
 *
 * - Inline artifacts: decode base64 data directly (no PubNub call).
 * - File artifacts: call pubnub.downloadFile() using the ref's
 *   channel, fileId, and fileName.
 *
 * Requires a PubNub instance with a valid token granting READ on the
 * artifact's channel. For TaskSession consumers, the T4 read token
 * already grants this.
 */
export async function downloadArtifact(
  ref: ArtifactRef,
  pubnub: PubNub,
): Promise<DownloadedArtifact> {
  if (ref.kind === 'inline') {
    if (!ref.data) {
      throw new Error('Inline ArtifactRef is missing data');
    }
    const data = decodeInlineArtifact(ref);
    return { data, mimeType: ref.mimeType, fileName: ref.fileName };
  }

  if (ref.kind === 'file') {
    if (!ref.channel || !ref.fileId || !ref.fileName) {
      throw new Error(
        'File ArtifactRef is missing required fields: channel, fileId, and fileName are all required',
      );
    }

    const result = await pubnub.downloadFile({
      channel: ref.channel,
      id: ref.fileId,
      name: ref.fileName,
    });

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const file = (result as any).data ?? result;
    let data: Uint8Array;

    // PubNub SDK v10 returns result.data as a Uint8Array (Node Buffer
    // extends Uint8Array, so the instanceof check catches both Node
    // and browser Uint8Array shapes) or as a Blob in older browser
    // builds. Even older @pubnub/file objects exposed toBuffer() /
    // toArrayBuffer(); we keep duck-typed fallbacks for those.
    if (file instanceof Uint8Array) {
      data = new Uint8Array(file);
    } else if (typeof Blob !== 'undefined' && file instanceof Blob) {
      const ab = await file.arrayBuffer();
      data = new Uint8Array(ab);
    } else if (typeof (file as { toArrayBuffer?: unknown })?.toArrayBuffer === 'function') {
      const ab = await (file as { toArrayBuffer: () => Promise<ArrayBuffer> }).toArrayBuffer();
      data = new Uint8Array(ab);
    } else if (typeof (file as { toBuffer?: unknown })?.toBuffer === 'function') {
      const buf = await (file as { toBuffer: () => Promise<Uint8Array | ArrayBuffer> }).toBuffer();
      data = new Uint8Array(buf);
    } else {
      throw new Error('PubNub downloadFile result has no usable data format');
    }

    return { data, mimeType: ref.mimeType, fileName: ref.fileName };
  }

  throw new Error(`Unknown ArtifactRef kind: ${ref.kind}`);
}
