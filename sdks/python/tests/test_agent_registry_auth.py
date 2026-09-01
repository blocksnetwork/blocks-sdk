"""
Credential forwarding on registry reads.

Registry reads are mounted on optional auth, so a credential is optional on
Blocks Network. A Blocks Enterprise deployment serves agent metadata to
authenticated callers only, so a caller that cannot send its credential reads
nothing there even when correctly configured. These assert the credential reaches
the wire, and mirror ``sdks/node/tests/agent-registry-auth.test.ts``.
"""

from __future__ import annotations

import json
from typing import Any, Dict, List, Optional
from unittest.mock import patch

import pytest

from blocks_network.agent_registry import (
    fetch_agent_registry,
    fetch_agents_by_listing,
    fetch_agents_by_tag,
    get_agent,
)

BASE_URL = "http://test-api.example.com"


def _capture_urlopen(response_data: Dict[str, Any], captured: List[Any]):
    """Stub for ``urlopen`` that records each Request so headers can be asserted."""

    def mock_urlopen(req, **_kwargs):
        captured.append(req)

        class _Resp:
            status = 200
            headers: Dict[str, str] = {}

            def read(self) -> bytes:
                return json.dumps(response_data).encode("utf-8")

            def __enter__(self):
                return self

            def __exit__(self, *_a) -> bool:
                return False

        return _Resp()

    return mock_urlopen


def _sent_auth(req: Any) -> Optional[str]:
    """Authorization header on a captured urllib Request, or None.

    urllib title-cases header names, so read through ``get_header`` rather than
    indexing ``headers`` — a direct lookup misses it and would make the negative
    assertions below pass for the wrong reason.
    """
    return req.get_header("Authorization")


class TestGetAgentCredentialForwarding:
    def test_sends_bearer_token_when_given_one(self) -> None:
        captured: List[Any] = []
        with patch(
            "urllib.request.urlopen",
            _capture_urlopen({"agent": {"agentName": "a", "card": {}}}, captured),
        ):
            get_agent("a", base_url=BASE_URL, api_key="cred-123")
        assert _sent_auth(captured[-1]) == "Bearer cred-123"

    def test_sends_no_auth_header_when_given_none(self) -> None:
        captured: List[Any] = []
        with patch(
            "urllib.request.urlopen",
            _capture_urlopen({"agent": {"agentName": "a", "card": {}}}, captured),
        ):
            get_agent("a", base_url=BASE_URL)
        assert _sent_auth(captured[-1]) is None


class TestFetchAgentRegistryCredentialForwarding:
    def test_sends_bearer_token_when_given_one(self) -> None:
        captured: List[Any] = []
        with patch(
            "urllib.request.urlopen",
            _capture_urlopen({"agents": [], "next": None, "totalCount": 0}, captured),
        ):
            fetch_agent_registry(base_url=BASE_URL, api_key="cred-456")
        assert _sent_auth(captured[-1]) == "Bearer cred-456"

    def test_sends_no_auth_header_when_given_none(self) -> None:
        captured: List[Any] = []
        with patch(
            "urllib.request.urlopen",
            _capture_urlopen({"agents": [], "next": None, "totalCount": 0}, captured),
        ):
            fetch_agent_registry(base_url=BASE_URL)
        assert _sent_auth(captured[-1]) is None


# Every exported registry read helper, so a newly added one is an obvious gap
# rather than a silent hole: on Enterprise a helper that cannot authenticate
# returns an empty page to a caller that holds a perfectly good credential.
LIST_BODY = {"agents": [], "next": None, "totalCount": 0}

LIST_HELPERS = [
    ("fetch_agent_registry", lambda **kw: fetch_agent_registry(base_url=BASE_URL, **kw)),
    ("fetch_agents_by_tag", lambda **kw: fetch_agents_by_tag("t", base_url=BASE_URL, **kw)),
    (
        "fetch_agents_by_listing",
        lambda **kw: fetch_agents_by_listing("public", base_url=BASE_URL, **kw),
    ),
]


@pytest.mark.parametrize("name,call", LIST_HELPERS, ids=[h[0] for h in LIST_HELPERS])
class TestEveryListHelperForwardsCredential:
    def test_sends_the_credential(self, name, call) -> None:
        captured = []
        with patch("urllib.request.urlopen", _capture_urlopen(LIST_BODY, captured)):
            call(api_key="cred-789")
        assert _sent_auth(captured[-1]) == "Bearer cred-789"

    def test_sends_nothing_when_given_nothing(self, name, call) -> None:
        captured = []
        with patch("urllib.request.urlopen", _capture_urlopen(LIST_BODY, captured)):
            call()
        assert _sent_auth(captured[-1]) is None
