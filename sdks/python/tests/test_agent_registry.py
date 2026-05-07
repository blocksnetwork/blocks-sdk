"""
Tests for blocks_network.agent_registry -- agent registration and discovery.

All registry functions now use REST endpoints. Tests mock urllib.request.urlopen.
"""

from __future__ import annotations

import json
from io import BytesIO
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.agent_registry import (
    AgentCard,
    OutputAgentCard,
    AgentScaling,
    ConnectAgentOptions,
    fetch_agent_registry,
    fetch_agents_by_skill,
    fetch_agents_by_listing,
    get_agent,
    connect_agent,
    registry_all_channel,
    registry_skill_channel,
    registry_log_channel,
    registry_visibility_channel,
    remove_agent,
    with_retry,
)


# ---------------------------------------------------------------------------
# Channel helpers
# ---------------------------------------------------------------------------


class TestChannelHelpers:
    def test_registry_all(self) -> None:
        assert registry_all_channel() == "registry.all"

    def test_registry_skill(self) -> None:
        assert registry_skill_channel("image_generation") == "registry.skill.image_generation"

    def test_registry_visibility_public(self) -> None:
        assert registry_visibility_channel(True) == "registry.public"

    def test_registry_visibility_private(self) -> None:
        assert registry_visibility_channel(False) == "registry.private"

    def test_registry_log(self) -> None:
        assert registry_log_channel() == "registry.log"


# ---------------------------------------------------------------------------
# with_retry
# ---------------------------------------------------------------------------


class TestWithRetry:
    def test_succeeds_first_try(self) -> None:
        result = with_retry(lambda: 42)
        assert result == 42

    def test_retries_on_transient_error(self) -> None:
        call_count = 0

        def _flaky():
            nonlocal call_count
            call_count += 1
            if call_count < 2:
                raise RuntimeError("timed out")
            return "ok"

        result = with_retry(_flaky, max_retries=3, base_delay_ms=1)
        assert result == "ok"
        assert call_count == 2

    def test_raises_non_transient_immediately(self) -> None:
        def _bad():
            raise ValueError("bad input")

        with pytest.raises(ValueError, match="bad input"):
            with_retry(_bad, max_retries=3)

    def test_raises_after_max_retries(self) -> None:
        call_count = 0

        def _always_fail():
            nonlocal call_count
            call_count += 1
            raise RuntimeError("Name or service not known")

        with pytest.raises(RuntimeError, match="Name or service not known"):
            with_retry(_always_fail, max_retries=3, base_delay_ms=1)
        assert call_count == 3


# ---------------------------------------------------------------------------
# connect_agent (REST)
# ---------------------------------------------------------------------------


