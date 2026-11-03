"""PubNubRouteClient against a stand-in for the pubnub-python client it wraps."""

from __future__ import annotations

from typing import Any, List, Optional

import pytest

from blocks_network._session.provider_transport import PubNubRouteClient


class _Chain:
    def __init__(self, calls: List[str], name: str, fail: bool = False) -> None:
        self._calls = calls
        self._name = name
        self._fail = fail

    def channels(self, _channels: List[str]) -> "_Chain":
        return self

    def execute(self) -> None:
        self._calls.append(self._name)
        if self._fail:
            raise RuntimeError(f"{self._name} failed")


class _PubNub:
    """Like pubnub-python: removing a listener it does not hold raises ValueError."""

    def __init__(self, failing: Optional[str] = None) -> None:
        self.calls: List[str] = []
        self._listeners: List[Any] = []
        # The one step that raises: "remove_listener", "unsubscribe" or "stop".
        self.failing = failing
        self.before_add_listener = lambda: None

    def add_listener(self, listener: Any) -> None:
        self.before_add_listener()
        self._listeners.append(listener)

    def remove_listener(self, listener: Any) -> None:
        self.calls.append("remove_listener")
        if self.failing == "remove_listener":
            raise RuntimeError("remove_listener failed")
        self._listeners.remove(listener)

    def subscribe(self) -> _Chain:
        return _Chain(self.calls, "subscribe")

    def unsubscribe(self) -> _Chain:
        return _Chain(self.calls, "unsubscribe", fail=self.failing == "unsubscribe")

    def stop(self) -> None:
        self.calls.append("stop")
        if self.failing == "stop":
            raise RuntimeError("stop failed")


def test_a_second_close_still_unsubscribes_and_stops() -> None:
    pubnub = _PubNub()
    client = PubNubRouteClient(pubnub)
    client.listen(lambda *_: None, lambda _category: None)
    client.close("sess.route1.*")
    client.subscribe("sess.route1.*")
    pubnub.calls.clear()

    client.close("sess.route1.*")
    assert pubnub.calls == ["unsubscribe", "stop"]


@pytest.mark.parametrize("failing", ["remove_listener", "unsubscribe", "stop"])
def test_close_runs_every_step_when_one_fails_and_raises_that_failure(failing) -> None:
    pubnub = _PubNub(failing=failing)
    client = PubNubRouteClient(pubnub)
    client.listen(lambda *_: None, lambda _category: None)

    with pytest.raises(RuntimeError, match=f"{failing} failed"):
        client.close("sess.route1.*")
    assert pubnub.calls == ["remove_listener", "unsubscribe", "stop"]


def test_a_close_during_listen_leaves_no_listener_after_the_next_close() -> None:
    pubnub = _PubNub()
    client = PubNubRouteClient(pubnub)
    pubnub.before_add_listener = lambda: client.close("sess.route1.*")
    client.listen(lambda *_: None, lambda _category: None)
    pubnub.before_add_listener = lambda: None

    client.close("sess.route1.*")
    assert pubnub._listeners == []
