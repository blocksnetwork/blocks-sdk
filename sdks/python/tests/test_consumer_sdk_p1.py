"""
Tests for Consumer SDK Phase 1 features:
- P1-1: download_artifact (inline + file)
- P1-2: connect() (active + terminal tasks)
- P1-3: Callback error handling (on_error routing)
- P1-5: TaskClient.create() factory
"""

from __future__ import annotations

import base64
import json
import logging
import os
import threading
import time
from io import BytesIO
from unittest.mock import MagicMock, patch, PropertyMock

import pytest

from blocks_network.artifacts import (
    DownloadedArtifact,
    decode_inline_artifact,
    download_artifact,
)
from blocks_network.auth_provider import StaticAuthProvider
from blocks_network.task_client import (
    TaskClient,
    TaskEventCallbacks,
    subscribe_to_task,
    _dispatch_event,
)
from blocks_network.task_session import (
    CallbackErrorContext,
    TaskEvent,
    TaskSession,
)
from blocks_network.types import ArtifactRef
from blocks_network.stream_ref import StreamRef


# ============================================================================
# Helpers
# ============================================================================


def _make_mock_pubnub() -> MagicMock:
    """Create a mock PubNub with subscribe/unsubscribe builder chains."""
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

    # download_file chain
    dl_chain = MagicMock()
    for method in ("channel", "file_id", "file_name"):
        getattr(dl_chain, method).side_effect = lambda *a, _c=dl_chain, **kw: _c
    download_result = MagicMock()
    download_result.result.data = b"mock-file-content"
    dl_chain.sync.return_value = download_result
    pn.download_file.return_value = dl_chain

    # fetch_messages chain
    fm_chain = MagicMock()
    fm_chain.channels.return_value = fm_chain
    fm_chain.maximum_per_channel.return_value = fm_chain
    fm_result = MagicMock()
    fm_result.result.channels = {}
    fm_chain.sync.return_value = fm_result
    pn.fetch_messages.return_value = fm_chain

    # time() chain
    time_result = MagicMock()
    time_result.result.timetoken = 17000000000000000
    time_chain = MagicMock()
    time_chain.sync.return_value = time_result
    pn.time.return_value = time_chain

    pn.set_token = MagicMock()
    pn.stop = MagicMock()

    return pn


def _simulate_message(pn: MagicMock, channel: str, message: dict) -> None:
    """Simulate a PubNub message event through all listeners."""
    event = MagicMock()
    event.channel = channel
    event.message = message
    event.timetoken = str(int(time.time() * 10_000_000))
    for listener in list(pn._listeners):
        if hasattr(listener, "message"):
            listener.message(pn, event)


def _make_session(pn=None, **kwargs):
    """Create a TaskSession with reasonable defaults."""
    if pn is None:
        pn = _make_mock_pubnub()
    defaults = dict(
        task_id="task-1",
        owner_id="alice",
        read_token="t4-token",
        agent_name="echo",
        pubnub=pn,
        sdk_options={"subscribe_key": "sub-key", "publish_key": "pub-key"},
    )
    defaults.update(kwargs)
    return TaskSession(**defaults), pn


# ============================================================================
# P1-1: download_artifact tests
# ============================================================================


class TestDownloadArtifact:
    def test_inline_artifact(self) -> None:
        """Inline artifact decodes base64 data directly."""
        raw_data = b"hello world"
        ref = ArtifactRef(
            kind="inline",
            data=base64.b64encode(raw_data).decode("ascii"),
            mime_type="text/plain",
            file_name="hello.txt",
            size=len(raw_data),
        )
        pn = MagicMock()
        result = download_artifact(ref, pn)

        assert isinstance(result, DownloadedArtifact)
        assert result.data == raw_data
        assert result.mime_type == "text/plain"
        assert result.file_name == "hello.txt"

    def test_file_artifact(self) -> None:
        """File artifact calls pubnub.download_file()."""
        ref = ArtifactRef(
            kind="file",
            channel="u.org1.task-1",
            file_id="f-123",
            file_name="data.csv",
            mime_type="text/csv",
            size=1024,
        )
        pn = _make_mock_pubnub()
        result = download_artifact(ref, pn)

        assert isinstance(result, DownloadedArtifact)
        assert result.data == b"mock-file-content"
        assert result.mime_type == "text/csv"
        assert result.file_name == "data.csv"
        pn.download_file.assert_called_once()

    def test_inline_missing_data_raises(self) -> None:
        """Inline ref without data raises ValueError."""
        ref = ArtifactRef(kind="inline", mime_type="text/plain")
        with pytest.raises(ValueError, match="missing 'data'"):
            download_artifact(ref, MagicMock())

    def test_file_missing_channel_raises(self) -> None:
        """File ref without channel raises ValueError."""
        ref = ArtifactRef(kind="file", file_id="f-1", file_name="x.txt")
        with pytest.raises(ValueError, match="missing required fields"):
            download_artifact(ref, MagicMock())

    def test_file_missing_file_id_raises(self) -> None:
        """File ref without file_id raises ValueError."""
        ref = ArtifactRef(kind="file", channel="ch", file_name="x.txt")
        with pytest.raises(ValueError, match="missing required fields"):
            download_artifact(ref, MagicMock())

    def test_file_missing_file_name_raises(self) -> None:
        """File ref without file_name raises ValueError."""
        ref = ArtifactRef(kind="file", channel="ch", file_id="f-1")
        with pytest.raises(ValueError, match="missing required fields"):
            download_artifact(ref, MagicMock())

    def test_unknown_kind_raises(self) -> None:
        """Unknown artifact kind raises ValueError."""
        ref = ArtifactRef(kind="unknown")
        with pytest.raises(ValueError, match="Unknown artifact kind"):
            download_artifact(ref, MagicMock())


