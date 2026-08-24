"""
Tests for SDK dynamic keyset fixes (Issues #6, #7, #8, #9).

- #6: Registration uses registry-resolved listing, not default
- #7: SwitchEnvironment without pamToken is rejected. Keyset transitions
      in either direction are allowed when a pamToken is supplied — the
      backend, not the SDK, gates which transitions fire (free <-> paid
      billingMode flips).
- #8: Race condition resolved by #7 (no async fallback registration)
- #9: TaskClient factory receives active keyset keys
"""

from __future__ import annotations

import threading
import time
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
    mock.set_filter_expression = MagicMock()
    mock.set_token = MagicMock()
    mock._listeners = []
    mock.add_listener.side_effect = lambda l: mock._listeners.append(l)
    mock.remove_listener.side_effect = lambda l: (
        mock._listeners.remove(l) if l in mock._listeners else None
    )
    return mock


def _get_listener(mock_pn: MagicMock) -> MagicMock:
    """Extract the SubscribeCallback listener attached to the mock PubNub."""
    calls = mock_pn.add_listener.call_args_list
    assert len(calls) > 0, "No listener was added to the mock PubNub"
    return calls[0][0][0]


def _send_message(listener, mock_pn, msg_dict, meta=None):
    """Simulate a PubNub message event on the listener."""
    event = MagicMock()
    event.message = msg_dict
    event.user_metadata = meta
    listener.message(mock_pn, event)


# ===========================================================================
# Issue #6: Registration uses registry-resolved listing
# ===========================================================================


