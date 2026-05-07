"""
Tests for blocks_network.channel_manager -- channel naming rules.

Covers:
- Control, task, obs, task-metadata channel formats
- Round-trip parse of task-metadata channels
- Registry channel helpers (registry.all, registry.skill.{slug})
- Skill slug normalization
- Owner ID validation
- Error paths for missing parameters
"""

from __future__ import annotations

import pytest

from blocks_network.channel_manager import (
    ChannelManager,
    create_channel_manager,
    normalize_skill_slug,
    registry_all_channel,
    registry_skill_channel,
    registry_log_channel,
    registry_visibility_channel,
    validate_owner_id,
)


# ---------------------------------------------------------------------------
# Channel format tests
# ---------------------------------------------------------------------------


class TestControlChannel:
    def test_control_channel(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.control_channel("agent-id-123") == "agent.agent-id-123.control"

    def test_control_channel_with_agent_id(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.control_channel("other.agent") == "agent.other.agent.control"


class TestTaskChannel:
    def test_task_channel(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.task_channel("task-123", "alice") == "u.alice.task-123"

    def test_task_channel_missing_params(self) -> None:
        cm = create_channel_manager("acme-echo")
        with pytest.raises(ValueError, match="org_id required"):
            cm.task_channel("task-123", "")
        with pytest.raises(ValueError, match="task_id required"):
            cm.task_channel("", "alice")


class TestObsChannel:
    def test_obs_channel(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.obs_channel() == "obs.acme-echo.log"

    def test_obs_channel_override(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.obs_channel("other.agent") == "obs.other.agent.log"


class TestTaskMetadataChannel:
    def test_task_metadata_channel(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.task_metadata_channel("task-xyz") == "task.task-xyz"

    def test_parse_task_metadata_channel_roundtrip(self) -> None:
        cm = create_channel_manager("acme-echo")
        task_id = "roundtrip-task-99"
        channel = cm.task_metadata_channel(task_id)
        parsed = cm.parse_task_metadata_channel(channel)
        assert parsed == task_id

    def test_parse_task_metadata_channel_non_matching(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.parse_task_metadata_channel("agent.foo.control") is None


# ---------------------------------------------------------------------------
# Registry channel helpers
# ---------------------------------------------------------------------------


class TestRegistryChannels:
    def test_registry_all_channel(self) -> None:
        assert registry_all_channel() == "registry.all"

    def test_registry_skill_channel(self) -> None:
        assert registry_skill_channel("image-generation") == "registry.skill.image_generation"

    def test_registry_skill_channel_preserves_dots(self) -> None:
        assert registry_skill_channel("text.embeddings") == "registry.skill.text.embeddings"


# ---------------------------------------------------------------------------
# Skill slug normalization
# ---------------------------------------------------------------------------


class TestNormalizeSkillSlug:
    def test_hyphen_to_underscore(self) -> None:
        assert normalize_skill_slug("image-generation") == "image_generation"

    def test_space_to_underscore_and_lowercase(self) -> None:
        assert normalize_skill_slug("Image Generation") == "image_generation"

    def test_dots_preserved(self) -> None:
        assert normalize_skill_slug("text.embeddings") == "text.embeddings"

    def test_multiple_special_chars(self) -> None:
        assert normalize_skill_slug("--multiple---dashes--") == "multiple_dashes"

    def test_mixed_case_and_symbols(self) -> None:
        assert normalize_skill_slug("NLP / Text!Analysis") == "nlp_text_analysis"


# ---------------------------------------------------------------------------
# Owner ID validation
# ---------------------------------------------------------------------------


class TestValidateOwnerId:
    def test_reserved_prefixes_rejected(self) -> None:
        for prefix in ("agent", "obs", "sys", "u", "anonymous", "system"):
            assert validate_owner_id(prefix) is False, f"'{prefix}' should be rejected"

    def test_reserved_case_insensitive(self) -> None:
        assert validate_owner_id("Agent") is False
        assert validate_owner_id("SYSTEM") is False

    def test_valid_owner_id(self) -> None:
        assert validate_owner_id("alice") is True
        assert validate_owner_id("user-123") is True

    def test_empty_string_rejected(self) -> None:
        assert validate_owner_id("") is False


# ---------------------------------------------------------------------------
# ChannelManager construction
# ---------------------------------------------------------------------------


class TestChannelManagerConstruction:
    def test_channel_manager_requires_agent_name(self) -> None:
        with pytest.raises(ValueError, match="agent_name is required"):
            ChannelManager("")

    def test_channel_manager_requires_agent_name_none(self) -> None:
        """Passing a falsy value should raise."""
        with pytest.raises((ValueError, TypeError)):
            create_channel_manager("")


# ---------------------------------------------------------------------------
# Task channel error paths
# ---------------------------------------------------------------------------


class TestTaskChannelErrors:
    def test_none_owner_id(self) -> None:
        cm = create_channel_manager("acme-echo")
        with pytest.raises(ValueError, match="org_id required"):
            cm.task_channel("task-1", None)

    def test_none_task_id(self) -> None:
        cm = create_channel_manager("acme-echo")
        with pytest.raises(ValueError, match="task_id required"):
            cm.task_channel(None, "alice")


# ---------------------------------------------------------------------------
# Task metadata channel errors
# ---------------------------------------------------------------------------


class TestTaskMetadataChannelErrors:
    def test_empty_string(self) -> None:
        cm = create_channel_manager("acme-echo")
        with pytest.raises(ValueError, match="task_id required"):
            cm.task_metadata_channel("")

    def test_none_value(self) -> None:
        cm = create_channel_manager("acme-echo")
        with pytest.raises((ValueError, TypeError)):
            cm.task_metadata_channel(None)


# ---------------------------------------------------------------------------
# User task pattern
# ---------------------------------------------------------------------------


class TestUserTaskPattern:
    def test_returns_wildcard_pattern(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.user_task_pattern("alice") == "u.alice.*"

    def test_errors_on_empty_owner(self) -> None:
        cm = create_channel_manager("acme-echo")
        with pytest.raises(ValueError, match="org_id required"):
            cm.user_task_pattern("")


# ---------------------------------------------------------------------------
# Agent wildcard
# ---------------------------------------------------------------------------


class TestAgentWildcard:
    def test_default_agent_name(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.agent_wildcard() == "agent.acme-echo.*"

    def test_override_agent_name(self) -> None:
        cm = create_channel_manager("acme-echo")
        assert cm.agent_wildcard("other.agent") == "agent.other.agent.*"


# ---------------------------------------------------------------------------
# Registry visibility channel
# ---------------------------------------------------------------------------


class TestRegistryVisibilityChannel:
    def test_public(self) -> None:
        assert registry_visibility_channel(True) == "registry.public"

    def test_private(self) -> None:
        assert registry_visibility_channel(False) == "registry.private"


# ---------------------------------------------------------------------------
# Registry log channel
# ---------------------------------------------------------------------------


class TestRegistryLogChannel:
    def test_log_channel(self) -> None:
        assert registry_log_channel() == "registry.log"


# ---------------------------------------------------------------------------
# Slug normalization edge cases
# ---------------------------------------------------------------------------


class TestSlugNormalizationEdgeCases:
    def test_collapses_multiple_underscores(self) -> None:
        assert normalize_skill_slug("a___b") == "a_b"

    def test_strips_leading_trailing(self) -> None:
        assert normalize_skill_slug("_hello_") == "hello"

    def test_complex_case(self) -> None:
        result = normalize_skill_slug("AI-Powered Image Generation!")
        assert result == "ai_powered_image_generation"
        # No leading/trailing underscores, no double underscores
        assert not result.startswith("_")
        assert not result.endswith("_")
        assert "__" not in result


# ---------------------------------------------------------------------------
# Various agent name strings produce correct channels
# ---------------------------------------------------------------------------


class TestVariousAgentNames:
    def test_dotted_agent_name(self) -> None:
        cm = create_channel_manager("org.example.my-agent")
        assert cm.control_channel("agent-id-dotted") == "agent.agent-id-dotted.control"
        assert cm.obs_channel() == "obs.org.example.my-agent.log"

    def test_simple_agent_name(self) -> None:
        cm = create_channel_manager("echo")
        assert cm.control_channel("echo-id") == "agent.echo-id.control"
        assert cm.task_channel("t1", "bob") == "u.bob.t1"
