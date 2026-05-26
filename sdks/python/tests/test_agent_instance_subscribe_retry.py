"""Pin: SDK-constructed control client opts into unbounded retry; per-task
clients keep the default. Mirrors the Node SDK test of the same name
(blocks-sdk/sdks/node/tests/agent_instance_subscribe_retry.test.ts).

Structurally distinct from tests/test_agent_instance.py: that file
passes a pre-built mock PubNub via AgentInstanceOptions.pubnub, which
bypasses create_pubnub_client. We need the construction path, so we
patch fetch_cdm_config + get_agent and let the SDK build the client
through the factory.
"""

from __future__ import annotations
from unittest.mock import MagicMock
import pytest


def _make_mock_pn():
    """Stub PubNub instance the SDK can call .set_token / .add_listener
    / .subscribe on without crashing. Same shape as
    test_agent_instance._make_mock_pubnub but inlined here so this file
    is independent of that test module's evolution.
    """
    pn = MagicMock()
    chain = MagicMock()
    for method in (
        "channel", "channels", "message", "meta", "should_store",
        "use_post", "with_presence", "state", "execute",
    ):
        getattr(chain, method).side_effect = lambda *a, _c=chain, **kw: _c
    pn.publish.return_value = chain
    pn.subscribe.return_value = chain
    pn.set_state.return_value = chain
    pn.unsubscribe.return_value = chain
    pn.here_now.return_value = chain
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


def _bootstrap_construction_path(monkeypatch):
    """Apply the minimum stubs that let start_agent_instance reach the
    control-client construction site (line ~428) without hitting any
    real network. Returns the captured_calls list and a stop callable
    for cleanup.
    """
    from blocks_network import agent_instance as ai
    from blocks_network.cdm_config import CdmApiConfig, CdmConfig, CdmKeyset
    from blocks_network.agent_registry import AgentEntry, ConnectAgentResult

    monkeypatch.setenv("BLOCKS_API_KEY", "test-key")

    fake_cdm = CdmConfig(
        playground=CdmKeyset(publish_key="pub-pg", subscribe_key="sub-pg"),
        network=CdmKeyset(publish_key="pub-net", subscribe_key="sub-net"),
        api=CdmApiConfig(base_url="http://test-backend", client_id=None),
    )
    monkeypatch.setattr(ai, "fetch_cdm_config", lambda url=None: fake_cdm)

    fake_agent = AgentEntry(
        agent_name="acme_echo",
        name="acme_echo",
        description="test",
        billing_mode="free",
        listing="public",
    )
    monkeypatch.setattr(
        ai, "get_agent",
        lambda agent_name, base_url=None, api_key=None: fake_agent,
    )

    captured_calls: list[dict] = []
    def fake_create(**kwargs):
        captured_calls.append(kwargs)
        return _make_mock_pn()
    monkeypatch.setattr(ai, "create_pubnub_client", fake_create)

    # The connect-agent thread runs as a daemon AFTER create_pubnub_client
    # returns, so its outcome can't change captured_calls. Stub it to a
    # benign no-op so the daemon thread doesn't spew warning logs.
    fake_connect_result = ConnectAgentResult(
        pam_token=None,
        agent_id="acme_echo",
        control_channel="agent.test.control",
    )
    monkeypatch.setattr(
        "blocks_network.agent_registry.connect_agent",
        lambda agent_name, options: fake_connect_result,
    )

    return captured_calls


def _minimal_card() -> dict:
    return {
        "identity": {
            "agentName": "acme_echo",
            "displayName": "Acme Echo",
            "description": "test",
            "version": "1.0.0",
            "provider": {"organization": "test"},
        },
        "capabilities": {"taskKinds": ["request"]},
        "skills": [],
    }


