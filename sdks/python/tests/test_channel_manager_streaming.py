"""
Tests for blocks_network.channel_manager -- streaming channel helpers.

Covers:
- stream_channel() returns correct format stream.{agentName}.{streamId}
- stream_wildcard() returns correct format stream.{agentName}.*
- validate_owner_id('stream') returns False (reserved prefix)
"""

from __future__ import annotations

import pytest

from blocks_network.channel_manager import (
    ChannelManager,
    create_channel_manager,
    validate_owner_id,
)


# ---------------------------------------------------------------------------
# stream_channel()
# ---------------------------------------------------------------------------


class TestStreamChannel:
    def test_returns_correct_format(self) -> None:
        cm = create_channel_manager("llm-agent")
        assert cm.stream_channel("task-abc") == "stream.llm-agent.task-abc"

    def test_dotted_agent_name(self) -> None:
        cm = create_channel_manager("org.example.my-agent")
        assert cm.stream_channel("s-1") == "stream.org.example.my-agent.s-1"

    def test_throws_on_empty_stream_id(self) -> None:
        cm = create_channel_manager("llm-agent")
        with pytest.raises(ValueError, match="stream_id required"):
            cm.stream_channel("")


# ---------------------------------------------------------------------------
# stream_wildcard()
# ---------------------------------------------------------------------------


class TestStreamWildcard:
    def test_returns_correct_format(self) -> None:
        cm = create_channel_manager("llm-agent")
        assert cm.stream_wildcard() == "stream.llm-agent.*"

    def test_dotted_agent_name(self) -> None:
        cm = create_channel_manager("org.example.my-agent")
        assert cm.stream_wildcard() == "stream.org.example.my-agent.*"


# ---------------------------------------------------------------------------
# validate_owner_id('stream') -- reserved prefix
# ---------------------------------------------------------------------------


class TestStreamReservedPrefix:
    def test_stream_is_reserved(self) -> None:
        assert validate_owner_id("stream") is False

    def test_stream_case_insensitive(self) -> None:
        assert validate_owner_id("Stream") is False
        assert validate_owner_id("STREAM") is False

    def test_streaming_is_not_reserved(self) -> None:
        """'streaming' != 'stream', so it should pass."""
        assert validate_owner_id("streaming") is True
