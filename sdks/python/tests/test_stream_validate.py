"""
Tests for validate_stream_id.

Covers:
- Valid IDs accepted
- Dots rejected
- Oversized (>92 bytes) rejected
- Empty rejected
- Invalid characters rejected
"""

from __future__ import annotations

import pytest
from blocks_network.stream.validate import validate_stream_id


class TestValidateStreamId:

    def test_accepts_simple_alphanumeric(self):
        validate_stream_id("myStream123")

    def test_accepts_hyphens_and_underscores(self):
        validate_stream_id("my-stream_id")

    def test_accepts_92_byte_id(self):
        validate_stream_id("a" * 92)

    def test_accepts_single_character_ids(self):
        for ch in ("a", "Z", "0", "-", "_"):
            validate_stream_id(ch)

    def test_rejects_empty_string(self):
        with pytest.raises(ValueError, match="Stream ID cannot be empty"):
            validate_stream_id("")

    def test_rejects_dots(self):
        with pytest.raises(ValueError, match="Stream ID contains invalid characters"):
            validate_stream_id("my.stream")

    def test_rejects_dot_only(self):
        with pytest.raises(ValueError, match="Stream ID contains invalid characters"):
            validate_stream_id(".")

    def test_rejects_oversized(self):
        with pytest.raises(ValueError, match="Stream ID exceeds 92 byte limit"):
            validate_stream_id("a" * 93)

    def test_rejects_multibyte_exceeding_92_bytes(self):
        # Each emoji is 4 bytes in UTF-8. 24 emojis = 96 bytes > 92
        emoji = "\U0001F600"
        with pytest.raises(ValueError):
            validate_stream_id(emoji * 24)

    def test_rejects_spaces(self):
        with pytest.raises(ValueError, match="Stream ID contains invalid characters"):
            validate_stream_id("my stream")

    def test_rejects_special_characters(self):
        for ch in ("my@stream", "my/stream", "my:stream", "my#stream"):
            with pytest.raises(
                ValueError, match="Stream ID contains invalid characters"
            ):
                validate_stream_id(ch)
