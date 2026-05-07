"""
Task Credential Cache

In-memory map that survives pipe-task handler exit. Caches ownerId,
writeToken (T2), agentName, and associated streamIds for each task so
that instance-level APIs (publish_terminal, fail_stream) can operate
after the per-task PubNub client is destroyed.

Lifecycle:
- Populated during task setup from StartTask message.
- StreamIds added via add_stream() during create_stream() calls.
- Survives pipe-task handler exit.
- Removed when the task reaches terminal state.
- Not persisted across process restart.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Set


@dataclass
class CachedCredentials:
    """Cached credentials for a single task."""

    owner_id: str
    write_token: str
    agent_name: str
    environment: str = "playground"
    stream_ids: Set[str] = field(default_factory=set)
    org_id: str = ""


class CredentialCache:
    """Thread-safe task credential cache."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._entries: Dict[str, CachedCredentials] = {}

    def set(
        self,
        task_id: str,
        *,
        owner_id: str,
        write_token: str,
        agent_name: str,
        environment: str = "playground",
        org_id: str = "",
    ) -> None:
        """Store credentials for a task."""
        with self._lock:
            existing = self._entries.get(task_id)
            if existing is not None:
                existing.owner_id = owner_id
                existing.write_token = write_token
                existing.agent_name = agent_name
                existing.environment = environment
                existing.org_id = org_id
                return
            self._entries[task_id] = CachedCredentials(
                owner_id=owner_id,
                write_token=write_token,
                agent_name=agent_name,
                environment=environment,
                org_id=org_id,
            )

    def get(self, task_id: str) -> Optional[CachedCredentials]:
        """Get cached credentials for a task."""
        with self._lock:
            return self._entries.get(task_id)

    def add_stream(self, task_id: str, stream_id: str) -> None:
        """Add a stream ID to a task's cache entry."""
        with self._lock:
            entry = self._entries.get(task_id)
            if entry is not None:
                entry.stream_ids.add(stream_id)

    def remove(self, task_id: str) -> None:
        """Remove a task's cache entry entirely."""
        with self._lock:
            self._entries.pop(task_id, None)

    def has(self, task_id: str) -> bool:
        """Check if a task has cached credentials."""
        with self._lock:
            return task_id in self._entries

    def task_ids(self) -> List[str]:
        """Get all task IDs with cached credentials."""
        with self._lock:
            return list(self._entries.keys())

    def clear(self) -> None:
        """Clear all entries."""
        with self._lock:
            self._entries.clear()
