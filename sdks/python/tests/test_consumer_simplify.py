"""Tests for Phase A consumer simplification (Fixes 1-6).

Covers:
- Fix 1: Auto-populate owner_id from authenticated identity
- Fix 2: wait_for_terminal for pre-closed/already-terminal sessions
- Fix 3: Part helpers (tested separately in test_part_helpers.py)
- Fix 4: Typed event properties on TaskEvent
- Fix 5: save_artifacts convenience method
- Fix 6: Resource management (context managers, stream cleanup in close)
"""

from __future__ import annotations

import os
import tempfile
import threading
import time
from pathlib import Path
from unittest.mock import MagicMock, patch, call

import pytest

from blocks_network.task_client import (
    SendMessageRequestPart,
    TaskClient,
)
from blocks_network.task_session import (
    TERMINAL_STATES,
    TaskEvent,
    TaskSession,
)
from blocks_network.types import ArtifactRef


# ============================================================================
# Test helpers
# ============================================================================


def _make_mock_pubnub() -> MagicMock:
    """Create a mock PubNub client matching the real SDK builder pattern."""
    pn = MagicMock()
    pn._listeners = []

    def _add_listener(listener):
        pn._listeners.append(listener)

    pn.add_listener.side_effect = _add_listener
    pn.remove_listener.side_effect = lambda l: (
        pn._listeners.remove(l) if l in pn._listeners else None
    )

    sub_chain = MagicMock()
    sub_chain.channels.return_value = sub_chain
    sub_chain.execute.return_value = None
    pn.subscribe.return_value = sub_chain

    unsub_chain = MagicMock()
    unsub_chain.channels.return_value = unsub_chain
    unsub_chain.execute.return_value = None
    pn.unsubscribe.return_value = unsub_chain

    # time().sync() -> result with .result.timetoken
    time_result = MagicMock()
    time_result.result.timetoken = "17000000000000000"
    pn.time.return_value.sync.return_value = time_result

    # fetch_messages() builder pattern
    fetch_chain = MagicMock()
    fetch_chain.channels.return_value = fetch_chain
    fetch_chain.maximum_per_channel.return_value = fetch_chain
    fetch_chain.start.return_value = fetch_chain
    fetch_result = MagicMock()
    fetch_result.result.channels = {}
    fetch_chain.sync.return_value = fetch_result
    pn.fetch_messages.return_value = fetch_chain

    return pn


def _simulate_message(pn: MagicMock, channel: str, message: dict) -> None:
    """Simulate a PubNub message event."""
    event = MagicMock()
    event.channel = channel
    event.message = message
    for listener in list(pn._listeners):
        if hasattr(listener, "message"):
            listener.message(pn, event)


def _make_session(**kwargs) -> TaskSession:
    """Create a TaskSession with reasonable defaults."""
    defaults = {
        "task_id": "task-1",
        "owner_id": "alice",
        "read_token": "t4",
        "agent_name": "echo",
        "sdk_options": {"subscribe_key": "sub", "publish_key": "pub"},
    }
    defaults.update(kwargs)
    return TaskSession(**defaults)


# ============================================================================
# Fix 1: Auto-populate owner_id
# ============================================================================


