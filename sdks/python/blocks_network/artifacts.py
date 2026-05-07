"""
Artifact reference builder and downloader.

Port of ``src/shared/artifacts.ts``.
"""

from __future__ import annotations

import base64
from dataclasses import dataclass
from typing import Any, Dict, Optional, Union

from . import config as _cfg
from .types import ArtifactRef


# ============================================================================
# Downloaded artifact
# ============================================================================


@dataclass
class DownloadedArtifact:
    """Result of downloading an artifact from an ArtifactRef."""

    data: bytes
    mime_type: str
    file_name: Optional[str] = None


# ============================================================================
# Decode / Download helpers
# ============================================================================


def decode_inline_artifact(ref: ArtifactRef) -> bytes:
    """Decode an inline artifact's base64 data to raw bytes.

    Raises ValueError if the ref has no ``data`` field.
    """
    if not ref.data:
        raise ValueError("Inline artifact ref is missing 'data' field")
    return base64.b64decode(ref.data)


def download_artifact(ref: ArtifactRef, pubnub: Any) -> DownloadedArtifact:
    """Download an artifact from an ArtifactRef.

    Inline artifacts are decoded from base64 (no PubNub call).
    File artifacts are downloaded via ``pubnub.download_file()``.

    Requires a PubNub instance with a valid token granting READ on
    the artifact's channel. For TaskSession consumers, the T4 read
    token already grants this.
    """
    if ref.kind == "inline":
        data = decode_inline_artifact(ref)
        return DownloadedArtifact(
            data=data,
            mime_type=ref.mime_type,
            file_name=ref.file_name,
        )

    if ref.kind == "file":
        if not ref.channel or not ref.file_id or not ref.file_name:
            raise ValueError(
                "File artifact ref is missing required fields "
                "(channel, file_id, file_name)"
            )
        result = (
            pubnub.download_file()
            .channel(ref.channel)
            .file_id(ref.file_id)
            .file_name(ref.file_name)
            .sync()
        )
        data = result.result.data
        if not isinstance(data, bytes):
            data = bytes(data)
        return DownloadedArtifact(
            data=data,
            mime_type=ref.mime_type,
            file_name=ref.file_name,
        )

    raise ValueError(f"Unknown artifact kind: {ref.kind!r}")


def should_inline_artifact(
    size_bytes: int,
    inline_limit: Optional[int] = None,
) -> bool:
    """Return ``True`` if an artifact of *size_bytes* should be inlined.

    The default limit comes from
    ``ARTIFACT_INLINE_LIMIT_BYTES`` in :mod:`blocks_network.config`.
    """
    if inline_limit is None:
        inline_limit = _cfg.ARTIFACT_INLINE_LIMIT_BYTES
    return size_bytes <= inline_limit


def build_artifact_ref(
    *,
    data: Optional[Union[bytes, str]] = None,
    mime_type: str = "application/octet-stream",
    size: Optional[int] = None,
    hash: Optional[str] = None,
    file: Optional[Dict[str, str]] = None,
    file_name: Optional[str] = None,
    inline_limit: Optional[int] = None,
) -> Dict[str, Any]:
    """Build an ``ArtifactRef`` dict (camelCase, ready for PubNub publish).

    Parameters
    ----------
    data:
        Raw artifact bytes (or UTF-8 string). Will be base64-encoded for
        inline artifacts.
    mime_type:
        MIME type of the artifact.
    size:
        Explicit size in bytes. Computed from *data* if omitted.
    hash:
        Integrity hash string (e.g. ``"sha256:abc..."``).
    file:
        Dict with keys ``id``, ``name``, ``channel``, and optionally
        ``expires_at`` for file-based artifacts.
    file_name:
        Original filename. Applies to both inline and file variants.
        For file variants, ``file["name"]`` takes precedence if present.
    inline_limit:
        Override for the inline size threshold.

    Returns
    -------
    dict
        A camelCase dict matching the ``ArtifactRef`` JSON schema.
    """
    # Normalize data to bytes
    raw: Optional[bytes] = None
    if isinstance(data, str):
        raw = data.encode("utf-8")
    elif isinstance(data, (bytes, bytearray)):
        raw = bytes(data)

    effective_size = size if size is not None else (len(raw) if raw is not None else 0)
    limit = inline_limit if inline_limit is not None else _cfg.ARTIFACT_INLINE_LIMIT_BYTES

    inline = should_inline_artifact(effective_size, limit) or file is None

    if inline:
        ref = ArtifactRef(
            kind="inline",
            mime_type=mime_type,
            size=effective_size,
            hash=hash,
            data=base64.b64encode(raw).decode("ascii") if raw is not None else None,
            file_name=file_name,
        )
    else:
        ref = ArtifactRef(
            kind="file",
            mime_type=mime_type,
            size=effective_size,
            hash=hash,
            channel=file.get("channel"),
            file_id=file.get("id"),
            file_name=file.get("name") or file_name,
            expires_at=file.get("expires_at") or file.get("expiresAt"),
        )

    return ref.to_dict()
