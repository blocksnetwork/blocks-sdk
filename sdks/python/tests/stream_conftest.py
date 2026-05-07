"""
Shared test fixtures for blocks_network.stream tests.
"""

from __future__ import annotations

import os
from unittest.mock import MagicMock
from typing import Any, Dict, List

import pytest


class MockPublishBuilder:
    """Mock PubNub publish builder chain."""

    def __init__(self, calls: List[Dict[str, Any]]) -> None:
        self._calls = calls
        self._channel: str = ""
        self._message: Any = None
        self._meta: Any = None
        self._store: bool = True
        self._use_post: bool = False

    def channel(self, ch: str) -> "MockPublishBuilder":
        self._channel = ch
        return self

    def message(self, msg: Any) -> "MockPublishBuilder":
        self._message = msg
        return self

    def meta(self, m: Any) -> "MockPublishBuilder":
        self._meta = m
        return self

    def should_store(self, store: bool) -> "MockPublishBuilder":
        self._store = store
        return self

    def use_post(self, flag: bool) -> "MockPublishBuilder":
        self._use_post = flag
        return self

    def sync(self) -> Any:
        self._calls.append(
            {
                "channel": self._channel,
                "message": self._message,
                "meta": self._meta,
                "store_in_history": self._store,
                "use_post": self._use_post,
            }
        )
        return MagicMock()


class MockSubscribeBuilder:
    """Mock PubNub subscribe builder chain."""

    def __init__(self, subscriptions: List[str]) -> None:
        self._subscriptions = subscriptions
        self._channels: List[str] = []

    def channels(self, chs: List[str]) -> "MockSubscribeBuilder":
        self._channels = chs
        return self

    def with_timetoken(self, tt: int) -> "MockSubscribeBuilder":
        return self

    def execute(self) -> None:
        self._subscriptions.extend(self._channels)


class MockUnsubscribeBuilder:
    """Mock PubNub unsubscribe builder chain."""

    def __init__(self) -> None:
        self._channels: List[str] = []

    def channels(self, chs: List[str]) -> "MockUnsubscribeBuilder":
        self._channels = chs
        return self

    def execute(self) -> None:
        pass


class MockHereNowBuilder:
    """Mock PubNub hereNow builder chain."""

    def __init__(self) -> None:
        self._channels: List[str] = []

    def channels(self, chs: List[str]) -> "MockHereNowBuilder":
        self._channels = chs
        return self

    def sync(self) -> Any:
        result = MagicMock()
        result.result.channels = []
        return result


def create_mock_pubnub() -> tuple:
    """Create a mock PubNub client with tracked calls.

    Returns (pubnub, calls, listeners, subscriptions).
    """
    calls: List[Dict[str, Any]] = []
    listeners: List[Any] = []
    subscriptions: List[str] = []

    pubnub = MagicMock()
    pubnub.publish.side_effect = lambda: MockPublishBuilder(calls)
    pubnub.subscribe.side_effect = lambda: MockSubscribeBuilder(subscriptions)
    pubnub.unsubscribe.side_effect = lambda: MockUnsubscribeBuilder()
    pubnub.here_now.side_effect = lambda: MockHereNowBuilder()
    pubnub.add_listener.side_effect = lambda listener: listeners.append(listener)
    pubnub.remove_listener.side_effect = lambda listener: None

    return pubnub, calls, listeners, subscriptions


@pytest.fixture(autouse=True)
def clean_env():
    """Remove Stream SDK env vars before and after each test."""
    env_vars = [
        "STREAM_MAX_MESSAGE_SIZE",
        "STREAM_BUNDLE_SIZE",
        "STREAM_MAX_LATENCY_MS",
        "STREAM_GATING",
    ]
    saved = {}
    for var in env_vars:
        saved[var] = os.environ.pop(var, None)
    yield
    for var in env_vars:
        if saved[var] is not None:
            os.environ[var] = saved[var]
        else:
            os.environ.pop(var, None)
