"""
Tests for task artifacts initiative changes.

Covers:
- RequestPart.artifact_ref round-trip
- ArtifactRef channel field (file variant)
- ArtifactRef fileName field (inline variant)
- download_input_artifact for inline and file variants
- SendMessageRequestPart dataclass
- TaskClient.send_message file handling (inline and large files)
- Handler plural artifacts processing
- TaskContext.download_input_artifact and publish_artifact availability
"""

from __future__ import annotations

import base64
import dataclasses
import json
import threading
import time
from unittest.mock import MagicMock, patch

import pytest


def _make_session_pubnub_mock():
    """Create a MagicMock PubNub with time()/fetch_messages() for sendMessage history catch-up."""
    pn = MagicMock()
    time_result = MagicMock()
    time_result.result.timetoken = "17000000000000000"
    pn.time.return_value.sync.return_value = time_result
    fetch_chain = MagicMock()
    fetch_chain.channels.return_value = fetch_chain
    fetch_chain.maximum_per_channel.return_value = fetch_chain
    fetch_chain.start.return_value = fetch_chain
    fetch_result = MagicMock()
    fetch_result.result.channels = {}
    fetch_chain.sync.return_value = fetch_result
    pn.fetch_messages.return_value = fetch_chain
    return pn

from blocks_network.types import (
    ArtifactRef,
    RequestPart,
    StartTaskMessage,
    TaskContext,
)
from blocks_network.task_client import SendMessageParams, SendMessageRequestPart, TaskClient
from blocks_network.agent_instance import _download_input_artifact
from blocks_network.artifacts import build_artifact_ref


# ---------------------------------------------------------------------------
# RequestPart.artifact_ref
# ---------------------------------------------------------------------------


class TestRequestPartArtifactRef:
    def test_artifact_ref_inline_round_trip(self) -> None:
        raw = {
            "partId": "doc",
            "artifactRef": {
                "kind": "inline",
                "mimeType": "text/plain",
                "size": 5,
                "data": "aGVsbG8=",
                "fileName": "hello.txt",
            },
        }
        part = RequestPart.from_dict(raw)
        assert part.artifact_ref is not None
        assert part.artifact_ref.kind == "inline"
        assert part.artifact_ref.file_name == "hello.txt"
        assert part.artifact_ref.data == "aGVsbG8="

        d = part.to_dict()
        assert d["artifactRef"]["kind"] == "inline"
        assert d["artifactRef"]["fileName"] == "hello.txt"

    def test_artifact_ref_file_round_trip(self) -> None:
        raw = {
            "partId": "dataset",
            "artifactRef": {
                "kind": "file",
                "channel": "u.org123.task-uuid",
                "mimeType": "text/csv",
                "size": 524288,
                "fileId": "pn-file-id-xyz",
                "fileName": "data.csv",
            },
        }
        part = RequestPart.from_dict(raw)
        assert part.artifact_ref is not None
        assert part.artifact_ref.kind == "file"
        assert part.artifact_ref.channel == "u.org123.task-uuid"
        assert part.artifact_ref.file_id == "pn-file-id-xyz"

    def test_artifact_ref_absent(self) -> None:
        raw = {"partId": "text", "text": "hello"}
        part = RequestPart.from_dict(raw)
        assert part.artifact_ref is None

    def test_artifact_ref_not_serialized_when_none(self) -> None:
        part = RequestPart(part_id="text", text="hello")
        d = part.to_dict()
        assert "artifactRef" not in d


# ---------------------------------------------------------------------------
# ArtifactRef channel field
# ---------------------------------------------------------------------------


class TestArtifactRefChannel:
    def test_file_variant_requires_channel(self) -> None:
        ref = ArtifactRef(
            kind="file",
            channel="u.org1.task-1",
            file_id="f-1",
            file_name="output.bin",
            size=1024,
        )
        d = ref.to_dict()
        assert d["channel"] == "u.org1.task-1"
        assert "fileUrl" not in d

    def test_file_variant_from_dict(self) -> None:
        raw = {
            "kind": "file",
            "channel": "u.org2.task-2",
            "mimeType": "image/png",
            "size": 2048,
            "fileId": "f-2",
            "fileName": "chart.png",
        }
        ref = ArtifactRef.from_dict(raw)
        assert ref.channel == "u.org2.task-2"
        assert ref.file_id == "f-2"

    def test_inline_variant_no_channel(self) -> None:
        ref = ArtifactRef(kind="inline", data="aGVsbG8=", size=5)
        d = ref.to_dict()
        assert "channel" not in d


