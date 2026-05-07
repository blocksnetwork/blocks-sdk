"""Bootstrap-path billing_mode tests for ``start_agent_instance``.

Phase 3 of the Billing Mode Contract initiative (IMPL §3 Python):

- ``start_agent_instance`` calls registry GET at boot (existing behavior).
- The resolved ``billing_mode`` is forwarded UNCONDITIONALLY into the
  connect request payload via ``ConnectAgentOptions.billing_mode``.
- No explicit-option override path exists. Provider's authoritative
  source is the registry; to change billing mode, update the registry,
  restart, and let the new value flow through registry GET -> connect.
- Missing/invalid registry billing_mode is a startup error — the SDK
  does NOT guess from price fields or keyset names.
"""

from __future__ import annotations

import time
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.agent_registry import AgentEntry, ConnectAgentResult
from blocks_network.cdm_config import CdmApiConfig, CdmConfig, CdmKeyset
from blocks_network.types import AgentInstanceOptions

from tests.conftest import minimal_card


def _make_chain_mock():
    mock = MagicMock()
    for method in (
        "channel", "channels", "message", "meta", "should_store",
        "use_post", "with_presence", "state", "file_name",
        "file_object", "file_id", "execute",
    ):
        getattr(mock, method).side_effect = lambda *a, _c=mock, **kw: _c
    mock.sync.return_value = MagicMock()
    return mock


def _make_mock_pubnub():
    pn = MagicMock()
    pn.publish.return_value = _make_chain_mock()
    pn.subscribe.return_value = _make_chain_mock()
    pn.set_state.return_value = _make_chain_mock()
    pn.unsubscribe.return_value = _make_chain_mock()
    pn._listeners = []
    pn.add_listener.side_effect = lambda l: pn._listeners.append(l)
    pn.remove_listener.side_effect = lambda l: (
        pn._listeners.remove(l) if l in pn._listeners else None
    )
    pn.set_filter_expression = MagicMock()
    pn.config = MagicMock()
    pn.config.filter_expression = None
    pn.set_token = MagicMock()
    return pn


def _make_cdm() -> CdmConfig:
    return CdmConfig(
        playground=CdmKeyset(publish_key="pg-pub", subscribe_key="pg-sub"),
        network=CdmKeyset(publish_key="nw-pub", subscribe_key="nw-sub"),
        api=CdmApiConfig(base_url="https://api.example.com"),
    )


def _make_agent_entry(billing_mode: str | None) -> AgentEntry:
    return AgentEntry(
        agent_name="acme_echo",
        name="acme_echo",
        description="test",
        listing="public",
        billing_mode=billing_mode,  # type: ignore[arg-type]
    )


# ---------------------------------------------------------------------------
# Bootstrap calls registry GET; resolved billing_mode is forwarded to connect
# ---------------------------------------------------------------------------


