"""
Convenience helpers for building request parts.

Provides :func:`text_part` and :func:`file_part` so consumers can
construct :class:`SendMessageRequestPart` instances without knowing
wire format field names or manually reading files.
"""

from __future__ import annotations

from pathlib import Path
from typing import Optional, Union

from .task_client import SendMessageRequestPart


def text_part(text: str, part_id: str = "text") -> SendMessageRequestPart:
    """Build a text-only request part.

    Parameters
    ----------
    text:
        The text content.
    part_id:
        Part ID matching a declared ``io.inputs[].id`` in the agent
        card. Defaults to ``"text"``.
    """
    return SendMessageRequestPart(part_id=part_id, text=text)


def file_part(
    path_or_data: Union[str, Path, bytes, bytearray],
    *,
    part_id: str = "file",
    file_name: Optional[str] = None,
    content_type: str = "application/octet-stream",
) -> SendMessageRequestPart:
    """Build a file request part from a path or raw bytes.

    Parameters
    ----------
    path_or_data:
        File path (str or Path) to read, or raw bytes/bytearray.
    part_id:
        Part ID matching a declared ``io.inputs[].id`` in the agent
        card. Defaults to ``"file"``.
    file_name:
        Override file name. When reading from a path, defaults to
        the file's basename.
    content_type:
        MIME type. Defaults to ``"application/octet-stream"``.
    """
    if isinstance(path_or_data, (str, Path)):
        p = Path(path_or_data)
        return SendMessageRequestPart(
            part_id=part_id,
            file=p.read_bytes(),
            file_name=file_name or p.name,
            content_type=content_type,
        )
    return SendMessageRequestPart(
        part_id=part_id,
        file=bytes(path_or_data),
        file_name=file_name or "file",
        content_type=content_type,
    )
