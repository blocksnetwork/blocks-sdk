"""
Tests for protocol versioning across the Python SDK.

Covers:
- protocol_version module constants and helpers
- Registration payload versioning fields (sdkVersion, protocolVersions,
  preferredProtocolVersion, cliVersion)
- Registration HTTP header (Blocks-Protocol-Version)
- RPC client header emission
- Control message protocolVersion parsing and serialization
- Task event protocolVersion in body and meta
- Presence state versioning fields
- Unsupported protocol version handling (targeted vs broadcast)
- Stream payload protocolVersion
"""

from __future__ import annotations

import json
import os
import threading
import time
from io import BytesIO
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.protocol_version import (
    CURRENT_PROTOCOL_VERSION,
    DEPRECATED_PROTOCOL_VERSIONS,
    PROTOCOL_VERSION_HEADER,
    SDK_VERSION,
    SUPPORTED_PROTOCOL_VERSIONS,
    is_deprecated,
    is_supported,
)
from blocks_network.types import (
    AgentInstancePresenceState,
    CancelTaskMessage,
    ControlMessage,
    ExpireTaskMessage,
    StartTaskMessage,
    parse_control_message,
)
from blocks_network.agent_registry import ConnectAgentOptions, ConnectAgentResult, connect_agent
from blocks_network.rpc_client import call_rpc

from tests.conftest import minimal_card


# ============================================================================
# protocol_version module
# ============================================================================


class TestProtocolVersionConstants:
    def test_current_version_format(self) -> None:
        """CURRENT_PROTOCOL_VERSION uses YYYY-MM-DD format."""
        import re
        assert re.fullmatch(r"\d{4}-\d{2}-\d{2}", CURRENT_PROTOCOL_VERSION)

    def test_current_version_value(self) -> None:
        assert CURRENT_PROTOCOL_VERSION == "2026-05-01"

    def test_supported_contains_current(self) -> None:
        assert CURRENT_PROTOCOL_VERSION in SUPPORTED_PROTOCOL_VERSIONS

    def test_supported_is_nonempty(self) -> None:
        assert len(SUPPORTED_PROTOCOL_VERSIONS) >= 1

    def test_deprecated_is_list(self) -> None:
        assert isinstance(DEPRECATED_PROTOCOL_VERSIONS, list)

    def test_header_name(self) -> None:
        assert PROTOCOL_VERSION_HEADER == "Blocks-Protocol-Version"

    def test_sdk_version_is_string(self) -> None:
        assert isinstance(SDK_VERSION, str)
        assert len(SDK_VERSION) > 0

    def test_is_supported_true(self) -> None:
        assert is_supported(CURRENT_PROTOCOL_VERSION) is True

    def test_is_supported_false(self) -> None:
        assert is_supported("1999-01-01") is False

    def test_is_deprecated_false_for_current(self) -> None:
        assert is_deprecated(CURRENT_PROTOCOL_VERSION) is False


# ============================================================================
# Registration payload versioning
# ============================================================================