class TestDecodeInlineArtifact:
    def test_decode_inline(self) -> None:
        ref = ArtifactRef(
            kind="inline",
            data=base64.b64encode(b"test data").decode("ascii"),
        )
        result = decode_inline_artifact(ref)
        assert result == b"test data"

    def test_decode_no_data_raises(self) -> None:
        ref = ArtifactRef(kind="inline")
        with pytest.raises(ValueError, match="missing 'data'"):
            decode_inline_artifact(ref)


class TestTaskSessionDownloadArtifact:
    def test_download_with_active_pubnub(self) -> None:
        """download_artifact delegates to active PubNub client."""
        session, pn = _make_session()
        ref = ArtifactRef(
            kind="inline",
            data=base64.b64encode(b"inline data").decode("ascii"),
            mime_type="text/plain",
        )
        result = session.download_artifact(ref)
        assert result.data == b"inline data"

    @patch("blocks_network.pubnub_client.create_pubnub_client")
    def test_download_on_preclosed_session(self, mock_create) -> None:
        """Pre-closed session creates temporary PubNub for download."""
        temp_pn = _make_mock_pubnub()
        mock_create.return_value = temp_pn

        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4-token",
            agent_name="echo",
            pubnub=None,
            sdk_options={"subscribe_key": "sub-key", "publish_key": "pub-key"},
            pre_closed_state="completed",
        )

        ref = ArtifactRef(
            kind="file",
            channel="u.org1.task-1",
            file_id="f-1",
            file_name="out.bin",
            mime_type="application/octet-stream",
            size=100,
        )
        result = session.download_artifact(ref)
        assert result.data == b"mock-file-content"
        # Temp client should be stopped after download
        temp_pn.stop.assert_called_once()


# ============================================================================
# P1-3: Callback error handling tests
# ============================================================================


class TestCallbackErrorRouting:
    def test_on_error_receives_callback_error(self) -> None:
        """on_error handler is called when a callback throws."""
        session, pn = _make_session()
        errors = []

        def _failing_cb(event):
            raise RuntimeError("callback exploded")

        def _error_handler(err, ctx):
            errors.append((err, ctx))

        session.on_progress(_failing_cb)
        session.on_error(_error_handler)

        _simulate_message(pn, session.status_channel, {
            "type": "progress",
            "taskId": "task-1",
            "progress": 50,
        })

        assert len(errors) == 1
        err, ctx = errors[0]
        assert isinstance(err, RuntimeError)
        assert str(err) == "callback exploded"
        assert ctx.entry_point == "taskSession"
        assert ctx.callback_type == "onProgress"

    def test_on_error_for_each_callback_type(self) -> None:
        """on_error correctly identifies each callback type."""
        session, pn = _make_session()
        errors = []

        def _error_handler(err, ctx):
            errors.append(ctx.callback_type)

        session.on_error(_error_handler)

        # onEvent
        session.on_event(lambda e: 1 / 0)
        _simulate_message(pn, session.status_channel, {
            "type": "progress", "taskId": "task-1",
        })
        assert "onEvent" in errors

        # onArtifact
        session.on_artifact(lambda e: 1 / 0)
        _simulate_message(pn, session.status_channel, {
            "type": "artifact", "taskId": "task-1",
        })
        assert "onArtifact" in errors

    def test_warn_logged_when_no_on_error(self, caplog) -> None:
        """Warning is logged when callback throws and no on_error registered."""
        session, pn = _make_session()

        session.on_progress(lambda e: 1 / 0)

        with caplog.at_level(logging.WARNING, logger="blocks_network.task_session"):
            _simulate_message(pn, session.status_channel, {
                "type": "progress", "taskId": "task-1",
            })

        assert any("callback error" in r.message for r in caplog.records)

    def test_on_error_handler_throws_no_infinite_loop(self) -> None:
        """If on_error handler itself throws, no infinite loop occurs."""
        session, pn = _make_session()

        def _failing_cb(event):
            raise ValueError("cb error")

        def _failing_error_handler(err, ctx):
            raise RuntimeError("error handler exploded")

        session.on_progress(_failing_cb)
        session.on_error(_failing_error_handler)

        # Should not raise or loop
        _simulate_message(pn, session.status_channel, {
            "type": "progress", "taskId": "task-1",
        })

    def test_remaining_callbacks_fire_after_error(self) -> None:
        """Error in first callback does not prevent subsequent callbacks."""
        session, pn = _make_session()
        results = []

        session.on_progress(lambda e: 1 / 0)  # This one throws
        session.on_progress(lambda e: results.append("second"))  # This should still fire

        _simulate_message(pn, session.status_channel, {
            "type": "progress", "taskId": "task-1",
        })

        assert "second" in results

    def test_on_error_unsubscribe(self) -> None:
        """on_error handler can be unregistered."""
        session, pn = _make_session()
        errors = []

        unsub = session.on_error(lambda err, ctx: errors.append(1))
        session.on_progress(lambda e: 1 / 0)

        _simulate_message(pn, session.status_channel, {
            "type": "progress", "taskId": "task-1",
        })
        assert len(errors) == 1

        unsub()
        _simulate_message(pn, session.status_channel, {
            "type": "progress", "taskId": "task-1",
        })
        # After unsub, errors list should still be 1 (no new error handler)
        assert len(errors) == 1

    def test_stream_predicate_error_routing(self) -> None:
        """Stream predicate errors are routed through on_error."""
        session, pn = _make_session()
        errors = []

        session.on_error(lambda err, ctx: errors.append(ctx.callback_type))

        # First announce a stream so _resolve_waiters gets called
        # Set up a waiter with a failing predicate
        evt = threading.Event()
        waiter = {
            "event": evt,
            "stream_id": None,
            "predicate": lambda ref: 1 / 0,
            "result": None,
        }
        with session._waiter_lock:
            session._stream_waiters.append(waiter)

        # Simulate stream_started
        _simulate_message(pn, session.status_channel, {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "channel": "stream.echo.s1",
                    "token": "tok-1",
                },
            },
        })

        assert "streamPredicate" in errors

    def test_on_stream_callback_error_routing(self) -> None:
        """on_stream callback errors are routed through on_error."""
        session, pn = _make_session()
        errors = []

        session.on_error(lambda err, ctx: errors.append(ctx.callback_type))
        session.on_stream(lambda ref: 1 / 0)

        _simulate_message(pn, session.status_channel, {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s2": {
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "channel": "stream.echo.s2",
                    "token": "tok-2",
                },
            },
        })

        assert "onStream" in errors

    def test_close_clears_error_cbs(self) -> None:
        """close() clears error callbacks."""
        session, pn = _make_session()
        session.on_error(lambda err, ctx: None)
        assert len(session._error_cbs) == 1
        session.close()
        assert len(session._error_cbs) == 0


