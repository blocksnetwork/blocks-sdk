"""
Tests for blocks_network.types -- message dataclasses and parsing.

Covers:
- Parsing StartTask, CancelTask, PauseTask dicts into dataclasses
- Unknown message types returning None
- Serialization to camelCase dicts
- Owner ID extraction priority chain
"""

from __future__ import annotations

from blocks_network.types import (
    AgentInstancePresenceState,
    ArtifactRef,
    CancelTaskMessage,
    ControlMessage,
    RequestPart,
    StartTaskMessage,
    parse_control_message,
)

# Import the private helper from agent_instance for owner ID extraction
from blocks_network.agent_instance import _extract_owner_id


# ---------------------------------------------------------------------------
# Parsing
# ---------------------------------------------------------------------------


class TestParseStartTask:
    def test_parse_start_task(self) -> None:
        raw = {
            "type": "StartTask",
            "taskId": "task-001",
            "agentName": "acme-echo",
            "ownerId": "alice",
            "requestParts": [{"text": "hello"}],
            "callerClaims": {"sub": "alice"},
        }
        msg = parse_control_message(raw)
        assert isinstance(msg, StartTaskMessage)
        assert msg.task_id == "task-001"
        assert msg.agent_name == "acme-echo"
        assert msg.owner_id == "alice"
        assert len(msg.request_parts) == 1
        assert isinstance(msg.request_parts[0], RequestPart)
        assert msg.request_parts[0].text == "hello"
        assert msg.caller_claims == {"sub": "alice"}

    def test_parse_start_task_minimal(self) -> None:
        raw = {"type": "StartTask", "taskId": "t-min"}
        msg = parse_control_message(raw)
        assert isinstance(msg, StartTaskMessage)
        assert msg.task_id == "t-min"
        assert msg.owner_id == ""
        assert msg.request_parts == []


class TestParseCancelTask:
    def test_parse_cancel_task(self) -> None:
        raw = {"type": "CancelTask", "taskId": "task-cancel", "reason": "user request"}
        msg = parse_control_message(raw)
        assert isinstance(msg, CancelTaskMessage)
        assert msg.task_id == "task-cancel"
        assert msg.reason == "user request"


class TestParseControlMessagePause:
    def test_parse_pause_task(self) -> None:
        raw = {"type": "PauseTask", "taskId": "task-pause"}
        msg = parse_control_message(raw)
        assert isinstance(msg, ControlMessage)
        assert msg.type == "PauseTask"
        assert msg.task_id == "task-pause"

    def test_parse_resume_task(self) -> None:
        raw = {"type": "ResumeTask", "taskId": "task-resume"}
        msg = parse_control_message(raw)
        assert isinstance(msg, ControlMessage)
        assert msg.type == "ResumeTask"

    def test_parse_terminate_task(self) -> None:
        raw = {"type": "TerminateTask", "taskId": "task-term"}
        msg = parse_control_message(raw)
        assert isinstance(msg, ControlMessage)
        assert msg.type == "TerminateTask"

    def test_parse_retry_task(self) -> None:
        raw = {"type": "RetryTask", "taskId": "task-retry"}
        msg = parse_control_message(raw)
        assert isinstance(msg, ControlMessage)
        assert msg.type == "RetryTask"


class TestParseUnknownType:
    def test_parse_unknown_type(self) -> None:
        raw = {"type": "SomethingElse", "taskId": "x"}
        msg = parse_control_message(raw)
        assert msg is None

    def test_parse_missing_type(self) -> None:
        raw = {"taskId": "no-type"}
        msg = parse_control_message(raw)
        assert msg is None


# ---------------------------------------------------------------------------
# Serialization (to_dict) -- camelCase output
# ---------------------------------------------------------------------------


class TestStartTaskToDict:
    def test_start_task_to_dict(self) -> None:
        msg = StartTaskMessage(
            task_id="t-1",
            agent_name="acme-echo",
            owner_id="bob",
            request_parts=[RequestPart(text="hi")],
            caller_claims={"sub": "bob"},
        )
        d = msg.to_dict()
        assert d["type"] == "StartTask"
        assert d["taskId"] == "t-1"
        assert d["agentName"] == "acme-echo"
        assert d["ownerId"] == "bob"
        assert d["requestParts"] == [{"text": "hi"}]
        assert d["callerClaims"] == {"sub": "bob"}

    def test_start_task_to_dict_omits_empty(self) -> None:
        """Fields that are empty/falsy are omitted from the dict."""
        msg = StartTaskMessage(task_id="t-2")
        d = msg.to_dict()
        assert "taskId" in d
        assert "agentName" not in d
        assert "ownerId" not in d
        assert "requestParts" not in d
        assert "callerClaims" not in d

    def test_cancel_task_to_dict(self) -> None:
        msg = CancelTaskMessage(task_id="tc-1", reason="timeout")
        d = msg.to_dict()
        assert d["type"] == "CancelTask"
        assert d["taskId"] == "tc-1"
        assert d["reason"] == "timeout"


# ---------------------------------------------------------------------------
# Owner ID extraction priority
# ---------------------------------------------------------------------------