class TestRegistrationVersionFields:
    def _mock_auth(self) -> MagicMock:
        auth = MagicMock()
        auth.init.return_value = {"pamToken": "pam-test"}
        return auth

    def test_payload_includes_sdk_version(self) -> None:
        auth = self._mock_auth()
        connect_agent(
            "test_agent",
            ConnectAgentOptions(base_url="http://localhost:8080", agent_auth=auth),
        )

        payload = auth.init.call_args[1]["registration_payload"]
        assert "sdkVersion" in payload
        assert payload["sdkVersion"] == SDK_VERSION

    def test_payload_includes_protocol_versions(self) -> None:
        auth = self._mock_auth()
        connect_agent(
            "test_agent",
            ConnectAgentOptions(base_url="http://localhost:8080", agent_auth=auth),
        )

        payload = auth.init.call_args[1]["registration_payload"]
        assert "protocolVersions" in payload
        assert payload["protocolVersions"] == list(SUPPORTED_PROTOCOL_VERSIONS)
        assert len(payload["protocolVersions"]) >= 1

    def test_payload_includes_preferred_protocol_version(self) -> None:
        auth = self._mock_auth()
        connect_agent(
            "test_agent",
            ConnectAgentOptions(base_url="http://localhost:8080", agent_auth=auth),
        )

        payload = auth.init.call_args[1]["registration_payload"]
        assert "preferredProtocolVersion" in payload
        assert payload["preferredProtocolVersion"] == CURRENT_PROTOCOL_VERSION

    def test_preferred_is_in_supported(self) -> None:
        auth = self._mock_auth()
        connect_agent(
            "test_agent",
            ConnectAgentOptions(base_url="http://localhost:8080", agent_auth=auth),
        )

        payload = auth.init.call_args[1]["registration_payload"]
        assert payload["preferredProtocolVersion"] in payload["protocolVersions"]

    def test_cli_version_absent_when_env_not_set(self) -> None:
        auth = self._mock_auth()
        env = {k: v for k, v in os.environ.items() if k != "BLOCKS_CLI_VERSION"}
        with patch.dict(os.environ, env, clear=True):
            connect_agent(
                "test_agent",
                ConnectAgentOptions(base_url="http://localhost:8080", agent_auth=auth),
            )

        payload = auth.init.call_args[1]["registration_payload"]
        # cliVersion is stripped because it's None
        assert "cliVersion" not in payload

    def test_cli_version_present_when_env_set(self) -> None:
        auth = self._mock_auth()
        with patch.dict(os.environ, {"BLOCKS_CLI_VERSION": "1.2.3"}):
            connect_agent(
                "test_agent",
                ConnectAgentOptions(base_url="http://localhost:8080", agent_auth=auth),
            )

        payload = auth.init.call_args[1]["registration_payload"]
        assert payload["cliVersion"] == "1.2.3"


# ============================================================================
# Registration HTTP header (via AgentAuth)
# ============================================================================


class TestRegistrationHeader:
    def test_agent_auth_sends_protocol_version_header(self) -> None:
        """When agent_auth is used, the auth init path includes version header."""
        mock_auth = MagicMock()
        captured_payload: dict = {}

        def _mock_init(registration_payload=None):
            captured_payload.update(registration_payload or {})
            return {"pamToken": "pam-123", "accessToken": "jwt-1", "refreshToken": "rt-1"}

        mock_auth.init.side_effect = _mock_init

        connect_agent(
            "test_agent",
            ConnectAgentOptions(
                base_url="http://localhost:8080",
                agent_auth=mock_auth,
            ),
        )

        # The payload should include version fields even through auth path
        assert captured_payload["sdkVersion"] == SDK_VERSION
        assert captured_payload["protocolVersions"] == list(SUPPORTED_PROTOCOL_VERSIONS)
        assert captured_payload["preferredProtocolVersion"] == CURRENT_PROTOCOL_VERSION


# ============================================================================
# RPC header emission
# ============================================================================


class TestRpcVersionHeader:
    @patch("blocks_network.rpc_client.urllib.request.urlopen")
    def test_sends_protocol_version_header(self, mock_urlopen) -> None:
        resp = MagicMock()
        resp.read.return_value = json.dumps(
            {"jsonrpc": "2.0", "id": "x", "result": {"ok": True}}
        ).encode("utf-8")
        resp.__enter__ = MagicMock(return_value=resp)
        resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = resp

        call_rpc("sub-c-test", "MyMethod", {"key": "val"}, base_url="http://localhost:3001")

        req = mock_urlopen.call_args[0][0]
        header_key = PROTOCOL_VERSION_HEADER.capitalize()
        header_val = req.headers.get(header_key) or req.headers.get(PROTOCOL_VERSION_HEADER)
        assert header_val == CURRENT_PROTOCOL_VERSION

    def test_agent_auth_rpc_sends_protocol_version_header(self) -> None:
        """When agent_auth is used for RPC, header is included."""
        mock_auth = MagicMock()
        captured_headers: dict = {}

        def _mock_auth_request(url, method="GET", body=None, headers=None):
            captured_headers.update(headers or {})
            return ({"jsonrpc": "2.0", "id": "x", "result": {"ok": True}}, 200)

        mock_auth.authenticated_request.side_effect = _mock_auth_request

        call_rpc(
            "sub-c-test", "MyMethod", {"key": "val"},
            base_url="http://localhost:3001", agent_auth=mock_auth,
        )

        assert PROTOCOL_VERSION_HEADER in captured_headers
        assert captured_headers[PROTOCOL_VERSION_HEADER] == CURRENT_PROTOCOL_VERSION


