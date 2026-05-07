"""
Shared pytest fixtures for Blocks Network Python SDK tests.

Provides a mock PubNub client that supports the builder/chained-call pattern
used by the PubNub Python SDK, so tests can run without the ``pubnub`` package
installed.
"""

from __future__ import annotations

import os
import sys
from typing import Any, Dict
from unittest.mock import MagicMock

import pytest


# ---------------------------------------------------------------------------
# Minimal agent card helper
# ---------------------------------------------------------------------------
#
# ``AgentInstanceOptions.card`` is required (parity with Node). Tests that
# exercise ``start_agent_instance`` behavior unrelated to a specific card
# shape pass this minimal card so the start path succeeds without inventing
# test-specific fixtures. Tests that need a card with declared streams or
# a specific affinity build their own local helpers (see, for example,
# ``_shared_card`` in ``test_agent_instance_shared_stream.py``).


def minimal_card() -> Dict[str, Any]:
    """Return the smallest valid agent-card dict for unit tests.

    Declares a single ``_default`` outbound byte stream so the SDK's
    card-driven stream contract (affinity lookup, declared-stream key
    resolution) has something to read when a test handler calls
    ``create_stream()`` without extra card configuration.
    """
    return {
        "streams": {
            "_default": {
                "direction": "outbound",
                "format": "bytes",
            }
        }
    }


# ---------------------------------------------------------------------------
# Builder-chain mock helper
# ---------------------------------------------------------------------------

def _make_chain_mock() -> MagicMock:
    """Return a MagicMock whose every attribute call returns itself.

    This supports the PubNub builder pattern:
        pubnub.publish().channel(ch).message(msg).meta(m).should_store(True).use_post(True).sync()

    Every intermediate method returns the *same* mock so the chain does not
    break, and ``.sync()`` returns a ``MagicMock`` result.
    """
    mock = MagicMock()

    # Make every chained call return the same mock so .x().y().z().sync()
    # works regardless of how many methods are in the chain.
    def _self_returning_call(*args, **kwargs):
        return mock

    for method_name in (
        "channel", "channels", "message", "meta", "should_store",
        "use_post", "with_presence", "state", "file_name",
        "file_object", "file_id", "execute",
        "uuid", "include_custom", "include_uuid",
        "include_total_count",
        "include_channel", "limit", "page",
        "channel_memberships",
        "start", "end", "with_timetoken",
    ):
        getattr(mock, method_name).side_effect = _self_returning_call

    # .sync() should return a plain MagicMock (the "result" envelope)
    mock.sync.return_value = MagicMock()

    return mock


# ---------------------------------------------------------------------------
# mock_pubnub fixture
# ---------------------------------------------------------------------------

@pytest.fixture()
def mock_pubnub() -> MagicMock:
    """A fully-stubbed PubNub client compatible with the SDK's builder API.

    Supports:
    - ``publish().channel().message().meta().should_store().use_post().sync()``
    - ``subscribe().channels().with_presence().execute()``
    - ``add_listener()``, ``remove_listener()``
    - ``set_state().channels().state().sync()``
    - ``set_filter_expression()`` and ``config.filter_expression``
    - ``unsubscribe().channels().execute()``
    - ``download_file().channel().file_id().file_name().sync()``
    """
    pn = MagicMock()

    # -- publish chain --
    pn.publish.return_value = _make_chain_mock()

    # -- subscribe chain --
    pn.subscribe.return_value = _make_chain_mock()

    # -- set_state chain --
    pn.set_state.return_value = _make_chain_mock()

    # -- unsubscribe chain --
    pn.unsubscribe.return_value = _make_chain_mock()

    # -- download_file chain --
    download_file_chain = _make_chain_mock()
    download_result = MagicMock()
    download_result.result.data = b"mock-file-content"
    download_file_chain.sync.return_value = download_result
    pn.download_file.return_value = download_file_chain

    # -- add_listener / remove_listener --
    pn._listeners = []

    def _add_listener(listener):
        pn._listeners.append(listener)

    pn.add_listener.side_effect = _add_listener
    pn.remove_listener.side_effect = lambda l: (
        pn._listeners.remove(l) if l in pn._listeners else None
    )

    # -- set_filter_expression --
    pn.set_filter_expression = MagicMock()

    # -- config.filter_expression (fallback path) --
    pn.config = MagicMock()
    pn.config.filter_expression = None

    # -- set_token --
    pn.set_token = MagicMock()

    return pn


# ---------------------------------------------------------------------------
# env_setup fixture
# ---------------------------------------------------------------------------

@pytest.fixture()
def env_setup(monkeypatch: pytest.MonkeyPatch) -> None:
    """Set the required environment variables for Blocks Network config.

    After the test, ``monkeypatch`` automatically restores the original
    environment.

    Note: AGENT_NAME, CONCURRENCY, and EXPECTED_INSTANCES env vars are no
    longer supported by the SDK. Agent name must be passed via options.
    """
    pass


# ---------------------------------------------------------------------------
# blocks_api_key fixture (autouse)
# ---------------------------------------------------------------------------

@pytest.fixture(autouse=True)
def _blocks_api_key(monkeypatch: pytest.MonkeyPatch) -> None:
    """Ensure BLOCKS_API_KEY is set for all tests.

    start_agent_instance() requires this env var. Tests that need to
    verify the missing-key error should explicitly unset it.
    """
    monkeypatch.setenv("BLOCKS_API_KEY", "bk_test_fixture")


# ---------------------------------------------------------------------------
# mock_pubnub_with_objects fixture
# ---------------------------------------------------------------------------

@pytest.fixture()
def mock_pubnub_with_objects(mock_pubnub: MagicMock) -> MagicMock:
    """Extends ``mock_pubnub`` with PubNub Objects API support.

    Adds a callable ``objects()`` that returns a builder with:
    - ``get_channel_members()``, ``get_uuid_metadata()``,
      ``remove_uuid_metadata()``, ``get_memberships()``,
      ``remove_memberships()``, ``set_uuid_metadata()``,
      ``set_memberships()``

    Each returns a chain mock.
    """
    objects_api = MagicMock()

    for method_name in (
        "get_channel_members",
        "get_uuid_metadata",
        "remove_uuid_metadata",
        "get_memberships",
        "remove_memberships",
        "set_uuid_metadata",
        "set_memberships",
    ):
        getattr(objects_api, method_name).return_value = _make_chain_mock()

    mock_pubnub.objects = MagicMock(return_value=objects_api)
    return mock_pubnub


# ---------------------------------------------------------------------------
# tracking_publish fixture
# ---------------------------------------------------------------------------

@pytest.fixture()
def tracking_publish():
    """Reusable publish tracker factory.

    Returns ``(install, records)`` where:
    - ``install(mock_pn)`` patches ``mock_pn.publish`` to record calls.
    - ``records`` is the list of ``{"channel": ..., "message": ..., "meta": ...}``
      dicts populated by publishes.
    """
    records: list = []

    def install(mock_pn: MagicMock) -> None:
        def _tracking():
            chain = MagicMock()
            record: dict = {}

            def _channel(ch):
                record["channel"] = ch
                return chain

            def _message(msg):
                record["message"] = msg
                return chain

            def _meta(m):
                record["meta"] = m
                return chain

            def _should_store(v):
                return chain

            def _use_post(v):
                return chain

            def _sync():
                records.append(dict(record))
                return MagicMock()

            chain.channel = _channel
            chain.message = _message
            chain.meta = _meta
            chain.should_store = _should_store
            chain.use_post = _use_post
            chain.sync = _sync
            return chain

        mock_pn.publish = _tracking

    return install, records
