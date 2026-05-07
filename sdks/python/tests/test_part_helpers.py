"""Tests for blocks_network.part_helpers -- text_part and file_part."""

from __future__ import annotations

import os
import tempfile

import pytest

from blocks_network.part_helpers import file_part, text_part
from blocks_network.task_client import SendMessageRequestPart


class TestTextPart:
    """Tests for text_part()."""

    def test_default_part_id(self) -> None:
        part = text_part("Hello")
        assert isinstance(part, SendMessageRequestPart)
        assert part.part_id == "text"
        assert part.text == "Hello"
        assert part.file is None
        assert part.file_name is None

    def test_custom_part_id(self) -> None:
        part = text_part("world", part_id="prompt")
        assert part.part_id == "prompt"
        assert part.text == "world"

    def test_empty_text(self) -> None:
        part = text_part("")
        assert part.text == ""
        assert part.part_id == "text"


class TestFilePart:
    """Tests for file_part()."""

    def test_from_path(self) -> None:
        with tempfile.NamedTemporaryFile(suffix=".txt", delete=False) as f:
            f.write(b"file content")
            path = f.name
        try:
            part = file_part(path)
            assert isinstance(part, SendMessageRequestPart)
            assert part.part_id == "file"
            assert part.file == b"file content"
            assert part.file_name == os.path.basename(path)
            assert part.content_type == "application/octet-stream"
        finally:
            os.unlink(path)

    def test_from_path_custom_options(self) -> None:
        with tempfile.NamedTemporaryFile(suffix=".csv", delete=False) as f:
            f.write(b"a,b,c")
            path = f.name
        try:
            part = file_part(
                path,
                part_id="data",
                file_name="custom.csv",
                content_type="text/csv",
            )
            assert part.part_id == "data"
            assert part.file == b"a,b,c"
            assert part.file_name == "custom.csv"
            assert part.content_type == "text/csv"
        finally:
            os.unlink(path)

    def test_from_bytes(self) -> None:
        part = file_part(b"\x00\x01\x02")
        assert part.part_id == "file"
        assert part.file == b"\x00\x01\x02"
        assert part.file_name == "file"
        assert part.content_type == "application/octet-stream"

    def test_from_bytearray(self) -> None:
        part = file_part(bytearray(b"hello"))
        assert part.part_id == "file"
        assert part.file == b"hello"
        assert part.file_name == "file"

    def test_from_bytes_with_custom_name(self) -> None:
        part = file_part(b"data", file_name="report.pdf", content_type="application/pdf")
        assert part.file_name == "report.pdf"
        assert part.content_type == "application/pdf"

    def test_from_pathlib_path(self) -> None:
        from pathlib import Path

        with tempfile.NamedTemporaryFile(suffix=".bin", delete=False) as f:
            f.write(b"\xff\xfe")
            path = Path(f.name)
        try:
            part = file_part(path)
            assert part.file == b"\xff\xfe"
            assert part.file_name == path.name
        finally:
            os.unlink(str(path))