# ============================================================================
# Control message protocolVersion
# ============================================================================


class TestControlMessageProtocolVersion:
    def test_start_task_parses_protocol_version(self) -> None:
        raw = {
            "type": "StartTask",
            "taskId": "task-1",
            "protocolVersion": "2026-05-01",
        }
        msg = parse_control_message(raw)
        assert isinstance(msg, StartTaskMessage)
        assert msg.protocol_version == "2026-05-01"

    def test_start_task_missing_protocol_version(self) -> None:
        raw = {"type": "StartTask", "taskId": "task-1"}
        msg = parse_control_message(raw)
        assert isinstance(msg, StartTaskMessage)
        assert msg.protocol_version is None

    def test_start_task_serializes_protocol_version(self) -> None:
        msg = StartTaskMessage(
            task_id="t-1",
            protocol_version="2026-05-01",
        )
        d = msg.to_dict()
        assert d["protocolVersion"] == "2026-05-01"

    def test_start_task_omits_none_protocol_version(self) -> None:
        msg = StartTaskMessage(task_id="t-1")
        d = msg.to_dict()
        assert "protocolVersion" not in d

    def test_cancel_task_parses_protocol_version(self) -> None:
        raw = {
            "type": "CancelTask",
            "taskId": "task-c",
            "protocolVersion": "2026-05-01",
        }
        msg = parse_control_message(raw)
        assert isinstance(msg, CancelTaskMessage)
        assert msg.protocol_version == "2026-05-01"

    def test_cancel_task_serializes_protocol_version(self) -> None:
        msg = CancelTaskMessage(
            task_id="tc-1",
            protocol_version="2026-05-01",
        )
        d = msg.to_dict()
        assert d["protocolVersion"] == "2026-05-01"

    def test_expire_task_parses_protocol_version(self) -> None:
        raw = {
            "type": "ExpireTask",
            "taskId": "task-e",
            "protocolVersion": "2026-05-01",
        }
        msg = parse_control_message(raw)
        assert isinstance(msg, ExpireTaskMessage)
        assert msg.protocol_version == "2026-05-01"

    def test_expire_task_serializes_protocol_version(self) -> None:
        msg = ExpireTaskMessage(
            task_id="te-1",
            protocol_version="2026-05-01",
        )
        d = msg.to_dict()
        assert d["protocolVersion"] == "2026-05-01"

    def test_generic_control_parses_protocol_version(self) -> None:
        raw = {
            "type": "PauseTask",
            "taskId": "task-p",
            "protocolVersion": "2026-05-01",
        }
        msg = parse_control_message(raw)
        assert isinstance(msg, ControlMessage)
        assert msg.protocol_version == "2026-05-01"

    def test_generic_control_serializes_protocol_version(self) -> None:
        msg = ControlMessage(
            type="ResumeTask",
            task_id="tr-1",
            protocol_version="2026-05-01",
        )
        d = msg.to_dict()
        assert d["protocolVersion"] == "2026-05-01"

    def test_round_trip_preserves_protocol_version(self) -> None:
        original = StartTaskMessage(
            task_id="rt-1",
            agent_name="echo",
            protocol_version="2026-05-01",
        )
        d = original.to_dict()
        restored = StartTaskMessage.from_dict(d)
        assert restored.protocol_version == original.protocol_version
        assert restored.to_dict() == d


# ============================================================================
# Presence state versioning
# ============================================================================


class TestPresenceStateVersionFields:
    def test_to_dict_includes_version_fields(self) -> None:
        state = AgentInstancePresenceState(
            instance_id="AG-echo-abc",
            active_tasks=0,
            concurrency=4,
            started_at=1700000000000,
            preferred_protocol_version="2026-05-01",
            protocol_versions=["2026-05-01"],
        )
        d = state.to_dict()
        assert d["preferredProtocolVersion"] == "2026-05-01"
        assert d["protocolVersions"] == ["2026-05-01"]

    def test_default_version_fields_empty(self) -> None:
        state = AgentInstancePresenceState(
            instance_id="AG-echo-abc",
        )
        d = state.to_dict()
        assert d["preferredProtocolVersion"] == ""
        assert d["protocolVersions"] == []


# ============================================================================
# Unsupported protocol version handling (agent_instance)
# ============================================================================


