"""Tests for the stream registry module."""

from __future__ import annotations

import pytest

from blocks_network.stream_registry import StreamRegistry


class TestStreamRegistry:
    """Stream registry unit tests."""

    def test_acquire_new_entry(self) -> None:
        reg = StreamRegistry()
        entry, is_new, is_new_for_task = reg.acquire(
            "stream-1", "task-1", direction="outbound", format="bytes", external=False
        )
        assert is_new is True
        assert is_new_for_task is True
        assert entry.stream_id == "stream-1"
        assert entry.direction == "outbound"
        assert entry.format == "bytes"
        assert entry.external is False
        assert entry.affinity == "dedicated"
        assert entry.ref_count == 1
        assert "task-1" in entry.task_ids

    def test_acquire_existing_compatible(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "stream-1", "task-1", direction="outbound", format="bytes", external=False,
            affinity="shared",
        )
        entry, is_new, is_new_for_task = reg.acquire(
            "stream-1", "task-2", direction="outbound", format="bytes", external=False,
            affinity="shared",
        )
        assert is_new is False
        assert is_new_for_task is True
        assert entry.ref_count == 2
        assert "task-1" in entry.task_ids
        assert "task-2" in entry.task_ids

    def test_acquire_idempotent_same_task(self) -> None:
        """Fix (e): a second acquire for the same (stream, task) is a no-op."""
        reg = StreamRegistry()
        first_entry, first_new, first_new_for_task = reg.acquire(
            "stream-1",
            "task-1",
            direction="outbound",
            format="bytes",
            external=False,
            affinity="shared",
        )
        assert first_new is True
        assert first_new_for_task is True

        second_entry, second_new, second_new_for_task = reg.acquire(
            "stream-1",
            "task-1",
            direction="outbound",
            format="bytes",
            external=False,
            affinity="shared",
        )
        assert second_new is False
        assert second_new_for_task is False
        assert second_entry is first_entry
        assert second_entry.ref_count == 1
        assert second_entry.task_ids == {"task-1"}

    def test_acquire_direction_mismatch(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "stream-1", "task-1", direction="outbound", format="bytes", external=False
        )
        with pytest.raises(ValueError, match="direction mismatch"):
            reg.acquire(
                "stream-1", "task-2", direction="inbound", format="bytes", external=False
            )

    def test_acquire_format_mismatch(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "stream-1", "task-1", direction="outbound", format="bytes", external=False
        )
        with pytest.raises(ValueError, match="format mismatch"):
            reg.acquire(
                "stream-1", "task-2", direction="outbound", format="events", external=False
            )

    def test_acquire_external_mismatch(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "stream-1", "task-1", direction="outbound", format="bytes", external=False
        )
        with pytest.raises(ValueError, match="cannot mix embedded and external"):
            reg.acquire(
                "stream-1", "task-2", direction="outbound", format="bytes", external=True
            )

    def test_acquire_affinity_mismatch(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "stream-1", "task-1", direction="outbound", format="bytes",
            external=False, affinity="dedicated",
        )
        with pytest.raises(ValueError, match="affinity mismatch"):
            reg.acquire(
                "stream-1", "task-2", direction="outbound", format="bytes",
                external=False, affinity="shared",
            )

    def test_release(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "stream-1", "task-1", direction="outbound", format="bytes", external=False
        )
        reg.acquire(
            "stream-1", "task-2", direction="outbound", format="bytes", external=False
        )
        remaining = reg.release("stream-1", "task-1")
        assert remaining == 1
        remaining = reg.release("stream-1", "task-2")
        assert remaining == 0
        # Entry should be removed when task_ids is empty
        assert reg.get("stream-1") is None

    def test_release_nonexistent(self) -> None:
        reg = StreamRegistry()
        assert reg.release("nonexistent", "task-1") == 0

    def test_force_remove(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "stream-1", "task-1", direction="outbound", format="bytes", external=False
        )
        reg.acquire(
            "stream-1", "task-2", direction="outbound", format="bytes", external=False
        )
        entry = reg.force_remove("stream-1")
        assert entry is not None
        # task_ids preserved on the returned entry so fail_stream can
        # fan out failed terminals to every ref-holder. See
        # QUESTIONS.md R6 (shared_stream_lifecycle).
        assert entry.task_ids == {"task-1", "task-2"}
        assert entry.ref_count == 2
        assert reg.get("stream-1") is None

    def test_force_remove_nonexistent(self) -> None:
        reg = StreamRegistry()
        assert reg.force_remove("nonexistent") is None

    def test_release_all_for_task(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "stream-1", "task-1", direction="outbound", format="bytes", external=False
        )
        reg.acquire(
            "stream-2", "task-1", direction="inbound", format="events", external=False
        )
        reg.acquire(
            "stream-1", "task-2", direction="outbound", format="bytes", external=False
        )

        destroyed = reg.release_all_for_task("task-1")
        destroyed_ids = [e.stream_id for e in destroyed]
        # stream-1 has task-2 still attached, so not destroyed
        # stream-2 was only for task-1, so destroyed
        assert "stream-2" in destroyed_ids
        assert "stream-1" not in destroyed_ids
        assert reg.get("stream-1") is not None
        assert reg.get("stream-1").ref_count == 1

    def test_active_stream_count(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "s1", "t1", direction="outbound", format="bytes", external=False
        )
        reg.acquire(
            "s2", "t1", direction="outbound", format="bytes", external=True
        )
        # Only non-external streams count
        assert reg.active_stream_count == 1

    def test_stream_ids(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "s1", "t1", direction="outbound", format="bytes", external=False
        )
        reg.acquire(
            "s2", "t1", direction="inbound", format="events", external=False
        )
        assert set(reg.stream_ids()) == {"s1", "s2"}

    def test_clear(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "s1", "t1", direction="outbound", format="bytes", external=False
        )
        reg.clear()
        assert reg.stream_ids() == []
        assert reg.active_stream_count == 0

    def test_get(self) -> None:
        reg = StreamRegistry()
        reg.acquire(
            "s1", "t1", direction="outbound", format="bytes", external=False
        )
        entry = reg.get("s1")
        assert entry is not None
        assert entry.stream_id == "s1"
        assert reg.get("nonexistent") is None
