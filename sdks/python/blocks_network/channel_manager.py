"""
Channel naming utility for Blocks Network.

Port of ``src/shared/channelManager.ts``.

Channel topology (org-scoped, 3-level max):
- ``u.{orgId}.{taskId}``          -- Org-scoped task events
- ``agent.{agentId}.control``      -- Control plane per agent ID
- ``obs.{agentName}.log``          -- Agent-level observability

Registry channels (App Context membership indexes):
- ``registry.all``
- ``registry.public`` / ``registry.private``
- ``registry.skill.{skill}``
- ``registry.log``
"""

from __future__ import annotations

import re
from typing import Optional

# ---------------------------------------------------------------------------
# Reserved prefixes (cannot be used as owner IDs)
# ---------------------------------------------------------------------------
_RESERVED_PREFIXES = frozenset(
    ["agent", "obs", "sys", "u", "anonymous", "system", "stream"]
)


# ============================================================================
# Standalone registry channel helpers
# ============================================================================


def registry_all_channel() -> str:
    """``registry.all`` -- membership index for all agents."""
    return "registry.all"


def registry_skill_channel(skill: str) -> str:
    """``registry.skill.{slug}`` -- membership index for a skill."""
    slug = normalize_skill_slug(skill)
    return f"registry.skill.{slug}"


def registry_visibility_channel(is_public: bool) -> str:
    """``registry.public`` or ``registry.private``."""
    return "registry.public" if is_public else "registry.private"


def registry_log_channel() -> str:
    """``registry.log`` -- audit log channel."""
    return "registry.log"


def normalize_skill_slug(skill: str) -> str:
    """Normalize a skill string to a stable slug.

    - Lowercase
    - Replace non-alphanumeric (except ``.`` and ``_``) with ``_``
    - Collapse multiple underscores
    - Strip leading/trailing underscores

    Examples::

        "image-generation"  -> "image_generation"
        "text.embeddings"   -> "text.embeddings"
        "Image Generation"  -> "image_generation"
    """
    slug = skill.lower()
    slug = re.sub(r"[^a-z0-9._]", "_", slug)
    slug = re.sub(r"_+", "_", slug)
    slug = slug.strip("_")
    return slug


def validate_owner_id(owner_id: str) -> bool:
    """Return ``True`` if *owner_id* is not a reserved prefix."""
    if not owner_id or not isinstance(owner_id, str):
        return False
    return owner_id.lower() not in _RESERVED_PREFIXES


def task_channel(task_id: str, org_id: str) -> str:
    """Org-scoped task channel: ``u.{orgId}.{taskId}``."""
    if not org_id:
        raise ValueError("org_id required for task channel")
    if not task_id:
        raise ValueError("task_id required for task channel")
    return f"u.{org_id}.{task_id}"


# ============================================================================
# ChannelManager class
# ============================================================================


class ChannelManager:
    """Per-agent channel name builder.

    Parameters
    ----------
    agent_name:
        The agent name string (e.g. ``"acme-echo"``).  Required.
    """

    def __init__(self, agent_name: str) -> None:
        if not agent_name:
            raise ValueError("agent_name is required for ChannelManager")
        self._agent_name = agent_name

    @property
    def agent_name(self) -> str:
        return self._agent_name

    # -- stream channels ----------------------------------------------------

    def stream_channel(self, stream_id: str) -> str:
        """Stream data channel: ``stream.{agentName}.{streamId}``."""
        if not stream_id:
            raise ValueError("stream_id required for stream channel")
        return f"stream.{self._agent_name}.{stream_id}"

    def stream_wildcard(self) -> str:
        """Wildcard for all streams of this agent: ``stream.{agentName}.*``."""
        return f"stream.{self._agent_name}.*"

    # -- pub/sub channels ---------------------------------------------------

    def task_channel(self, task_id: str, org_id: str) -> str:
        """Org-scoped task channel: ``u.{orgId}.{taskId}``."""
        if not org_id:
            raise ValueError("org_id required for task channel")
        if not task_id:
            raise ValueError("task_id required for task channel")
        return f"u.{org_id}.{task_id}"

    def control_channel(self, agent_id: str) -> str:
        """Control channel: ``agent.{agentId}.control``."""
        return f"agent.{agent_id}.control"

    def obs_channel(self, agent_name: Optional[str] = None) -> str:
        """Observability channel: ``obs.{agentName}.log``."""
        return f"obs.{agent_name or self._agent_name}.log"

    # -- App Context helpers (not pub/sub) ----------------------------------

    def task_metadata_channel(self, task_id: str) -> str:
        """Task metadata channel ID for App Context: ``task.{taskId}``."""
        if not task_id:
            raise ValueError("task_id required for task metadata channel")
        return f"task.{task_id}"

    def parse_task_metadata_channel(self, channel: str) -> Optional[str]:
        """Extract task_id from ``task.{taskId}`` channel, or ``None``."""
        if channel.startswith("task."):
            return channel[5:]
        return None

    # -- PAM wildcard patterns ----------------------------------------------

    def user_task_pattern(self, org_id: str) -> str:
        """Wildcard for all org tasks: ``u.{orgId}.*``."""
        if not org_id:
            raise ValueError("org_id required for user task pattern")
        return f"u.{org_id}.*"

    def agent_wildcard(self, agent_name: Optional[str] = None) -> str:
        """Wildcard for agent channels: ``agent.{agentName}.*``."""
        return f"agent.{agent_name or self._agent_name}.*"


# ============================================================================
# Factory
# ============================================================================


def create_channel_manager(agent_name: str) -> ChannelManager:
    """Create a :class:`ChannelManager` for *agent_name*."""
    return ChannelManager(agent_name)
