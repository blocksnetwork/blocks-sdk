"""Test payloads — Python port of payloads.ts.

The deterministic LCG below is byte-for-byte equivalent to Node's
``Math.imul(s, 1103515245) + 12345 >>> 0``. Same seed → same output
sequence, so the bytes-payload hashes match across languages too
(useful when running cross-language symmetry tests).
"""

from __future__ import annotations

from typing import Any, List


def _deterministic_random(size: int, seed: int) -> bytes:
    out = bytearray(size)
    s = seed & 0xFFFFFFFF
    for i in range(size):
        s = (s * 1103515245 + 12345) & 0xFFFFFFFF
        s = s & 0x7FFFFFFF
        out[i] = s & 0xFF
    return bytes(out)


# 1. Empty
# 2. All-zero (1 KB)         — proves no NUL-as-string-terminator handling.
# 3. All-high (1 KB)         — proves the base64 path is taken.
# 4. Multipart boundary      — exactly STREAM_MAX_MESSAGE_SIZE (16384 bytes).
# 5. 64 KB pseudo-random     — forces multi-fragment reassembly.
BYTES_VARIANTS: List[bytes] = [
    b"",
    bytes(1024),
    bytes([0xFF] * 1024),
    bytes([0x42] * 16384),
    _deterministic_random(64 * 1024, 0xBEEF),
]

EVENTS_VARIANTS: List[Any] = [
    {"type": "primitive", "n": 42, "b": True, "s": "hello", "nullValue": None},
    {
        "type": "nested",
        "meta": {
            "tags": ["a", "b", "c"],
            "count": 7,
            "deep": {"x": {"y": "z"}},
        },
    },
    {"type": "special", "text": "emoji \U0001F680 RTL מבחן"},
    *[{"type": "batch", "i": i} for i in range(10)],
]