class TestExtractOwnerIdPriority:
    def test_explicit_owner_id_wins(self) -> None:
        """ownerId takes highest priority."""
        result = _extract_owner_id("alice", {"sub": "bob"})
        assert result == "alice"

    def test_caller_claims_sub_fallback(self) -> None:
        """callerClaims.sub is used when ownerId is empty."""
        result = _extract_owner_id("", {"sub": "bob"})
        assert result == "bob"

    def test_anonymous_fallback(self) -> None:
        """Returns 'anonymous' when both are missing."""
        result = _extract_owner_id("", None)
        assert result == "anonymous"

    def test_none_owner_id(self) -> None:
        result = _extract_owner_id(None, {"sub": "charlie"})
        assert result == "charlie"

    def test_empty_caller_claims(self) -> None:
        result = _extract_owner_id(None, {})
        assert result == "anonymous"

    def test_empty_sub_in_claims(self) -> None:
        result = _extract_owner_id(None, {"sub": ""})
        assert result == "anonymous"


# ---------------------------------------------------------------------------
# Round-trip serialization tests
# ---------------------------------------------------------------------------


class TestStartTaskRoundTrip:
    def test_full_round_trip(self) -> None:
        original = StartTaskMessage(
            task_id="rt-1",
            agent_name="acme-echo",
            owner_id="alice",
            request_parts=[
                RequestPart(text="hello"),
                RequestPart(extra={"data": 42}),
            ],
            caller_claims={"sub": "alice", "aud": "test"},
            request_summary={"intent": "echo"},
        )
        d = original.to_dict()
        restored = StartTaskMessage.from_dict(d)
        assert restored.task_id == original.task_id
        assert restored.agent_name == original.agent_name
        assert restored.owner_id == original.owner_id
        assert len(restored.request_parts) == len(original.request_parts)
        assert restored.request_parts[0].text == "hello"
        assert restored.request_parts[1].extra.get("data") == 42
        assert restored.caller_claims == original.caller_claims
        assert restored.request_summary == original.request_summary
        # Re-serialize should match
        assert restored.to_dict() == d


class TestCancelTaskRoundTrip:
    def test_round_trip(self) -> None:
        original = CancelTaskMessage(
            task_id="ct-1",
            reason="user request",
            caller="admin",
        )
        d = original.to_dict()
        restored = CancelTaskMessage.from_dict(d)
        assert restored.task_id == original.task_id
        assert restored.reason == original.reason
        assert restored.caller == original.caller
        assert restored.to_dict() == d


class TestArtifactRefRoundTrip:
    def test_inline_round_trip(self) -> None:
        original = ArtifactRef(
            kind="inline",
            mime_type="text/plain",
            size=11,
            data="aGVsbG8gd29ybGQ=",
            hash="sha256:abc",
        )
        d = original.to_dict()
        restored = ArtifactRef.from_dict(d)
        assert restored.kind == "inline"
        assert restored.mime_type == "text/plain"
        assert restored.size == 11
        assert restored.data == "aGVsbG8gd29ybGQ="
        assert restored.hash == "sha256:abc"
        assert restored.file_id is None
        assert restored.to_dict() == d

    def test_file_round_trip(self) -> None:
        original = ArtifactRef(
            kind="file",
            mime_type="image/png",
            size=1024,
            channel="u.org123.task-uuid",
            file_id="f-123",
            file_name="image.png",
            expires_at="2025-12-31T23:59:59Z",
        )
        d = original.to_dict()
        restored = ArtifactRef.from_dict(d)
        assert restored.kind == "file"
        assert restored.channel == "u.org123.task-uuid"
        assert restored.file_id == "f-123"
        assert restored.file_name == "image.png"
        assert restored.expires_at == "2025-12-31T23:59:59Z"
        assert restored.data is None
        assert restored.to_dict() == d

    def test_inline_with_file_name(self) -> None:
        original = ArtifactRef(
            kind="inline",
            mime_type="text/csv",
            size=42,
            data="aGVsbG8=",
            file_name="report.csv",
        )
        d = original.to_dict()
        assert d["fileName"] == "report.csv"
        assert d["kind"] == "inline"
        restored = ArtifactRef.from_dict(d)
        assert restored.file_name == "report.csv"


class TestAgentInstancePresenceStateToDict:
    def test_to_dict(self) -> None:
        state = AgentInstancePresenceState(
            instance_id="AG-echo-abc",
            active_tasks=2,
            concurrency=4,
            started_at=1700000000000,
        )
        d = state.to_dict()
        assert d["instanceId"] == "AG-echo-abc"
        assert d["activeTasks"] == 2
        assert d["concurrency"] == 4
        assert d["startedAt"] == 1700000000000


class TestControlMessageToDict:
    def test_to_dict_without_caller(self) -> None:
        msg = ControlMessage(type="PauseTask", task_id="t-1")
        d = msg.to_dict()
        assert d["type"] == "PauseTask"
        assert d["taskId"] == "t-1"
        assert "caller" not in d

    def test_to_dict_with_caller(self) -> None:
        msg = ControlMessage(type="ResumeTask", task_id="t-2", caller="admin")
        d = msg.to_dict()
        assert d["type"] == "ResumeTask"
        assert d["taskId"] == "t-2"
        assert d["caller"] == "admin"
