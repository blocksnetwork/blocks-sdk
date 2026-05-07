"""Tests for CDM-based single-active-instance with environment switching."""

from unittest.mock import MagicMock, patch, call

import pytest
from blocks_network.agent_registry import AgentEntry, ConnectAgentResult
from blocks_network.cdm_config import CdmConfig, CdmKeyset, CdmApiConfig

from tests.conftest import minimal_card


MOCK_CDM_CONFIG = CdmConfig(
    playground=CdmKeyset(publish_key="pub-c-pg", subscribe_key="sub-c-pg"),
    network=CdmKeyset(publish_key="pub-c-nw", subscribe_key="sub-c-nw"),
    api=CdmApiConfig(base_url="http://localhost:3001"),
)


def _make_mock_pn() -> MagicMock:
    """Create a mock PubNub client that supports chained builder calls."""
    mock = MagicMock()
    mock.subscribe.return_value = MagicMock()
    mock.subscribe.return_value.channels.return_value = MagicMock()
    mock.subscribe.return_value.channels.return_value.execute.return_value = None
    mock.unsubscribe.return_value = MagicMock()
    mock.unsubscribe.return_value.channels.return_value = MagicMock()
    mock.unsubscribe.return_value.channels.return_value.execute.return_value = None
    mock.set_state.return_value = MagicMock()
    mock.set_state.return_value.channels.return_value = MagicMock()
    mock.set_state.return_value.channels.return_value.state.return_value = MagicMock()
    mock.set_state.return_value.channels.return_value.state.return_value.sync.return_value = None
    pub = MagicMock()
    pub.channel.return_value = pub
    pub.message.return_value = pub
    pub.meta.return_value = pub
    pub.should_store.return_value = pub
    pub.use_post.return_value = pub
    pub.sync.return_value = None
    mock.publish.return_value = pub
    return mock


