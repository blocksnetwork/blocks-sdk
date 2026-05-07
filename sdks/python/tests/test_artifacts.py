"""
Tests for blocks_network.artifacts -- artifact reference building and inline/file decisions.

Covers:
- should_inline_artifact threshold logic
- build_artifact_ref for inline artifacts (base64 encoding, kind, mimeType, size)
- build_artifact_ref for file artifacts (kind='file', file info)
- Fallback to inline when no file info is provided
"""

from __future__ import annotations

import base64

from blocks_network.artifacts import build_artifact_ref, should_inline_artifact


# ---------------------------------------------------------------------------
# should_inline_artifact
# ---------------------------------------------------------------------------


class TestShouldInlineArtifact:
    def test_should_inline_small_artifact(self) -> None:
        """Payload smaller than the default limit (16384 bytes) should inline."""
        assert should_inline_artifact(100) is True

    def test_should_inline_at_boundary(self) -> None:
        """Exactly at the default limit should still inline (<= check)."""
        assert should_inline_artifact(16384) is True

    def test_should_not_inline_large_artifact(self) -> None:
        """Payload larger than the default limit should NOT inline."""
        assert should_inline_artifact(16385) is False

    def test_custom_inline_limit(self) -> None:
        """Explicitly passing an inline_limit overrides the config default."""
        assert should_inline_artifact(500, inline_limit=1000) is True
        assert should_inline_artifact(1500, inline_limit=1000) is False


# ---------------------------------------------------------------------------
# build_artifact_ref -- inline
# ---------------------------------------------------------------------------


class TestBuildArtifactRefInline:
    def test_build_artifact_ref_inline(self) -> None:
        data = b"hello world"
        ref = build_artifact_ref(data=data, mime_type="text/plain")

        assert ref["kind"] == "inline"
        assert ref["mimeType"] == "text/plain"
        assert ref["size"] == len(data)
        # Verify base64 round-trip
        decoded = base64.b64decode(ref["data"])
        assert decoded == data

    def test_build_artifact_ref_inline_from_string(self) -> None:
        data = "utf-8 string payload"
        ref = build_artifact_ref(data=data, mime_type="text/plain")

        assert ref["kind"] == "inline"
        assert ref["size"] == len(data.encode("utf-8"))
        decoded = base64.b64decode(ref["data"])
        assert decoded == data.encode("utf-8")

    def test_build_artifact_ref_default_mime(self) -> None:
        ref = build_artifact_ref(data=b"\x00\x01")
        assert ref["mimeType"] == "application/octet-stream"


# ---------------------------------------------------------------------------
# build_artifact_ref -- file
# ---------------------------------------------------------------------------


class TestBuildArtifactRefFile:
    def test_build_artifact_ref_file(self) -> None:
        """When size exceeds inline limit and file info is provided, kind='file'."""
        large_data = b"x" * 50_000
        file_info = {
            "id": "file-abc",
            "name": "output.bin",
            "channel": "u.org123.task-uuid",
        }
        ref = build_artifact_ref(
            data=large_data,
            mime_type="application/octet-stream",
            file=file_info,
            inline_limit=1000,
        )

        assert ref["kind"] == "file"
        assert ref["fileId"] == "file-abc"
        assert ref["fileName"] == "output.bin"
        assert ref["channel"] == "u.org123.task-uuid"
        assert ref["size"] == 50_000
        assert ref["mimeType"] == "application/octet-stream"
        assert "fileUrl" not in ref

    def test_build_artifact_ref_file_name_param(self) -> None:
        """file_name parameter is used when file dict has no name."""
        large_data = b"x" * 50_000
        file_info = {
            "id": "file-abc",
            "channel": "u.org123.task-uuid",
        }
        ref = build_artifact_ref(
            data=large_data,
            mime_type="application/octet-stream",
            file=file_info,
            file_name="fallback.bin",
            inline_limit=1000,
        )

        assert ref["kind"] == "file"
        assert ref["fileName"] == "fallback.bin"

    def test_build_artifact_ref_inline_with_file_name(self) -> None:
        """file_name is preserved on inline artifacts."""
        ref = build_artifact_ref(
            data=b"small",
            mime_type="text/plain",
            file_name="report.txt",
        )

        assert ref["kind"] == "inline"
        assert ref["fileName"] == "report.txt"

    def test_build_artifact_ref_fallback_to_inline(self) -> None:
        """When file info is None but payload is large, it falls back to inline."""
        large_data = b"y" * 50_000
        ref = build_artifact_ref(
            data=large_data,
            mime_type="application/json",
            file=None,
            inline_limit=1000,
        )

        # The code has: inline = should_inline_artifact(...) or file is None
        # So when file is None, inline is always True
        assert ref["kind"] == "inline"
        assert ref["size"] == 50_000
        assert ref["mimeType"] == "application/json"
        decoded = base64.b64decode(ref["data"])
        assert decoded == large_data


# ---------------------------------------------------------------------------
# Hash field
# ---------------------------------------------------------------------------


class TestBuildArtifactRefHash:
    def test_hash_included_when_provided(self) -> None:
        ref = build_artifact_ref(
            data=b"data",
            mime_type="text/plain",
            hash="sha256:abc123",
        )
        assert ref["hash"] == "sha256:abc123"

    def test_hash_omitted_when_none(self) -> None:
        ref = build_artifact_ref(data=b"data", mime_type="text/plain")
        assert "hash" not in ref
