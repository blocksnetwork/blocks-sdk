"""
blocks_network.stream - Public API surface.

Exports the StreamClient class, StreamDescriptor dataclass,
invert_direction helper, validate_stream_id validator, and all shared types.
"""

from .stream_client import StreamClient, StreamError
from .descriptor import StreamAffinity, StreamDescriptor, invert_direction
from .validate import validate_stream_id
from .types import (
    StreamFormat,
    StreamDirection,
    StreamBundleConfig,
    InboundMessage,
)

__all__ = [
    "StreamClient",
    "StreamError",
    "StreamDescriptor",
    "StreamAffinity",
    "invert_direction",
    "validate_stream_id",
    "StreamFormat",
    "StreamDirection",
    "StreamBundleConfig",
    "InboundMessage",
]