class TestFix1AutoPopulateOwnerId:
    """Fix 1: owner_id auto-population from authenticated identity."""

    def test_send_message_uses_default_owner_id_when_not_provided(self) -> None:
        """send_message falls back to _default_owner_id when owner_id is empty."""
        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            default_owner_id="auto-user-123",
        )

        # Mock call_rpc to return a valid response
        mock_response = {
            "taskId": "task-abc",
            "idempotent": False,
            "extensions": {
                "blocks": {
                    "readToken": "tok",
                    "streamChannels": {"status": "u.auto-user-123.task-abc"},
                }
            },
        }

        mock_pn = _make_mock_pubnub()
        client._create_session_pubnub_factory = lambda: mock_pn

        with patch("blocks_network.task_client.call_rpc", return_value=mock_response) as mock_rpc:
            session = client.send_message(agent_name="echo")

            # Verify the RPC was called with the default owner ID
            rpc_params = mock_rpc.call_args[0][2]
            assert rpc_params["ownerId"] == "auto-user-123"

            # Verify the session got the correct owner_id
            assert session.owner_id == "auto-user-123"

    def test_explicit_owner_id_overrides_default(self) -> None:
        """Explicit owner_id takes precedence over _default_owner_id."""
        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            default_owner_id="auto-user-123",
        )

        mock_response = {
            "taskId": "task-abc",
            "idempotent": False,
            "extensions": {
                "blocks": {
                    "readToken": "tok",
                    "streamChannels": {"status": "u.explicit-user.task-abc"},
                }
            },
        }

        mock_pn = _make_mock_pubnub()
        client._create_session_pubnub_factory = lambda: mock_pn

        with patch("blocks_network.task_client.call_rpc", return_value=mock_response) as mock_rpc:
            session = client.send_message(
                agent_name="echo", owner_id="explicit-user"
            )

            rpc_params = mock_rpc.call_args[0][2]
            assert rpc_params["ownerId"] == "explicit-user"

    def test_send_message_empty_when_no_default(self) -> None:
        """Without default_owner_id, owner_id falls back to empty string."""
        client = TaskClient(subscribe_key="sub-key", billing_mode="free")

        mock_response = {
            "taskId": "task-abc",
            "idempotent": False,
            "extensions": {
                "blocks": {
                    "readToken": "tok",
                    "streamChannels": {"status": "u.someone.task-abc"},
                }
            },
        }

        mock_pn = _make_mock_pubnub()
        client._create_session_pubnub_factory = lambda: mock_pn

        with patch("blocks_network.task_client.call_rpc", return_value=mock_response) as mock_rpc:
            client.send_message(agent_name="echo")
            rpc_params = mock_rpc.call_args[0][2]
            assert rpc_params["ownerId"] == ""

    def test_create_wires_consumer_auth_user_id(self) -> None:
        """TaskClient.create() sets _default_owner_id from ConsumerAuth."""
        mock_consumer_auth = MagicMock()
        mock_consumer_auth.get_user_id.return_value = "jwt-sub-claim"
        mock_consumer_auth.get_auth_header.return_value = "Bearer tok"

        with (
            patch("blocks_network.cdm_config.fetch_cdm_config") as mock_cdm,
            patch("blocks_network.consumer_auth.ConsumerAuth", return_value=mock_consumer_auth),
        ):
            # Set up CDM mock
            mock_keyset = MagicMock()
            mock_keyset.subscribe_key = "sub-key"
            mock_keyset.publish_key = "pub-key"
            mock_cdm.return_value = MagicMock(
                playground=mock_keyset,
                api=MagicMock(base_url="https://api.example.com"),
            )

            client = TaskClient.create(billing_mode="free", api_key="test-key")

            assert client._default_owner_id == "jwt-sub-claim"


# ============================================================================
# Fix 2: wait_for_terminal for pre-closed sessions
# ============================================================================