def _make_mock_per_task():
    pn = MagicMock()
    pn.set_token = MagicMock()
    pn.stop = MagicMock()
    pn.publish.return_value = MagicMock()
    return pn


def _simulate_start_task(mock_pn, msg, meta=None):
    assert len(mock_pn._listeners) > 0
    listener = mock_pn._listeners[0]
    event = MagicMock()
    event.message = msg
    event.user_metadata = meta
    if hasattr(listener, "message") and callable(listener.message):
        listener.message(mock_pn, event)
    elif isinstance(listener, dict) and "message" in listener:
        listener["message"](event)


def _wait_for(predicate, timeout_sec=2.0, poll_sec=0.05):
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(poll_sec)
    assert predicate(), "Timed out waiting for predicate"


class TestUnsupportedProtocolVersion:
    @pytest.fixture(autouse=True)
    def _patch_create_pubnub(self, monkeypatch):
        import blocks_network.agent_instance as _ai_mod
        monkeypatch.setattr(
            _ai_mod,
            "create_pubnub_client",
            lambda **kw: _make_mock_per_task(),
        )

    def test_targeted_unsupported_version_publishes_terminal_failed(
        self, mock_pubnub, tracking_publish,
    ) -> None:
        """Targeted StartTask with unsupported protocolVersion publishes
        terminal failed with error=unsupported_protocol_version."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        install, records = tracking_publish
        install(mock_pubnub)

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="echo_agent",
                handler=lambda task, ctx: None,
            )
        )

        _simulate_start_task(mock_pubnub, {
            "type": "StartTask",
            "taskId": "task-unsupported",
            "ownerId": "user1",
            "protocolVersion": "1999-01-01",
        })

        _wait_for(lambda: any(
            r.get("message", {}).get("type") == "terminal"
            for r in records
        ), timeout_sec=2.0)

        terminal_records = [
            r for r in records
            if r.get("message", {}).get("type") == "terminal"
        ]
        assert len(terminal_records) >= 1
        msg = terminal_records[0]["message"]
        assert msg["state"] == "failed"
        assert msg["error"] == "unsupported_protocol_version"

        result["stop"]()

    def test_broadcast_unsupported_version_silently_ignored(
        self, mock_pubnub, tracking_publish,
    ) -> None:
        """Broadcast StartTask with unsupported protocolVersion is silently
        ignored (no terminal published)."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        install, records = tracking_publish
        install(mock_pubnub)

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="echo_agent",
                handler=lambda task, ctx: None,
            )
        )

        _simulate_start_task(
            mock_pubnub,
            {
                "type": "StartTask",
                "taskId": "task-broadcast-unsupported",
                "ownerId": "user1",
                "protocolVersion": "1999-01-01",
            },
            meta={"broadcast": "true"},
        )

        # Give it a moment to process
        time.sleep(0.3)

        # No terminal should be published for broadcast unsupported
        terminal_records = [
            r for r in records
            if r.get("message", {}).get("type") == "terminal"
            and r.get("message", {}).get("taskId") == "task-broadcast-unsupported"
        ]
        assert len(terminal_records) == 0

        result["stop"]()

    def test_supported_version_processes_normally(
        self, mock_pubnub, tracking_publish,
    ) -> None:
        """StartTask with supported protocolVersion is processed normally."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        install, records = tracking_publish
        install(mock_pubnub)

        handler_called = threading.Event()

        def handler(task, ctx):
            handler_called.set()

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="echo_agent",
                handler=handler,
            )
        )

        _simulate_start_task(mock_pubnub, {
            "type": "StartTask",
            "taskId": "task-supported",
            "ownerId": "user1",
            "protocolVersion": CURRENT_PROTOCOL_VERSION,
        })

        _wait_for(lambda: handler_called.is_set(), timeout_sec=2.0)
        assert handler_called.is_set()

        result["stop"]()

    def test_missing_version_processes_normally(
        self, mock_pubnub, tracking_publish,
    ) -> None:
        """StartTask without protocolVersion is processed normally
        (backward compat during rollout)."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        install, records = tracking_publish
        install(mock_pubnub)

        handler_called = threading.Event()

        def handler(task, ctx):
            handler_called.set()

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="echo_agent",
                handler=handler,
            )
        )

        _simulate_start_task(mock_pubnub, {
            "type": "StartTask",
            "taskId": "task-no-version",
            "ownerId": "user1",
        })

        _wait_for(lambda: handler_called.is_set(), timeout_sec=2.0)
        assert handler_called.is_set()

        result["stop"]()


