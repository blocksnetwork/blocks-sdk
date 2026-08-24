"""
Phase 4 Hardening Tests -- External Flow, V1 Boundary, Consumer Experience

Tests the external happy path, error paths, credential scoping,
fail_stream, publish_terminal, shared/embedded stream hardening, consumer
experience (TaskSession -> StreamRef -> StreamClient), direction inversion,
wait_for_stream, auto-close, and v1 boundary enforcement.
"""

from __future__ import annotations

import threading
import time
from typing import Any, Dict, List, Optional
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.stream_context import ExternalStreamObject, StreamObject
from blocks_network.stream_registry import StreamRegistry
from blocks_network.credential_cache import CredentialCache
from blocks_network.stream_ref import StreamRef
from blocks_network.task_session import TaskSession


# ---------------------------------------------------------------------------
# Mock helpers
# ---------------------------------------------------------------------------


class MockPubNub:
    """Minimal PubNub mock for task session tests."""

    def __init__(self):
        self._listener = None
        self._subscribed_channels: List[str] = []
        self.publish_calls: List[Dict[str, Any]] = []
        # Real PubNub stamps each message with a unique timetoken, which
        # TaskSession uses to dedup replays. Hand out a distinct one per message:
        # leaving it unset makes the listener stringify an auto-vivified MagicMock
        # whose repr embeds id(obj), and a GC'd mock's address can be reused — so
        # two distinct messages collide and the second is silently dropped (a
        # flaky, GC/interpreter-version-dependent failure).
        self._next_timetoken = 17_000_000_000_000_000

    def add_listener(self, listener):
        self._listener = listener

    def remove_listener(self, listener):
        self._listener = None

    def subscribe(self):
        return _SubscribeBuilder(self)

    def unsubscribe(self):
        return _UnsubscribeBuilder(self)

    def set_token(self, token):
        pass

    def stop(self):
        pass

    def simulate_message(self, channel: str, message: dict):
        if self._listener and hasattr(self._listener, "message"):
            self._next_timetoken += 1
            evt = MagicMock()
            evt.channel = channel
            evt.message = message
            evt.timetoken = str(self._next_timetoken)
            self._listener.message(self._listener, evt)


class _SubscribeBuilder:
    def __init__(self, pn: MockPubNub):
        self._pn = pn
        self._channels: List[str] = []

    def channels(self, ch_list):
        self._channels = ch_list
        return self

    def with_timetoken(self, tt):
        return self

    def execute(self):
        self._pn._subscribed_channels.extend(self._channels)


class _UnsubscribeBuilder:
    def __init__(self, pn: MockPubNub):
        self._pn = pn
        self._channels: List[str] = []

    def channels(self, ch_list):
        self._channels = ch_list
        return self

    def execute(self):
        for ch in self._channels:
            if ch in self._pn._subscribed_channels:
                self._pn._subscribed_channels.remove(ch)


# Mock StreamDescriptor and helpers from blocks_network.stream
class MockStreamDescriptor:
    def __init__(self, **kwargs):
        self.task_id = kwargs.get("task_id", "")
        self.stream_id = kwargs.get("stream_id", "")
        self.agent_name = kwargs.get("agent_name", "")
        self.channel = kwargs.get("channel", "")
        self.token = kwargs.get("token", "")
        self.agent_direction = kwargs.get("agent_direction", "outbound")
        self.local_direction = kwargs.get("local_direction", "inbound")
        self.format = kwargs.get("format", "bytes")
        self.metadata = kwargs.get("metadata")


class MockStreamClient:
    def __init__(self, **kwargs):
        self._is_active = True
        self.channel = kwargs.get("channel", "")
        self.format = kwargs.get("format", "bytes")
        self.direction = kwargs.get("direction", "outbound")
        self._end_cbs = []

    @property
    def is_active(self):
        return self._is_active

    def write(self, data):
        pass

    def end(self):
        self._is_active = False
        for cb in self._end_cbs:
            cb()

    def on_end(self, cb):
        self._end_cbs.append(cb)

    @classmethod
    def from_descriptor(cls, descriptor, **kwargs):
        return cls(
            channel=descriptor.channel,
            format=descriptor.format,
            direction=descriptor.local_direction,
        )


# =======================================================================
# 1. External Stream Object Tests
# =======================================================================


