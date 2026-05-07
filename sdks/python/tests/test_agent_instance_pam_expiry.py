"""
Tests for PAM token expiry detection in agent instance.

Verifies that PNAccessDeniedCategory status events trigger stop()
on the control client and that duplicate events are deduplicated.
"""

from __future__ import annotations

import time
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.agent_instance import start_agent_instance
from blocks_network.types import AgentInstanceOptions

from tests.conftest import minimal_card


def _make_mock_per_task():
    pn = MagicMock()
    pn.set_token = MagicMock()
    pn.stop = MagicMock()
    pn.publish.return_value = MagicMock()
    return pn


@pytest.fixture(autouse=True)
def _patch_create_pubnub(monkeypatch):
    import blocks_network.agent_instance as _ai_mod
    monkeypatch.setattr(
        _ai_mod,
        "create_pubnub_client",
        lambda **kw: _make_mock_per_task(),
    )


def _wait_for(predicate, timeout_sec=2.0, poll_sec=0.05):
    deadline = time.monotonic() + timeout_sec
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(poll_sec)
    assert predicate(), "Timed out waiting for predicate"


def _simulate_status_event(mock_pn, category_value):
    """Simulate a PubNub status event via the listener."""
    assert len(mock_pn._listeners) > 0
    listener = mock_pn._listeners[0]
    event = MagicMock()
    event.category = category_value
    if hasattr(listener, "status") and callable(listener.status):
        listener.status(mock_pn, event)


class TestPamTokenExpiry:
    def test_calls_stop_on_access_denied(self, mock_pubnub) -> None:
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="test_pam",

            )
        )

        # Wait for registration thread
        _wait_for(lambda: len(mock_pubnub._listeners) > 0)

        # Import the actual enum value
        from pubnub.enums import PNStatusCategory

        _simulate_status_event(mock_pubnub, PNStatusCategory.PNAccessDeniedCategory)

        mock_pubnub.stop.assert_called_once()

        result["stop"]()

    def test_deduplicates_access_denied_events(self, mock_pubnub) -> None:
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="test_pam_dedup",

            )
        )

        _wait_for(lambda: len(mock_pubnub._listeners) > 0)

        from pubnub.enums import PNStatusCategory

        # Fire twice (subscribe + heartbeat both fail with 403)
        _simulate_status_event(mock_pubnub, PNStatusCategory.PNAccessDeniedCategory)
        _simulate_status_event(mock_pubnub, PNStatusCategory.PNAccessDeniedCategory)

        mock_pubnub.stop.assert_called_once()

        result["stop"]()

    def test_ignores_non_access_denied_events(self, mock_pubnub) -> None:
        result = start_agent_instance(
            AgentInstanceOptions(
                card=minimal_card(),
                pubnub=mock_pubnub,
                agent_name="test_pam_ignore",

            )
        )

        _wait_for(lambda: len(mock_pubnub._listeners) > 0)

        from pubnub.enums import PNStatusCategory

        _simulate_status_event(mock_pubnub, PNStatusCategory.PNConnectedCategory)
        _simulate_status_event(mock_pubnub, PNStatusCategory.PNReconnectedCategory)

        mock_pubnub.stop.assert_not_called()

        result["stop"]()
