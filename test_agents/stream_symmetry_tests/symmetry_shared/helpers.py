"""Shared producer / consumer helpers — Python port of helpers.ts.

Symmetry-by-construction: handler.py and main.py both import the SAME
functions below. If StreamObject and StreamClient ever diverge in shape,
this file fails to import on one of the sides and the test breaks loudly.

Cross-language symmetry: the canonical_json walker uses sorted keys +
ensure_ascii=False + no whitespace so the SHA-256 digest produced by
this file matches the Node helpers.ts digest for an identical event
payload. The bytes-stream hash trivially matches across languages
because both sides hash raw bytes.
"""

from __future__ import annotations

import hashlib
import json
import time
from typing import Any, Iterable, List


def canonical_json(value: Any) -> str:
    """Stable JSON serialization for cross-side hashing — matches helpers.ts.

    - Objects: keys sorted lexicographically.
    - Strings: ensure_ascii=False to keep raw UTF-8 (matches JSON.stringify).
    - No whitespace between tokens (separators=(",", ":")).
    """
    if value is None or isinstance(value, bool) or isinstance(value, (int, float, str)):
        return json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    if isinstance(value, list):
        return "[" + ",".join(canonical_json(v) for v in value) + "]"
    if isinstance(value, dict):
        keys = sorted(value.keys())
        return (
            "{"
            + ",".join(
                json.dumps(k, ensure_ascii=False) + ":" + canonical_json(value[k])
                for k in keys
            )
            + "}"
        )
    raise TypeError(f"unsupported type for canonical_json: {type(value).__name__}")


def produce_bytes(stream: Any, payloads: List[bytes]) -> dict:
    h = hashlib.sha256()
    total_bytes = 0
    for p in payloads:
        stream.write(p)
        h.update(p)
        total_bytes += len(p)
    stream.end()
    return {
        "hash": h.hexdigest(),
        "totalBytes": total_bytes,
        "chunkCount": len(payloads),
    }


def consume_bytes(stream: Any) -> dict:
    h = hashlib.sha256()
    total_bytes = 0
    chunk_count = 0
    for chunk in stream.bytes():
        h.update(chunk)
        total_bytes += len(chunk)
        chunk_count += 1
    return {"hash": h.hexdigest(), "totalBytes": total_bytes, "chunkCount": chunk_count}


def produce_events(stream: Any, events: List[Any]) -> dict:
    h = hashlib.sha256()
    for ev in events:
        stream.write(ev)
        h.update(canonical_json(ev).encode("utf-8"))
    stream.end()
    return {"hash": h.hexdigest(), "eventCount": len(events)}


def consume_events(stream: Any) -> dict:
    h = hashlib.sha256()
    event_count = 0
    for ev in stream.events():
        h.update(canonical_json(ev).encode("utf-8"))
        event_count += 1
    return {"hash": h.hexdigest(), "eventCount": event_count}


def sleep(seconds: float) -> None:
    time.sleep(seconds)