class TestExternalStreamObject:
    """Tests for the ExternalStreamObject interface."""

    def test_write_throws(self):
        ext = ExternalStreamObject("s1", "stream.echo.s1", "T7A", lambda **kw: None)
        with pytest.raises(RuntimeError, match="external"):
            ext.write("data")

    def test_inbound_throws(self):
        ext = ExternalStreamObject("s1", "stream.echo.s1", "T7A", lambda **kw: None)
        with pytest.raises(RuntimeError, match="external"):
            _ = ext.inbound

    def test_token_available(self):
        ext = ExternalStreamObject("s1", "stream.echo.s1", "T7A-TOKEN", lambda **kw: None)
        assert ext.token == "T7A-TOKEN"

    def test_external_flag(self):
        ext = ExternalStreamObject("s1", "stream.echo.s1", "T7A", lambda **kw: None)
        assert ext.external is True

    def test_is_active_before_end(self):
        ext = ExternalStreamObject("s1", "stream.echo.s1", "T7A", lambda **kw: None)
        assert ext.is_active is True

    def test_is_active_after_end(self):
        ext = ExternalStreamObject("s1", "stream.echo.s1", "T7A", lambda **kw: None)
        ext.end()
        assert ext.is_active is False

    def test_activate_calls_fn(self):
        activated = []
        ext = ExternalStreamObject(
            "s1", "stream.echo.s1", "T7A",
            lambda **kw: activated.append(kw),
        )
        ext.activate(metadata={"key": "value"})
        assert len(activated) == 1
        assert activated[0]["metadata"] == {"key": "value"}

    def test_on_end_callback(self):
        ext = ExternalStreamObject("s1", "stream.echo.s1", "T7A", lambda **kw: None)
        called = []
        ext.on_end(lambda: called.append(True))
        ext.end()
        assert len(called) == 1


# =======================================================================
# 2. Stream Registry Hardening
# =======================================================================


class TestStreamRegistryHardening:
    """Tests for refCount, cancellation isolation, and compatibility checks."""

    def test_shared_stream_refcount(self):
        reg = StreamRegistry()
        entry1, new1, _ = reg.acquire(
            "shared", "task-1", direction="outbound", format="bytes", external=False,
        )
        assert new1 is True
        assert entry1.ref_count == 1

        entry2, new2, _ = reg.acquire(
            "shared", "task-2", direction="outbound", format="bytes", external=False,
        )
        assert new2 is False
        assert entry2.ref_count == 2
        assert entry2 is entry1  # Same entry

    def test_cancellation_isolation(self):
        """One task's release does not destroy a shared stream still used by another."""
        reg = StreamRegistry()
        reg.acquire("shared", "task-1", direction="outbound", format="bytes", external=False)
        reg.acquire("shared", "task-2", direction="outbound", format="bytes", external=False)

        destroyed = reg.release_all_for_task("task-1")
        assert len(destroyed) == 0  # Stream still alive for task-2

        entry = reg.get("shared")
        assert entry is not None
        assert entry.ref_count == 1
        assert "task-2" in entry.task_ids

    def test_last_task_release_destroys_stream(self):
        reg = StreamRegistry()
        reg.acquire("shared", "task-1", direction="outbound", format="bytes", external=False)
        reg.acquire("shared", "task-2", direction="outbound", format="bytes", external=False)

        reg.release_all_for_task("task-1")
        destroyed = reg.release_all_for_task("task-2")
        assert "shared" in [e.stream_id for e in destroyed]
        assert reg.get("shared") is None

    def test_direction_mismatch_raises(self):
        reg = StreamRegistry()
        reg.acquire("shared", "task-1", direction="outbound", format="bytes", external=False)
        with pytest.raises(ValueError, match="direction mismatch"):
            reg.acquire("shared", "task-2", direction="inbound", format="bytes", external=False)

    def test_format_mismatch_raises(self):
        reg = StreamRegistry()
        reg.acquire("shared", "task-1", direction="outbound", format="bytes", external=False)
        with pytest.raises(ValueError, match="format mismatch"):
            reg.acquire("shared", "task-2", direction="outbound", format="events", external=False)

    def test_external_mismatch_raises(self):
        reg = StreamRegistry()
        reg.acquire("shared", "task-1", direction="outbound", format="bytes", external=False)
        with pytest.raises(ValueError, match="embedded and external"):
            reg.acquire("shared", "task-2", direction="outbound", format="bytes", external=True)

    def test_force_remove_preserves_task_ids(self):
        # task_ids on the returned entry is preserved so fail_stream can
        # fan out failed terminals to every ref-holder.
        reg = StreamRegistry()
        reg.acquire("stream1", "task-1", direction="outbound", format="bytes", external=False)
        entry = reg.force_remove("stream1")
        assert entry is not None
        assert entry.task_ids == {"task-1"}
        assert entry.ref_count == 1
        assert reg.get("stream1") is None


