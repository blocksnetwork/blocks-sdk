"""
Tests for blocks_network.file_upload -- pre-signed URL upload helper.

Covers:
- _build_multipart_body construction
- request_upload, confirm_upload, upload_to_presigned_url
- presigned_upload_flow end-to-end
- FileUploadError handling
"""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.file_upload import (
    FileUploadError,
    _build_multipart_body,
    confirm_upload,
    presigned_upload_flow,
    request_upload,
    upload_to_presigned_url,
)


# ---------------------------------------------------------------------------
# _build_multipart_body
# ---------------------------------------------------------------------------


class TestBuildMultipartBody:
    def test_basic_body_structure(self) -> None:
        form_fields = [
            {"key": "Content-Type", "value": "application/pdf"},
            {"key": "key", "value": "some/path"},
        ]
        body, content_type = _build_multipart_body(
            form_fields, b"file data", "test.pdf", "application/pdf"
        )

        assert "multipart/form-data; boundary=" in content_type
        assert b"Content-Type" in body
        assert b"application/pdf" in body
        assert b"file data" in body
        assert b'name="file"' in body
        assert b'filename="test.pdf"' in body

    def test_empty_form_fields(self) -> None:
        body, content_type = _build_multipart_body(
            [], b"data", "f.bin", "application/octet-stream"
        )
        assert b'name="file"' in body
        assert b"data" in body


# ---------------------------------------------------------------------------
# request_upload
# ---------------------------------------------------------------------------


class TestRequestUpload:
    @patch("blocks_network.file_upload._authenticated_json_post")
    def test_consumer_input_first_file(self, mock_post) -> None:
        mock_post.return_value = {
            "uploadSessionId": "session-1",
            "uploadId": "upload-1",
            "uploadUrl": "https://s3.example.com/upload",
            "formFields": [],
        }
        result = request_upload(
            "http://localhost:3000",
            role="consumer-input",
            file_name="report.pdf",
            file_size=1024,
            mime_type="application/pdf",
            agent_name="my_agent",
            part_id="document",
        )
        assert result["uploadSessionId"] == "session-1"
        assert result["uploadId"] == "upload-1"

        call_args = mock_post.call_args
        body = call_args[0][1]
        assert body["role"] == "consumer-input"
        assert body["agentName"] == "my_agent"
        assert body["partId"] == "document"

    @patch("blocks_network.file_upload._authenticated_json_post")
    def test_consumer_input_join_session(self, mock_post) -> None:
        mock_post.return_value = {"uploadId": "upload-2"}
        request_upload(
            "http://localhost:3000",
            role="consumer-input",
            file_name="photo.jpg",
            file_size=2048,
            mime_type="image/jpeg",
            upload_session_id="session-1",
            part_id="photo",
        )
        body = mock_post.call_args[0][1]
        assert body["uploadSessionId"] == "session-1"
        assert "agentName" not in body

    @patch("blocks_network.file_upload._authenticated_json_post")
    def test_provider_output(self, mock_post) -> None:
        mock_post.return_value = {"uploadId": "upload-3"}
        request_upload(
            "http://localhost:3000",
            role="provider-output",
            file_name="chart.png",
            file_size=4096,
            mime_type="image/png",
            task_id="task-1",
            output_id="chart",
        )
        body = mock_post.call_args[0][1]
        assert body["role"] == "provider-output"
        assert body["taskId"] == "task-1"
        assert body["outputId"] == "chart"


# ---------------------------------------------------------------------------
# confirm_upload
# ---------------------------------------------------------------------------


class TestConfirmUpload:
    @patch("blocks_network.file_upload._authenticated_json_post")
    def test_confirm_sends_upload_id(self, mock_post) -> None:
        mock_post.return_value = {"uploadId": "upload-1"}
        result = confirm_upload("http://localhost:3000", "upload-1")
        body = mock_post.call_args[0][1]
        assert body["uploadId"] == "upload-1"
        assert result["uploadId"] == "upload-1"

    @patch("blocks_network.file_upload._authenticated_json_post")
    def test_confirm_returns_artifact_ref_for_provider(self, mock_post) -> None:
        mock_post.return_value = {
            "uploadId": "upload-2",
            "artifactRef": {
                "kind": "file",
                "channel": "u.org1.task-1",
                "fileId": "f-1",
                "fileName": "output.pdf",
                "mimeType": "application/pdf",
                "size": 4096,
            },
        }
        result = confirm_upload("http://localhost:3000", "upload-2")
        assert "artifactRef" in result
        assert result["artifactRef"]["kind"] == "file"


# ---------------------------------------------------------------------------
# presigned_upload_flow
# ---------------------------------------------------------------------------


class TestPresignedUploadFlow:
    @patch("blocks_network.file_upload.confirm_upload")
    @patch("blocks_network.file_upload.upload_to_presigned_url")
    @patch("blocks_network.file_upload.request_upload")
    def test_full_flow_consumer_input(self, mock_request, mock_upload, mock_confirm) -> None:
        mock_request.return_value = {
            "uploadSessionId": "session-1",
            "uploadId": "upload-1",
            "uploadUrl": "https://s3.example.com/upload",
            "formFields": [{"key": "key", "value": "path"}],
        }
        mock_confirm.return_value = {"uploadId": "upload-1"}

        result = presigned_upload_flow(
            "http://localhost:3000",
            b"file content",
            role="consumer-input",
            file_name="data.csv",
            mime_type="text/csv",
            agent_name="my_agent",
            part_id="dataset",
        )

        assert result["uploadId"] == "upload-1"
        assert result["uploadSessionId"] == "session-1"
        mock_upload.assert_called_once()
        mock_confirm.assert_called_once()

    @patch("blocks_network.file_upload.confirm_upload")
    @patch("blocks_network.file_upload.upload_to_presigned_url")
    @patch("blocks_network.file_upload.request_upload")
    def test_full_flow_provider_output(self, mock_request, mock_upload, mock_confirm) -> None:
        mock_request.return_value = {
            "uploadId": "upload-2",
            "uploadUrl": "https://s3.example.com/upload",
            "formFields": [],
        }
        mock_confirm.return_value = {
            "uploadId": "upload-2",
            "artifactRef": {"kind": "file", "channel": "u.org1.task-1"},
        }

        result = presigned_upload_flow(
            "http://localhost:3000",
            b"large file",
            role="provider-output",
            file_name="output.bin",
            task_id="task-1",
            output_id="result",
        )

        assert result["uploadId"] == "upload-2"
        assert "artifactRef" in result
        assert "uploadSessionId" not in result

    @patch("blocks_network.file_upload.request_upload")
    def test_raises_on_missing_upload_url(self, mock_request) -> None:
        mock_request.return_value = {"uploadId": "x"}
        with pytest.raises(FileUploadError, match="uploadUrl"):
            presigned_upload_flow(
                "http://localhost:3000",
                b"data",
                role="consumer-input",
                file_name="f.bin",
            )


# ---------------------------------------------------------------------------
# FileUploadError
# ---------------------------------------------------------------------------


class TestFileUploadError:
    def test_error_message(self) -> None:
        err = FileUploadError("something failed", status_code=400)
        assert "something failed" in str(err)
        assert err.status_code == 400

    def test_error_without_status(self) -> None:
        err = FileUploadError("no status")
        assert err.status_code is None