class TestSubscribeToTaskErrorRouting:
    def test_on_error_in_subscribe_to_task(self) -> None:
        """subscribe_to_task routes callback errors through on_error."""
        errors = []

        def _on_error(err, ctx):
            errors.append((err, ctx))

        callbacks = TaskEventCallbacks(
            on_progress=lambda msg: 1 / 0,
            on_error=_on_error,
        )

        event = MagicMock()
        event.channel = "u.alice.task-1"
        event.message = {"type": "progress", "taskId": "task-1"}

        _dispatch_event(event, "u.alice.task-1", callbacks)

        assert len(errors) == 1
        err, ctx = errors[0]
        assert ctx.entry_point == "subscribeToTask"
        assert ctx.callback_type == "onProgress"

    def test_warn_logged_in_subscribe_to_task(self, caplog) -> None:
        """Warning logged when no on_error in subscribe_to_task."""
        callbacks = TaskEventCallbacks(
            on_event=lambda msg: 1 / 0,
        )

        event = MagicMock()
        event.channel = "u.alice.task-1"
        event.message = {"type": "progress", "taskId": "task-1"}

        with caplog.at_level(logging.WARNING, logger="blocks_network.task_client"):
            _dispatch_event(event, "u.alice.task-1", callbacks)

        assert any("callback error" in r.message for r in caplog.records)

    def test_subscribe_on_error_handler_throws(self) -> None:
        """on_error handler in subscribe that throws does not crash."""
        callbacks = TaskEventCallbacks(
            on_progress=lambda msg: 1 / 0,
            on_error=lambda err, ctx: 1 / 0,
        )

        event = MagicMock()
        event.channel = "u.alice.task-1"
        event.message = {"type": "progress", "taskId": "task-1"}

        # Should not raise
        _dispatch_event(event, "u.alice.task-1", callbacks)


# ============================================================================
# P1-5: TaskClient.create() factory tests
# ============================================================================


class TestTaskClientCreate:
    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_with_free_billing_mode(self, mock_cdm) -> None:
        """create() with billing_mode='free' selects playground keyset."""
        from blocks_network.cdm_config import CdmConfig, CdmKeyset, CdmApiConfig

        mock_cdm.return_value = CdmConfig(
            playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
            network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
            api=CdmApiConfig(base_url="https://api.example.com"),
        )

        client = TaskClient.create(billing_mode="free")
        assert client._subscribe_key == "pg-sub"
        assert client._publish_key == "pg-pub"
        assert client._base_url == "https://api.example.com"

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_with_paid_billing_mode(self, mock_cdm) -> None:
        """create() with billing_mode='paid' selects network keyset."""
        from blocks_network.cdm_config import CdmConfig, CdmKeyset, CdmApiConfig

        mock_cdm.return_value = CdmConfig(
            playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
            network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
            api=CdmApiConfig(base_url="https://api.example.com"),
        )

        client = TaskClient.create(billing_mode="paid")
        assert client._subscribe_key == "nw-sub"
        assert client._publish_key == "nw-pub"

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_env_vars_override_cdm(self, mock_cdm, monkeypatch) -> None:
        """Env vars override CDM-derived values."""
        from blocks_network.cdm_config import CdmConfig, CdmKeyset, CdmApiConfig

        mock_cdm.return_value = CdmConfig(
            playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
            network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
            api=CdmApiConfig(base_url="https://api.example.com"),
        )

        monkeypatch.setenv("BLOCKS_SUBSCRIBE_KEY", "env-sub")
        monkeypatch.setenv("BLOCKS_PUBLISH_KEY", "env-pub")
        monkeypatch.setenv("BLOCKS_BACKEND_URL", "https://env.example.com")

        client = TaskClient.create(billing_mode="free")
        assert client._subscribe_key == "env-sub"
        assert client._publish_key == "env-pub"
        assert client._base_url == "https://env.example.com"

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_explicit_overrides_env(self, mock_cdm, monkeypatch) -> None:
        """Explicit options override env vars."""
        from blocks_network.cdm_config import CdmConfig, CdmKeyset, CdmApiConfig

        mock_cdm.return_value = CdmConfig(
            playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
            network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
            api=CdmApiConfig(base_url="https://api.example.com"),
        )

        monkeypatch.setenv("BLOCKS_SUBSCRIBE_KEY", "env-sub")

        client = TaskClient.create(
            billing_mode="free",
            subscribe_key="explicit-sub",
        )
        assert client._subscribe_key == "explicit-sub"

    def test_create_invalid_billing_mode_raises(self) -> None:
        """create() with invalid billing_mode raises ValueError."""
        with pytest.raises(ValueError, match="billing_mode must be"):
            TaskClient.create(billing_mode="invalid")  # type: ignore

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_no_base_url_raises(self, mock_cdm) -> None:
        """create() raises when base_url cannot be resolved."""
        from blocks_network.cdm_config import CdmConfig, CdmKeyset, CdmApiConfig

        mock_cdm.return_value = CdmConfig(
            playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
            network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
            api=CdmApiConfig(base_url=""),
        )

        with pytest.raises(ValueError, match="base_url could not be resolved"):
            TaskClient.create(billing_mode="free")

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_provides_session_pubnub_factory(self, mock_cdm) -> None:
        """create() provides a default create_session_pubnub factory."""
        from blocks_network.cdm_config import CdmConfig, CdmKeyset, CdmApiConfig

        mock_cdm.return_value = CdmConfig(
            playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
            network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
            api=CdmApiConfig(base_url="https://api.example.com"),
        )

        client = TaskClient.create(billing_mode="free")
        assert client._create_session_pubnub_factory is not None

