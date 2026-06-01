"""
Shared pre-signed URL upload helper.

Implements the three-step upload handshake:
1. POST /api/v1/files/request-upload -> get uploadSessionId, uploadId, uploadUrl, formFields
2. POST file data to uploadUrl (direct S3 upload via multipart/form-data)
3. POST /api/v1/files/confirm-upload -> confirmation

Used by both TaskClient (consumer input) and AgentInstance (provider output).
"""

from __future__ import annotations

import io
import json
import ssl
import urllib.error
import urllib.request
from typing import Any, Dict, List, Optional, Tuple

import certifi

from .protocol_version import CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER
from .write_affinity import capture_affinity, inject_affinity


class FileUploadError(Exception):
    """Raised when a file upload step fails."""

    def __init__(self, message: str, status_code: Optional[int] = None) -> None:
        super().__init__(message)
        self.status_code = status_code


def _build_multipart_body(
    form_fields: List[Dict[str, str]],
    file_data: bytes,
    file_name: str,
    mime_type: str,
) -> Tuple[bytes, str]:
    """Build a multipart/form-data body with form fields followed by the file.

    Returns ``(body_bytes, content_type_header)`` where the content type
    includes the boundary string.
    """
    boundary = "----BlocksUploadBoundary"
    parts: list[bytes] = []

    for field in form_fields:
        key = field.get("key", field.get("name", ""))
        value = field.get("value", "")
        parts.append(
            f"--{boundary}\r\n"
            f'Content-Disposition: form-data; name="{key}"\r\n'
            f"\r\n"
            f"{value}\r\n".encode("utf-8")
        )

    # File part must be last (S3 pre-signed POST requirement)
    parts.append(
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="file"; filename="{file_name}"\r\n'
        f"Content-Type: {mime_type}\r\n"
        f"\r\n".encode("utf-8")
    )
    parts.append(file_data)
    parts.append(f"\r\n--{boundary}--\r\n".encode("utf-8"))

    body = b"".join(parts)
    content_type = f"multipart/form-data; boundary={boundary}"
    return body, content_type


def _authenticated_json_post(
    url: str,
    body: Dict[str, Any],
    agent_auth: Any = None,
    auth_provider: Any = None,
) -> Dict[str, Any]:
    """POST JSON to a backend endpoint with authentication.

    Returns the parsed JSON response body.
    """
    # Pre-flight: when the auth provider has a recorded permanent-refresh
    # error, attempt one reactive recovery. On failure the typed
    # AuthRefreshFailedError is raised so file uploads surface it instead
    # of an opaque 401 from the request-upload / confirm-upload endpoints.
    from .auth_provider import preflight_auth_or_raise
    preflight_auth_or_raise(auth_provider)

    payload = json.dumps(body).encode("utf-8")
    headers: Dict[str, str] = {
        "Content-Type": "application/json",
        PROTOCOL_VERSION_HEADER: CURRENT_PROTOCOL_VERSION,
    }

    if agent_auth is not None:
        resp_data, status = agent_auth.authenticated_request(
            url,
            method="POST",
            body=payload,
            headers=headers,
        )
        if isinstance(resp_data, dict):
            if resp_data.get("error"):
                raise FileUploadError(
                    resp_data.get("error", "Upload request failed"),
                    status_code=status,
                )
            return resp_data
        return {}

    def _build_auth_headers() -> Dict[str, str]:
        hdrs = dict(headers)
        if auth_provider is not None:
            auth_header = auth_provider.get_auth_header()
            if auth_header:
                hdrs["Authorization"] = auth_header
        inject_affinity(hdrs)
        return hdrs

    def _execute(hdrs: Dict[str, str]) -> Dict[str, Any]:
        ssl_ctx = ssl.create_default_context(cafile=certifi.where())
        req = urllib.request.Request(url, data=payload, headers=hdrs, method="POST")

        try:
            with urllib.request.urlopen(req, context=ssl_ctx, timeout=30) as resp:
                capture_affinity(resp)
                resp_body = resp.read().decode("utf-8")
                return json.loads(resp_body) if resp_body else {}
        except urllib.error.HTTPError as exc:
            error_body = ""
            try:
                error_body = exc.read().decode("utf-8")
            except Exception:
                pass
            raise FileUploadError(
                f"HTTP {exc.code}: {error_body or exc.reason}",
                status_code=exc.code,
            ) from exc
        except urllib.error.URLError as exc:
            raise FileUploadError(str(exc.reason)) from exc

    req_headers = _build_auth_headers()
    try:
        return _execute(req_headers)
    except FileUploadError as err:
        # 401 reactive refresh: retry once if auth_provider can refresh
        if err.status_code == 401 and auth_provider is not None:
            if auth_provider.on_auth_failure():
                return _execute(_build_auth_headers())
        raise