# ============================================================================
# Task event protocolVersion in body and meta
# ============================================================================


class TestTaskEventProtocolVersion:
    def test_outbound_events_include_protocol_version(
        self, mock_pubnub,
    ) -> None:
        """All outbound task events should include protocolVersion in
        body and meta."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        # Track publishes on per-task PubNub clients
        all_records: list = []

        def _make_tracked_per_task(**kw):
            pn = MagicMock()
            pn.set_token = MagicMock()
            pn.stop = MagicMock()

            def _tracking_publish():
                chain = MagicMock()
                record: dict = {}

                def _channel(ch):
                    record["channel"] = ch
                    return chain

                def _message(msg):
                    record["message"] = msg
                    return chain

                def _meta(m):
                    record["meta"] = m
                    return chain

                def _should_store(v):
                    return chain

                def _use_post(v):
                    return chain

                def _sync():
                    all_records.append(dict(record))
                    return MagicMock()

                chain.channel = _channel
                chain.message = _message
                chain.meta = _meta
                chain.should_store = _should_store
                chain.use_post = _use_post
                chain.sync = _sync
                return chain

            pn.publish = _tracking_publish
            return pn

        import blocks_network.agent_instance as _ai_mod
        original_create = _ai_mod.create_pubnub_client
        _ai_mod.create_pubnub_client = _make_tracked_per_task

        try:
            result = start_agent_instance(
                AgentInstanceOptions(
                    card=minimal_card(),
                    pubnub=mock_pubnub,
                    agent_name="echo_agent",
                    handler=lambda task, ctx: None,
                )
            )

            _simulate_start_task(mock_pubnub, {
                "type": "StartTask",
                "taskId": "task-pv",
                "ownerId": "user1",
                "protocolVersion": CURRENT_PROTOCOL_VERSION,
            })

            # Wait for terminal event
            _wait_for(lambda: any(
                r.get("message", {}).get("type") == "terminal"
                for r in all_records
            ), timeout_sec=2.0)

            for record in all_records:
                msg = record.get("message", {})
                meta = record.get("meta", {})
                assert msg.get("protocolVersion") == CURRENT_PROTOCOL_VERSION, \
                    f"Missing protocolVersion in body of {msg.get('type')} event"
                assert meta.get("protocolVersion") == CURRENT_PROTOCOL_VERSION, \
                    f"Missing protocolVersion in meta of {msg.get('type')} event"

            result["stop"]()
        finally:
            _ai_mod.create_pubnub_client = original_create


# ============================================================================
# Presence state includes version fields
# ============================================================================


class TestPresenceStateInInstance:
    @pytest.fixture(autouse=True)
    def _patch_create_pubnub(self, monkeypatch):
        import blocks_network.agent_instance as _ai_mod
        monkeypatch.setattr(
            _ai_mod,
            "create_pubnub_client",
            lambda **kw: _make_mock_per_task(),
        )

    def _make_tracking_set_state(self):
        records = []

        def _tracking():
            chain = MagicMock()
            record = {}

            def _channels(chs):
                record["channels"] = chs
                return chain

            def _state(s):
                record["state"] = s
                return chain

            def _sync():
                records.append(dict(record))
                return MagicMock()

            chain.channels = _channels
            chain.state = _state
            chain.sync = _sync
            return chain

        return _tracking, records

    @patch(
        "blocks_network.agent_registry.connect_agent",
        return_value=ConnectAgentResult(control_channel="agent.test-pv-id.control"),
    )
    def test_presence_includes_protocol_version_fields(
        self, _mock_connect, mock_pubnub,
    ) -> None:
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        set_state_fn, records = self._make_tracking_set_state()
        mock_pubnub.set_state = set_state_fn

        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="echo_agent",
                concurrency=2,
            )
        )

        _wait_for(lambda: len(records) >= 1)
        state = records[0]["state"]
        assert state["preferredProtocolVersion"] == CURRENT_PROTOCOL_VERSION
        assert state["protocolVersions"] == list(SUPPORTED_PROTOCOL_VERSIONS)

        result["stop"]()
