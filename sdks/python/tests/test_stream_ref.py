"""Tests for the StreamRef module."""

from __future__ import annotations

from unittest.mock import MagicMock, patch

import pytest

from blocks_network.stream_ref import StreamRef, StreamUnavailableError


class TestStreamRef:
    """StreamRef unit tests."""

    def _make_descriptor(self) -> MagicMock:
        desc = MagicMock()
        desc.task_id = "task-1"
        desc.stream_id = "stream-1"
        desc.agent_name = "echo"
        desc.channel = "stream.echo.stream-1"
        desc.token = "t7c-token"
        desc.agent_direction = "outbound"
        desc.local_direction = "inbound"
        desc.format = "bytes"
        desc.metadata = None
        return desc

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_creates_client(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        mock_client.on_end = MagicMock()
        MockStreamClient.from_descriptor.return_value = mock_client

        sdk_options = {"subscribe_key": "sub-c-test", "publish_key": "pub-c-test"}
        ref = StreamRef(descriptor, sdk_options)

        client = ref.open()
        assert client is mock_client
        assert ref.is_open is True
        MockStreamClient.from_descriptor.assert_called_once_with(
            descriptor,
            subscribe_key="sub-c-test",
            publish_key="pub-c-test",
        )

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_idempotent_while_active(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        mock_client.on_end = MagicMock()
        MockStreamClient.from_descriptor.return_value = mock_client

        ref = StreamRef(descriptor, {"subscribe_key": "sub", "publish_key": "pub"})
        client1 = ref.open()
        client2 = ref.open()
        assert client1 is client2
        # from_descriptor should only be called once
        assert MockStreamClient.from_descriptor.call_count == 1

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_after_ended_raises(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        on_end_callbacks = []
        mock_client.on_end.side_effect = lambda cb: on_end_callbacks.append(cb)
        MockStreamClient.from_descriptor.return_value = mock_client

        ref = StreamRef(descriptor, {"subscribe_key": "sub", "publish_key": "pub"})
        ref.open()

        # Simulate stream ending
        for cb in on_end_callbacks:
            cb()

        with pytest.raises(RuntimeError, match="already been ended"):
            ref.open()

    def test_descriptor_property(self) -> None:
        descriptor = self._make_descriptor()
        ref = StreamRef(descriptor, {})
        assert ref.descriptor is descriptor

    def test_is_open_initially_false(self) -> None:
        descriptor = self._make_descriptor()
        ref = StreamRef(descriptor, {})
        assert ref.is_open is False

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_forwards_reorder_timeout_ms(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        mock_client.on_end = MagicMock()
        MockStreamClient.from_descriptor.return_value = mock_client

        sdk_options = {"subscribe_key": "sub-c-test", "publish_key": "pub-c-test"}
        ref = StreamRef(descriptor, sdk_options)
        ref.open(reorder_timeout_ms=250)
        MockStreamClient.from_descriptor.assert_called_once_with(
            descriptor,
            subscribe_key="sub-c-test",
            publish_key="pub-c-test",
            reorder_timeout_ms=250,
        )


class TestOnOpenHook:
    """Tests for the on_open hook on StreamRef."""

    def _make_descriptor(self) -> MagicMock:
        desc = MagicMock()
        desc.task_id = "task-1"
        desc.stream_id = "stream-1"
        desc.agent_name = "echo"
        desc.channel = "stream.echo.stream-1"
        desc.token = "t7c-token"
        desc.agent_direction = "outbound"
        desc.local_direction = "inbound"
        desc.format = "bytes"
        desc.metadata = None
        return desc

    @patch("blocks_network.stream_ref.StreamClient")
    def test_on_open_fires_when_open_called(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        mock_client.on_end = MagicMock()
        MockStreamClient.from_descriptor.return_value = mock_client

        on_open_calls = []
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "sub", "publish_key": "pub"},
            on_open=lambda c: on_open_calls.append(c),
        )
        client = ref.open()
        assert len(on_open_calls) == 1
        assert on_open_calls[0] is client

    @patch("blocks_network.stream_ref.StreamClient")
    def test_on_open_not_called_when_not_provided(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        mock_client.on_end = MagicMock()
        MockStreamClient.from_descriptor.return_value = mock_client

        ref = StreamRef(descriptor, {"subscribe_key": "sub", "publish_key": "pub"})
        ref.open()
        assert ref.is_open is True


class TestTerminalSessionShortCircuit:
    """Fix A: StreamRef.open() short-circuits on terminal sessions."""

    def _make_descriptor(self, declared: str | None = None) -> MagicMock:
        desc = MagicMock()
        desc.task_id = "task-1"
        desc.stream_id = "stream-1"
        desc.agent_name = "echo"
        desc.channel = "stream.echo.stream-1"
        desc.token = "t7c-token"
        desc.agent_direction = "outbound"
        desc.local_direction = "inbound"
        desc.format = "bytes"
        desc.metadata = None
        desc.declared_stream = declared
        return desc

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_raises_on_completed(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "completed",
        )
        with pytest.raises(StreamUnavailableError):
            ref.open()
        MockStreamClient.from_descriptor.assert_not_called()

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_raises_on_failed(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "failed",
        )
        with pytest.raises(StreamUnavailableError):
            ref.open()

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_raises_on_canceled(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "canceled",
        )
        with pytest.raises(StreamUnavailableError):
            ref.open()

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_proceeds_on_running(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        mock_client.on_end = MagicMock()
        MockStreamClient.from_descriptor.return_value = mock_client

        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "running",
        )
        client = ref.open()
        assert client is mock_client

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_proceeds_when_state_is_none(self, MockStreamClient: MagicMock) -> None:
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        mock_client.on_end = MagicMock()
        MockStreamClient.from_descriptor.return_value = mock_client

        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: None,
        )
        client = ref.open()
        assert client is mock_client

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_proceeds_when_no_session_state_hook(
        self, MockStreamClient: MagicMock,
    ) -> None:
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        mock_client.on_end = MagicMock()
        MockStreamClient.from_descriptor.return_value = mock_client

        ref = StreamRef(descriptor, {"subscribe_key": "s", "publish_key": "p"})
        client = ref.open()
        assert client is mock_client

    def test_error_fields_populated(self) -> None:
        descriptor = self._make_descriptor(declared="audio")
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "completed",
        )
        with pytest.raises(StreamUnavailableError) as exc_info:
            ref.open()
        err = exc_info.value
        assert err.task_id == "task-1"
        assert err.stream_id == "stream-1"
        assert err.declared_stream == "audio"
        assert err.terminal_state == "completed"

    def test_error_fields_when_no_declared_stream(self) -> None:
        descriptor = self._make_descriptor(declared=None)
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "failed",
        )
        with pytest.raises(StreamUnavailableError) as exc_info:
            ref.open()
        err = exc_info.value
        assert err.declared_stream is None
        assert err.terminal_state == "failed"

    def test_error_message_names_declared_stream_task_and_state(self) -> None:
        descriptor = self._make_descriptor(declared="audio")
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "failed",
        )
        with pytest.raises(StreamUnavailableError) as exc_info:
            ref.open()
        msg = str(exc_info.value)
        assert '"audio"' in msg
        assert '"task-1"' in msg
        assert '"failed"' in msg
        assert "live-only" in msg
        assert "ref.descriptor" in msg
        assert "session.list_artifacts()" in msg
        assert "session.state" in msg

    def test_error_message_falls_back_to_stream_id(self) -> None:
        descriptor = self._make_descriptor(declared=None)
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "canceled",
        )
        with pytest.raises(StreamUnavailableError) as exc_info:
            ref.open()
        msg = str(exc_info.value)
        # No declared_stream -> fall back to stream_id
        assert '"stream-1"' in msg

    def test_descriptor_accessible_on_terminal_session_ref(self) -> None:
        descriptor = self._make_descriptor(declared="audio")
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "completed",
        )
        # Descriptor access must not raise on a terminal-session ref
        assert ref.descriptor is descriptor
        assert ref.descriptor.stream_id == "stream-1"
        assert ref.descriptor.declared_stream == "audio"
        assert ref.is_open is False

    def test_session_state_is_re_evaluated_on_each_open(self) -> None:
        # Ensures the getter is consulted live, not captured at construction
        descriptor = self._make_descriptor()
        state_box = {"value": "running"}
        # Fresh ref whose state getter reads the box
        with patch("blocks_network.stream_ref.StreamClient") as MockStreamClient:
            mock_client = MagicMock()
            mock_client.is_active = True
            mock_client.on_end = MagicMock()
            MockStreamClient.from_descriptor.return_value = mock_client

            ref = StreamRef(
                descriptor,
                {"subscribe_key": "s", "publish_key": "p"},
                session_state=lambda: state_box["value"],
            )
            # Running -> open succeeds
            ref.open()

        # Build a fresh ref and flip state to terminal; must now raise
        state_box["value"] = "completed"
        fresh_ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: state_box["value"],
        )
        with pytest.raises(StreamUnavailableError):
            fresh_ref.open()

    def test_stream_unavailable_error_is_runtime_error(self) -> None:
        # Parity with Node: typed error should also be catchable as a
        # RuntimeError (Python equivalent of extending Error).
        descriptor = self._make_descriptor()
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: "completed",
        )
        with pytest.raises(RuntimeError):
            ref.open()

    @patch("blocks_network.stream_ref.StreamClient")
    def test_idempotency_wins_over_short_circuit_when_active(
        self, MockStreamClient: MagicMock,
    ) -> None:
        # Regression: a consumer that opened a stream while the task was
        # running MUST continue to receive the same live StreamClient from
        # open() during the auto-drain window or with auto_drain=False,
        # per SDK_CONTRACT §8.7.2 "idempotent while active". The terminal
        # short-circuit only protects against *constructing* a new client
        # against a revoked T7c token.
        descriptor = self._make_descriptor()
        mock_client = MagicMock()
        mock_client.is_active = True
        mock_client.on_end = MagicMock()
        MockStreamClient.from_descriptor.return_value = mock_client

        state_box = {"value": "running"}
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: state_box["value"],
        )

        client1 = ref.open()
        assert client1 is mock_client

        # Task transitions terminal; live client is still active (drain window).
        state_box["value"] = "completed"

        # Second open() during the drain window returns the same live client,
        # NOT a StreamUnavailableError.
        client2 = ref.open()
        assert client2 is client1
        # from_descriptor still called only once.
        MockStreamClient.from_descriptor.assert_called_once()

    @patch("blocks_network.stream_ref.StreamClient")
    def test_ended_client_on_terminal_session_raises_already_ended(
        self, MockStreamClient: MagicMock,
    ) -> None:
        # After the live client ends, idempotency no longer applies and the
        # "already ended" guard fires before the terminal short-circuit.
        # Both signals mean "no new client"; the "already ended" error is
        # fine here because the typical path that reaches this state on a
        # terminal session is auto-drain ending the client.
        descriptor = self._make_descriptor()

        ended_flag = {"value": False}
        mock_client = MagicMock()
        # is_active is a property-like: True until end() fires
        type(mock_client).is_active = property(
            lambda _self: not ended_flag["value"],
        )

        def _end() -> None:
            ended_flag["value"] = True
            for cb in on_end_cbs:
                cb()

        on_end_cbs: list = []
        mock_client.end = _end
        mock_client.on_end = lambda cb: on_end_cbs.append(cb)
        MockStreamClient.from_descriptor.return_value = mock_client

        state_box = {"value": "running"}
        ref = StreamRef(
            descriptor,
            {"subscribe_key": "s", "publish_key": "p"},
            session_state=lambda: state_box["value"],
        )
        client = ref.open()
        state_box["value"] = "completed"
        client.end()

        with pytest.raises(RuntimeError, match="already been ended"):
            ref.open()