class TestRegistryGetIsAuthoritative:
    """Boot-time registry GET feeds connect payload — no provider override."""

    @patch.dict("os.environ", {"BLOCKS_API_KEY": "bk_test"})
    @patch("blocks_network.agent_registry.connect_agent")
    @patch("blocks_network.agent_instance.get_agent")
    @patch("blocks_network.agent_instance.fetch_cdm_config")
    def test_registry_billing_mode_paid_flows_into_connect(
        self,
        mock_fetch_cdm,
        mock_get_agent,
        mock_connect,
    ):
        from blocks_network.agent_instance import start_agent_instance

        mock_fetch_cdm.return_value = _make_cdm()
        mock_get_agent.return_value = _make_agent_entry("paid")
        mock_connect.return_value = ConnectAgentResult(
            control_channel="agent.test-id.control",
        )

        with patch(
            "blocks_network.agent_instance.create_pubnub_client",
            return_value=_make_mock_pubnub(),
        ):
            result = start_agent_instance(
                AgentInstanceOptions(
                    card=minimal_card(),
                    agent_name="acme_echo",
                )
            )

        time.sleep(0.5)  # let the registration thread run

        # Registry GET happened with the agent_name
        mock_get_agent.assert_called_once()
        get_args, get_kwargs = mock_get_agent.call_args
        assert get_args[0] == "acme_echo" or get_kwargs.get("agent_name") == "acme_echo"

        # connect_agent was called with billing_mode='paid'
        mock_connect.assert_called_once()
        _, connect_kwargs = mock_connect.call_args
        connect_options = connect_kwargs.get("options") or mock_connect.call_args[0][1]
        assert connect_options.billing_mode == "paid"

        result["stop"]()

    @patch.dict("os.environ", {"BLOCKS_API_KEY": "bk_test"})
    @patch("blocks_network.agent_registry.connect_agent")
    @patch("blocks_network.agent_instance.get_agent")
    @patch("blocks_network.agent_instance.fetch_cdm_config")
    def test_registry_billing_mode_free_flows_into_connect(
        self,
        mock_fetch_cdm,
        mock_get_agent,
        mock_connect,
    ):
        from blocks_network.agent_instance import start_agent_instance

        mock_fetch_cdm.return_value = _make_cdm()
        mock_get_agent.return_value = _make_agent_entry("free")
        mock_connect.return_value = ConnectAgentResult(
            control_channel="agent.test-id.control",
        )

        with patch(
            "blocks_network.agent_instance.create_pubnub_client",
            return_value=_make_mock_pubnub(),
        ):
            result = start_agent_instance(
                AgentInstanceOptions(
                    card=minimal_card(),
                    agent_name="acme_echo",
                )
            )

        time.sleep(0.5)

        mock_connect.assert_called_once()
        _, connect_kwargs = mock_connect.call_args
        connect_options = connect_kwargs.get("options") or mock_connect.call_args[0][1]
        assert connect_options.billing_mode == "free"

        result["stop"]()


class TestMissingRegistryBillingModeIsFatal:
    """The SDK does NOT guess from prices or keyset names — fail fast."""

    @patch.dict("os.environ", {"BLOCKS_API_KEY": "bk_test"})
    @patch("blocks_network.agent_instance.get_agent")
    @patch("blocks_network.agent_instance.fetch_cdm_config")
    def test_missing_billing_mode_raises_runtime_error(
        self, mock_fetch_cdm, mock_get_agent
    ):
        from blocks_network.agent_instance import start_agent_instance

        mock_fetch_cdm.return_value = _make_cdm()
        mock_get_agent.return_value = _make_agent_entry(None)

        with pytest.raises(RuntimeError, match="missing a valid billing_mode"):
            start_agent_instance(
                AgentInstanceOptions(
                    card=minimal_card(),
                    agent_name="acme_echo",
                )
            )

    @patch.dict("os.environ", {"BLOCKS_API_KEY": "bk_test"})
    @patch("blocks_network.agent_instance.get_agent")
    @patch("blocks_network.agent_instance.fetch_cdm_config")
    def test_invalid_billing_mode_raises_runtime_error(
        self, mock_fetch_cdm, mock_get_agent
    ):
        """Non-enum value (e.g. 'network') must fail rather than coerce."""
        from blocks_network.agent_instance import start_agent_instance

        mock_fetch_cdm.return_value = _make_cdm()
        mock_get_agent.return_value = _make_agent_entry("network")  # invalid

        with pytest.raises(RuntimeError, match="missing a valid billing_mode"):
            start_agent_instance(
                AgentInstanceOptions(
                    card=minimal_card(),
                    agent_name="acme_echo",
                )
            )

    @patch.dict("os.environ", {"BLOCKS_API_KEY": "bk_test"})
    @patch("blocks_network.agent_instance.get_agent")
    @patch("blocks_network.agent_instance.fetch_cdm_config")
    def test_agent_not_in_registry_is_fatal(
        self, mock_fetch_cdm, mock_get_agent
    ):
        """Unregistered agent at boot is a fatal startup error.

        Replaces the prior soft warning + playground default. The
        backend connect schema requires billingMode; without a
        registry record, the SDK has no authoritative value.
        """
        from blocks_network.agent_instance import start_agent_instance

        mock_fetch_cdm.return_value = _make_cdm()
        mock_get_agent.return_value = None

        with pytest.raises(RuntimeError, match="not found in registry"):
            start_agent_instance(
                AgentInstanceOptions(
                    card=minimal_card(),
                    agent_name="acme_echo",
                )
            )