class TestRegistrationUsesRegistryListing:
    """Issue #6: Registration should use registryListing from the DB,
    not the default options.listing ('playground')."""

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_registration_uses_public_from_registry(
        self, mock_create_pn: MagicMock
    ) -> None:
        """A 'public' agent in the registry should register with listing='public',
        not the options default."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public",
            billing_mode="free",
        )
        mock_reg_result = ConnectAgentResult(pam_token="test-token", control_channel="agent.test-agent-id.control")

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
                return_value=mock_reg_result,
            ) as mock_register,
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent",
                handler=lambda task, ctx: {},
                # Note: listing defaults to None (falls back to "playground" in connect_agent)
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

            # Registration should have been called with 'public' (from registry),
            # NOT 'playground' (the default)
            mock_register.assert_called_once()
            reg_opts = mock_register.call_args[0][1]
            assert reg_opts.listing == "public", (
                f"Expected listing='public' from registry, got '{reg_opts.listing}'"
            )

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_registration_uses_private_from_registry(
        self, mock_create_pn: MagicMock
    ) -> None:
        """A 'private' agent in the registry should register with listing='private'."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="private",
            billing_mode="free",
        )
        mock_reg_result = ConnectAgentResult(pam_token="test-token", control_channel="agent.test-agent-id.control")

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
                return_value=mock_reg_result,
            ) as mock_register,
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent",
                handler=lambda task, ctx: {},
            )
            result = start_agent_instance(opts)

            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

            mock_register.assert_called_once()
            reg_opts = mock_register.call_args[0][1]
            assert reg_opts.listing == "private"

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_registration_fails_when_agent_not_found_in_registry(
        self, mock_create_pn: MagicMock
    ) -> None:
        """When agent is not in registry, ``start_agent_instance`` raises.

        Registry GET
        is the authoritative source for billing_mode; without a registered
        agent the SDK has no value to forward into the connect payload.
        Previously this test asserted soft-fallback to ``options.listing``;
        that behavior is intentionally removed — the SDK does NOT guess.
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
                return_value=None,  # Not found in registry
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent",
                handler=lambda task, ctx: {},
                listing="private",
            )
            with pytest.raises(RuntimeError, match="not found in registry"):
                start_agent_instance(opts)


# ===========================================================================
# Issue #7: SwitchEnvironment requires pamToken + forbidden transitions
# ===========================================================================


class TestSwitchEnvironmentRequiresPamToken:
    """Issue #7: SwitchEnvironment without pamToken must be rejected."""

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_switch_without_pam_token_rejected(
        self, mock_create_pn: MagicMock
    ) -> None:
        """SwitchEnvironment message without pamToken is silently rejected."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        listener = _get_listener(mock_pn)

        # Reset to track calls during switch
        mock_create_pn.reset_mock()

        # Simulate SwitchEnvironment WITHOUT pamToken
        _send_message(listener, mock_pn, {
            "type": "SwitchEnvironment",
            "environment": "network",
        })

        # No new PubNub client should be created
        mock_create_pn.assert_not_called()

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_switch_with_empty_pam_token_rejected(
        self, mock_create_pn: MagicMock
    ) -> None:
        """SwitchEnvironment with empty string pamToken is also rejected."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        listener = _get_listener(mock_pn)
        mock_create_pn.reset_mock()

        # Simulate SwitchEnvironment with empty pamToken
        _send_message(listener, mock_pn, {
            "type": "SwitchEnvironment",
            "environment": "network",
            "pamToken": "",
        })

        # No new PubNub client should be created
        mock_create_pn.assert_not_called()

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_no_fallback_registration_threads(
        self, mock_create_pn: MagicMock
    ) -> None:
        """No env-switch-register daemon threads should be spawned."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        listener = _get_listener(mock_pn)

        # Simulate SwitchEnvironment without pamToken
        _send_message(listener, mock_pn, {
            "type": "SwitchEnvironment",
            "environment": "network",
        })

        # Verify no env-switch-register threads were spawned
        register_threads = [
            t for t in threading.enumerate()
            if t.name and t.name.startswith("env-switch-register")
        ]
        assert len(register_threads) == 0, (
            f"Found {len(register_threads)} env-switch-register threads "
            "but fallback registration should have been removed"
        )

        result["stop"]()


class TestSwitchEnvironmentTransitions:
    """Keyset transitions are driven by billingMode and unrestricted by
    listing. Either direction (paid<->free) is allowed when a pamToken
    is present."""

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_public_to_playground_allowed(
        self, mock_create_pn: MagicMock
    ) -> None:
        """A public+paid agent edited back to public+free must be able to
        switch from network to playground keyset."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        # Start as public + paid (network keyset under billing_mode routing)
        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="paid"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        listener = _get_listener(mock_pn)
        mock_create_pn.reset_mock()
        new_mock_pn = _make_mock_pn()
        mock_create_pn.return_value = new_mock_pn

        # Switch from network (public+paid) to playground — must succeed
        _send_message(listener, mock_pn, {
            "type": "SwitchEnvironment",
            "environment": "playground",
            "pamToken": "playground-token",
        })

        # A new PubNub client should be created with playground keys
        mock_create_pn.assert_called_once()
        factory_call = mock_create_pn.call_args
        assert factory_call.kwargs.get("publish_key") == "pub-c-pg"
        assert factory_call.kwargs.get("subscribe_key") == "sub-c-pg"
        new_mock_pn.set_token.assert_called_with("playground-token")

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_private_to_playground_allowed(
        self, mock_create_pn: MagicMock
    ) -> None:
        """A private+paid agent edited back to private+free must be able to
        switch from network to playground keyset."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        # private + paid (network keyset under billing_mode routing)
        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="private", billing_mode="paid"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        listener = _get_listener(mock_pn)
        mock_create_pn.reset_mock()
        new_mock_pn = _make_mock_pn()
        mock_create_pn.return_value = new_mock_pn

        # Switch from network (private+paid) to playground — must succeed
        _send_message(listener, mock_pn, {
            "type": "SwitchEnvironment",
            "environment": "playground",
            "pamToken": "playground-token",
        })

        mock_create_pn.assert_called_once()
        factory_call = mock_create_pn.call_args
        assert factory_call.kwargs.get("publish_key") == "pub-c-pg"
        assert factory_call.kwargs.get("subscribe_key") == "sub-c-pg"
        new_mock_pn.set_token.assert_called_with("playground-token")

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_playground_to_network_allowed(
        self, mock_create_pn: MagicMock
    ) -> None:
        """Switching from playground to network (upgrade) is allowed."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        listener = _get_listener(mock_pn)
        mock_create_pn.reset_mock()
        new_mock_pn = _make_mock_pn()
        mock_create_pn.return_value = new_mock_pn

        # Switch from playground to network with pamToken — should succeed
        _send_message(listener, mock_pn, {
            "type": "SwitchEnvironment",
            "environment": "network",
            "pamToken": "network-token",
        })

        # A new PubNub client should have been created
        mock_create_pn.assert_called_once()
        # Token should be applied
        new_mock_pn.set_token.assert_called_with("network-token")

        result["stop"]()