def _find_control_call(captured_calls):
    """The control-client call uses instance_id as user_id; instance_id
    starts with 'AG-{agent_name}-'. The TaskClient call uses
    f'{instance_id}-taskclient'. Discriminate on the suffix.
    """
    control = [
        c for c in captured_calls
        if c.get("user_id", "").startswith("AG-acme_echo-")
        and not c.get("user_id", "").endswith("-taskclient")
    ]
    assert len(control) >= 1, (
        f"No control-client call captured. captured_calls={captured_calls}"
    )
    return control[0]


def _find_per_task_call(captured_calls):
    per_task = [
        c for c in captured_calls
        if c.get("user_id", "").endswith("-taskclient")
    ]
    return per_task[0] if per_task else None


def test_control_client_opts_into_unbounded_retry(monkeypatch):
    """The control-client call MUST pass subscribe_retry_unbounded=True.
    Per-task clients MUST NOT (or must pass False).
    """
    from blocks_network.agent_instance import start_agent_instance
    from blocks_network.types import AgentInstanceOptions

    captured_calls = _bootstrap_construction_path(monkeypatch)

    result = start_agent_instance(
        AgentInstanceOptions(
            card=_minimal_card(),
            agent_name="acme_echo",
        )
    )
    try:
        control = _find_control_call(captured_calls)
        assert control.get("subscribe_retry_unbounded") is True

        per_task = _find_per_task_call(captured_calls)
        if per_task is not None:
            assert per_task.get("subscribe_retry_unbounded") in (False, None)
    finally:
        result["stop"]()


def test_control_client_wires_on_retry_callback(monkeypatch):
    """The control-client call MUST pass an on_retry callable that emits
    neutral, de-branded transport events through the SDK's structured
    logger. Per-task clients MUST NOT wire on_retry.
    """
    from blocks_network import agent_instance as ai
    from blocks_network.agent_instance import start_agent_instance
    from blocks_network.types import AgentInstanceOptions

    captured_calls = _bootstrap_construction_path(monkeypatch)

    emitted: list[tuple[str, str, dict]] = []

    def _spy(level, message, **kwargs):
        emitted.append((level, message, kwargs))

    monkeypatch.setattr(ai, "log_agent_instance_event", _spy)

    result = start_agent_instance(
        AgentInstanceOptions(
            card=_minimal_card(),
            agent_name="acme_echo",
        )
    )
    try:
        control = _find_control_call(captured_calls)
        on_retry = control.get("on_retry")
        assert callable(on_retry)

        # Snapshot the boot-time emissions so the retry-callback assertions
        # only see what the on_retry dispatch produces.
        emitted.clear()

        on_retry("retry", "reconnect interval increment at: 2026-05-07 14:38:15")
        on_retry("recovered", "reconnection manager stop due success time endpoint call: 2026-05-07 14:39:00")
        on_retry("failed", "Reconnection retry limit reached. Disconnecting.")

        # Exactly three events, in order, with the neutral vocabulary.
        levels_msgs_events = [
            (lv, msg, kw.get("event")) for lv, msg, kw in emitted
        ]
        assert levels_msgs_events == [
            ("warn",  "transport retrying",  "transport_retry"),
            ("info",  "transport recovered", "transport_recovered"),
            ("error", "transport failed",    "transport_failed"),
        ], f"unexpected on_retry emissions: {emitted}"

        # Negative pin: nothing PubNub/PAM-flavored leaks through this
        # surface in *any* emitted entry — message strings, event slugs,
        # or any other kwarg value. A regression that dual-emits a
        # legacy pubnub_transport_* slug alongside the new transport_*
        # slug would surface here.
        for lv, msg, kw in emitted:
            assert "pubnub" not in msg.lower(), msg
            assert "PAM" not in msg, msg
            for key, value in kw.items():
                if isinstance(value, str):
                    assert "pubnub" not in value.lower(), (key, value)
                    assert "PAM" not in value, (key, value)

        per_task = _find_per_task_call(captured_calls)
        if per_task is not None:
            assert per_task.get("on_retry") is None
    finally:
        result["stop"]()