# ---------------------------------------------------------------------------
# download_input_artifact
# ---------------------------------------------------------------------------


class TestDownloadInputArtifact:
    def test_download_inline(self) -> None:
        ref = ArtifactRef(
            kind="inline",
            data=base64.b64encode(b"hello world").decode("ascii"),
            size=11,
        )
        result = _download_input_artifact(MagicMock(), ref)
        assert result == b"hello world"

    def test_download_inline_from_dict(self) -> None:
        ref_dict = {
            "kind": "inline",
            "data": base64.b64encode(b"dict data").decode("ascii"),
            "size": 9,
        }
        result = _download_input_artifact(MagicMock(), ref_dict)
        assert result == b"dict data"

    def test_download_file(self) -> None:
        pn = MagicMock()
        chain = MagicMock()
        for method in ("channel", "file_id", "file_name"):
            getattr(chain, method).side_effect = lambda *a, _c=chain, **kw: _c
        download_result = MagicMock()
        download_result.result.data = b"file content from pubnub"
        chain.sync.return_value = download_result
        pn.download_file.return_value = chain

        ref = ArtifactRef(
            kind="file",
            channel="u.org1.task-1",
            file_id="f-123",
            file_name="data.csv",
            size=100,
        )
        result = _download_input_artifact(pn, ref)
        assert result == b"file content from pubnub"

    def test_download_raises_no_ref(self) -> None:
        with pytest.raises(ValueError, match="No artifactRef"):
            _download_input_artifact(MagicMock(), None)

    def test_download_raises_missing_fields(self) -> None:
        ref = ArtifactRef(kind="file")
        with pytest.raises(ValueError, match="missing required fields"):
            _download_input_artifact(MagicMock(), ref)


# ---------------------------------------------------------------------------
# SendMessageRequestPart
# ---------------------------------------------------------------------------


class TestSendMessageRequestPart:
    def test_basic_fields(self) -> None:
        part = SendMessageRequestPart(
            part_id="doc",
            text="hello",
            content_type="text/plain",
        )
        assert part.part_id == "doc"
        assert part.file is None
        assert part.file_name is None

    def test_with_file(self) -> None:
        part = SendMessageRequestPart(
            part_id="attachment",
            file=b"binary data",
            file_name="report.pdf",
            content_type="application/pdf",
        )
        assert part.file == b"binary data"
        assert part.file_name == "report.pdf"


# ---------------------------------------------------------------------------
# TaskClient.send_message with files
# ---------------------------------------------------------------------------