# ===========================================================================
# Issue #8: Race condition resolved by #7
# ===========================================================================


class TestRaceConditionResolvedByPamTokenRequirement:
    """Issue #8: The subscribe-before-token race is resolved by requiring
    pamToken in SwitchEnvironment (no async fallback registration)."""

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_switch_with_token_subscribes_synchronously(
        self, mock_create_pn: MagicMock
    ) -> None:
        """When pamToken is provided, subscribe happens after set_token,
        ensuring no access-denied race."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        listener = _get_listener(mock_pn)

        mock_create_pn.reset_mock()
        new_mock_pn = _make_mock_pn()

        # Track call order
        call_order = []
        original_set_token = new_mock_pn.set_token
        original_subscribe = new_mock_pn.subscribe

        def tracked_set_token(*args, **kwargs):
            call_order.append("set_token")
            return original_set_token(*args, **kwargs)

        def tracked_subscribe(*args, **kwargs):
            call_order.append("subscribe")
            return original_subscribe(*args, **kwargs)

        new_mock_pn.set_token = tracked_set_token
        new_mock_pn.subscribe = tracked_subscribe
        mock_create_pn.return_value = new_mock_pn

        _send_message(listener, mock_pn, {
            "type": "SwitchEnvironment",
            "environment": "network",
            "pamToken": "network-token",
        })

        # set_token must come before subscribe
        assert "set_token" in call_order
        assert "subscribe" in call_order
        assert call_order.index("set_token") < call_order.index("subscribe"), (
            f"set_token must be called before subscribe, got order: {call_order}"
        )

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_old_client_stopped_after_new_client_subscribes(
        self, mock_create_pn: MagicMock
    ) -> None:
        """Old PubNub client must be stopped AFTER the new client subscribes,
        not before. This avoids a gap where no client is listening and
        prevents orphaning if new client creation fails."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        old_mock_pn = _make_mock_pn()
        mock_create_pn.return_value = old_mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        listener = _get_listener(old_mock_pn)

        mock_create_pn.reset_mock()
        new_mock_pn = _make_mock_pn()

        # Track cross-client call ordering
        call_order = []

        original_old_stop = old_mock_pn.stop
        def tracked_old_stop(*args, **kwargs):
            call_order.append("old_stop")
            return original_old_stop(*args, **kwargs)
        old_mock_pn.stop = tracked_old_stop

        original_new_subscribe = new_mock_pn.subscribe
        def tracked_new_subscribe(*args, **kwargs):
            call_order.append("new_subscribe")
            return original_new_subscribe(*args, **kwargs)
        new_mock_pn.subscribe = tracked_new_subscribe

        mock_create_pn.return_value = new_mock_pn

        _send_message(listener, old_mock_pn, {
            "type": "SwitchEnvironment",
            "environment": "network",
            "pamToken": "network-token",
        })

        # Both events must have occurred
        assert "new_subscribe" in call_order, "New client was never subscribed"
        assert "old_stop" in call_order, "Old client was never stopped"
        # Old client stop must come AFTER new client subscribe
        assert call_order.index("new_subscribe") < call_order.index("old_stop"), (
            f"Old client must be stopped after new client subscribes, "
            f"got order: {call_order}"
        )

        result["stop"]()


