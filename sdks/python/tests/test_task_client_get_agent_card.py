"""``TaskClient.get_agent_card()`` — credential forwarding and auth-failure behaviour.

``test_agent_registry_auth.py`` covers the standalone registry helpers, which take
an explicit ``api_key``. This covers the client method, whose whole job is to
derive that credential from the auth provider: prefix handling, lazy provider
init, the reactive refresh driven off an empty result, and the two different
meanings an empty result can carry. Mirrors
``sdks/node/tests/task-client-get-agent-card.test.ts``.

The distinction that needs pinning: on a Blocks Enterprise deployment a *rejected*
credential produces the same 404 as a *missing agent*, because the registry read is
on optional auth and degrades a bad bearer to anonymous rather than 401ing. So
``None`` must mean "no such agent" and an auth failure must raise — but only on
evidence the provider actually has one, or every ordinary missing-agent lookup
through a static provider would raise.
"""

from __future__ import annotations

import json
import urllib.error
from typing import Any, Dict, List, Optional

import pytest

from blocks_network.task_client import TaskClient

BASE_URL = "http://test-api.example.com"

FOUND_BODY = {"agent": {"agentName": "a", "card": {"name": "a"}}}


def _urlopen_stub(outcomes: List[Any], captured: List[Any]):
    """Stub for ``urlopen`` that records each Request and replays ``outcomes``.

    An outcome is either a dict (200 with that JSON body) or the int 404, which
    raises ``HTTPError`` the way a real missing agent does. The last outcome
    repeats so a test need not enumerate calls it does not assert on.
    """

    def mock_urlopen(req, **_kwargs):
        captured.append(req)
        outcome = outcomes[min(len(captured) - 1, len(outcomes) - 1)]
        if outcome == 404:
            raise urllib.error.HTTPError(BASE_URL, 404, "Not Found", {}, None)

        class _Resp:
            status = 200
            headers: Dict[str, str] = {}

            def read(self_inner) -> bytes:
                return json.dumps(outcome).encode()

            def __enter__(self_inner):
                return self_inner

            def __exit__(self_inner, *_a):
                return False

        return _Resp()

    return mock_urlopen


def _sent_auth(req: Any) -> Optional[str]:
    """Authorization header from a captured urllib Request, or None."""
    for key, value in req.header_items():
        if key.lower() == "authorization":
            return value
    return None


class FakeProvider:
    """Minimal AuthProvider covering the card-lookup paths.

    ``record_on_failure`` models a provider that learns its credential is
    permanently bad only once a refresh is attempted.
    """

    def __init__(
        self,
        header: Optional[str] = "Bearer jwt-abc",
        refresh_succeeds: bool = False,
        recorded: Optional[Exception] = None,
        record_on_failure: Optional[Exception] = None,
        rotate_to: Optional[str] = None,
    ) -> None:
        """Configure the header, refresh outcome, and recorded-error behaviour."""
        self._header = header
        self._refresh_succeeds = refresh_succeeds
        self._recorded = recorded
        self._record_on_failure = record_on_failure
        self._rotate_to = rotate_to
        self.calls: List[str] = []

    def get_auth_header(self) -> Optional[str]:
        self.calls.append("get_auth_header")
        return self._header

    def on_auth_failure(self) -> bool:
        self.calls.append("on_auth_failure")
        if self._record_on_failure is not None:
            self._recorded = self._record_on_failure
        if self._refresh_succeeds and self._rotate_to is not None:
            self._header = self._rotate_to
        return self._refresh_succeeds

    def ensure_ready(self) -> None:
        self.calls.append("ensure_ready")

    def get_last_auth_error(self) -> Optional[Exception]:
        return self._recorded


class NoLastErrorProvider:
    """A provider that does not implement ``get_last_auth_error`` at all.

    That method is the optional half of the protocol, probed with ``hasattr``.
    """

    def __init__(self, header: str = "Bearer static") -> None:
        """Hold a fixed header; this provider never refreshes."""
        self._header = header

    def get_auth_header(self) -> Optional[str]:
        return self._header

    def on_auth_failure(self) -> bool:
        return False

    def ensure_ready(self) -> None:
        return None


class MinimalProvider:
    """A provider implementing only the two methods the protocol requires.

    ``AuthProvider``'s docstring requires ``get_auth_header`` and
    ``on_auth_failure`` and nothing else, and a Protocol is not enforced at
    runtime, so a hand-written provider can legitimately look like this.
    ``ensure_ready`` and ``get_last_auth_error`` are both absent.
    """

    def get_auth_header(self) -> Optional[str]:
        """Return a fixed header."""
        return "Bearer minimal-jwt"

    def on_auth_failure(self) -> bool:
        """Report that no refresh is possible."""
        return False


def _client(auth_provider: Any = None) -> TaskClient:
    return TaskClient(
        subscribe_key="sub-c-test",
        billing_mode="free",
        base_url=BASE_URL,
        auth_provider=auth_provider,
    )


