"""
Tests for blocks_network.stream_setup_helper -- T7a abort-payload parsing.

Covers:
- extract_from_payload with valid payloads (all phases, directions)
- extract_from_payload rejection of invalid/missing fields
- parse_stream_setup_response with simulated PubNubException
- parse_stream_setup_response rejection of non-403, missing fields
"""

from __future__ import annotations

import json
from typing import Any, Dict, Optional

import pytest

from blocks_network.stream_setup_helper import (
    StreamSetupError,
    StreamSetupResult,
    extract_error_from_payload,
    extract_from_payload,
    parse_stream_setup_error,
    parse_stream_setup_response,
)


# ---------------------------------------------------------------------------
# Test data helpers
# ---------------------------------------------------------------------------


def valid_payload(
    overrides: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    response: Dict[str, Any] = {
        "taskId": "task-123",
        "streamId": "temperature",
        "channel": "stream.weather.temperature",
        "direction": "outbound",
        "phase": "embedded",
        "token": "pam-token-t7a",
        "tokenTtlMinutes": 62,
    }
    if overrides:
        response.update(overrides)
    return {
        "ok": True,
        "streamSetupResponse": response,
    }


class FakePubNubException(Exception):
    """Simulates PubNub Python SDK exception structure."""

    def __init__(
        self,
        status_code: int = 403,
        errormsg: Optional[str] = None,
    ) -> None:
        super().__init__(errormsg or "error")
        self._status_code = status_code
        self._errormsg = errormsg

    def get_status_code(self) -> int:
        return self._status_code


# ---------------------------------------------------------------------------
# extract_from_payload tests
# ---------------------------------------------------------------------------


class TestExtractFromPayload:
    def test_valid_embedded_payload(self) -> None:
        result = extract_from_payload(valid_payload())
        assert result is not None
        assert result.task_id == "task-123"
        assert result.stream_id == "temperature"
        assert result.channel == "stream.weather.temperature"
        assert result.direction == "outbound"
        assert result.phase == "embedded"
        assert result.token == "pam-token-t7a"
        assert result.token_ttl_minutes == 62

    def test_valid_token_request_phase(self) -> None:
        result = extract_from_payload(valid_payload({"phase": "token_request"}))
        assert result is not None
        assert result.phase == "token_request"
        assert result.token == "pam-token-t7a"

    def test_valid_activate_phase_without_token(self) -> None:
        payload = valid_payload({"phase": "activate"})
        del payload["streamSetupResponse"]["token"]
        result = extract_from_payload(payload)
        assert result is not None
        assert result.phase == "activate"
        assert result.token is None

    def test_bidirectional_direction(self) -> None:
        result = extract_from_payload(valid_payload({"direction": "bidirectional"}))
        assert result is not None
        assert result.direction == "bidirectional"

    def test_inbound_direction(self) -> None:
        result = extract_from_payload(valid_payload({"direction": "inbound"}))
        assert result is not None
        assert result.direction == "inbound"

    def test_returns_none_when_ok_false(self) -> None:
        payload = valid_payload()
        payload["ok"] = False
        assert extract_from_payload(payload) is None

    def test_returns_none_when_ok_missing(self) -> None:
        payload = valid_payload()
        del payload["ok"]
        assert extract_from_payload(payload) is None

    def test_returns_none_when_response_missing(self) -> None:
        assert extract_from_payload({"ok": True}) is None

    def test_returns_none_when_response_not_dict(self) -> None:
        assert extract_from_payload({"ok": True, "streamSetupResponse": "string"}) is None

    def test_returns_none_for_none_input(self) -> None:
        assert extract_from_payload(None) is None

    def test_returns_none_for_string_input(self) -> None:
        assert extract_from_payload("string") is None

    def test_returns_none_for_int_input(self) -> None:
        assert extract_from_payload(42) is None

    def test_returns_none_for_missing_task_id(self) -> None:
        payload = valid_payload()
        del payload["streamSetupResponse"]["taskId"]
        assert extract_from_payload(payload) is None

    def test_returns_none_for_missing_stream_id(self) -> None:
        payload = valid_payload()
        del payload["streamSetupResponse"]["streamId"]
        assert extract_from_payload(payload) is None

    def test_returns_none_for_missing_channel(self) -> None:
        payload = valid_payload()
        del payload["streamSetupResponse"]["channel"]
        assert extract_from_payload(payload) is None

    def test_returns_none_for_missing_direction(self) -> None:
        payload = valid_payload()
        del payload["streamSetupResponse"]["direction"]
        assert extract_from_payload(payload) is None

    def test_returns_none_for_missing_phase(self) -> None:
        payload = valid_payload()
        del payload["streamSetupResponse"]["phase"]
        assert extract_from_payload(payload) is None

    def test_returns_none_for_missing_token_ttl(self) -> None:
        payload = valid_payload()
        del payload["streamSetupResponse"]["tokenTtlMinutes"]
        assert extract_from_payload(payload) is None

    def test_returns_none_for_invalid_direction(self) -> None:
        assert extract_from_payload(valid_payload({"direction": "upstream"})) is None

    def test_returns_none_for_invalid_phase(self) -> None:
        assert extract_from_payload(valid_payload({"phase": "unknown"})) is None

    def test_returns_none_for_zero_ttl(self) -> None:
        assert extract_from_payload(valid_payload({"tokenTtlMinutes": 0})) is None

    def test_returns_none_for_negative_ttl(self) -> None:
        assert extract_from_payload(valid_payload({"tokenTtlMinutes": -5})) is None

    def test_returns_none_for_empty_strings(self) -> None:
        assert extract_from_payload(valid_payload({"taskId": ""})) is None
        assert extract_from_payload(valid_payload({"streamId": ""})) is None
        assert extract_from_payload(valid_payload({"channel": ""})) is None

    def test_omits_token_when_empty(self) -> None:
        result = extract_from_payload(valid_payload({"token": ""}))
        assert result is not None
        assert result.token is None

    def test_token_ttl_coerced_to_int(self) -> None:
        result = extract_from_payload(valid_payload({"tokenTtlMinutes": 62.5}))
        assert result is not None
        assert result.token_ttl_minutes == 62
        assert isinstance(result.token_ttl_minutes, int)


# ---------------------------------------------------------------------------
# parse_stream_setup_response tests (PubNub exception structure)
# ---------------------------------------------------------------------------


class TestParseStreamSetupResponse:
    def test_extracts_from_valid_403_exception(self) -> None:
        # PubNub wraps abort payload as {"message": <payload>, "status": 403}
        raw_body = json.dumps({"message": valid_payload(), "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        result = parse_stream_setup_response(exc)
        assert result is not None
        assert result.task_id == "task-123"
        assert result.stream_id == "temperature"
        assert result.token == "pam-token-t7a"

    def test_returns_none_for_non_403(self) -> None:
        raw_body = json.dumps({"message": valid_payload(), "status": 403})
        exc = FakePubNubException(status_code=401, errormsg=raw_body)
        assert parse_stream_setup_response(exc) is None

    def test_returns_none_when_errormsg_is_none(self) -> None:
        exc = FakePubNubException(status_code=403, errormsg=None)
        assert parse_stream_setup_response(exc) is None

    def test_returns_none_when_errormsg_not_json(self) -> None:
        exc = FakePubNubException(status_code=403, errormsg="not json")
        assert parse_stream_setup_response(exc) is None

    def test_returns_none_for_real_403_error(self) -> None:
        raw_body = json.dumps({"error": "Forbidden", "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        assert parse_stream_setup_response(exc) is None

    def test_returns_none_for_403_with_ok_false(self) -> None:
        payload = valid_payload()
        payload["ok"] = False
        raw_body = json.dumps({"message": payload, "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        assert parse_stream_setup_response(exc) is None

    def test_extracts_token_request_phase(self) -> None:
        payload = valid_payload({"phase": "token_request"})
        raw_body = json.dumps({"message": payload, "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        result = parse_stream_setup_response(exc)
        assert result is not None
        assert result.phase == "token_request"

    def test_extracts_activate_phase_without_token(self) -> None:
        payload = valid_payload({"phase": "activate"})
        del payload["streamSetupResponse"]["token"]
        raw_body = json.dumps({"message": payload, "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        result = parse_stream_setup_response(exc)
        assert result is not None
        assert result.phase == "activate"
        assert result.token is None

    def test_handles_all_directions(self) -> None:
        for direction in ("outbound", "inbound", "bidirectional"):
            payload = valid_payload({"direction": direction})
            raw_body = json.dumps({"message": payload, "status": 403})
            exc = FakePubNubException(status_code=403, errormsg=raw_body)
            result = parse_stream_setup_response(exc)
            assert result is not None, f"Failed for direction={direction}"
            assert result.direction == direction

    def test_returns_none_for_non_exception(self) -> None:
        # Should handle arbitrary objects gracefully
        assert parse_stream_setup_response(ValueError("test")) is None  # type: ignore[arg-type]

    def test_handles_payload_without_message_wrapper(self) -> None:
        # When the raw body is the payload itself (not wrapped)
        raw_body = json.dumps(valid_payload())
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        result = parse_stream_setup_response(exc)
        assert result is not None
        assert result.task_id == "task-123"


# ---------------------------------------------------------------------------
# extract_error_from_payload tests
# ---------------------------------------------------------------------------


class TestExtractErrorFromPayload:
    def test_extracts_valid_error_payload(self) -> None:
        payload = {
            "ok": False,
            "error": {
                "code": "InvalidArgument",
                "message": "durationMinutes is required and must be a positive number",
            },
        }
        result = extract_error_from_payload(payload)
        assert result is not None
        assert result.code == "InvalidArgument"
        assert result.message == "durationMinutes is required and must be a positive number"

    def test_extracts_token_grant_failed_error(self) -> None:
        payload = {
            "ok": False,
            "error": {
                "code": "TokenGrantFailed",
                "message": "Failed to grant tokens for channel stream.weather.temp",
            },
        }
        result = extract_error_from_payload(payload)
        assert result is not None
        assert result.code == "TokenGrantFailed"
        assert result.message == "Failed to grant tokens for channel stream.weather.temp"

    def test_returns_none_for_success_payload(self) -> None:
        assert extract_error_from_payload(valid_payload()) is None

    def test_returns_none_when_ok_missing(self) -> None:
        assert extract_error_from_payload({"error": {"code": "X", "message": "Y"}}) is None

    def test_returns_none_when_error_missing(self) -> None:
        assert extract_error_from_payload({"ok": False}) is None

    def test_returns_none_when_error_not_dict(self) -> None:
        assert extract_error_from_payload({"ok": False, "error": "string"}) is None

    def test_returns_none_when_code_missing(self) -> None:
        assert extract_error_from_payload({"ok": False, "error": {"message": "msg"}}) is None

    def test_returns_none_when_message_missing(self) -> None:
        assert extract_error_from_payload({"ok": False, "error": {"code": "X"}}) is None

    def test_returns_none_when_code_empty(self) -> None:
        assert extract_error_from_payload({"ok": False, "error": {"code": "", "message": "msg"}}) is None

    def test_returns_none_when_message_empty(self) -> None:
        assert extract_error_from_payload({"ok": False, "error": {"code": "X", "message": ""}}) is None

    def test_returns_none_for_none_input(self) -> None:
        assert extract_error_from_payload(None) is None

    def test_returns_none_for_string_input(self) -> None:
        assert extract_error_from_payload("string") is None

    def test_returns_none_for_int_input(self) -> None:
        assert extract_error_from_payload(42) is None


# ---------------------------------------------------------------------------
# parse_stream_setup_error tests (PubNub exception structure)
# ---------------------------------------------------------------------------


class TestParseStreamSetupError:
    def test_extracts_error_from_403_exception(self) -> None:
        error_payload = {
            "ok": False,
            "error": {
                "code": "InvalidArgument",
                "message": "durationMinutes is required and must be a positive number",
            },
        }
        raw_body = json.dumps({"message": error_payload, "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        result = parse_stream_setup_error(exc)
        assert result is not None
        assert result.code == "InvalidArgument"
        assert result.message == "durationMinutes is required and must be a positive number"

    def test_extracts_token_grant_failed_from_403(self) -> None:
        error_payload = {
            "ok": False,
            "error": {
                "code": "TokenGrantFailed",
                "message": "Failed to grant agent token for channel stream.weather.temp",
            },
        }
        raw_body = json.dumps({"message": error_payload, "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        result = parse_stream_setup_error(exc)
        assert result is not None
        assert result.code == "TokenGrantFailed"

    def test_returns_none_for_success_payload(self) -> None:
        raw_body = json.dumps({"message": valid_payload(), "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        assert parse_stream_setup_error(exc) is None

    def test_returns_none_for_real_403(self) -> None:
        raw_body = json.dumps({"error": "Forbidden", "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        assert parse_stream_setup_error(exc) is None

    def test_returns_none_for_non_403(self) -> None:
        error_payload = {
            "ok": False,
            "error": {"code": "InvalidArgument", "message": "test"},
        }
        raw_body = json.dumps({"message": error_payload, "status": 403})
        exc = FakePubNubException(status_code=401, errormsg=raw_body)
        assert parse_stream_setup_error(exc) is None

    def test_returns_none_when_errormsg_is_none(self) -> None:
        exc = FakePubNubException(status_code=403, errormsg=None)
        assert parse_stream_setup_error(exc) is None

    def test_returns_none_for_non_exception(self) -> None:
        assert parse_stream_setup_error(ValueError("test")) is None  # type: ignore[arg-type]

    def test_error_payload_not_parsed_as_success(self) -> None:
        """Verify that an ok:false error payload is NOT treated as success."""
        error_payload = {
            "ok": False,
            "error": {
                "code": "InvalidArgument",
                "message": "Missing required fields",
            },
        }
        raw_body = json.dumps({"message": error_payload, "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        # parse_stream_setup_response should return None for error payloads
        assert parse_stream_setup_response(exc) is None
        # parse_stream_setup_error should extract the error
        result = parse_stream_setup_error(exc)
        assert result is not None
        assert result.code == "InvalidArgument"
        assert result.message == "Missing required fields"

    def test_success_payload_not_parsed_as_error(self) -> None:
        """Verify that an ok:true success payload is NOT treated as error."""
        raw_body = json.dumps({"message": valid_payload(), "status": 403})
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        # parse_stream_setup_error should return None for success payloads
        assert parse_stream_setup_error(exc) is None
        # parse_stream_setup_response should extract the result
        result = parse_stream_setup_response(exc)
        assert result is not None
        assert result.task_id == "task-123"

    def test_handles_unwrapped_error_payload(self) -> None:
        """When the raw body is the error payload itself (not wrapped)."""
        error_payload = {
            "ok": False,
            "error": {
                "code": "InvalidArgument",
                "message": "Invalid direction",
            },
        }
        raw_body = json.dumps(error_payload)
        exc = FakePubNubException(status_code=403, errormsg=raw_body)
        result = parse_stream_setup_error(exc)
        assert result is not None
        assert result.code == "InvalidArgument"
        assert result.message == "Invalid direction"
