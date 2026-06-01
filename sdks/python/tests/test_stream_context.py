"""Tests for the stream context module."""

from __future__ import annotations

import threading
import time
from unittest.mock import MagicMock

import pytest

from blocks_network.stream.stream_client import StreamError
from blocks_network.stream_context import (
    ExternalStreamObject,
    StreamObject,
    run_on_activate,
)


class TestStreamObject:
    """StreamObject unit tests."""

    def _make_mock_client(self) -> MagicMock:
        client = MagicMock()
        client.channel = "stream.echo.test-stream"
        client.is_active = True
        client.inbound = iter([])
        client.write = MagicMock()
        client.end = MagicMock()
        client.on_end = MagicMock()
        return client

    def test_properties(self) -> None:
        client = self._make_mock_client()
        obj = StreamObject("test-stream", client)
        assert obj.stream_id == "test-stream"
        assert obj.channel == "stream.echo.test-stream"
        assert obj.is_active is True
        assert obj.external is False

    def test_write_delegates(self) -> None:
        client = self._make_mock_client()
        obj = StreamObject("test-stream", client)
        obj.write("hello")
        client.write.assert_called_once_with("hello")

    def test_end_delegates(self) -> None:
        client = self._make_mock_client()
        obj = StreamObject("test-stream", client)
        obj.end()
        client.end.assert_called_once()

    def test_inbound_delegates(self) -> None:
        client = self._make_mock_client()
        obj = StreamObject("test-stream", client)
        _ = obj.inbound
        # Accessing inbound should not raise

    def test_on_end_delegates(self) -> None:
        client = self._make_mock_client()
        obj = StreamObject("test-stream", client)
        cb = MagicMock()
        obj.on_end(cb)
        client.on_end.assert_called_once_with(cb)

    # -- forwarded read / error / uuid surface ----------------

    def test_uuid_delegates(self) -> None:
        """``uuid`` reads through to the underlying StreamClient.uuid."""
        client = self._make_mock_client()
        client.uuid = "agent-stream-0001"
        obj = StreamObject("test-stream", client)
        assert obj.uuid == "agent-stream-0001"

    def test_bytes_delegates(self) -> None:
        """``bytes()`` returns whatever StreamClient.bytes() returns (identity)."""
        client = self._make_mock_client()
        sentinel = iter([b"hello", b"world"])
        client.bytes = MagicMock(return_value=sentinel)
        obj = StreamObject("test-stream", client)
        result = obj.bytes()
        client.bytes.assert_called_once_with()
        assert result is sentinel

    def test_events_delegates(self) -> None:
        """``events()`` returns whatever StreamClient.events() returns (identity)."""
        client = self._make_mock_client()
        sentinel = iter([{"kind": "partial"}, {"kind": "final"}])
        client.events = MagicMock(return_value=sentinel)
        obj = StreamObject("test-stream", client)
        result = obj.events()
        client.events.assert_called_once_with()
        assert result is sentinel

    def test_as_file_delegates(self) -> None:
        """``as_file()`` returns whatever StreamClient.as_file() returns (identity)."""
        client = self._make_mock_client()
        sentinel = MagicMock(name="BufferedReader")
        client.as_file = MagicMock(return_value=sentinel)
        obj = StreamObject("test-stream", client)
        result = obj.as_file()
        client.as_file.assert_called_once_with()
        assert result is sentinel

    def test_on_error_delegates(self) -> None:
        """``on_error(cb)`` registers the same callback on the underlying client.

        Note: callbacks must be registered before the read path activates
        — ``StreamClient.on_error`` only appends to its callback list, it
        does not buffer or replay past errors.
        """
        client = self._make_mock_client()
        client.on_error = MagicMock()
        obj = StreamObject("test-stream", client)
        cb = MagicMock()
        obj.on_error(cb)
        client.on_error.assert_called_once_with(cb)

    def test_on_error_fires_via_fake_callback_list(self) -> None:
        """A callback registered via the wrapper fires when the fake's error
        list is dispatched.

        Models the live path: the wrapper appends to the underlying
        client's error-callback list; when the client surfaces a
        StreamError, the registered callback runs.
        """
        callbacks: list = []

        client = MagicMock()
        client.channel = "stream.echo.test-stream"
        client.is_active = True
        client.on_error = lambda cb: callbacks.append(cb)

        obj = StreamObject("test-stream", client)
        spy = MagicMock()
        obj.on_error(spy)

        err = StreamError(
            category="PNAccessDeniedCategory",
            error="forbidden",
            channel="stream.echo.test-stream",
            timestamp=1714600000.0,
            fatal=True,
        )
        for cb in callbacks:
            cb(err)

        spy.assert_called_once_with(err)