# ============================================================================
# P1-2: connect() tests
# ============================================================================


class TestConnect:
    def test_connect_requires_auth(self) -> None:
        """connect() without auth provider raises RuntimeError."""
        client = TaskClient(subscribe_key="sub-key", billing_mode="free")
        with pytest.raises(RuntimeError, match="requires an authenticated TaskClient"):
            client.connect("task-1")

    @patch("blocks_network.task_client.call_rpc")
    def test_connect_terminal_task(self, mock_rpc) -> None:
        """connect() on terminal task returns skip_subscription session."""
        mock_rpc.return_value = {
            "task": {
                "taskId": "task-1",
                "agentName": "echo",
                "owner": "alice",
                "state": "completed",
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            auth_provider=StaticAuthProvider("jwt-token"),
            base_url="http://localhost:3000",
        )

        mock_pn = _make_mock_pubnub()
        with patch.object(client, "_create_session_pubnub", return_value=mock_pn), \
             patch.object(client, "_fetch_task_read_token", return_value={
                 "pamToken": "t4-fresh",
                 "channel": "u.org1.task-1",
                 "ttlMinutes": 60,
             }):
            session = client.connect("task-1")

        assert session.state == "completed"
        assert session._skip_subscription is True
        assert session.is_closed is False  # Not closed -- can still download
        assert session._pubnub is not None  # Holds PubNub for download

    @patch("blocks_network.task_client.call_rpc")
    def test_connect_terminal_task_list_artifacts(self, mock_rpc) -> None:
        """connect() on terminal task pre-populates artifacts from history."""
        mock_rpc.return_value = {
            "task": {
                "taskId": "task-1",
                "agentName": "echo",
                "owner": "alice",
                "state": "completed",
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            auth_provider=StaticAuthProvider("jwt-token"),
            base_url="http://localhost:3000",
        )

        mock_pn = _make_mock_pubnub()

        # Set up history with mixed task events, plus one invalid payload.
        request_msg = MagicMock()
        request_msg.timetoken = "16000000000000000"
        request_msg.message = {
            "type": "request",
            "taskId": "task-1",
            "requestParts": [],
        }
        progress_msg = MagicMock()
        progress_msg.timetoken = "16000000000000001"
        progress_msg.message = {
            "type": "progress",
            "taskId": "task-1",
            "message": "Working",
            "progress": 50,
        }
        artifact_msg = MagicMock()
        artifact_msg.timetoken = "16000000000000002"
        artifact_msg.message = {
            "type": "artifact",
            "taskId": "task-1",
            "artifactRef": {
                "kind": "inline",
                "mimeType": "text/plain",
                "size": 5,
                "data": base64.b64encode(b"hello").decode("ascii"),
            },
        }
        system_msg = MagicMock()
        system_msg.timetoken = "16000000000000003"
        system_msg.message = {
            "type": "system",
            "taskId": "task-1",
            "status": "paused",
        }
        log_msg = MagicMock()
        log_msg.timetoken = "16000000000000004"
        log_msg.message = {
            "type": "log",
            "taskId": "task-1",
            "message": "finished",
        }
        terminal_msg = MagicMock()
        terminal_msg.timetoken = "16000000000000005"
        terminal_msg.message = {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        }
        invalid_msg = MagicMock()
        invalid_msg.timetoken = "16000000000000006"
        invalid_msg.message = "ignore-me"
        fm_result = MagicMock()
        fm_result.result.channels = {
            "u.org1.task-1": [
                request_msg,
                progress_msg,
                artifact_msg,
                system_msg,
                log_msg,
                terminal_msg,
                invalid_msg,
            ],
        }
        fm_chain = MagicMock()
        fm_chain.channels.return_value = fm_chain
        fm_chain.maximum_per_channel.return_value = fm_chain
        fm_chain.sync.return_value = fm_result
        mock_pn.fetch_messages.return_value = fm_chain

        with patch.object(client, "_create_session_pubnub", return_value=mock_pn), \
             patch.object(client, "_fetch_task_read_token", return_value={
                 "pamToken": "t4-fresh",
                 "channel": "u.org1.task-1",
                 "ttlMinutes": 60,
             }):
            session = client.connect("task-1")

        artifacts = session.list_artifacts()
        assert len(artifacts) == 1
        assert artifacts[0].kind == "inline"
        assert [event.type for event in session.list_events()] == [
            "request",
            "progress",
            "artifact",
            "system",
            "log",
            "terminal",
        ]
        assert fm_chain.sync.call_count == 1

    @patch("blocks_network.task_client.call_rpc")
    def test_connect_terminal_task_list_streams(self, mock_rpc) -> None:
        """connect() on terminal task pre-populates streams from history."""
        mock_rpc.return_value = {
            "task": {
                "taskId": "task-1",
                "agentName": "echo",
                "owner": "alice",
                "state": "completed",
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            auth_provider=StaticAuthProvider("jwt-token"),
            base_url="http://localhost:3000",
        )

        mock_pn = _make_mock_pubnub()

        # Set up history with a stream_started event
        hist_msg = MagicMock()
        hist_msg.timetoken = "16000000000000001"
        hist_msg.message = {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "channel": "stream.echo.s1",
                    "token": "tok-1",
                },
            },
        }
        fm_result = MagicMock()
        fm_result.result.channels = {"u.org1.task-1": [hist_msg]}
        fm_chain = MagicMock()
        fm_chain.channels.return_value = fm_chain
        fm_chain.maximum_per_channel.return_value = fm_chain
        fm_chain.sync.return_value = fm_result
        mock_pn.fetch_messages.return_value = fm_chain

        with patch.object(client, "_create_session_pubnub", return_value=mock_pn), \
             patch.object(client, "_fetch_task_read_token", return_value={
                 "pamToken": "t4-fresh",
                 "channel": "u.org1.task-1",
                 "ttlMinutes": 60,
             }):
            session = client.connect("task-1")

        streams = session.list_streams()
        assert len(streams) == 1
        assert streams[0].descriptor.stream_id == "s1"

    def test_wait_for_stream_skip_subscription_no_match(self) -> None:
        """wait_for_stream on skip_subscription session fails immediately."""
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            skip_subscription=True,
            state="completed",
        )

        with pytest.raises(RuntimeError, match="terminal task session"):
            session.wait_for_stream(stream_id="nonexistent")

    def test_wait_for_stream_where_skip_subscription_no_match(self) -> None:
        """wait_for_stream_where on skip_subscription session fails immediately."""
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            skip_subscription=True,
            state="completed",
        )

        with pytest.raises(RuntimeError, match="terminal task session"):
            session.wait_for_stream_where(lambda ref: True)

    @patch("blocks_network.task_client.call_rpc")
    def test_connect_active_task(self, mock_rpc) -> None:
        """connect() on active task returns subscribing session."""
        mock_rpc.return_value = {
            "task": {
                "taskId": "task-1",
                "agentName": "echo",
                "owner": "alice",
                "state": "running",
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            auth_provider=StaticAuthProvider("jwt-token"),
            base_url="http://localhost:3000",
        )

        mock_pn = _make_mock_pubnub()
        request_msg = MagicMock()
        request_msg.timetoken = "16000000000000000"
        request_msg.message = {
            "type": "request",
            "taskId": "task-1",
            "requestParts": [],
        }
        stream_msg = MagicMock()
        stream_msg.timetoken = "16000000000000001"
        stream_msg.message = {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t7c-1",
                    "tokenTtlMinutes": 62,
                },
            },
        }
        system_msg = MagicMock()
        system_msg.timetoken = "16000000000000002"
        system_msg.message = {
            "type": "system",
            "taskId": "task-1",
            "status": "heartbeat",
        }
        invalid_msg = MagicMock()
        invalid_msg.timetoken = "16000000000000003"
        invalid_msg.message = None
        fm_result = MagicMock()
        fm_result.result.channels = {
            "u.org1.task-1": [request_msg, stream_msg, system_msg, invalid_msg],
        }
        fm_chain = MagicMock()
        fm_chain.channels.return_value = fm_chain
        fm_chain.maximum_per_channel.return_value = fm_chain
        fm_chain.sync.return_value = fm_result
        mock_pn.fetch_messages.return_value = fm_chain

        with patch.object(client, "_create_session_pubnub", return_value=mock_pn), \
             patch.object(client, "_fetch_task_read_token", return_value={
                 "pamToken": "t4-fresh",
                 "channel": "u.org1.task-1",
                 "ttlMinutes": 60,
             }), \
             patch("pubnub.callbacks.SubscribeCallback", MagicMock()):
            session = client.connect("task-1")

        assert session.state == "running"
        assert session.is_closed is False
        assert session._skip_subscription is False
        assert [(event.type, event.get("streamEvent")) for event in session.list_events()] == [
            ("request", None),
            ("progress", "stream_started"),
            ("system", None),
        ]
        assert fm_chain.sync.call_count == 1

    @patch("blocks_network.task_client.call_rpc")
    def test_connect_derives_terminal_from_history_when_rpc_lags(
        self, mock_rpc,
    ) -> None:
        """Regression: connect() must use history-derived terminal state
        when the backend RPC still reports running.

        A consumer reconnecting within a few seconds of task completion
        can beat the backend's taskFanout -> DB write, so GetTask
        returns state=running. Without this fix, connect() falls into
        the active path, hands back state=running, and ref.open()
        bypasses the terminal short-circuit — a silent-hang against
        an about-to-be-revoked T7c token.
        """
        from blocks_network.stream_ref import StreamUnavailableError

        mock_rpc.return_value = {
            "task": {
                "taskId": "task-1",
                "agentName": "echo",
                "owner": "alice",
                "state": "running",  # backend hasn't caught up
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            auth_provider=StaticAuthProvider("jwt-token"),
            base_url="http://localhost:3000",
        )

        mock_pn = _make_mock_pubnub()

        # History contains both a stream_started and a terminal event.
        stream_msg = MagicMock()
        stream_msg.timetoken = "16000000000000001"
        stream_msg.message = {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t7c-1",
                    "tokenTtlMinutes": 17,
                },
            },
        }
        term_msg = MagicMock()
        term_msg.timetoken = "16000000000000002"
        term_msg.message = {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        }
        fm_result = MagicMock()
        fm_result.result.channels = {"u.org1.task-1": [stream_msg, term_msg]}
        fm_chain = MagicMock()
        fm_chain.channels.return_value = fm_chain
        fm_chain.maximum_per_channel.return_value = fm_chain
        fm_chain.sync.return_value = fm_result
        mock_pn.fetch_messages.return_value = fm_chain

        with patch.object(client, "_create_session_pubnub", return_value=mock_pn), \
             patch.object(client, "_fetch_task_read_token", return_value={
                 "pamToken": "t4-fresh",
                 "channel": "u.org1.task-1",
                 "ttlMinutes": 60,
             }):
            session = client.connect("task-1")

        # Despite RPC state='running', history's terminal event wins.
        assert session.state == "completed"
        assert session._skip_subscription is True

        # ref.open() must short-circuit now that state is terminal.
        refs = session.list_streams()
        assert len(refs) == 1
        with pytest.raises(StreamUnavailableError):
            refs[0].open()

    def test_connect_terminal_close_cleans_up(self) -> None:
        """close() on terminal connect() session destroys PubNub client."""
        mock_pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=mock_pn,
            owns_subscribe_client=True,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            skip_subscription=True,
            state="completed",
        )

        assert not session.is_closed
        session.close()
        assert session.is_closed
        mock_pn.stop.assert_called_once()


