"""Tests for the credential cache module."""

from __future__ import annotations

from blocks_network.credential_cache import CredentialCache


class TestCredentialCache:
    """Credential cache unit tests."""

    def test_set_and_get(self) -> None:
        cache = CredentialCache()
        cache.set("task-1", owner_id="alice", write_token="t2-abc", agent_name="echo")
        creds = cache.get("task-1")
        assert creds is not None
        assert creds.owner_id == "alice"
        assert creds.write_token == "t2-abc"
        assert creds.agent_name == "echo"
        assert creds.stream_ids == set()

    def test_get_missing(self) -> None:
        cache = CredentialCache()
        assert cache.get("nonexistent") is None

    def test_has(self) -> None:
        cache = CredentialCache()
        assert not cache.has("task-1")
        cache.set("task-1", owner_id="bob", write_token="t2", agent_name="x")
        assert cache.has("task-1")

    def test_add_stream(self) -> None:
        cache = CredentialCache()
        cache.set("task-1", owner_id="alice", write_token="t2", agent_name="echo")
        cache.add_stream("task-1", "stream-a")
        cache.add_stream("task-1", "stream-b")
        creds = cache.get("task-1")
        assert creds is not None
        assert creds.stream_ids == {"stream-a", "stream-b"}

    def test_add_stream_missing_task(self) -> None:
        cache = CredentialCache()
        cache.add_stream("nonexistent", "stream-a")
        # Should not raise, just no-op

    def test_remove(self) -> None:
        cache = CredentialCache()
        cache.set("task-1", owner_id="alice", write_token="t2", agent_name="echo")
        cache.remove("task-1")
        assert cache.get("task-1") is None
        assert not cache.has("task-1")

    def test_remove_missing(self) -> None:
        cache = CredentialCache()
        cache.remove("nonexistent")  # Should not raise

    def test_task_ids(self) -> None:
        cache = CredentialCache()
        cache.set("task-1", owner_id="a", write_token="t1", agent_name="x")
        cache.set("task-2", owner_id="b", write_token="t2", agent_name="y")
        ids = cache.task_ids()
        assert set(ids) == {"task-1", "task-2"}

    def test_clear(self) -> None:
        cache = CredentialCache()
        cache.set("task-1", owner_id="a", write_token="t1", agent_name="x")
        cache.set("task-2", owner_id="b", write_token="t2", agent_name="y")
        cache.clear()
        assert cache.task_ids() == []
        assert not cache.has("task-1")

    def test_update_existing(self) -> None:
        cache = CredentialCache()
        cache.set("task-1", owner_id="alice", write_token="old", agent_name="echo")
        cache.add_stream("task-1", "stream-x")
        cache.set("task-1", owner_id="alice", write_token="new", agent_name="echo")
        creds = cache.get("task-1")
        assert creds is not None
        assert creds.write_token == "new"
        # Stream IDs should be preserved on update
        assert "stream-x" in creds.stream_ids