def _patch(monkeypatch, outcomes: List[Any], captured: List[Any]) -> None:
    monkeypatch.setattr("urllib.request.urlopen", _urlopen_stub(outcomes, captured))


class TestCredentialForwarding:
    def test_forwards_provider_credential_as_single_bearer(self, monkeypatch) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)

        card = _client(FakeProvider(header="Bearer jwt-abc")).get_agent_card("a")

        # Not ``Bearer Bearer jwt-abc``: the method strips the prefix off the
        # header because ``get_agent`` re-adds it around the raw credential.
        assert _sent_auth(captured[-1]) == "Bearer jwt-abc"
        assert card == {"name": "a"}

    def test_adds_bearer_prefix_when_header_omits_it(self, monkeypatch) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)

        _client(FakeProvider(header="raw-token")).get_agent_card("a")

        assert _sent_auth(captured[-1]) == "Bearer raw-token"

    def test_sends_no_auth_header_without_a_provider(self, monkeypatch) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)

        card = _client().get_agent_card("a")

        assert _sent_auth(captured[-1]) is None
        assert card == {"name": "a"}

    def test_initializes_provider_before_reading_header(self, monkeypatch) -> None:
        # Agent-side clients had not always initialized the provider by the time a
        # card was requested, so an uninitialized ConsumerAuth read None and the
        # lookup went out anonymous.
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)
        provider = FakeProvider()

        _client(provider).get_agent_card("a")

        assert provider.calls.index("ensure_ready") < provider.calls.index(
            "get_auth_header"
        )

    def test_works_with_a_provider_lacking_the_optional_hooks(
        self, monkeypatch
    ) -> None:
        # Calling ``ensure_ready`` outright raised AttributeError here for any
        # provider without it, while every other transport in both SDKs probes
        # first. The card lookup was the only unguarded call site.
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)

        card = _client(MinimalProvider()).get_agent_card("a")

        assert _sent_auth(captured[-1]) == "Bearer minimal-jwt"
        assert card == {"name": "a"}

    def test_missing_agent_stays_none_for_a_minimal_provider(
        self, monkeypatch
    ) -> None:
        # The refusal branch also touches the optional half of the protocol
        # (``get_last_auth_error``), so exercise it with the same provider.
        captured: List[Any] = []
        _patch(monkeypatch, [404], captured)

        assert _client(MinimalProvider()).get_agent_card("nope") is None

    def test_forwards_agent_auth_token_without_a_provider(self, monkeypatch) -> None:
        # ``agent_auth`` is the other supported way to construct an authenticated
        # client, and the RPC and file-upload paths both honour it. An agent-side
        # client that reads None on Enterprise while its other calls succeed is
        # the exact regression this guards.
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)

        class _AgentAuth:
            def get_access_token(self) -> Optional[str]:
                return "agent-jwt"

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url=BASE_URL,
            agent_auth=_AgentAuth(),
        )
        card = client.get_agent_card("a")

        assert _sent_auth(captured[-1]) == "Bearer agent-jwt"
        assert card == {"name": "a"}

    def test_refreshes_a_stale_agent_auth_token_and_retries(self, monkeypatch) -> None:
        # ``AgentAuth.refresh()`` is driven only by ``authenticated_fetch``'s 401
        # retry, and this read is on optional auth so it never 401s — and there is
        # no proactive scheduler. Without driving refresh here, a stale agent token
        # answers None for the client's lifetime.
        captured: List[Any] = []
        _patch(monkeypatch, [404, FOUND_BODY], captured)

        class _AgentAuth:
            def __init__(self) -> None:
                self.token = "stale"
                self.refreshed = False

            def get_access_token(self) -> Optional[str]:
                return self.token

            def refresh(self) -> None:
                self.refreshed = True
                self.token = "fresh"

        agent_auth = _AgentAuth()
        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url=BASE_URL,
            agent_auth=agent_auth,
        )
        card = client.get_agent_card("a")

        assert agent_auth.refreshed is True
        assert card == {"name": "a"}
        assert len(captured) == 2
        assert _sent_auth(captured[0]) == "Bearer stale"
        assert _sent_auth(captured[1]) == "Bearer fresh"

    def test_surfaces_an_agent_auth_refresh_failure(self, monkeypatch) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [404], captured)

        class _AgentAuth:
            def get_access_token(self) -> Optional[str]:
                return "stale"

            def refresh(self) -> None:
                raise RuntimeError("API key invalid")

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url=BASE_URL,
            agent_auth=_AgentAuth(),
        )

        with pytest.raises(RuntimeError, match="API key invalid"):
            client.get_agent_card("a")

    def test_absent_agent_stays_none_after_a_clean_agent_auth_refresh(
        self, monkeypatch
    ) -> None:
        # Refresh succeeds, the retry is still empty: the agent really is missing.
        captured: List[Any] = []
        _patch(monkeypatch, [404, 404], captured)

        class _AgentAuth:
            def get_access_token(self) -> Optional[str]:
                return "valid"

            def refresh(self) -> None:
                return None

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url=BASE_URL,
            agent_auth=_AgentAuth(),
        )

        assert client.get_agent_card("nope") is None
        assert len(captured) == 2

    def test_prefers_auth_provider_over_agent_auth(self, monkeypatch) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)

        class _AgentAuth:
            def get_access_token(self) -> Optional[str]:
                raise AssertionError("agent_auth must not be consulted")

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url=BASE_URL,
            auth_provider=FakeProvider(header="Bearer consumer-jwt"),
            agent_auth=_AgentAuth(),
        )
        client.get_agent_card("a")

        assert _sent_auth(captured[-1]) == "Bearer consumer-jwt"

    def test_sends_no_header_when_agent_auth_has_no_token_yet(
        self, monkeypatch
    ) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)

        class _AgentAuth:
            def get_access_token(self) -> Optional[str]:
                return None

        client = TaskClient(
            subscribe_key="sub-c-test",
            billing_mode="free",
            base_url=BASE_URL,
            agent_auth=_AgentAuth(),
        )
        client.get_agent_card("a")

        assert _sent_auth(captured[-1]) is None

    def test_reads_header_per_call_so_rotated_token_is_not_stale(
        self, monkeypatch
    ) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)
        provider = FakeProvider(header="Bearer first")
        client = _client(provider)

        client.get_agent_card("a")
        provider._header = "Bearer second"
        client.get_agent_card("a")

        assert _sent_auth(captured[0]) == "Bearer first"
        assert _sent_auth(captured[1]) == "Bearer second"


