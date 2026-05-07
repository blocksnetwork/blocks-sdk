/** Deterministic LCG so both sides generate identical byte streams from the
 * same seed. Avoids depending on platform PRNGs that may vary across runs. */
function deterministicRandom(size: number, seed: number): Uint8Array {
  const out = new Uint8Array(size);
  let s = seed >>> 0;
  for (let i = 0; i < size; i++) {
    s = (Math.imul(s, 1103515245) + 12345) >>> 0;
    s = s & 0x7fffffff;
    out[i] = s & 0xff;
  }
  return out;
}

/** Five byte-payload variants exercising the BLOCKS-262 review's boundary cases:
 * empty, all-zero, all-high, multipart-boundary (16384 bytes = STREAM_MAX_MESSAGE_SIZE
 * default), and a 64 KB pseudo-random blob that forces multi-fragment reassembly. */
export const BYTES_VARIANTS: readonly Uint8Array[] = [
  // 1. Empty — does the SDK pass a zero-byte write through?
  new Uint8Array(0),
  // 2. All-zero (1 KB) — proves no NUL-as-string-terminator handling anywhere.
  new Uint8Array(1024),
  // 3. All-high (1 KB) — proves the base64 path is taken; utf8 path would corrupt.
  Uint8Array.from(new Array(1024).fill(0xff)),
  // 4. Exactly multipart boundary (16384 bytes, repeating 0x42).
  Uint8Array.from(new Array(16384).fill(0x42)),
  // 5. Large pseudo-random (64 KB) — forces multi-fragment reassembly + reorder buffer.
  deterministicRandom(64 * 1024, 0xbeef),
];

/** Three event shape categories — primitive, nested, special-chars — plus a
 * batch of 10 small writes to exercise SDK producer-side batching and the
 * consumer-side events() flatten path (the BLOCKS-262 events bug). */
export const EVENTS_VARIANTS: readonly unknown[] = [
  { type: 'primitive', n: 42, b: true, s: 'hello', nullValue: null },
  {
    type: 'nested',
    meta: { tags: ['a', 'b', 'c'], count: 7, deep: { x: { y: 'z' } } },
  },
  { type: 'special', text: 'emoji \u{1F680} RTL מבחן' },
  ...Array.from({ length: 10 }, (_, i) => ({ type: 'batch', i })),
];
