/**
 * Normalize a consumer-supplied file input into a form the SDK can
 * hand to `buildArtifactRef` (which wants `Uint8Array`) and
 * `uploadToStorage` (which wants a `Blob`-or-bytes).
 *
 * Accepts the four shapes a browser or Node consumer might provide:
 *   - `Uint8Array` (including Node `Buffer`, which extends Uint8Array).
 *   - `ArrayBuffer`.
 *   - `Blob` (browser-native; also available in Node 18+).
 *   - `File` (browser-native subclass of Blob; also Node 20+).
 *
 * Returned shape is a three-field handle:
 *   - `size`: byte length, available synchronously for every input
 *     shape (the caller uses it to choose inline vs upload path).
 *   - `uploadBody`: the canonical body to hand to `uploadToStorage`.
 *     Blobs pass through unchanged — no pre-copy. Uint8Array /
 *     ArrayBuffer inputs are returned as raw bytes so
 *     `uploadToStorage` can wrap them in a Blob on demand.
 *   - `getBytes()`: a lazy accessor that returns a `Uint8Array` view
 *     of the input contents. For `Uint8Array` / `ArrayBuffer` inputs
 *     it's effectively free; for `Blob` / `File` inputs it performs
 *     one `arrayBuffer()` read, cached against repeat calls. Callers
 *     on the upload path **MUST NOT** call `getBytes()`, because it
 *     pulls the full Blob into memory — that defeats the streaming
 *     upload. `getBytes()` exists only for the inline-artifact path
 *     (files under `shouldInlineArtifact()` threshold, default 16 KB).
 *
 * Throws `TypeError` for unsupported inputs synchronously — malformed
 * shapes surface immediately at call site rather than at first
 * `getBytes()`.
 */
export interface NormalizedFileInput {
  readonly size: number;
  readonly uploadBody: Uint8Array | Blob;
  getBytes(): Promise<Uint8Array>;
}

export function normalizeFileInput(
  input: Uint8Array | ArrayBuffer | Blob | File,
): NormalizedFileInput {
  if (input instanceof Uint8Array) {
    return {
      size: input.byteLength,
      uploadBody: input,
      getBytes: async () => input,
    };
  }
  if (input instanceof ArrayBuffer) {
    const bytes = new Uint8Array(input);
    return {
      size: bytes.byteLength,
      uploadBody: bytes,
      getBytes: async () => bytes,
    };
  }
  if (typeof Blob !== 'undefined' && input instanceof Blob) {
    let cached: Uint8Array | undefined;
    return {
      size: input.size,
      uploadBody: input,
      getBytes: async () => {
        if (!cached) cached = new Uint8Array(await input.arrayBuffer());
        return cached;
      },
    };
  }
  throw new TypeError(
    'Unsupported file input: expected Uint8Array, ArrayBuffer, Blob, or File',
  );
}