class TestTaskClientFileHandling:
    @patch("blocks_network.task_client.call_rpc")
    def test_inline_small_file(self, mock_rpc) -> None:
        mock_rpc.return_value = {
            "taskId": "t-1",
            "extensions": {"blocks": {"readToken": "rt-1"}},
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            base_url="http://localhost:3000",
        )

        small_data = b"small file content"  # < 16 KB
        params = SendMessageParams(
            agent_name="echo",
            owner_id="alice",
            request_parts=[
                SendMessageRequestPart(
                    part_id="attachment",
                    file=small_data,
                    file_name="small.txt",
                    content_type="text/plain",
                ),
            ],
        )

        with patch.object(client, "_create_session_pubnub", return_value=_make_session_pubnub_mock()):
            client.send_message(
                agent_name=params.agent_name,
                owner_id=params.owner_id,
                request_parts=params.request_parts,
                idempotency_key=params.idempotency_key,
                task_kind=params.task_kind,
                duration=params.duration,
                consumer_public_key=params.consumer_public_key,
            )

        call_args = mock_rpc.call_args
        rpc_params = call_args[0][2]
        wire_part = rpc_params["requestParts"][0]
        assert "artifactRef" in wire_part
        assert wire_part["artifactRef"]["kind"] == "inline"
        assert wire_part["artifactRef"]["fileName"] == "small.txt"
        # No uploadSessionId for inline-only
        assert "uploadSessionId" not in rpc_params

    @patch("blocks_network.task_client.call_rpc")
    def test_text_only_part(self, mock_rpc) -> None:
        mock_rpc.return_value = {
            "taskId": "t-2",
            "extensions": {"blocks": {"readToken": "rt-2"}},
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            base_url="http://localhost:3000",
        )

        params = SendMessageParams(
            agent_name="echo",
            owner_id="alice",
            request_parts=[{"partId": "text", "text": "hello"}],
        )

        with patch.object(client, "_create_session_pubnub", return_value=_make_session_pubnub_mock()):
            client.send_message(
                agent_name=params.agent_name,
                owner_id=params.owner_id,
                request_parts=params.request_parts,
                idempotency_key=params.idempotency_key,
                task_kind=params.task_kind,
                duration=params.duration,
                consumer_public_key=params.consumer_public_key,
            )

        rpc_params = mock_rpc.call_args[0][2]
        assert "uploadSessionId" not in rpc_params
        assert rpc_params["requestParts"][0]["text"] == "hello"

    @patch("blocks_network.file_upload.presigned_upload_flow")
    @patch("blocks_network.task_client.call_rpc")
    def test_large_file_uses_presigned_flow(self, mock_rpc, mock_upload) -> None:
        mock_rpc.return_value = {
            "taskId": "t-3",
            "extensions": {"blocks": {"readToken": "rt-3"}},
        }
        mock_upload.return_value = {
            "uploadSessionId": "session-1",
            "uploadId": "upload-1",
        }

        client = TaskClient(
            subscribe_key="sub-key",
            billing_mode="free",
            base_url="http://localhost:3000",
        )

        large_data = b"x" * 20_000  # > 16 KB
        params = SendMessageParams(
            agent_name="echo",
            owner_id="alice",
            request_parts=[
                SendMessageRequestPart(
                    part_id="big_file",
                    file=large_data,
                    file_name="big.bin",
                    content_type="application/octet-stream",
                ),
            ],
        )

        with patch.object(client, "_create_session_pubnub", return_value=_make_session_pubnub_mock()):
            client.send_message(
                agent_name=params.agent_name,
                owner_id=params.owner_id,
                request_parts=params.request_parts,
                idempotency_key=params.idempotency_key,
                task_kind=params.task_kind,
                duration=params.duration,
                consumer_public_key=params.consumer_public_key,
            )

        mock_upload.assert_called_once()
        rpc_params = mock_rpc.call_args[0][2]
        assert rpc_params["uploadSessionId"] == "session-1"
        # The wire part should NOT have artifactRef (backend reconstructs it)
        wire_part = rpc_params["requestParts"][0]
        assert "artifactRef" not in wire_part
        assert wire_part["partId"] == "big_file"


# ---------------------------------------------------------------------------
# build_artifact_ref updated shapes
# ---------------------------------------------------------------------------


class TestBuildArtifactRefUpdated:
    def test_inline_with_file_name(self) -> None:
        ref = build_artifact_ref(
            data=b"small",
            mime_type="text/plain",
            file_name="report.txt",
        )
        assert ref["kind"] == "inline"
        assert ref["fileName"] == "report.txt"

    def test_file_with_channel(self) -> None:
        ref = build_artifact_ref(
            data=b"x" * 20_000,
            mime_type="application/pdf",
            file={"id": "f-1", "name": "doc.pdf", "channel": "u.org1.task-1"},
            inline_limit=1000,
        )
        assert ref["kind"] == "file"
        assert ref["channel"] == "u.org1.task-1"
        assert "fileUrl" not in ref


# ---------------------------------------------------------------------------
# TaskContext stubs
# ---------------------------------------------------------------------------


class TestTaskContextStubs:
    def test_download_input_artifact_stub_raises(self) -> None:
        ctx = TaskContext(task_id="t-1")
        with pytest.raises(RuntimeError, match="download_input_artifact"):
            ctx.download_input_artifact(MagicMock())

    def test_publish_artifact_stub_raises(self) -> None:
        ctx = TaskContext(task_id="t-1")
        with pytest.raises(RuntimeError, match="publish_artifact"):
            ctx.publish_artifact(b"data")


