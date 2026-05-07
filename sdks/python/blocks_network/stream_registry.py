"""
Shared Stream Registry

Instance-level map of active embedded and externally-coordinated streams.
Named streams are ref-counted across tasks via per-task `task_ids` set.
Unnamed streams have a single task id and are scoped to that task.

Compatibility checks on named stream reuse:
- direction must match
- format must match
- external flag must match
- affinity must match (populated at first acquire)

First creator wins for: on_activate, transport tuning options.
Duplicate on_activate callbacks for existing streams are silently ignored.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional, Set


@dataclass
class StreamRegistryEntry:
    """A single entry in the stream registry."""

    stream_id: str
    direction: str  # 'outbound' | 'inbound' | 'bidirectional'
    format: str  # 'bytes' | 'events'
    external: bool
    affinity: str = "dedicated"  # 'dedicated' | 'shared'
    task_ids: Set[str] = field(default_factory=set)
    stream_client: Any = None  # blocks_network.stream.StreamClient or None
    activated: bool = False
    activate_thread: Optional[threading.Thread] = None
    setup_complete: Optional[threading.Event] = None
    """
    First-acquirer setup signal. Set by ``create_stream`` synchronously
    before the first acquirer runs its setup handshake; signalled once
    ``stream_client`` is installed (or setup fails). Concurrent second
    acquirers on the same shared entry MUST wait on this event before
    consulting ``stream_client``; otherwise a race between
    first-acquirer setup and second-acquirer attach silently creates a
    duplicate ``StreamClient`` on the same shared channel. None when
    setup is not in flight.
    """
    setup_error: Optional[BaseException] = None
    """
    Captured setup-handshake exception, if any. When ``setup_complete``
    is signalled with a non-None ``setup_error``, second acquirers
    awaiting the event must re-raise this exception rather than
    treating ``stream_client == None`` as an attach-before-setup race.
    """

    @property
    def ref_count(self) -> int:
        """Number of tasks currently attached to this stream.

        Derived from ``task_ids``; the scalar field was dropped in favor of
        the set-backed per-task tracking required by shared-stream
        task-scoped release semantics.
        """
        return len(self.task_ids)


class StreamRegistry:
    """Thread-safe shared stream registry with per-task ref tracking."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._entries: Dict[str, StreamRegistryEntry] = {}

    def acquire(
        self,
        stream_id: str,
        task_id: str,
        *,
        direction: str,
        format: str,
        external: bool,
        affinity: str = "dedicated",
    ) -> tuple:
        """Get or create a registry entry for a stream.

        Idempotent within a task: a second acquire for the same
        ``(stream_id, task_id)`` returns the existing entry without
        modifying ``task_ids``.

        Returns ``(entry, is_new, is_new_for_task)``:
        - ``is_new`` is True only when the entry itself was created on
          this call (first acquirer across all tasks).
        - ``is_new_for_task`` is True when THIS task attached for the
          first time (distinct from ``is_new``: subsequent tasks
          attaching to an existing shared entry get ``False``/``True``).
        """
        with self._lock:
            existing = self._entries.get(stream_id)
            if existing is not None:
                if existing.external != external:
                    raise ValueError(
                        f'Stream "{stream_id}" incompatible: '
                        f"cannot mix embedded and external"
                    )
                if existing.direction != direction:
                    raise ValueError(
                        f'Stream "{stream_id}" incompatible: direction mismatch '
                        f"(existing: {existing.direction}, requested: {direction})"
                    )
                if existing.format != format:
                    raise ValueError(
                        f'Stream "{stream_id}" incompatible: format mismatch '
                        f"(existing: {existing.format}, requested: {format})"
                    )
                if existing.affinity != affinity:
                    raise ValueError(
                        f'Stream "{stream_id}" incompatible: affinity mismatch '
                        f"(existing: {existing.affinity}, requested: {affinity})"
                    )
                if task_id in existing.task_ids:
                    # Idempotent: same task acquiring same stream again.
                    # No-op, signal by is_new_for_task=False so callers
                    # skip re-publishing stream_setup.
                    return existing, False, False
                existing.task_ids.add(task_id)
                return existing, False, True

            entry = StreamRegistryEntry(
                stream_id=stream_id,
                direction=direction,
                format=format,
                external=external,
                affinity=affinity,
                task_ids={task_id},
            )
            self._entries[stream_id] = entry
            return entry, True, True

    def get(self, stream_id: str) -> Optional[StreamRegistryEntry]:
        """Get a registry entry by stream ID."""
        with self._lock:
            return self._entries.get(stream_id)

    def release(self, stream_id: str, task_id: str) -> int:
        """Release a task's reference to a stream.

        Removes ``task_id`` from the entry's task set. When the set is
        empty the entry is removed from the registry and the caller
        should ``end()`` the underlying ``stream_client``.
        Returns the remaining ref count (``len(task_ids)``).
        """
        with self._lock:
            entry = self._entries.get(stream_id)
            if entry is None:
                return 0
            entry.task_ids.discard(task_id)
            if not entry.task_ids:
                del self._entries[stream_id]
                return 0
            return len(entry.task_ids)

    def force_remove(self, stream_id: str) -> Optional[StreamRegistryEntry]:
        """Force-remove a stream entry (for fail_stream).

        Removes the entry from the registry and returns it. ``task_ids``
        on the returned entry is LEFT INTACT so ``fail_stream`` can
        iterate the set to publish ``state: 'failed'`` terminals to
        every ref-holding task. The returned entry is disowned — no
        one can ``release`` it.

        DO NOT clear ``task_ids`` here "for hygiene": the single reader
        at ``agent_instance.py#fail_stream`` fans out failed terminals
        over ``entry.task_ids``; clearing the set silently breaks the
        fan-out. See QUESTIONS.md R6 (shared_stream_lifecycle).
        """
        with self._lock:
            entry = self._entries.get(stream_id)
            if entry is not None:
                del self._entries[stream_id]
            return entry

    def release_all_for_task(self, task_id: str) -> "List[StreamRegistryEntry]":
        """Release all streams for a given task.

        Returns entries that reached an empty ``task_ids`` set (removed
        from the registry). Callers should ``end()`` the
        ``stream_client`` on each returned entry.
        """
        with self._lock:
            destroyed: List[StreamRegistryEntry] = []
            to_remove: List[str] = []
            for stream_id, entry in self._entries.items():
                if task_id in entry.task_ids:
                    entry.task_ids.discard(task_id)
                    if not entry.task_ids:
                        to_remove.append(stream_id)
                        destroyed.append(entry)
            for stream_id in to_remove:
                del self._entries[stream_id]
            return destroyed

    @property
    def active_stream_count(self) -> int:
        """Count of active embedded stream processing contexts."""
        with self._lock:
            return sum(
                1 for entry in self._entries.values() if not entry.external
            )

    def stream_ids(self) -> List[str]:
        """All stream IDs in the registry."""
        with self._lock:
            return list(self._entries.keys())

    def clear(self) -> None:
        """Clear the entire registry."""
        with self._lock:
            self._entries.clear()