class TestRegisterAgent:
    def test_rejects_agent_name_with_dot(self) -> None:
        """agent_name containing a dot must raise ValueError."""
        with pytest.raises(ValueError, match="alphanumeric"):
            connect_agent("acme.echo")

    def test_raises_without_agent_auth(self) -> None:
        """Raises RuntimeError when agent_auth is not provided."""
        with pytest.raises(RuntimeError, match="agent_auth is required"):
            connect_agent(
                "acme_echo",
                ConnectAgentOptions(base_url="http://localhost:8080"),
            )

    def test_sends_correct_connect_payload(self) -> None:
        """Verifies payload passed to agentAuth.init() has correct fields."""
        mock_auth = MagicMock()
        mock_auth.init.return_value = {"pamToken": "pam-123", "accessToken": "jwt-1", "refreshToken": "rt-1"}

        connect_agent(
            "acme_echo",
            ConnectAgentOptions(
                instance_id="inst-1",
                base_url="http://localhost:8080",
                agent_auth=mock_auth,
            ),
        )

        mock_auth.init.assert_called_once()
        payload = mock_auth.init.call_args[1]["registration_payload"]
        assert payload["agentName"] == "acme_echo"
        assert payload["instanceId"] == "inst-1"

    def test_does_not_send_card_description_skills(self) -> None:
        """Connect payload must NOT include card, description, skills, cardRef, cardSummary."""
        mock_auth = MagicMock()
        mock_auth.init.return_value = {"pamToken": "pam-123", "accessToken": "jwt-1", "refreshToken": "rt-1"}

        connect_agent(
            "acme_echo",
            ConnectAgentOptions(
                base_url="http://localhost:8080",
                agent_auth=mock_auth,
                description="should not be sent",
                skills=["echo"],
                card={"identity": {"displayName": "Echo"}},
                card_ref="https://example.com/card.json",
                card_summary="An echo agent",
            ),
        )

        mock_auth.init.assert_called_once()
        payload = mock_auth.init.call_args[1]["registration_payload"]
        assert payload["agentName"] == "acme_echo"
        assert "card" not in payload
        assert "description" not in payload
        assert "skills" not in payload
        assert "cardRef" not in payload
        assert "cardSummary" not in payload

    def test_omits_listing_when_not_provided(self) -> None:
        """When caller does not set listing, the connect payload omits it
        (no forced 'playground' default). The backend applies its own
        default ('public') under the billing_mode invariant."""
        mock_auth = MagicMock()
        mock_auth.init.return_value = {"pamToken": "pam-123"}

        connect_agent(
            "acme_echo",
            ConnectAgentOptions(base_url="http://localhost:8080", agent_auth=mock_auth),
        )

        payload = mock_auth.init.call_args[1]["registration_payload"]
        assert "listing" not in payload

    def test_passes_listing_when_explicit(self) -> None:
        mock_auth = MagicMock()
        mock_auth.init.return_value = {"pamToken": "pam-123"}

        connect_agent(
            "acme_echo",
            ConnectAgentOptions(
                listing="public",
                base_url="http://localhost:8080",
                agent_auth=mock_auth,
            ),
        )

        payload = mock_auth.init.call_args[1]["registration_payload"]
        assert payload["listing"] == "public"

    def test_includes_scaling_params(self) -> None:
        mock_auth = MagicMock()
        mock_auth.init.return_value = {"pamToken": "pam-123"}

        scaling = AgentScaling(
            expected_instances=3,
            concurrency=5,
            max_pending_backlog=20,
            max_running_time_sec=300,
        )

        connect_agent(
            "acme_echo",
            ConnectAgentOptions(
                scaling=scaling,
                base_url="http://localhost:8080",
                agent_auth=mock_auth,
            ),
        )

        payload = mock_auth.init.call_args[1]["registration_payload"]
        assert payload["expectedInstances"] == 3
        assert payload["concurrency"] == 5
        assert payload["maxPendingBacklog"] == 20
        assert payload["maxRunningTimeSec"] == 300

    def test_connect_payload_includes_listing_and_scaling(self) -> None:
        """Connect payload includes listing and scaling fields."""
        mock_auth = MagicMock()
        mock_auth.init.return_value = {"pamToken": "pam-123", "accessToken": "jwt-1", "refreshToken": "rt-1"}

        scaling = AgentScaling(
            expected_instances=3,
            concurrency=5,
            max_pending_backlog=20,
            max_running_time_sec=300,
        )

        connect_agent(
            "acme_echo",
            ConnectAgentOptions(
                instance_id="AG-acme_echo-123",
                listing="public",
                scaling=scaling,
                base_url="http://localhost:8080",
                agent_auth=mock_auth,
            ),
        )

        payload = mock_auth.init.call_args[1]["registration_payload"]
        assert payload["agentName"] == "acme_echo"
        assert payload["instanceId"] == "AG-acme_echo-123"
        assert payload["listing"] == "public"
        assert payload["expectedInstances"] == 3
        assert payload["concurrency"] == 5
        assert payload["maxPendingBacklog"] == 20
        assert payload["maxRunningTimeSec"] == 300
        assert payload["sdkLanguage"] == "Python"

    def test_returns_pam_token(self) -> None:
        mock_auth = MagicMock()
        mock_auth.init.return_value = {"pamToken": "pam-xyz"}

        result = connect_agent(
            "acme_echo",
            ConnectAgentOptions(
                base_url="http://localhost:8080",
                agent_auth=mock_auth,
            ),
        )

        assert result.pam_token == "pam-xyz"


# ---------------------------------------------------------------------------
# Helper to create a mock urlopen that returns JSON
# ---------------------------------------------------------------------------


def _mock_urlopen_json(response_data, status=200):
    """Create a mock urlopen context manager returning JSON data."""
    def mock_urlopen(req, **kwargs):
        if status >= 400:
            import urllib.error
            body = json.dumps(response_data).encode("utf-8") if response_data else b""
            raise urllib.error.HTTPError(
                req.full_url, status, "Error", {}, BytesIO(body),
            )
        resp_bytes = json.dumps(response_data).encode("utf-8")
        resp = MagicMock()
        resp.read.return_value = resp_bytes
        resp.__enter__ = lambda s: resp
        resp.__exit__ = MagicMock(return_value=False)
        return resp
    return mock_urlopen


