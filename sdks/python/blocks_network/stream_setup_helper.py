"""
Stream Setup Helper -- T7a abort-payload parsing.

Internal helper for consuming the streamSetup Function's response.
The streamSetup Function returns T7a via request.abort(customPayload),
which the PubNub Python SDK surfaces as a PubNubException with a 403
status code. This helper extracts the T7a token from the exception's
error message after verifying the expected markers.

This is a protocol-consumption helper only. It does not implement the
full stream setup handshake (that belongs to Phase 3 SDK runtime).

The stream setup protocol is documented in the SDK contract and event flow docs.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any, Dict, Optional

VALID_DIRECTIONS = frozenset(("outbound", "inbound", "bidirectional"))
VALID_PHASES = frozenset(("embedded", "token_request", "activate"))


@dataclass
class StreamSetupResult:
    """Parsed stream setup response extracted from the abort payload."""

    task_id: str
    stream_id: str
    channel: str
    direction: str  # "outbound" | "inbound" | "bidirectional"
    phase: str  # "embedded" | "token_request" | "activate"
    token_ttl_minutes: int
    token: Optional[str] = None


@dataclass
class StreamSetupError:
    """Structured error returned by the streamSetup Function for validation
    failures. The Function returns ``{ ok: false, error: { code, message } }``
    via ``request.abort()``, which is also surfaced as a 403 error. This type
    lets callers distinguish a server-side validation rejection from an
    opaque PubNub 403."""

    code: str
    message: str


def extract_error_from_payload(payload: Any) -> Optional[StreamSetupError]:
    """Extract a :class:`StreamSetupError` from a raw abort payload dict.

    The streamSetup Function returns ``{ ok: false, error: { code, message } }``
    for validation failures (missing fields, invalid direction, invalid
    durationMinutes, etc.). This function checks for that shape and returns
    the error details, or ``None`` if the payload is not a structured error.

    Args:
        payload: The parsed abort payload (dict).

    Returns:
        The parsed :class:`StreamSetupError`, or ``None`` if not an error payload.
    """
    if not isinstance(payload, dict):
        return None

    # Error payloads have ok == False and an error object
    if payload.get("ok") is not False:
        return None

    error = payload.get("error")
    if not isinstance(error, dict):
        return None

    code = error.get("code")
    message = error.get("message")

    if not isinstance(code, str) or not code:
        return None
    if not isinstance(message, str) or not message:
        return None

    return StreamSetupError(code=code, message=message)


def parse_stream_setup_error(exc: BaseException) -> Optional[StreamSetupError]:
    """Attempt to extract a structured error from a PubNub exception.

    When the streamSetup Function rejects a request (e.g., missing
    durationMinutes, invalid direction), it returns
    ``{ ok: false, error: { code, message } }`` via ``request.abort()``.
    This is surfaced as a 403 by the PubNub SDK, just like the success path.
    This function extracts the structured error from the 403 body.

    Returns ``None`` if the exception is not a structured setup error (i.e.,
    it is either a real 403 or a success abort payload).

    Args:
        exc: The exception raised by ``pubnub.publish().sync()``

    Returns:
        The parsed :class:`StreamSetupError`, or ``None``.
    """
    status_code: Optional[int] = None
    if hasattr(exc, "get_status_code"):
        try:
            status_code = exc.get_status_code()  # type: ignore[union-attr]
        except Exception:
            pass
    if hasattr(exc, "_status_code") and status_code is None:
        status_code = getattr(exc, "_status_code", None)

    if status_code != 403:
        return None

    raw_msg: Optional[str] = None
    if hasattr(exc, "_errormsg"):
        raw_msg = getattr(exc, "_errormsg", None)
    elif hasattr(exc, "get_error_message"):
        try:
            raw_msg = exc.get_error_message()  # type: ignore[union-attr]
        except Exception:
            pass

    if not isinstance(raw_msg, str):
        return None

    try:
        body = json.loads(raw_msg)
    except (json.JSONDecodeError, TypeError):
        return None

    if not isinstance(body, dict):
        return None

    # PubNub wraps the abort payload as {"message": <payload>, "status": 403}
    payload = body.get("message", body)

    return extract_error_from_payload(payload)


def parse_stream_setup_response(exc: BaseException) -> Optional[StreamSetupResult]:
    """Attempt to parse a T7a stream setup response from a PubNub exception.

    The streamSetup Function calls ``request.abort(customPayload)``, which
    causes the PubNub Python SDK to raise a ``PubNubException`` with:

    - ``exc.get_status_code()`` == 403
    - ``exc._errormsg`` contains the raw JSON string of the abort payload

    The payload is wrapped by PubNub as::

        {"message": {<custom payload>}, "status": 403}

    This function checks the marker fields (``ok: true``,
    ``streamSetupResponse`` present) and extracts the result.
    Returns ``None`` if the exception is not a valid stream setup response
    (i.e., it is a real 403 error).

    Args:
        exc: The exception raised by ``pubnub.publish().sync()``

    Returns:
        The parsed :class:`StreamSetupResult`, or ``None`` if not valid.
    """
    # Check status code (PubNubException has _status_code or get_status_code)
    status_code: Optional[int] = None
    if hasattr(exc, "get_status_code"):
        try:
            status_code = exc.get_status_code()  # type: ignore[union-attr]
        except Exception:
            pass
    if hasattr(exc, "_status_code") and status_code is None:
        status_code = getattr(exc, "_status_code", None)

    if status_code != 403:
        return None

    # Extract raw error message string
    raw_msg: Optional[str] = None
    if hasattr(exc, "_errormsg"):
        raw_msg = getattr(exc, "_errormsg", None)
    elif hasattr(exc, "get_error_message"):
        try:
            raw_msg = exc.get_error_message()  # type: ignore[union-attr]
        except Exception:
            pass

    if not isinstance(raw_msg, str):
        return None

    # Parse the JSON string
    try:
        body = json.loads(raw_msg)
    except (json.JSONDecodeError, TypeError):
        return None

    if not isinstance(body, dict):
        return None

    # PubNub wraps the abort payload as {"message": <payload>, "status": 403}
    payload = body.get("message", body)

    return extract_from_payload(payload)


def extract_from_payload(payload: Any) -> Optional[StreamSetupResult]:
    """Extract a :class:`StreamSetupResult` from a raw abort payload dict.

    Validates the marker fields (``ok: true``, ``streamSetupResponse``)
    and all required properties.

    Args:
        payload: The parsed abort payload (dict).

    Returns:
        The parsed :class:`StreamSetupResult`, or ``None`` if invalid.
    """
    if not isinstance(payload, dict):
        return None

    # Check marker fields
    if payload.get("ok") is not True:
        return None

    response = payload.get("streamSetupResponse")
    if not isinstance(response, dict):
        return None

    # Validate required fields
    task_id = response.get("taskId")
    stream_id = response.get("streamId")
    channel = response.get("channel")
    direction = response.get("direction")
    phase = response.get("phase")
    token_ttl_minutes = response.get("tokenTtlMinutes")

    if not isinstance(task_id, str) or not task_id:
        return None
    if not isinstance(stream_id, str) or not stream_id:
        return None
    if not isinstance(channel, str) or not channel:
        return None
    if not isinstance(direction, str) or direction not in VALID_DIRECTIONS:
        return None
    if not isinstance(phase, str) or phase not in VALID_PHASES:
        return None
    if not isinstance(token_ttl_minutes, (int, float)) or token_ttl_minutes <= 0:
        return None

    result = StreamSetupResult(
        task_id=task_id,
        stream_id=stream_id,
        channel=channel,
        direction=direction,
        phase=phase,
        token_ttl_minutes=int(token_ttl_minutes),
    )

    # Token is present for embedded and token_request phases, absent for activate
    token = response.get("token")
    if isinstance(token, str) and token:
        result.token = token

    return result