class TestExternalStreamObject:
    """ExternalStreamObject unit tests."""

    def test_properties(self) -> None:
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", lambda **kw: None)
        assert obj.stream_id == "ext-1"
        assert obj.channel == "stream.echo.ext-1"
        assert obj.is_active is True
        assert obj.external is True
        assert obj.token == "t7a-token"

    def test_write_throws(self) -> None:
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", lambda **kw: None)
        with pytest.raises(RuntimeError, match="Cannot write to an external stream"):
            obj.write("data")

    def test_inbound_throws(self) -> None:
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", lambda **kw: None)
        with pytest.raises(RuntimeError, match="Cannot read from an external stream"):
            _ = obj.inbound

    def test_uuid_throws(self) -> None:
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", lambda **kw: None)
        with pytest.raises(RuntimeError, match="external stream"):
            _ = obj.uuid

    def test_bytes_throws(self) -> None:
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", lambda **kw: None)
        with pytest.raises(RuntimeError, match="external stream"):
            obj.bytes()

    def test_events_throws(self) -> None:
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", lambda **kw: None)
        with pytest.raises(RuntimeError, match="external stream"):
            obj.events()

    def test_as_file_throws(self) -> None:
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", lambda **kw: None)
        with pytest.raises(RuntimeError, match="external stream"):
            obj.as_file()

    def test_on_error_throws(self) -> None:
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", lambda **kw: None)
        with pytest.raises(RuntimeError, match="external stream"):
            obj.on_error(lambda _err: None)

    def test_end(self) -> None:
        cb = MagicMock()
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", lambda **kw: None)
        obj.on_end(cb)
        assert obj.is_active is True
        obj.end()
        assert obj.is_active is False
        cb.assert_called_once()

    def test_activate(self) -> None:
        activate_fn = MagicMock()
        obj = ExternalStreamObject("ext-1", "stream.echo.ext-1", "t7a-token", activate_fn)
        obj.activate(metadata={"key": "val"})
        activate_fn.assert_called_once_with(metadata={"key": "val"})


class TestRunOnActivate:
    """run_on_activate unit tests."""

    def test_successful_callback(self) -> None:
        called = threading.Event()
        received_obj = [None]

        def on_activate(stream: StreamObject) -> None:
            received_obj[0] = stream
            called.set()

        client = MagicMock()
        client.channel = "stream.echo.s1"
        client.is_active = True
        obj = StreamObject("s1", client)

        fail_cb = MagicMock()
        thread = run_on_activate("s1", obj, on_activate, fail_cb)
        assert called.wait(timeout=2.0)
        thread.join(timeout=2.0)
        assert received_obj[0] is obj
        fail_cb.assert_not_called()

    def test_error_triggers_fail_stream(self) -> None:
        fail_called = threading.Event()
        fail_args = []

        def on_activate(stream: StreamObject) -> None:
            raise ValueError("boom")

        def fail_cb(stream_id: str, reason: str) -> None:
            fail_args.extend([stream_id, reason])
            fail_called.set()

        client = MagicMock()
        client.channel = "stream.echo.s1"
        client.is_active = True
        obj = StreamObject("s1", client)

        thread = run_on_activate("s1", obj, on_activate, fail_cb)
        assert fail_called.wait(timeout=2.0)
        thread.join(timeout=2.0)
        assert fail_args == ["s1", "stream_crashed"]

    def test_runs_on_daemon_thread(self) -> None:
        thread_info = {}
        done = threading.Event()

        def on_activate(stream: StreamObject) -> None:
            thread_info["daemon"] = threading.current_thread().daemon
            thread_info["name"] = threading.current_thread().name
            done.set()

        client = MagicMock()
        client.channel = "stream.echo.s1"
        client.is_active = True
        obj = StreamObject("s1", client)

        thread = run_on_activate("s1", obj, on_activate, MagicMock())
        assert done.wait(timeout=2.0)
        thread.join(timeout=2.0)
        assert thread_info["daemon"] is True
