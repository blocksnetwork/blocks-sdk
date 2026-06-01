"""Lock the package-root re-exports cited by the BillingModeMismatch hint.

The backend hint tells users to call ``get_agent(agent_name).billing_mode``
to recover from a BillingModeMismatch. That recipe assumes both
``get_agent`` and the ``AgentEntry`` type are reachable as
``blocks_network`` package-root attributes. This test fails fast if either
is renamed, dropped, or moved back behind a submodule path so the shipped
hint can't drift from the public surface.
"""

from __future__ import annotations

import blocks_network


def test_get_agent_is_reachable_from_package_root() -> None:
    assert hasattr(blocks_network, "get_agent")
    from blocks_network.agent_registry import get_agent as registry_get_agent

    assert blocks_network.get_agent is registry_get_agent


def test_agent_entry_is_reachable_from_package_root() -> None:
    assert hasattr(blocks_network, "AgentEntry")
    from blocks_network.agent_registry import AgentEntry as registry_agent_entry

    assert blocks_network.AgentEntry is registry_agent_entry


def test_get_agent_and_agent_entry_listed_in_dunder_all() -> None:
    assert "get_agent" in blocks_network.__all__
    assert "AgentEntry" in blocks_network.__all__