class TestSingleActiveInstance:
    """Tests for CDM config with single active PubNub instance."""

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_creates_single_control_client(self, mock_create_pn: MagicMock) -> None:
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )

        with (
            patch("blocks_network.agent_instance.fetch_cdm_config", return_value=MOCK_CDM_CONFIG),
            patch("blocks_network.agent_instance.get_agent", return_value=mock_entry),
        ):
            opts = AgentInstanceOptions(card=minimal_card(), agent_name="test_agent", handler=lambda task, ctx: {})
            result = start_agent_instance(opts)

        # Should create only ONE control client (playground by default)
        create_calls = mock_create_pn.call_args_list
        # First call is the control client
        first_call = create_calls[0]
        assert first_call.kwargs.get("subscribe_key") == "sub-c-pg"

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_exposes_cdm_config(self, mock_create_pn: MagicMock) -> None:
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )

        with (
            patch("blocks_network.agent_instance.fetch_cdm_config", return_value=MOCK_CDM_CONFIG),
            patch("blocks_network.agent_instance.get_agent", return_value=mock_entry),
        ):
            opts = AgentInstanceOptions(card=minimal_card(), agent_name="test_agent", handler=lambda task, ctx: {})
            result = start_agent_instance(opts)

        assert result["cdm_config"] is not None
        assert result["cdm_config"].playground.publish_key == "pub-c-pg"
        assert result["cdm_config"].network.subscribe_key == "sub-c-nw"

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_no_clients_dict_in_return(self, mock_create_pn: MagicMock) -> None:
        """Return dict should NOT contain 'clients' (single-instance model)."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )

        with (
            patch("blocks_network.agent_instance.fetch_cdm_config", return_value=MOCK_CDM_CONFIG),
            patch("blocks_network.agent_instance.get_agent", return_value=mock_entry),
        ):
            opts = AgentInstanceOptions(card=minimal_card(), agent_name="test_agent", handler=lambda task, ctx: {})
            result = start_agent_instance(opts)

        assert "clients" not in result

        result["stop"]()


class TestRegistryEnvironmentSelection:
    """Tests for registry-based environment selection at startup."""

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_uses_playground_when_billing_mode_is_free(
        self, mock_create_pn: MagicMock
    ) -> None:
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )

        with (
            patch(
                "blocks_network.agent_instance.fetch_cdm_config",
                return_value=MOCK_CDM_CONFIG,
            ),
            patch(
                "blocks_network.agent_instance.get_agent",
                return_value=mock_entry,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

        first_call = mock_create_pn.call_args_list[0]
        assert first_call.kwargs.get("subscribe_key") == "sub-c-pg"

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_uses_network_when_paid_with_public_listing(
        self, mock_create_pn: MagicMock
    ) -> None:
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="paid"
        )

        with (
            patch(
                "blocks_network.agent_instance.fetch_cdm_config",
                return_value=MOCK_CDM_CONFIG,
            ),
            patch(
                "blocks_network.agent_instance.get_agent",
                return_value=mock_entry,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

        first_call = mock_create_pn.call_args_list[0]
        assert first_call.kwargs.get("subscribe_key") == "sub-c-nw"

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_uses_network_when_paid_with_private_listing(
        self, mock_create_pn: MagicMock
    ) -> None:
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="private", billing_mode="paid"
        )

        with (
            patch(
                "blocks_network.agent_instance.fetch_cdm_config",
                return_value=MOCK_CDM_CONFIG,
            ),
            patch(
                "blocks_network.agent_instance.get_agent",
                return_value=mock_entry,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

        first_call = mock_create_pn.call_args_list[0]
        assert first_call.kwargs.get("subscribe_key") == "sub-c-nw"

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_raises_when_registry_billing_mode_is_missing(
        self, mock_create_pn: MagicMock
    ) -> None:
        """Missing billing_mode on the registry entry is now a fatal startup error.

        Per Billing Mode Contract IMPL §3 the SDK does NOT guess from
        prices or keyset names — registry GET is the authoritative source
        and must return a valid ``free`` or ``paid`` value. The previous
        soft-fallback to playground would silently send connect requests
        without a billingMode field, which the backend connect Zod schema
        now rejects.
        """
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent",
            name="Test Agent",
            listing="public",
            billing_mode=None,
        )

        with (
            patch(
                "blocks_network.agent_instance.fetch_cdm_config",
                return_value=MOCK_CDM_CONFIG,
            ),
            patch(
                "blocks_network.agent_instance.get_agent",
                return_value=mock_entry,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            with pytest.raises(RuntimeError, match="missing a valid billing_mode"):
                start_agent_instance(opts)

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_raises_when_agent_not_found(
        self, mock_create_pn: MagicMock
    ) -> None:
        """Unregistered agent at boot is a fatal startup error.

        Per Billing Mode Contract IMPL §3 the registry GET is the
        authoritative source for billing_mode; without a registered
        agent the SDK cannot construct a valid connect payload.
        """
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        with (
            patch(
                "blocks_network.agent_instance.fetch_cdm_config",
                return_value=MOCK_CDM_CONFIG,
            ),
            patch(
                "blocks_network.agent_instance.get_agent",
                return_value=None,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="unknown_agent", handler=lambda task, ctx: {}
            )
            with pytest.raises(RuntimeError, match="not found in registry"):
                start_agent_instance(opts)

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_throws_when_registry_api_fails(
        self, mock_create_pn: MagicMock
    ) -> None:
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        with (
            patch(
                "blocks_network.agent_instance.fetch_cdm_config",
                return_value=MOCK_CDM_CONFIG,
            ),
            patch(
                "blocks_network.agent_instance.get_agent",
                side_effect=Exception("Network error"),
            ),
            pytest.raises(Exception, match="Network error"),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            start_agent_instance(opts)


class TestSwitchEnvironmentPamToken:
    """Tests for PAM token handling during environment switch."""

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_applies_pam_token_from_switch_message(
        self, mock_create_pn: MagicMock
    ) -> None:
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )

        with (
            patch(
                "blocks_network.agent_instance.fetch_cdm_config",
                return_value=MOCK_CDM_CONFIG,
            ),
            patch(
                "blocks_network.agent_instance.get_agent",
                return_value=mock_entry,
            ),
            patch(
                "blocks_network.agent_registry.connect_agent",
                return_value=ConnectAgentResult(control_channel="agent.test-agent-id.control"),
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

        # Capture the listener that was added to the control client
        add_listener_calls = mock_pn.add_listener.call_args_list
        assert len(add_listener_calls) > 0
        listener = add_listener_calls[0][0][0]

        # Reset mock to track calls on the NEW client created during switch
        mock_create_pn.reset_mock()
        new_mock_pn = _make_mock_pn()
        mock_create_pn.return_value = new_mock_pn

        # Simulate SwitchEnvironment with pamToken
        # Python SubscribeCallback.message takes (self, pubnub_instance, event)
        msg_event = MagicMock()
        msg_event.message = {
            "type": "SwitchEnvironment",
            "environment": "network",
            "pamToken": "token-for-network",
        }
        msg_event.user_metadata = {"broadcast": "true"}
        listener.message(mock_pn, msg_event)

        # New client should have set_token called with the provided pamToken
        new_mock_pn.set_token.assert_called_with("token-for-network")

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_rejects_switch_environment_without_pam_token(
        self, mock_create_pn: MagicMock
    ) -> None:
        """SwitchEnvironment without pamToken is rejected (no fallback re-registration)."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )

        with (
            patch(
                "blocks_network.agent_instance.fetch_cdm_config",
                return_value=MOCK_CDM_CONFIG,
            ),
            patch(
                "blocks_network.agent_instance.get_agent",
                return_value=mock_entry,
            ),
            patch(
                "blocks_network.agent_registry.connect_agent",
                return_value=ConnectAgentResult(control_channel="agent.test-agent-id.control"),
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

        # Capture the listener
        add_listener_calls = mock_pn.add_listener.call_args_list
        listener = add_listener_calls[0][0][0]

        # Reset and prepare new client
        mock_create_pn.reset_mock()
        new_mock_pn = _make_mock_pn()
        mock_create_pn.return_value = new_mock_pn

        # Simulate SwitchEnvironment WITHOUT pamToken — should be rejected
        msg_event = MagicMock()
        msg_event.message = {
            "type": "SwitchEnvironment",
            "environment": "network",
        }
        msg_event.user_metadata = {"broadcast": "true"}
        listener.message(mock_pn, msg_event)

        # No new client should have been created — switch was rejected
        mock_create_pn.assert_not_called()

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_clears_latest_control_token_seeded_by_start_task(
        self, mock_create_pn: MagicMock
    ) -> None:
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )

        with (
            patch(
                "blocks_network.agent_instance.fetch_cdm_config",
                return_value=MOCK_CDM_CONFIG,
            ),
            patch(
                "blocks_network.agent_instance.get_agent",
                return_value=mock_entry,
            ),
            patch(
                "blocks_network.agent_registry.connect_agent",
                return_value=ConnectAgentResult(control_channel="agent.test-agent-id.control"),
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

        add_listener_calls = mock_pn.add_listener.call_args_list
        listener = add_listener_calls[0][0][0]

        # Seed latestControlToken via StartTask with controlToken
        seed_event = MagicMock()
        seed_event.message = {
            "type": "StartTask",
            "taskId": "seed-task-1",
            "requestParts": [{"role": "user", "parts": [{"type": "text", "text": "hi"}]}],
            "controlToken": "stale-control-token",
        }
        seed_event.user_metadata = {"broadcast": "true"}
        listener.message(mock_pn, seed_event)

        # Prepare new client for the switch
        mock_create_pn.reset_mock()
        new_mock_pn = _make_mock_pn()
        mock_create_pn.return_value = new_mock_pn

        # Switch environment with pamToken — stale latestControlToken must NOT be applied
        switch_event = MagicMock()
        switch_event.message = {
            "type": "SwitchEnvironment",
            "environment": "network",
            "pamToken": "network-token",
        }
        switch_event.user_metadata = {"broadcast": "true"}
        listener.message(mock_pn, switch_event)

        # Should have exactly one set_token call with the message's pamToken
        set_token_calls = new_mock_pn.set_token.call_args_list
        assert len(set_token_calls) == 1
        assert set_token_calls[0][0][0] == "network-token"

        result["stop"]()
