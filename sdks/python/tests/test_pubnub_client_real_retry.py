"""Real-pubnub regression: create_pubnub_client must not raise when
subscribe_retry_unbounded is set, and the raised budget must land on
PNConfiguration's flat int attrs as the synchronous SDK reads them
(see Layer 1 header note for why this differs from Node's
RetryPolicy approach).
"""

from __future__ import annotations


def test_create_pubnub_client_with_unbounded_retry_does_not_throw():
    from blocks_network.pubnub_client import create_pubnub_client

    pn = create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="real-retry",
        subscribe_retry_unbounded=True,
    )
    # The synchronous SDK reads these directly off PNConfiguration on
    # every reconnect attempt; the test pins both knobs together so a
    # future pubnub release that splits or renames either of them will
    # surface as a clear failure rather than a silent regression.
    assert pn.config.maximum_reconnection_retries == 43_200
    assert pn.config.maximum_reconnection_interval == 60
    # subscribe_request_timeout intentionally left at the SDK default
    # (310s). Lowering it caused the long-poll to abort every N seconds
    # on healthy clients, which the broker observed as a clean socket
    # close → presence leave/join churn at exactly N-second intervals.
    # See BLOCKS-129 revert commit for evidence and the kept-default rule.
    assert pn.config.subscribe_request_timeout == 310
    # Tear down so the daemon thread doesn't leak.
    pn.stop()


def test_presence_timeout_is_actually_applied_to_pnconfiguration():
    """Regression for BLOCKS-129 follow-up: pnconfig.presence_timeout
    is a @property with no setter. Direct attribute assignment
    silently shadows in __dict__ but the getter still returns
    DEFAULT_PRESENCE_TIMEOUT (300s), causing heartbeats to fire only
    every ~280s. The SDK must use config.set_presence_timeout(N) so
    _presence_timeout is written and _heartbeat_interval derives as
    (timeout/2 - 1).

    AND it must flip enable_presence_heartbeat to True, otherwise
    NativeSubscriptionManager.reconnect() skips _register_heartbeat_timer()
    entirely (pubnub/pubnub.py line 413: gated on
    config.enable_presence_heartbeat is True). Subscribe URL still
    carries heartbeat=N so the broker expects heartbeats; SDK never
    sends them; broker fires Action: timeout at exactly N seconds.

    Pre-fix manifestation (live, 2026-05-07): a healthy agent
    received `Action: timeout` ~20s after each `Action: join` because
    presence_timeout=20 set the announce-interval but
    enable_presence_heartbeat=False kept the heartbeat thread asleep.
    With both flipped, the broker keeps the UUID present indefinitely.
    """
    from blocks_network.pubnub_client import create_pubnub_client

    pn = create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="presence-applied",
        presence_timeout=20,
    )
    # The values that _actually_ control heartbeat behavior:
    assert pn.config.presence_timeout == 20
    # set_presence_timeout derives interval as (timeout / 2) - 1.
    # Type may be float (legacy SDK behavior); compare numerically.
    assert pn.config.heartbeat_interval == 9
    # And it flips heartbeat_default_values to False so the SDK knows
    # the user explicitly chose this value rather than the SDK default.
    assert pn.config.heartbeat_default_values is False
    # Critical: without this flag the heartbeat thread is never spawned
    # by NativeSubscriptionManager.reconnect(), even though the
    # subscribe URL still carries heartbeat=N. Default is False; we
    # must flip it on whenever the caller asks for a presence timeout.
    assert pn.config.enable_presence_heartbeat is True
    pn.stop()


def test_presence_timeout_omitted_leaves_heartbeat_thread_disabled():
    """Per-task / per-stream clients pass presence_timeout=None and
    must NOT flip enable_presence_heartbeat — they should rely on the
    SDK default (False) so no heartbeat HTTPs fire from short-lived
    clients. Pinning the default also catches a future SDK release
    that changes it.
    """
    from blocks_network.pubnub_client import create_pubnub_client

    pn = create_pubnub_client(
        publish_key="pub-test",
        subscribe_key="sub-test",
        user_id="presence-omitted",
    )
    assert pn.config.enable_presence_heartbeat is False
    pn.stop()