class TestFix2WaitForTerminalPreClosed:
    """Fix 2: wait_for_terminal resolves immediately for pre-closed sessions."""

    def test_pre_closed_session_resolves_immediately(self) -> None:
        """Pre-closed session returns synthetic terminal event immediately."""
        session = _make_session(
            pubnub=None,
            pre_closed_state="completed",
        )

        event = session.wait_for_terminal(timeout=1)
        assert event.type == "terminal"
        assert event.task_id == "task-1"
        assert event.state == "completed"

    def test_pre_closed_failed_resolves_immediately(self) -> None:
        session = _make_session(
            pubnub=None,
            pre_closed_state="failed",
        )

        event = session.wait_for_terminal(timeout=1)
        assert event.state == "failed"

    def test_pre_closed_canceled_resolves_immediately(self) -> None:
        session = _make_session(
            pubnub=None,
            pre_closed_state="canceled",
        )

        event = session.wait_for_terminal(timeout=1)
        assert event.state == "canceled"

    def test_terminal_connect_session_resolves_immediately(self) -> None:
        """Terminal connect() sessions (state set, skip_subscription) resolve immediately."""
        pn = _make_mock_pubnub()
        session = _make_session(
            pubnub=pn,
            state="completed",
            skip_subscription=True,
        )

        event = session.wait_for_terminal(timeout=1)
        assert event.type == "terminal"
        assert event.state == "completed"

    def test_live_session_still_works(self) -> None:
        """Normal live sessions still wait for and receive terminal events."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        def _send_terminal():
            time.sleep(0.05)
            _simulate_message(pn, "u.alice.task-1", {
                "type": "terminal",
                "taskId": "task-1",
                "state": "completed",
            })

        t = threading.Thread(target=_send_terminal)
        t.start()

        event = session.wait_for_terminal(timeout=5)
        assert event.type == "terminal"
        assert event.state == "completed"
        t.join()

    def test_timeout_still_works(self) -> None:
        """Timeout still raises for active sessions with no terminal event."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        with pytest.raises(TimeoutError, match="Timed out"):
            session.wait_for_terminal(timeout=0.1)

    def test_non_terminal_state_does_not_resolve_immediately(self) -> None:
        """A session with state='working' should NOT resolve immediately."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn, state="working")

        with pytest.raises(TimeoutError):
            session.wait_for_terminal(timeout=0.1)


# ============================================================================
# Fix 4: Typed event properties on TaskEvent
# ============================================================================


class TestFix4TypedEventProperties:
    """Fix 4: Typed properties on TaskEvent."""

    def test_message_property(self) -> None:
        event = TaskEvent({"type": "progress", "taskId": "t1", "message": "Working..."})
        assert event.message == "Working..."

    def test_message_property_none(self) -> None:
        event = TaskEvent({"type": "progress", "taskId": "t1"})
        assert event.message is None

    def test_progress_property(self) -> None:
        event = TaskEvent({"type": "progress", "taskId": "t1", "progress": 0.75})
        assert event.progress == 0.75

    def test_progress_property_none(self) -> None:
        event = TaskEvent({"type": "progress", "taskId": "t1"})
        assert event.progress is None

    def test_state_property(self) -> None:
        event = TaskEvent({"type": "terminal", "taskId": "t1", "state": "completed"})
        assert event.state == "completed"

    def test_state_property_none(self) -> None:
        event = TaskEvent({"type": "progress", "taskId": "t1"})
        assert event.state is None

    def test_artifact_ref_property_from_dict(self) -> None:
        event = TaskEvent({
            "type": "artifact",
            "taskId": "t1",
            "artifactRef": {
                "kind": "inline",
                "mimeType": "text/plain",
                "size": 5,
                "data": "aGVsbG8=",
                "fileName": "test.txt",
            },
        })
        ref = event.artifact_ref
        assert isinstance(ref, ArtifactRef)
        assert ref.kind == "inline"
        assert ref.mime_type == "text/plain"
        assert ref.size == 5
        assert ref.data == "aGVsbG8="
        assert ref.file_name == "test.txt"

    def test_artifact_ref_property_none(self) -> None:
        event = TaskEvent({"type": "progress", "taskId": "t1"})
        assert event.artifact_ref is None

    def test_artifact_ref_property_already_artifact_ref(self) -> None:
        ref = ArtifactRef(kind="file", mime_type="application/pdf", size=100)
        event = TaskEvent({
            "type": "artifact",
            "taskId": "t1",
            "artifactRef": ref,
        })
        assert event.artifact_ref is ref

    def test_raw_still_works(self) -> None:
        data = {"type": "progress", "taskId": "t1", "custom": "field"}
        event = TaskEvent(data)
        assert event.raw is data
        assert event.raw["custom"] == "field"

    def test_typed_properties_on_live_callback(self) -> None:
        """Typed properties work on events received from live callbacks."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        received_messages: list = []
        session.on_progress(lambda e: received_messages.append(e.message))

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "message": "50% done",
        })

        assert received_messages == ["50% done"]


# ============================================================================
# Fix 5: save_artifacts
# ============================================================================


