"""
Shared types and constants for the Stream SDK.

These types define the public API surface for stream configuration,
direction modes, and inbound message shape.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Literal, Optional, Union


# Wire format for stream data: 'bytes' for chunked text/binary,
# 'events' for structured objects.
StreamFormat = Literal["bytes", "events"]

# Direction of stream data flow relative to the local client.
StreamDirection = Literal["outbound", "inbound", "bidirectional"]


@dataclass
class StreamBundleConfig:
    """Configuration for StreamBundle (internal transport engine)."""

    max_message_size: int = 16384
    """Maximum serialized message size before multipart splitting."""

    bundle_size_bytes: int = 4096
    """Flush buffer when accumulated byte size reaches this limit."""

    max_latency_ms: int = 250
    """Flush buffer after this many ms since first unflushed write."""

    uuid: str = ""
    """Publisher UUID for meta.sender on every publish."""


@dataclass
class InboundMessage:
    """Normalized inbound message yielded by the inbound iterator.

    Discriminated by ``format``. ``data`` runtime shape:
      - ``format == "bytes"``  → ``list[str]`` (chunks; ``encoding`` is
        ``"utf8"`` or ``"base64"``)
      - ``format == "events"`` → ``list[Any]`` (one or more events from a
        single producer flush)
      - ``format == "raw"``    → ``dict[str, Any]`` (passthrough for
        unknown message types)

    The array-vs-single shape is the SDK's own producer-side bundler
    (``stream_bundle.py``) coalescing writes by size/latency — NOT PubNub
    transport batching. A single producer ``write()`` already yields a
    1-element list on the wire. Application code should prefer
    ``bytes()`` / ``events()`` which flatten on the consumer's behalf;
    reach for ``inbound`` only when you need raw envelope metadata.
    """

    data: Any
    seq: int
    ts: int
    format: Literal["bytes", "events", "raw"]
    encoding: str


# Reserved bytes for the per-part envelope in multipart messages.
ENVELOPE_RESERVE = 512