# ---------------------------------------------------------------------------
# get_agent (REST)
# ---------------------------------------------------------------------------


class TestGetAgent:
    def test_fetches_single_agent(self) -> None:
        response = {
            "agent": {
                "agentName": "acme_echo",
                "name": "Echo Agent",
                "description": "An echo agent",
                "skills": [{"id": "echo", "name": "Echo"}],
                "listing": "public",
                "billingMode": "paid",
                "card": {"name": "Echo Agent"},
                "registeredAt": "2024-01-01T00:00:00Z",
            }
        }

        with patch("urllib.request.urlopen", side_effect=_mock_urlopen_json(response)):
            entry = get_agent("acme_echo", base_url="http://localhost:8080")

        assert entry is not None
        assert entry.agent_name == "acme_echo"
        assert entry.name == "Echo Agent"
        assert entry.description == "An echo agent"
        assert entry.skills == [{"id": "echo", "name": "Echo"}]
        assert entry.listing == "public"
        assert entry.billing_mode == "paid"
        assert entry.card == {"name": "Echo Agent"}
        assert entry.created_at == "2024-01-01T00:00:00Z"

    def test_billing_mode_missing_defaults_to_none(self) -> None:
        """When the server response omits ``billingMode`` (e.g. older backend
        or pre-migration row), the parsed entry's ``billing_mode`` is ``None``
        so the SDK can treat it as ``free`` and route to playground."""
        response = {
            "agent": {
                "agentName": "acme_echo",
                "name": "Echo Agent",
            }
        }

        with patch("urllib.request.urlopen", side_effect=_mock_urlopen_json(response)):
            entry = get_agent("acme_echo", base_url="http://localhost:8080")

        assert entry is not None
        assert entry.billing_mode is None
        assert entry.listing is None

    def test_uses_custom_base_url(self) -> None:
        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["url"] = req.full_url
            resp_bytes = json.dumps({"agent": {"agentName": "acme_echo", "name": "Echo"}}).encode("utf-8")
            resp = MagicMock()
            resp.read.return_value = resp_bytes
            resp.__enter__ = lambda s: resp
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            get_agent("acme_echo", base_url="http://localhost:8080")

        assert captured["url"] == "http://localhost:8080/api/v1/registry/agents?agentName=acme_echo"

    def test_returns_none_on_404(self) -> None:
        with patch("urllib.request.urlopen", side_effect=_mock_urlopen_json({"code": "NotFound"}, status=404)):
            entry = get_agent("nonexistent", base_url="http://localhost:8080")

        assert entry is None

    def test_sends_authorization_header_when_api_key_provided(self) -> None:
        captured: dict = {}

        def mock_urlopen(req, **kwargs):
            captured["headers"] = dict(req.headers)
            resp_bytes = json.dumps(
                {"agent": {"agentName": "private_agent", "name": "Private", "listing": "private"}}
            ).encode("utf-8")
            resp = MagicMock()
            resp.read.return_value = resp_bytes
            resp.__enter__ = lambda s: resp
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            get_agent(
                "private_agent",
                base_url="http://localhost:8080",
                api_key="bk_test_key",
            )

        # urllib lowercases header names in `req.headers`; check case-insensitively.
        assert captured["headers"].get("Authorization") == "Bearer bk_test_key"

    def test_omits_authorization_header_when_no_api_key(self) -> None:
        captured: dict = {}

        def mock_urlopen(req, **kwargs):
            captured["headers"] = dict(req.headers)
            resp_bytes = json.dumps(
                {"agent": {"agentName": "public_agent", "name": "Public"}}
            ).encode("utf-8")
            resp = MagicMock()
            resp.read.return_value = resp_bytes
            resp.__enter__ = lambda s: resp
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            get_agent("public_agent", base_url="http://localhost:8080")

        assert "Authorization" not in captured["headers"]


# ---------------------------------------------------------------------------
# fetch_agent_registry (REST)
# ---------------------------------------------------------------------------


