"""
Tests for device OS detection and SDK language in the registration payload.

"""

from __future__ import annotations

import json
from unittest.mock import MagicMock, patch

from blocks_network.agent_registry import (
    ConnectAgentOptions,
    _detect_device_os,
    _SDK_LANGUAGE,
    connect_agent,
)


# ---------------------------------------------------------------------------
# _detect_device_os
# ---------------------------------------------------------------------------


class TestDetectDeviceOs:
    def test_returns_non_empty_string(self) -> None:
        result = _detect_device_os()
        assert isinstance(result, str)
        assert len(result) > 0

    def test_returns_lowercase_string(self) -> None:
        result = _detect_device_os()
        assert result == result.lower()

    def test_returns_unknown_when_platform_raises(self) -> None:
        with patch("blocks_network.agent_registry.platform") as mock_platform:
            mock_platform.system.side_effect = RuntimeError("broken")
            result = _detect_device_os()
        assert result == "unknown"


# ---------------------------------------------------------------------------
# _SDK_LANGUAGE
# ---------------------------------------------------------------------------


class TestSdkLanguage:
    def test_sdk_language_is_python(self) -> None:
        assert _SDK_LANGUAGE == "Python"


# ---------------------------------------------------------------------------
# Registration payload includes deviceOs and sdkLanguage
# ---------------------------------------------------------------------------


class TestRegistrationPayloadEnvFields:
    def _mock_auth(self) -> MagicMock:
        auth = MagicMock()
        auth.init.return_value = {"pamToken": "pam-test"}
        return auth

    def test_payload_includes_device_os_and_sdk_language(self) -> None:
        auth = self._mock_auth()
        connect_agent(
            "test_agent",
            ConnectAgentOptions(
                instance_id="AG-test_agent-abc123",
                base_url="http://localhost:8080",
                agent_auth=auth,
            ),
        )

        payload = auth.init.call_args[1]["registration_payload"]
        assert "deviceOs" in payload
        assert isinstance(payload["deviceOs"], str)
        assert len(payload["deviceOs"]) > 0
        assert payload["sdkLanguage"] == "Python"

    def test_device_os_matches_detect_helper(self) -> None:
        auth = self._mock_auth()
        connect_agent(
            "test_agent",
            ConnectAgentOptions(
                instance_id="AG-test_agent-xyz789",
                base_url="http://localhost:8080",
                agent_auth=auth,
            ),
        )

        payload = auth.init.call_args[1]["registration_payload"]
        assert payload["deviceOs"] == _detect_device_os()

    def test_env_fields_survive_none_stripping(self) -> None:
        """deviceOs and sdkLanguage always have values, so they must survive
        the None-stripping filter applied to the payload."""
        auth = self._mock_auth()
        connect_agent(
            "minimal_agent",
            ConnectAgentOptions(base_url="http://localhost:8080", agent_auth=auth),
        )

        payload = auth.init.call_args[1]["registration_payload"]
        assert "deviceOs" in payload
        assert payload["deviceOs"] is not None
        assert "sdkLanguage" in payload
        assert payload["sdkLanguage"] == "Python"