# ---------------------------------------------------------------------------
# StartTaskMessage with artifactRef on parts
# ---------------------------------------------------------------------------


class TestStartTaskMessageWithArtifactRef:
    def test_start_task_with_artifact_ref_parts(self) -> None:
        raw = {
            "type": "StartTask",
            "taskId": "t-1",
            "agentName": "echo",
            "ownerId": "alice",
            "requestParts": [
                {
                    "partId": "document",
                    "artifactRef": {
                        "kind": "file",
                        "channel": "u.org1.t-1",
                        "mimeType": "application/pdf",
                        "size": 1024,
                        "fileId": "f-1",
                        "fileName": "doc.pdf",
                    },
                },
                {"partId": "text", "text": "Summarize this"},
            ],
        }
        msg = StartTaskMessage.from_dict(raw)
        assert len(msg.request_parts) == 2
        assert msg.request_parts[0].artifact_ref is not None
        assert msg.request_parts[0].artifact_ref.kind == "file"
        assert msg.request_parts[0].artifact_ref.channel == "u.org1.t-1"
        assert msg.request_parts[1].artifact_ref is None
        assert msg.request_parts[1].text == "Summarize this"

    def test_start_task_round_trip_with_artifact_ref(self) -> None:
        raw = {
            "type": "StartTask",
            "taskId": "t-2",
            "requestParts": [
                {
                    "partId": "inline_doc",
                    "artifactRef": {
                        "kind": "inline",
                        "mimeType": "text/plain",
                        "size": 5,
                        "data": "aGVsbG8=",
                        "fileName": "hello.txt",
                    },
                },
            ],
        }
        msg = StartTaskMessage.from_dict(raw)
        d = msg.to_dict()
        restored = StartTaskMessage.from_dict(d)
        assert restored.request_parts[0].artifact_ref.kind == "inline"
        assert restored.request_parts[0].artifact_ref.file_name == "hello.txt"


# ---------------------------------------------------------------------------
# D5: Fail fast for large artifacts without base_url
# ---------------------------------------------------------------------------


class TestLargeArtifactWithoutBaseUrl:
    """Large artifacts (> 16 KB) MUST raise ValueError when base_url is not configured."""

    @patch("blocks_network.task_client.call_rpc")
    def test_send_message_large_file_no_base_url_raises(self, mock_rpc) -> None:
        """TaskClient.send_message raises ValueError for large file without base_url."""
        client = TaskClient(subscribe_key="sub-key", billing_mode="free")  # no base_url

        large_data = b"x" * 20_000  # > 16 KB
        params = SendMessageParams(
            agent_name="echo",
            owner_id="alice",
            request_parts=[
                SendMessageRequestPart(
                    part_id="big_file",
                    file=large_data,
                    file_name="big.bin",
                    content_type="application/octet-stream",
                ),
            ],
        )

        with pytest.raises(ValueError, match="base_url is required for artifacts larger than 16 KB"):
            client.send_message(
                agent_name=params.agent_name,
                owner_id=params.owner_id,
                request_parts=params.request_parts,
                idempotency_key=params.idempotency_key,
                task_kind=params.task_kind,
                duration=params.duration,
                consumer_public_key=params.consumer_public_key,
            )

        mock_rpc.assert_not_called()

    @patch("blocks_network.task_client.call_rpc")
    def test_send_message_large_file_dict_part_no_base_url_raises(self, mock_rpc) -> None:
        """TaskClient.send_message raises ValueError for large dict file part without base_url."""
        client = TaskClient(subscribe_key="sub-key", billing_mode="free")  # no base_url

        large_data = b"y" * 20_000  # > 16 KB
        params = SendMessageParams(
            agent_name="echo",
            owner_id="alice",
            request_parts=[
                {"partId": "big", "file": large_data, "fileName": "big.bin"},
            ],
        )

        with pytest.raises(ValueError, match="base_url is required for artifacts larger than 16 KB"):
            client.send_message(
                agent_name=params.agent_name,
                owner_id=params.owner_id,
                request_parts=params.request_parts,
                idempotency_key=params.idempotency_key,
                task_kind=params.task_kind,
                duration=params.duration,
                consumer_public_key=params.consumer_public_key,
            )

        mock_rpc.assert_not_called()