# ============================================================================
# P1-2: list_artifacts accumulation
# ============================================================================


class TestListArtifacts:
    def test_list_artifacts_from_live_events(self) -> None:
        """list_artifacts() accumulates from live artifact events."""
        session, pn = _make_session()

        assert session.list_artifacts() == []

        _simulate_message(pn, session.status_channel, {
            "type": "artifact",
            "taskId": "task-1",
            "artifactRef": {
                "kind": "inline",
                "mimeType": "text/plain",
                "size": 5,
                "data": base64.b64encode(b"hello").decode("ascii"),
            },
        })

        artifacts = session.list_artifacts()
        assert len(artifacts) == 1
        assert artifacts[0].kind == "inline"

    def test_list_artifacts_preloaded(self) -> None:
        """list_artifacts() returns preloaded artifacts from connect()."""
        ref = ArtifactRef(kind="inline", data="aGVsbG8=", mime_type="text/plain", size=5)
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            preloaded_artifacts=[ref],
        )

        assert len(session.list_artifacts()) == 1


# ============================================================================
# P1-2: state property
# ============================================================================


class TestStateProperty:
    def test_state_returns_explicit_state(self) -> None:
        """state property returns explicit state kwarg."""
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            state="running",
        )
        assert session.state == "running"

    def test_state_falls_back_to_pre_closed(self) -> None:
        """state property falls back to pre_closed_state."""
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=None,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            pre_closed_state="completed",
        )
        assert session.state == "completed"

    def test_state_explicit_overrides_pre_closed(self) -> None:
        """Explicit state overrides pre_closed_state."""
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            state="running",
            skip_subscription=True,
        )
        assert session.state == "running"

    def test_state_none_when_neither_set(self) -> None:
        """state returns None when neither explicit nor pre_closed."""
        session, _ = _make_session()
        assert session.state is None