class TestFetchAgentRegistry:
    def test_fetches_all_agents(self) -> None:
        response = {
            "agents": [
                {
                    "agentName": "agent_1",
                    "name": "Agent One",
                    "description": "First",
                    "listing": "public",
                    "skills": [{"id": "s1", "name": "S1"}],
                },
                {
                    "agentName": "agent_2",
                    "name": "Agent Two",
                    "listing": "private",
                },
            ],
            "totalCount": 2,
            "next": None,
        }

        with patch("urllib.request.urlopen", side_effect=_mock_urlopen_json(response)):
            result = fetch_agent_registry(base_url="http://localhost:8080")

        assert len(result.agents) == 2
        assert result.total_count == 2
        assert result.agents[0].agent_name == "agent_1"
        assert result.agents[0].listing == "public"
        assert result.agents[1].agent_name == "agent_2"
        assert result.agents[1].listing == "private"

    def test_returns_empty_on_404(self) -> None:
        with patch("urllib.request.urlopen", side_effect=_mock_urlopen_json({"code": "NotFound"}, status=404)):
            result = fetch_agent_registry(base_url="http://localhost:8080")

        assert result.agents == []
        assert result.total_count == 0

    def test_uses_custom_base_url(self) -> None:
        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["url"] = req.full_url
            resp_bytes = json.dumps({"agents": [], "totalCount": 0}).encode("utf-8")
            resp = MagicMock()
            resp.read.return_value = resp_bytes
            resp.__enter__ = lambda s: resp
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            fetch_agent_registry(base_url="http://localhost:8080")

        assert "http://localhost:8080/api/v1/registry/agents" in captured["url"]

    def test_passes_query_params(self) -> None:
        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["url"] = req.full_url
            resp_bytes = json.dumps({"agents": [], "totalCount": 0}).encode("utf-8")
            resp = MagicMock()
            resp.read.return_value = resp_bytes
            resp.__enter__ = lambda s: resp
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            fetch_agent_registry(limit=25, cursor="abc123", base_url="http://localhost:8080")

        assert "limit=25" in captured["url"]
        assert "cursor=abc123" in captured["url"]
        assert "include=full" in captured["url"]


# ---------------------------------------------------------------------------
# fetch_agents_by_skill (REST)
# ---------------------------------------------------------------------------


class TestFetchAgentsBySkill:
    def test_passes_skill_query_param(self) -> None:
        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["url"] = req.full_url
            resp_bytes = json.dumps({"agents": [], "totalCount": 0}).encode("utf-8")
            resp = MagicMock()
            resp.read.return_value = resp_bytes
            resp.__enter__ = lambda s: resp
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            fetch_agents_by_skill("image-generation", base_url="http://localhost:8080")

        assert "skill=image-generation" in captured["url"]
        assert "include=full" in captured["url"]


# ---------------------------------------------------------------------------
# fetch_agents_by_listing (REST)
# ---------------------------------------------------------------------------


class TestFetchAgentsByListing:
    def test_passes_listing_public(self) -> None:
        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["url"] = req.full_url
            resp_bytes = json.dumps({"agents": [], "totalCount": 0}).encode("utf-8")
            resp = MagicMock()
            resp.read.return_value = resp_bytes
            resp.__enter__ = lambda s: resp
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            fetch_agents_by_listing("public", base_url="http://localhost:8080")

        assert "listing=public" in captured["url"]
        assert "include=full" in captured["url"]

    def test_passes_listing_private(self) -> None:
        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["url"] = req.full_url
            resp_bytes = json.dumps({"agents": [], "totalCount": 0}).encode("utf-8")
            resp = MagicMock()
            resp.read.return_value = resp_bytes
            resp.__enter__ = lambda s: resp
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            fetch_agents_by_listing("private", base_url="http://localhost:8080")

        assert "listing=private" in captured["url"]



# ---------------------------------------------------------------------------
# remove_agent (REST)
# ---------------------------------------------------------------------------


class TestRemoveAgent:
    def test_sends_delete_and_returns_true(self) -> None:
        captured = {}

        def mock_urlopen(req, **kwargs):
            captured["url"] = req.full_url
            captured["method"] = req.get_method()
            resp_bytes = json.dumps({"agentName": "acme_echo", "status": "deleted"}).encode("utf-8")
            resp = MagicMock()
            resp.read.return_value = resp_bytes
            resp.__enter__ = lambda s: resp
            resp.__exit__ = MagicMock(return_value=False)
            return resp

        with patch("urllib.request.urlopen", side_effect=mock_urlopen):
            result = remove_agent("acme_echo", base_url="http://localhost:8080")

        assert result is True
        assert captured["method"] == "DELETE"
        assert "acme_echo" in captured["url"]

    def test_returns_false_on_404(self) -> None:
        with patch("urllib.request.urlopen", side_effect=_mock_urlopen_json({"code": "NotFound"}, status=404)):
            result = remove_agent("nonexistent", base_url="http://localhost:8080")

        assert result is False
