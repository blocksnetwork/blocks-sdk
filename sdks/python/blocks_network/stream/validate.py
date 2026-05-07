"""
Stream ID validation.

Stream IDs appear in the channel name: stream.{agentName}.{streamId}
Dots are disallowed because they are PubNub channel hierarchy separators.
"""

from __future__ import annotations

import re

_STREAM_ID_REGEX = re.compile(r"^[a-zA-Z0-9\-_]+$")
_MAX_STREAM_ID_BYTES = 92


def validate_stream_id(stream_id: str) -> None:
    """Validate a stream ID. Raises ValueError if the ID is invalid.

    Rules:
    - Cannot be empty
    - Cannot exceed 92 bytes (UTF-8)
    - Only [a-zA-Z0-9-_] allowed (no dots)
    """
    if len(stream_id) == 0:
        raise ValueError("Stream ID cannot be empty")
    if len(stream_id.encode("utf-8")) > _MAX_STREAM_ID_BYTES:
        raise ValueError("Stream ID exceeds 92 byte limit")
    if not _STREAM_ID_REGEX.match(stream_id):
        raise ValueError(
            "Stream ID contains invalid characters. "
            "Allowed: a-z, A-Z, 0-9, -, _"
        )