# =======================================================================
# 3. Credential Cache Hardening
# =======================================================================


class TestCredentialCacheHardening:
    def test_set_and_get(self):
        cache = CredentialCache()
        cache.set("task-1", owner_id="alice", write_token="wt-1", agent_name="echo")
        entry = cache.get("task-1")
        assert entry is not None
        assert entry.owner_id == "alice"
        assert entry.write_token == "wt-1"

    def test_add_stream(self):
        cache = CredentialCache()
        cache.set("task-1", owner_id="alice", write_token="wt-1", agent_name="echo")
        cache.add_stream("task-1", "s1")
        entry = cache.get("task-1")
        assert "s1" in entry.stream_ids

    def test_remove(self):
        cache = CredentialCache()
        cache.set("task-1", owner_id="alice", write_token="wt-1", agent_name="echo")
        cache.remove("task-1")
        assert cache.get("task-1") is None


# =======================================================================
# 4. TaskSession Consumer Experience
# =======================================================================


class TestTaskSessionConsumer:
    """Tests for TaskSession -> StreamRef -> StreamClient flow."""

    def _make_session(self, pn=None):
        if pn is None:
            pn = MockPubNub()
        return TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sk", "publish_key": "pk"},
        ), pn

    def test_direction_inversion_outbound_to_inbound(self):
        session, pn = self._make_session()
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1", "direction": "outbound",
                    "format": "bytes", "affinity": "dedicated", "token": "t7c-1", "tokenTtlMinutes": 62,
                },
            },
        })
        streams = session.list_streams()
        assert len(streams) == 1
        assert streams[0].descriptor.agent_direction == "outbound"
        assert streams[0].descriptor.local_direction == "inbound"

    def test_direction_inversion_inbound_to_outbound(self):
        session, pn = self._make_session()
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1", "direction": "inbound",
                    "format": "events", "affinity": "dedicated", "token": "t7c-1", "tokenTtlMinutes": 62,
                },
            },
        })
        streams = session.list_streams()
        assert streams[0].descriptor.local_direction == "outbound"

    def test_direction_inversion_bidirectional(self):
        session, pn = self._make_session()
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1", "direction": "bidirectional",
                    "format": "bytes", "affinity": "dedicated", "token": "t7c-1", "tokenTtlMinutes": 62,
                },
            },
        })
        streams = session.list_streams()
        assert streams[0].descriptor.local_direction == "bidirectional"

    def test_format_propagation_bytes(self):
        session, pn = self._make_session()
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1", "direction": "outbound",
                    "format": "bytes", "affinity": "dedicated", "token": "t7c", "tokenTtlMinutes": 62,
                },
            },
        })
        ref = session.list_streams()[0]
        assert ref.descriptor.format == "bytes"

    def test_format_propagation_events(self):
        session, pn = self._make_session()
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1", "direction": "outbound",
                    "format": "events", "affinity": "dedicated", "token": "t7c", "tokenTtlMinutes": 62,
                },
            },
        })
        ref = session.list_streams()[0]
        assert ref.descriptor.format == "events"

    def test_invalid_format_skipped(self):
        session, pn = self._make_session()
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1", "direction": "outbound",
                    "format": "invalid", "affinity": "dedicated", "token": "t7c", "tokenTtlMinutes": 62,
                },
            },
        })
        assert len(session.list_streams()) == 0

    def test_wait_for_stream_resolves(self):
        session, pn = self._make_session()

        # Start waiting in background
        result_holder = [None]
        error_holder = [None]

        def _wait():
            try:
                result_holder[0] = session.wait_for_stream("s1", timeout=5)
            except Exception as e:
                error_holder[0] = e

        t = threading.Thread(target=_wait)
        t.start()

        # Small delay then announce stream
        time.sleep(0.05)
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1", "direction": "outbound",
                    "format": "bytes", "affinity": "dedicated", "token": "t7c", "tokenTtlMinutes": 62,
                    "metadata": {"unit": "celsius"},
                },
            },
        })

        t.join(timeout=5)
        assert error_holder[0] is None
        assert result_holder[0] is not None
        assert result_holder[0].descriptor.stream_id == "s1"
        assert result_holder[0].descriptor.metadata == {"unit": "celsius"}

    def test_wait_for_stream_where_resolves(self):
        session, pn = self._make_session()

        result_holder = [None]

        def _wait():
            result_holder[0] = session.wait_for_stream_where(
                lambda ref: ref.descriptor.metadata and ref.descriptor.metadata.get("kind") == "temp",
                timeout=5,
            )

        t = threading.Thread(target=_wait)
        t.start()

        time.sleep(0.05)
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "temp": {
                    "channel": "stream.echo.temp", "direction": "outbound",
                    "format": "bytes", "affinity": "dedicated", "token": "t7c", "tokenTtlMinutes": 62,
                    "metadata": {"kind": "temp"},
                },
            },
        })

        t.join(timeout=5)
        assert result_holder[0] is not None
        assert result_holder[0].descriptor.stream_id == "temp"

    def test_terminal_auto_close(self):
        session, pn = self._make_session()
        terminal_cb = MagicMock()
        session.on_terminal(terminal_cb)

        pn.simulate_message("u.alice.task-1", {
            "type": "terminal", "taskId": "task-1", "state": "completed",
        })
        assert session.is_closed

    def test_terminal_rejects_pending_waiters(self):
        session, pn = self._make_session()

        result_holder = [None]
        error_holder = [None]

        def _wait():
            try:
                result_holder[0] = session.wait_for_stream("s1", timeout=5)
            except Exception as e:
                error_holder[0] = e

        t = threading.Thread(target=_wait)
        t.start()

        time.sleep(0.05)
        pn.simulate_message("u.alice.task-1", {
            "type": "terminal", "taskId": "task-1", "state": "completed",
        })

        t.join(timeout=5)
        assert error_holder[0] is not None or result_holder[0] is None

    def test_on_stream_fires_for_each(self):
        session, pn = self._make_session()
        cb = MagicMock()
        session.on_stream(cb)

        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {"channel": "c1", "direction": "outbound", "format": "bytes", "affinity": "dedicated", "token": "t1", "tokenTtlMinutes": 62},
            },
        })
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s2": {"channel": "c2", "direction": "inbound", "format": "events", "affinity": "dedicated", "token": "t2", "tokenTtlMinutes": 62},
            },
        })

        assert cb.call_count == 2

    def test_events_after_close_not_delivered(self):
        session, pn = self._make_session()
        cb = MagicMock()
        session.on_progress(cb)
        session.close()
        pn.simulate_message("u.alice.task-1", {
            "type": "progress", "taskId": "task-1",
        })
        cb.assert_not_called()

    def test_no_duplicate_streams(self):
        session, pn = self._make_session()
        cb = MagicMock()
        session.on_stream(cb)

        event = {
            "type": "progress", "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {"channel": "c1", "direction": "outbound", "format": "bytes", "affinity": "dedicated", "token": "t1", "tokenTtlMinutes": 62},
            },
        }
        pn.simulate_message("u.alice.task-1", event)
        pn.simulate_message("u.alice.task-1", event)
        assert cb.call_count == 1

    def test_close_is_idempotent(self):
        session, _ = self._make_session()
        session.close()
        session.close()  # Should not raise
        assert session.is_closed

    def test_wait_for_stream_rejects_after_close(self):
        session, _ = self._make_session()
        session.close()
        with pytest.raises(RuntimeError, match="closed"):
            session.wait_for_stream(timeout=0.01)