class TestConnectAgentOptionsHasBillingMode:
    """ConnectAgentOptions.billing_mode is the typed boundary field."""

    def test_default_is_none(self):
        from blocks_network.agent_registry import ConnectAgentOptions

        opts = ConnectAgentOptions()
        assert opts.billing_mode is None

    def test_accepts_free(self):
        from blocks_network.agent_registry import ConnectAgentOptions

        opts = ConnectAgentOptions(billing_mode="free")
        assert opts.billing_mode == "free"

    def test_accepts_paid(self):
        from blocks_network.agent_registry import ConnectAgentOptions

        opts = ConnectAgentOptions(billing_mode="paid")
        assert opts.billing_mode == "paid"


class TestConnectAgentPayloadIncludesBillingMode:
    """``connect_agent`` puts ``billingMode`` in the wire payload."""

    @patch("blocks_network.agent_registry.with_retry")
    def test_payload_includes_billing_mode_camelcase(self, mock_retry):
        from blocks_network.agent_registry import (
            AgentScaling,
            ConnectAgentOptions,
            connect_agent,
        )

        captured_payloads: list = []

        # connect_agent invokes with_retry around _do_auth_register, which
        # calls agent_auth.init(payload=...). Capture the payload.
        def _retry(fn, **kwargs):
            return fn()

        mock_retry.side_effect = _retry

        fake_auth = MagicMock()

        def _capture_init(*, registration_payload):
            captured_payloads.append(registration_payload)
            return {
                "pamToken": "tok",
                "agentId": "id",
                "controlChannel": "agent.id.control",
                "accessToken": "jwt",
                "refreshToken": "rt",
            }

        fake_auth.init.side_effect = _capture_init

        connect_agent(
            "acme_echo",
            ConnectAgentOptions(
                instance_id="AG-acme_echo-test",
                billing_mode="paid",
                listing="public",
                scaling=AgentScaling(expected_instances=1, concurrency=1),
                agent_auth=fake_auth,
            ),
        )

        assert len(captured_payloads) == 1
        payload = captured_payloads[0]
        # camelCase wire field
        assert payload["billingMode"] == "paid"
        # snake_case must not leak
        assert "billing_mode" not in payload

    @patch("blocks_network.agent_registry.with_retry")
    def test_payload_strips_billing_mode_when_none(self, mock_retry):
        """If billing_mode is None on options, the wire field is omitted entirely.

        This protects backend Zod schemas that treat ``null`` on optional
        fields as invalid. The agent_instance.py path always sets a real
        value; this test documents the strip-None behavior.
        """
        from blocks_network.agent_registry import (
            AgentScaling,
            ConnectAgentOptions,
            connect_agent,
        )

        captured_payloads: list = []

        def _retry(fn, **kwargs):
            return fn()

        mock_retry.side_effect = _retry

        fake_auth = MagicMock()

        def _capture_init(*, registration_payload):
            captured_payloads.append(registration_payload)
            return {
                "accessToken": "jwt",
                "refreshToken": "rt",
            }

        fake_auth.init.side_effect = _capture_init

        connect_agent(
            "acme_echo",
            ConnectAgentOptions(
                instance_id="AG-acme_echo-test",
                billing_mode=None,
                scaling=AgentScaling(expected_instances=1, concurrency=1),
                agent_auth=fake_auth,
            ),
        )

        payload = captured_payloads[0]
        assert "billingMode" not in payload