class TestAuthFailureVersusMissingAgent:
    def test_raises_recorded_auth_error_before_any_request(self, monkeypatch) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [FOUND_BODY], captured)
        failure = RuntimeError("refresh permanently failed")
        provider = FakeProvider(recorded=failure)

        with pytest.raises(RuntimeError, match="refresh permanently failed"):
            _client(provider).get_agent_card("a")
        assert captured == []

    def test_drives_one_reactive_refresh_off_empty_result(self, monkeypatch) -> None:
        # Nothing 401s — optional auth degrades a stale bearer to anonymous — so
        # the empty result is the only signal available to trigger the refresh.
        captured: List[Any] = []
        _patch(monkeypatch, [404, FOUND_BODY], captured)
        provider = FakeProvider(
            header="Bearer stale", refresh_succeeds=True, rotate_to="Bearer fresh"
        )

        card = _client(provider).get_agent_card("a")

        assert card == {"name": "a"}
        assert len(captured) == 2
        assert _sent_auth(captured[0]) == "Bearer stale"
        assert _sent_auth(captured[1]) == "Bearer fresh"

    def test_raises_when_unrecoverable_and_provider_recorded_why(
        self, monkeypatch
    ) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [404], captured)
        failure = RuntimeError("refresh rejected by backend")
        provider = FakeProvider(header="Bearer stale", record_on_failure=failure)

        with pytest.raises(RuntimeError, match="refresh rejected by backend"):
            _client(provider).get_agent_card("a")

    def test_raises_when_a_real_reactive_refresh_fails(self, monkeypatch) -> None:
        # The case that matters most and was missed: ConsumerAuth's reactive
        # refresh reported failure as a bare False and recorded nothing, so a
        # live auth outage read as "no such agent" — the exact confusion the
        # raise exists to prevent. The record now happens in ConsumerAuth, so
        # this asserts the whole path: refresh attempted, failed, recorded,
        # raised.
        captured: List[Any] = []
        _patch(monkeypatch, [404], captured)
        outage = RuntimeError("token endpoint unreachable")
        provider = FakeProvider(
            header="Bearer stale", record_on_failure=outage, refresh_succeeds=False
        )

        with pytest.raises(RuntimeError, match="token endpoint unreachable"):
            _client(provider).get_agent_card("a")
        assert "on_auth_failure" in provider.calls

    def test_returns_none_for_provider_that_simply_cannot_refresh(
        self, monkeypatch
    ) -> None:
        # A static-token provider always answers ``on_auth_failure()`` false. That
        # means "no refresh possible", not "the credential was rejected" — so a
        # genuinely absent agent must stay None here rather than raising.
        captured: List[Any] = []
        _patch(monkeypatch, [404], captured)
        provider = FakeProvider(header="Bearer static")

        assert _client(provider).get_agent_card("nope") is None
        # One GET, not two: no retry is spent when refresh was not possible.
        assert len(captured) == 1

    def test_returns_none_for_provider_without_get_last_auth_error(
        self, monkeypatch
    ) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [404], captured)

        assert _client(NoLastErrorProvider()).get_agent_card("nope") is None

    def test_returns_none_without_retry_when_no_provider(self, monkeypatch) -> None:
        captured: List[Any] = []
        _patch(monkeypatch, [404], captured)

        assert _client().get_agent_card("nope") is None
        assert len(captured) == 1