# ============================================================================
# P1-2: Timetoken dedup
# ============================================================================


class TestTimetokenDedup:
    @patch("blocks_network.task_client.call_rpc")
    def test_dedup_filters_history_events(self, mock_rpc) -> None:
        """Events already in history are not dispatched again from buffer."""
        mock_rpc.return_value = {
            "task": {
                "taskId": "task-1",
                "agentName": "echo",
                "owner": "alice",
                "state": "running",
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            auth_provider=StaticAuthProvider("jwt-token"),
            base_url="http://localhost:3000",
        )

        mock_pn = _make_mock_pubnub()

        # Set up history with a message at timetoken 100
        hist_msg = MagicMock()
        hist_msg.timetoken = "100"
        hist_msg.message = {
            "type": "progress",
            "taskId": "task-1",
            "progress": 50,
        }
        fm_result = MagicMock()
        fm_result.result.channels = {"u.org1.task-1": [hist_msg]}
        fm_chain = MagicMock()
        fm_chain.channels.return_value = fm_chain
        fm_chain.maximum_per_channel.return_value = fm_chain
        fm_chain.sync.return_value = fm_result
        mock_pn.fetch_messages.return_value = fm_chain

        with patch.object(client, "_create_session_pubnub", return_value=mock_pn), \
             patch.object(client, "_fetch_task_read_token", return_value={
                 "pamToken": "t4-fresh",
                 "channel": "u.org1.task-1",
                 "ttlMinutes": 60,
             }), \
             patch("pubnub.callbacks.SubscribeCallback", MagicMock()):
            session = client.connect("task-1")

        # The session should have been created without errors
        assert session.state == "running"


# ============================================================================
# BLOCKS-243: Anonymous consumer mode (anon_fingerprint)
# ============================================================================


class TestTaskClientCreateAnonMode:
    @staticmethod
    def _mock_cdm():
        from blocks_network.cdm_config import CdmApiConfig, CdmConfig, CdmKeyset

        return CdmConfig(
            playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
            network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
            api=CdmApiConfig(base_url="https://api.example.com"),
        )

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_anon_fingerprint_free_billing_succeeds(self, mock_cdm) -> None:
        """anon_fingerprint + billing_mode='free' succeeds and stores the fingerprint."""
        mock_cdm.return_value = self._mock_cdm()

        client = TaskClient.create(
            billing_mode="free",
            anon_fingerprint="fp-abc123",
        )
        assert client._anon_fingerprint == "fp-abc123"
        # Anon mode must not wire up ConsumerAuth.
        assert client._consumer_auth is None
        assert client._auth_provider is None

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_anon_fingerprint_paid_billing_raises(self, mock_cdm) -> None:
        """anon_fingerprint requires billing_mode='free'."""
        mock_cdm.return_value = self._mock_cdm()

        with pytest.raises(ValueError, match="billing_mode='free'"):
            TaskClient.create(
                billing_mode="paid",
                anon_fingerprint="fp-abc123",
            )

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_anon_fingerprint_with_api_key_raises(self, mock_cdm) -> None:
        """anon_fingerprint is mutually exclusive with api_key."""
        mock_cdm.return_value = self._mock_cdm()

        with pytest.raises(ValueError, match="Only one token provider mode"):
            TaskClient.create(
                billing_mode="free",
                anon_fingerprint="fp-abc123",
                api_key="bk_test",
            )

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_anon_fingerprint_with_token_endpoint_raises(self, mock_cdm) -> None:
        """anon_fingerprint is mutually exclusive with token_endpoint."""
        mock_cdm.return_value = self._mock_cdm()

        with pytest.raises(ValueError, match="Only one token provider mode"):
            TaskClient.create(
                billing_mode="free",
                anon_fingerprint="fp-abc123",
                token_endpoint="https://example.com/token",
            )

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_anon_fingerprint_with_token_provider_raises(self, mock_cdm) -> None:
        """anon_fingerprint is mutually exclusive with token_provider."""
        mock_cdm.return_value = self._mock_cdm()

        def _provider():
            return {"token": "x", "expiresAt": 0}

        with pytest.raises(ValueError, match="Only one token provider mode"):
            TaskClient.create(
                billing_mode="free",
                anon_fingerprint="fp-abc123",
                token_provider=_provider,
            )

    @patch("blocks_network.cdm_config.fetch_cdm_config")
    def test_create_without_anon_still_works(self, mock_cdm) -> None:
        """Existing authed-mode create() still works and leaves anon state unset."""
        mock_cdm.return_value = self._mock_cdm()

        client = TaskClient.create(billing_mode="free")
        assert client._anon_fingerprint is None


class TestAnonConnect:
    def test_send_message_on_anon_client_raises(self) -> None:
        """send_message() is blocked on anon-mode TaskClients."""
        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            base_url="http://localhost:3000",
            anon_fingerprint="fp-abc123",
        )
        with pytest.raises(
            ValueError, match="anon-mode TaskClient does not support send_message"
        ):
            client.send_message(agent_name="echo", request_parts=[])

    @patch("blocks_network.task_client.call_rpc")
    def test_anon_connect_calls_anon_endpoint_and_skips_auth(
        self, mock_rpc,
    ) -> None:
        """connect() on an anon TaskClient skips the auth gate and mints via anon endpoint."""
        mock_rpc.return_value = {
            "task": {
                "taskId": "task-1",
                "agentName": "echo",
                "owner": "anonymous",
                "state": "completed",
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            base_url="http://localhost:3000",
            anon_fingerprint="fp-abc123",
        )

        mock_pn = _make_mock_pubnub()

        with patch.object(client, "_create_session_pubnub", return_value=mock_pn), \
             patch.object(
                 client,
                 "_fetch_anon_consumer_read_token",
                 return_value={
                     "pamToken": "t4-anon",
                     "channel": "u.anon-org.task-1",
                     "ttlMinutes": 60,
                 },
             ) as mock_anon_fetch, \
             patch.object(client, "_fetch_task_read_token") as mock_authed_fetch:
            session = client.connect("task-1")

        # The anon endpoint was used; the authed endpoint was not.
        mock_anon_fetch.assert_called_once_with("task-1")
        mock_authed_fetch.assert_not_called()
        assert session.state == "completed"
        assert session._skip_subscription is True
        assert session._read_token == "t4-anon"

    def test_anon_fetch_sends_correct_body_and_no_auth_header(self) -> None:
        """_fetch_anon_consumer_read_token POSTs {taskId, fingerprint} with no Authorization."""
        import urllib.request

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            base_url="http://localhost:3000",
            anon_fingerprint="fp-abc123",
        )

        captured_req = {}

        def _fake_urlopen(req, context=None, timeout=None):
            captured_req["url"] = req.full_url
            captured_req["method"] = req.get_method()
            captured_req["data"] = req.data
            captured_req["headers"] = dict(req.header_items())
            resp = MagicMock()
            resp.read.return_value = b'{"pamToken":"t4-anon","channel":"u.anon-org.task-1","ttlMinutes":60}'
            resp.__enter__ = MagicMock(return_value=resp)
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch.object(urllib.request, "urlopen", side_effect=_fake_urlopen):
            result = client._fetch_anon_consumer_read_token("task-1")

        assert result["pamToken"] == "t4-anon"
        assert result["channel"] == "u.anon-org.task-1"
        assert captured_req["method"] == "POST"
        assert captured_req["url"].endswith("/api/v1/auth/anon-task-read-token")
        body = json.loads(captured_req["data"].decode("utf-8"))
        assert body == {"taskId": "task-1", "fingerprint": "fp-abc123"}
        # No Authorization header should be set in anon mode.
        lower_header_keys = {k.lower() for k in captured_req["headers"].keys()}
        assert "authorization" not in lower_header_keys

    def test_anon_fetch_403_raises_anon_task_access_denied(self) -> None:
        """HTTP 403 from the anon endpoint surfaces as AnonTaskAccessDenied."""
        import urllib.error
        import urllib.request

        from blocks_network.task_client import AnonTaskAccessDenied

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            base_url="http://localhost:3000",
            anon_fingerprint="fp-wrong",
        )

        def _raise_403(req, context=None, timeout=None):
            raise urllib.error.HTTPError(
                url=req.full_url,
                code=403,
                msg="Forbidden",
                hdrs={},
                fp=BytesIO(b'{"error":"Not authorized to view this task"}'),
            )

        with patch.object(urllib.request, "urlopen", side_effect=_raise_403):
            with pytest.raises(AnonTaskAccessDenied) as exc_info:
                client._fetch_anon_consumer_read_token("task-1")

        # Message must embed "403" so the existing 403 fallback regex matches.
        assert "403" in str(exc_info.value)

    @patch("blocks_network.task_client.call_rpc")
    def test_anon_connect_propagates_403_as_access_denied(self, mock_rpc) -> None:
        """connect() on an anon client with a mismatching fingerprint raises AnonTaskAccessDenied."""
        import urllib.error
        import urllib.request

        from blocks_network.task_client import AnonTaskAccessDenied

        mock_rpc.return_value = {
            "task": {
                "taskId": "task-1",
                "agentName": "echo",
                "owner": "anonymous",
                "state": "running",
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            base_url="http://localhost:3000",
            anon_fingerprint="fp-wrong",
        )

        def _raise_403(req, context=None, timeout=None):
            raise urllib.error.HTTPError(
                url=req.full_url,
                code=403,
                msg="Forbidden",
                hdrs={},
                fp=BytesIO(b'{"error":"Not authorized to view this task"}'),
            )

        with patch.object(urllib.request, "urlopen", side_effect=_raise_403):
            with pytest.raises(AnonTaskAccessDenied):
                client.connect("task-1")


class TestNonAnonConnectUnchanged:
    """Regression: existing authed-mode connect() behavior is preserved
    when anon_fingerprint is None (no silent routing change)."""

    def test_non_anon_connect_still_requires_auth_provider(self) -> None:
        client = TaskClient(subscribe_key="sub-key", billing_mode="free")
        # anon_fingerprint is None by default.
        assert client._anon_fingerprint is None
        with pytest.raises(RuntimeError, match="requires an authenticated TaskClient"):
            client.connect("task-1")


class TestConnectRoleParameter:
    """connect() passes the role parameter to the read-token endpoint."""

    @patch("blocks_network.task_client.call_rpc")
    def test_connect_with_provider_role(self, mock_rpc) -> None:
        """connect(role='provider') sends role:'provider' in the token request."""
        mock_rpc.return_value = {
            "task": {
                "taskId": "task-1",
                "agentName": "echo",
                "owner": "other-user",
                "state": "completed",
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            auth_provider=StaticAuthProvider("jwt-token"),
            base_url="http://localhost:3000",
        )

        mock_pn = _make_mock_pubnub()
        with patch.object(client, "_create_session_pubnub", return_value=mock_pn), \
             patch.object(client, "_fetch_task_read_token", return_value={
                 "pamToken": "t4-provider",
                 "channel": "u.org1.task-1",
                 "ttlMinutes": 60,
             }) as mock_fetch_token:
            session = client.connect("task-1", role="provider")

        mock_fetch_token.assert_called_once_with("task-1", "provider")
        assert session.state == "completed"
        session.close()

    @patch("blocks_network.task_client.call_rpc")
    def test_connect_defaults_to_consumer_role(self, mock_rpc) -> None:
        """connect() without role sends role:'consumer' (default)."""
        mock_rpc.return_value = {
            "task": {
                "taskId": "task-2",
                "agentName": "echo",
                "owner": "user-1",
                "state": "completed",
            },
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            auth_provider=StaticAuthProvider("jwt-token"),
            base_url="http://localhost:3000",
        )

        mock_pn = _make_mock_pubnub()
        with patch.object(client, "_create_session_pubnub", return_value=mock_pn), \
             patch.object(client, "_fetch_task_read_token", return_value={
                 "pamToken": "t4-consumer",
                 "channel": "u.org1.task-2",
                 "ttlMinutes": 60,
             }) as mock_fetch_token:
            session = client.connect("task-2")

        mock_fetch_token.assert_called_once_with("task-2", "consumer")
        assert session.state == "completed"
        session.close()