def request_upload(
    base_url: str,
    *,
    role: str,
    file_name: str,
    file_size: int,
    mime_type: str,
    agent_name: Optional[str] = None,
    task_id: Optional[str] = None,
    part_id: Optional[str] = None,
    output_id: Optional[str] = None,
    upload_session_id: Optional[str] = None,
    agent_auth: Any = None,
    auth_provider: Any = None,
) -> Dict[str, Any]:
    """Step 1: Request a pre-signed upload URL from the backend.

    Parameters
    ----------
    base_url:
        Backend base URL.
    role:
        ``"consumer-input"`` or ``"provider-output"``.
    file_name, file_size, mime_type:
        File metadata.
    agent_name:
        Required for consumer-input (first file in session).
    task_id:
        Required for provider-output.
    part_id:
        Maps to ``requestPart.partId`` (consumer input).
    output_id:
        Maps to ``io.outputs[].id`` (provider output).
    upload_session_id:
        Pass to join an existing upload session (consumer input, additional files).
    agent_auth:
        AgentAuth instance for automatic token refresh.

    Returns
    -------
    dict
        Response with ``uploadSessionId``, ``uploadId``, ``uploadUrl``, ``formFields``.
        Provider output omits ``uploadSessionId``.
    """
    url = f"{base_url.rstrip('/')}/api/v1/files/request-upload"
    body: Dict[str, Any] = {
        "role": role,
        "fileName": file_name,
        "fileSize": file_size,
        "mimeType": mime_type,
    }
    if role == "consumer-input":
        if upload_session_id:
            body["uploadSessionId"] = upload_session_id
        elif agent_name:
            body["agentName"] = agent_name
        if part_id:
            body["partId"] = part_id
    elif role == "provider-output":
        if task_id:
            body["taskId"] = task_id
        if output_id:
            body["outputId"] = output_id

    return _authenticated_json_post(url, body, agent_auth=agent_auth, auth_provider=auth_provider)


def upload_to_presigned_url(
    upload_url: str,
    form_fields: List[Dict[str, str]],
    file_data: bytes,
    file_name: str,
    mime_type: str = "application/octet-stream",
) -> None:
    """Step 2: Upload file data directly to the pre-signed S3 URL.

    The upload is a standard S3 multipart POST with form fields first,
    then the file as the last part.

    Raises :class:`FileUploadError` on failure.
    """
    body, content_type = _build_multipart_body(form_fields, file_data, file_name, mime_type)

    ssl_ctx = ssl.create_default_context(cafile=certifi.where())
    req = urllib.request.Request(
        upload_url,
        data=body,
        headers={"Content-Type": content_type},
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, context=ssl_ctx, timeout=120) as resp:
            # S3 returns 204 on success
            if resp.status not in (200, 204):
                raise FileUploadError(
                    f"Upload returned unexpected status {resp.status}",
                    status_code=resp.status,
                )
    except urllib.error.HTTPError as exc:
        error_body = ""
        try:
            error_body = exc.read().decode("utf-8")
        except Exception:
            pass
        raise FileUploadError(
            f"Upload failed: HTTP {exc.code}: {error_body or exc.reason}",
            status_code=exc.code,
        ) from exc
    except urllib.error.URLError as exc:
        raise FileUploadError(f"Upload failed: {exc.reason}") from exc


def confirm_upload(
    base_url: str,
    upload_id: str,
    *,
    agent_auth: Any = None,
    auth_provider: Any = None,
) -> Dict[str, Any]:
    """Step 3: Confirm the upload with the backend.

    For consumer input, returns ``{ uploadId }``.
    For provider output, returns ``{ uploadId, artifactRef }`` and the
    backend publishes the typed artifact event.
    """
    url = f"{base_url.rstrip('/')}/api/v1/files/confirm-upload"
    body = {"uploadId": upload_id}
    return _authenticated_json_post(url, body, agent_auth=agent_auth, auth_provider=auth_provider)


def presigned_upload_flow(
    base_url: str,
    file_data: bytes,
    *,
    role: str,
    file_name: str,
    mime_type: str = "application/octet-stream",
    agent_name: Optional[str] = None,
    task_id: Optional[str] = None,
    part_id: Optional[str] = None,
    output_id: Optional[str] = None,
    upload_session_id: Optional[str] = None,
    agent_auth: Any = None,
    auth_provider: Any = None,
) -> Dict[str, Any]:
    """Execute the full three-step pre-signed URL upload flow.

    Returns the combined result containing at least ``uploadId``.
    For consumer input, also contains ``uploadSessionId``.
    For provider output, also contains ``artifactRef``.
    """
    # Step 1: Request upload URL
    req_result = request_upload(
        base_url,
        role=role,
        file_name=file_name,
        file_size=len(file_data),
        mime_type=mime_type,
        agent_name=agent_name,
        task_id=task_id,
        part_id=part_id,
        output_id=output_id,
        upload_session_id=upload_session_id,
        agent_auth=agent_auth,
        auth_provider=auth_provider,
    )

    upload_url = req_result.get("uploadUrl", "")
    form_fields = req_result.get("formFields", [])
    upload_id = req_result.get("uploadId", "")

    if not upload_url or not upload_id:
        raise FileUploadError("request-upload did not return uploadUrl or uploadId")

    # Step 2: Direct upload to S3
    upload_to_presigned_url(upload_url, form_fields, file_data, file_name, mime_type)

    # Step 3: Confirm
    confirm_result = confirm_upload(
        base_url, upload_id,
        agent_auth=agent_auth,
        auth_provider=auth_provider,
    )

    # Merge request-upload and confirm-upload results
    merged: Dict[str, Any] = {}
    merged["uploadId"] = upload_id
    if "uploadSessionId" in req_result:
        merged["uploadSessionId"] = req_result["uploadSessionId"]
    if "artifactRef" in confirm_result:
        merged["artifactRef"] = confirm_result["artifactRef"]

    return merged