# =======================================================================
# 5. StreamRef Hardening
# =======================================================================


class TestStreamRefHardening:
    """Tests for StreamRef open() idempotency and descriptor completeness."""

    def _make_ref(self, **desc_kwargs):
        defaults = {
            "task_id": "t1", "stream_id": "s1", "agent_name": "echo",
            "channel": "stream.echo.s1", "token": "tk",
            "agent_direction": "outbound", "local_direction": "inbound",
            "format": "bytes",
            "affinity": "dedicated",
        }
        defaults.update(desc_kwargs)
        # Use the real StreamDescriptor from blocks_network.stream if available
        try:
            from blocks_network.stream import StreamDescriptor
            desc = StreamDescriptor(**defaults)
        except ImportError:
            desc = MockStreamDescriptor(**defaults)
        return StreamRef(desc, {"subscribe_key": "sk", "publish_key": "pk"})

    def test_descriptor_fields(self):
        ref = self._make_ref(metadata={"key": "val"})
        assert ref.descriptor.task_id == "t1"
        assert ref.descriptor.stream_id == "s1"
        assert ref.descriptor.agent_name == "echo"
        assert ref.descriptor.channel == "stream.echo.s1"
        assert ref.descriptor.token == "tk"
        assert ref.descriptor.agent_direction == "outbound"
        assert ref.descriptor.local_direction == "inbound"
        assert ref.descriptor.format == "bytes"
        assert ref.descriptor.metadata == {"key": "val"}

    def test_is_open_false_before_open(self):
        ref = self._make_ref()
        assert ref.is_open is False