# ===========================================================================
# Issue #9: TaskClient factory receives active keyset keys
# ===========================================================================


class TestTaskClientKeysetWiring:
    """Issue #9: TaskClient's create_pubnub lambda must pass the
    active keyset's keys to create_pubnub_client."""

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_task_client_factory_uses_active_keyset(
        self, mock_create_pn: MagicMock
    ) -> None:
        """The create_pubnub lambda should use the current keyset keys."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        # Get the task_client and invoke its create_pubnub factory
        task_client = result["task_client"]

        # Reset to capture only the factory call
        mock_create_pn.reset_mock()

        # Trigger internal PubNub creation via _get_pubnub()
        task_client._create_pubnub()

        # Should have been called with playground keys (the initial env)
        mock_create_pn.assert_called_once()
        factory_call = mock_create_pn.call_args
        assert factory_call.kwargs.get("publish_key") == "pub-c-pg"
        assert factory_call.kwargs.get("subscribe_key") == "sub-c-pg"

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_task_client_factory_uses_keys_not_empty_strings(
        self, mock_create_pn: MagicMock
    ) -> None:
        """The factory must NOT pass empty strings for keys (the old bug)."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        task_client = result["task_client"]
        mock_create_pn.reset_mock()
        task_client._create_pubnub()

        factory_call = mock_create_pn.call_args
        assert factory_call.kwargs.get("publish_key") != ""
        assert factory_call.kwargs.get("subscribe_key") != ""

        result["stop"]()

    @patch("blocks_network.agent_instance.create_pubnub_client")
    def test_task_client_factory_tracks_environment_switch(
        self, mock_create_pn: MagicMock
    ) -> None:
        """After environment switch, factory should use the new env's keys."""
        from blocks_network.agent_instance import start_agent_instance
        from blocks_network.types import AgentInstanceOptions

        mock_pn = _make_mock_pn()
        mock_create_pn.return_value = mock_pn

        mock_entry = AgentEntry(
            agent_name="test_agent", name="Test Agent", listing="public", billing_mode="free"
        )
        mock_reg_result = ConnectAgentResult(
            pam_token="test-token",
            control_channel="agent.test-agent-id.control",
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
                return_value=mock_reg_result,
            ),
        ):
            opts = AgentInstanceOptions(
                card=minimal_card(),
                agent_name="test_agent", handler=lambda task, ctx: {}
            )
            result = start_agent_instance(opts)

            # Wait for registration thread
            for t in threading.enumerate():
                if t.name and t.daemon:
                    t.join(timeout=2)
            time.sleep(0.2)

        listener = _get_listener(mock_pn)

        # Switch to network environment
        new_mock_pn = _make_mock_pn()
        mock_create_pn.return_value = new_mock_pn

        _send_message(listener, mock_pn, {
            "type": "SwitchEnvironment",
            "environment": "network",
            "pamToken": "network-token",
        })

        # Reset to capture only the factory call
        mock_create_pn.reset_mock()
        mock_create_pn.return_value = _make_mock_pn()

        # Now call the TaskClient factory -- should use network keys
        task_client = result["task_client"]
        task_client._create_pubnub()

        factory_call = mock_create_pn.call_args
        assert factory_call.kwargs.get("publish_key") == "pub-c-nw", (
            f"Expected network publish key, got {factory_call.kwargs.get('publish_key')}"
        )
        assert factory_call.kwargs.get("subscribe_key") == "sub-c-nw", (
            f"Expected network subscribe key, got {factory_call.kwargs.get('subscribe_key')}"
        )

        result["stop"]()