class TestFix5SaveArtifacts:
    """Fix 5: save_artifacts convenience method."""

    def test_save_artifacts_writes_files(self) -> None:
        """save_artifacts downloads and writes all artifacts to disk."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        # Manually add artifacts
        session._artifacts = [
            ArtifactRef(kind="inline", data="aGVsbG8=", mime_type="text/plain", size=5, file_name="hello.txt"),
            ArtifactRef(kind="inline", data="d29ybGQ=", mime_type="text/plain", size=5, file_name="world.txt"),
        ]

        # Mock download_artifact to return DownloadedArtifact
        from blocks_network.artifacts import DownloadedArtifact

        def mock_download(ref):
            import base64
            return DownloadedArtifact(
                data=base64.b64decode(ref.data),
                mime_type=ref.mime_type,
                file_name=ref.file_name,
            )

        session.download_artifact = mock_download

        with tempfile.TemporaryDirectory() as tmpdir:
            paths = session.save_artifacts(tmpdir)

            assert len(paths) == 2
            assert os.path.basename(paths[0]) == "hello.txt"
            assert os.path.basename(paths[1]) == "world.txt"
            assert Path(paths[0]).read_bytes() == b"hello"
            assert Path(paths[1]).read_bytes() == b"world"

    def test_save_artifacts_creates_directory(self) -> None:
        """save_artifacts creates the target directory if it does not exist."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)
        session._artifacts = []

        with tempfile.TemporaryDirectory() as tmpdir:
            nested = os.path.join(tmpdir, "a", "b", "c")
            paths = session.save_artifacts(nested)
            assert paths == []
            assert os.path.isdir(nested)

    def test_save_artifacts_uses_fallback_name(self) -> None:
        """Artifacts without file_name get fallback names like artifact-0."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        session._artifacts = [
            ArtifactRef(kind="inline", data="AA==", mime_type="application/octet-stream", size=1),
        ]

        from blocks_network.artifacts import DownloadedArtifact

        session.download_artifact = lambda ref: DownloadedArtifact(
            data=b"\x00",
            mime_type="application/octet-stream",
            file_name=None,
        )

        with tempfile.TemporaryDirectory() as tmpdir:
            paths = session.save_artifacts(tmpdir)
            assert len(paths) == 1
            assert os.path.basename(paths[0]) == "artifact-0"

    def test_save_artifacts_returns_correct_paths(self) -> None:
        """Returned paths are absolute and point to written files."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        session._artifacts = [
            ArtifactRef(kind="inline", data="dGVzdA==", mime_type="text/plain", size=4, file_name="test.txt"),
        ]

        from blocks_network.artifacts import DownloadedArtifact

        session.download_artifact = lambda ref: DownloadedArtifact(
            data=b"test",
            mime_type="text/plain",
            file_name="test.txt",
        )

        with tempfile.TemporaryDirectory() as tmpdir:
            paths = session.save_artifacts(tmpdir)
            assert len(paths) == 1
            assert os.path.exists(paths[0])
            assert Path(paths[0]).read_bytes() == b"test"


# ============================================================================
# Fix 6: Resource management
# ============================================================================


class TestFix6ResourceManagement:
    """Fix 6: Context managers and stream cleanup."""

    # -- TaskClient context manager --

    def test_task_client_context_manager(self) -> None:
        """TaskClient context manager calls destroy() on exit."""
        client = TaskClient(subscribe_key="sub-key", billing_mode="free")
        client.destroy = MagicMock()

        with client as c:
            assert c is client
        client.destroy.assert_called_once()

    def test_task_client_context_manager_on_exception(self) -> None:
        """TaskClient context manager calls destroy() even on exception."""
        client = TaskClient(subscribe_key="sub-key", billing_mode="free")
        client.destroy = MagicMock()

        with pytest.raises(ValueError):
            with client:
                raise ValueError("test error")
        client.destroy.assert_called_once()

    # -- TaskSession context manager --

    def test_task_session_context_manager(self) -> None:
        """TaskSession context manager calls close() on exit."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        with session as s:
            assert s is session
            assert not session.is_closed
        assert session.is_closed

    def test_task_session_context_manager_on_exception(self) -> None:
        """TaskSession context manager calls close() even on exception."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        with pytest.raises(ValueError):
            with session:
                raise ValueError("test error")
        assert session.is_closed

    # -- Stream cleanup in close() --

    def test_close_ends_open_stream_clients(self) -> None:
        """session.close() ends all active stream clients."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        # Create mock stream clients
        client1 = MagicMock()
        client1.is_active = True
        client2 = MagicMock()
        client2.is_active = True
        client3 = MagicMock()
        client3.is_active = False  # already ended

        session._open_stream_clients = {client1, client2, client3}
        session.close()

        client1.end.assert_called_once()
        client2.end.assert_called_once()
        client3.end.assert_not_called()  # already inactive
        assert len(session._open_stream_clients) == 0

    def test_close_handles_stream_end_error(self) -> None:
        """close() swallows errors from stream client end()."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        client1 = MagicMock()
        client1.is_active = True
        client1.end.side_effect = RuntimeError("stream error")

        session._open_stream_clients = {client1}

        # Should not raise
        session.close()
        assert session.is_closed

    def test_close_idempotent(self) -> None:
        """Calling close() twice does not error."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        session.close()
        session.close()  # second call should be no-op
        assert session.is_closed

    def test_context_manager_ends_streams(self) -> None:
        """Context manager exit ends stream clients via close()."""
        pn = _make_mock_pubnub()
        session = _make_session(pubnub=pn)

        client1 = MagicMock()
        client1.is_active = True
        session._open_stream_clients = {client1}

        with session:
            pass

        client1.end.assert_called_once()
        assert session.is_closed
