"""The checkpoint loop on its own: drains run inline, and time moves only on ``advance``."""

from __future__ import annotations

import copy
import json
import threading
from typing import Any, Callable, Dict, List, Optional
from urllib.parse import urlparse

import pytest

from blocks_network.agent_auth import AgentAuth, _HttpError
from blocks_network._session.provider_api import (
    ProviderApiError,
    ProviderSessionApi,
    agent_auth_request,
)
from blocks_network._session.provider_checkpoints import (
    CHECKPOINT_FLUSH_S,
    CHECKPOINT_MAX_EVENTS,
    CheckpointLoop,
)

from tests.session_provider_fakes import FakeScheduler


def _ready(session_id: str) -> Dict[str, Any]:
    return {"type": "session_ready", "sessionId": session_id}


class Loop:
    def __init__(self) -> None:
        self.sent: List[Dict[str, Any]] = []
        self.failures: List[BaseException] = []
        self.lost: List[bool] = []
        self.scheduler = FakeScheduler()
        self.checkpoints = CheckpointLoop(
            route_id="route1",
            post=self._post,
            open_sessions=lambda: ["s1"],
            on_route_lost=lambda: self.lost.append(True),
            scheduler=self.scheduler,
            spawn=lambda drain: drain(),
        )
        self.checkpoints.start()

    def _post(self, batch: Dict[str, Any]) -> None:
        self.sent.append(copy.deepcopy(batch))
        if self.failures:
            raise self.failures.pop(0)

    def tick(self) -> None:
        self.scheduler.advance(CHECKPOINT_FLUSH_S * 1000)

    def session_ids(self) -> List[List[str]]:
        return [[e["sessionId"] for e in batch["events"]] for batch in self.sent]


@pytest.fixture
def loop():
    created = Loop()
    yield created
    created.checkpoints.stop()


def test_sends_an_empty_batch_as_the_heartbeat_on_every_tick(loop: Loop) -> None:
    loop.tick()
    loop.tick()
    assert len(loop.sent) == 2
    for batch in loop.sent:
        assert batch["routeId"] == "route1"
        assert batch["openSessions"] == ["s1"]
        assert batch["events"] == []
        assert batch["probeEchoes"] == []
    assert loop.sent[0]["idempotencyKey"] != loop.sent[1]["idempotencyKey"]


def test_retains_a_failed_batch_and_replays_it_same_key_before_later_ones(loop: Loop) -> None:
    loop.failures.append(RuntimeError("503"))
    loop.checkpoints.record(_ready("a"))
    loop.tick()
    loop.checkpoints.record(_ready("b"))
    loop.tick()

    assert loop.session_ids() == [["a"], ["a"], ["b"]]
    assert loop.sent[1]["idempotencyKey"] == loop.sent[0]["idempotencyKey"]
    assert loop.sent[2]["idempotencyKey"] != loop.sent[0]["idempotencyKey"]


@pytest.mark.parametrize("status", [400, 413])
def test_drops_a_batch_refused_as_unreplayable(loop: Loop, status: int) -> None:
    loop.failures.append(ProviderApiError(status, None, "refused"))
    loop.checkpoints.record(_ready("a"))
    loop.tick()
    loop.checkpoints.record(_ready("b"))
    loop.tick()
    assert loop.session_ids() == [["a"], ["b"]]


def test_flushes_at_the_event_limit_without_waiting_for_the_tick(loop: Loop) -> None:
    for i in range(CHECKPOINT_MAX_EVENTS):
        loop.checkpoints.record(_ready(f"s{i}"))
    assert len(loop.sent) == 1
    assert len(loop.sent[0]["events"]) == CHECKPOINT_MAX_EVENTS


def test_stops_and_reports_route_lost_without_replaying(loop: Loop) -> None:
    loop.failures.append(ProviderApiError(409, "ROUTE_LOST", "lost"))
    loop.checkpoints.record(_ready("a"))
    loop.tick()
    loop.tick()
    assert loop.lost == [True]
    assert len(loop.sent) == 1


def test_echoes_each_probe_nonce_once_and_replays_it_with_its_batch(loop: Loop) -> None:
    loop.failures.append(RuntimeError("503"))
    loop.checkpoints.echo_probe("n-1")
    loop.checkpoints.echo_probe("n-1")
    loop.tick()
    loop.tick()
    loop.tick()
    assert [b["probeEchoes"] for b in loop.sent] == [["n-1"], ["n-1"], []]
    assert loop.sent[1]["idempotencyKey"] == loop.sent[0]["idempotencyKey"]


@pytest.mark.parametrize("nonce", [42, None, "", "n" * 257])
def test_drops_a_nonce_the_backend_would_refuse(loop: Loop, nonce: Any) -> None:
    loop.checkpoints.echo_probe(nonce)
    loop.tick()
    assert [b["probeEchoes"] for b in loop.sent] == [[]]


def test_echoes_a_256_character_nonce_and_at_most_256_in_one_batch(loop: Loop) -> None:
    longest = "n" * 256
    loop.checkpoints.echo_probe(longest)
    for i in range(1, 257):
        loop.checkpoints.echo_probe(f"n{i}")
    loop.tick()
    assert len(loop.sent[0]["probeEchoes"]) == 256
    assert loop.sent[0]["probeEchoes"][0] == longest


def test_an_end_recorded_while_a_batch_is_posting_goes_out_in_the_same_drain() -> None:
    closed = {"type": "session_closed", "sessionId": "a", "reason": "x"}
    sent: List[Dict[str, Any]] = []
    drains: List[Callable[[], None]] = []

    def post(batch: Dict[str, Any]) -> None:
        sent.append(batch)
        if len(sent) == 1:
            checkpoints.record(closed)

    checkpoints = CheckpointLoop(
        route_id="route1",
        post=post,
        open_sessions=lambda: [],
        on_route_lost=lambda: None,
        scheduler=FakeScheduler(),
        spawn=drains.append,
    )
    drained = checkpoints.flush()
    drains.pop()()

    assert drains == []
    assert drained.is_set()
    assert [b["events"] for b in sent] == [[], [closed]]


class _ReleaseHook:
    """A lock that runs ``hook`` once, just after the first release following ``arm()``:
    another thread taking the lock in that gap."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._armed = False
        self.fired = False
        self.hook: Callable[[], None] = lambda: None

    def arm(self) -> None:
        self._armed = True

    def __enter__(self) -> None:
        self._lock.acquire()

    def __exit__(self, *_: Any) -> None:
        self._lock.release()
        if self._armed:
            self._armed = False
            self.fired = True
            self.hook()


def test_an_end_recorded_as_a_drain_finishes_still_goes_out_at_once(monkeypatch) -> None:
    closed = {"type": "session_closed", "sessionId": "a", "reason": "x"}
    sent: List[Dict[str, Any]] = []
    drains: List[Callable[[], None]] = []
    lock = _ReleaseHook()

    def post(batch: Dict[str, Any]) -> None:
        sent.append(batch)
        if len(sent) == 1:
            lock.arm()

    checkpoints = CheckpointLoop(
        route_id="route1",
        post=post,
        open_sessions=lambda: [],
        on_route_lost=lambda: None,
        scheduler=FakeScheduler(),
        spawn=drains.append,
    )
    # The race needs another thread between two critical sections; this lock stands in for it.
    monkeypatch.setattr(checkpoints, "_lock", lock)
    lock.hook = lambda: checkpoints.record(closed)
    checkpoints.flush()
    while drains:
        drains.pop()()

    assert lock.fired
    assert [b["events"] for b in sent] == [[], [closed]]


def test_posts_nothing_until_a_rate_limited_failure_s_retry_after_passes(
    loop: Loop, monkeypatch
) -> None:
    from blocks_network import RateLimitedError
    from blocks_network._session import provider_checkpoints

    monkeypatch.setattr(
        provider_checkpoints.time, "monotonic", lambda: loop.scheduler.now_ms / 1000
    )
    loop.failures.append(RateLimitedError("rate limited", 7.0))
    loop.checkpoints.record(_ready("a"))
    loop.tick()
    assert len(loop.sent) == 1

    loop.tick()
    loop.tick()
    loop.tick()
    assert len(loop.sent) == 1

    loop.tick()
    assert loop.session_ids() == [["a"], ["a"]]
    assert loop.sent[1]["idempotencyKey"] == loop.sent[0]["idempotencyKey"]


INSTANCE = "AG-echo_agent-1"


class _Backend:
    """The backend over HTTP, behind a real ``AgentAuth`` and ``ProviderSessionApi``."""

    def __init__(self) -> None:
        self.agent_deleted = False
        self.stale_jwt = ""
        self.refresh_status = 0
        self.refuses_every_jwt = False
        self.checkpoint_retry_after: Optional[float] = None
        self.issued = 1
        self.calls: List[str] = []

    def request(self, url: str, method: str = "GET", body: Any = None, headers: Any = None):
        path = urlparse(url).path
        self.calls.append(path)
        if path == "/api/v1/auth/agent/connect":
            if self.agent_deleted:
                raise _HttpError(404, {"error": "Agent not found"})
            return self._tokens(), 200
        if path == "/api/v1/auth/agent/refresh":
            if self.refresh_status:
                raise _HttpError(self.refresh_status, {"error": "unavailable"})
            if self.agent_deleted:
                raise _HttpError(401, {"code": "REFRESH_TOKEN_INVALID", "error": "invalid"})
            self.issued += 1
            return self._tokens(), 200
        stale = (headers or {}).get("Authorization") == f"Bearer {self.stale_jwt}"
        if self.agent_deleted or self.refuses_every_jwt or stale:
            raise _HttpError(401, {"error": "Unauthorized"})
        if self.checkpoint_retry_after is not None:
            retry_after, self.checkpoint_retry_after = self.checkpoint_retry_after, None
            raise _HttpError(429, {"code": "RATE_LIMITED", "error": "slow down"}, retry_after)
        batch = json.loads(body)
        return {"routeId": batch["routeId"], "idempotencyKey": batch["idempotencyKey"]}, 200

    def _tokens(self) -> Dict[str, Any]:
        return {"accessToken": f"jwt{self.issued}", "refreshToken": f"r{self.issued}"}


class _OverHttp:
    def __init__(self) -> None:
        self.backend = _Backend()
        auth = AgentAuth(api_key="bk_test", base_url="https://blocks.test")
        auth._request = self.backend.request  # type: ignore[method-assign]
        auth.init({"agentName": "echo_agent", "instanceId": INSTANCE, "billingMode": "free"})
        api = ProviderSessionApi("https://blocks.test", agent_auth_request(auth))
        self.lost: List[bool] = []
        self.scheduler = FakeScheduler()
        self.checkpoints = CheckpointLoop(
            route_id="route1",
            post=api.post_checkpoint,
            open_sessions=lambda: ["s1"],
            on_route_lost=lambda: self.lost.append(True),
            scheduler=self.scheduler,
            spawn=lambda drain: drain(),
        )
        self.checkpoints.start()
        self.backend.calls.clear()

    def ticks(self, count: int) -> None:
        for _ in range(count):
            self.scheduler.advance(CHECKPOINT_FLUSH_S * 1000)


@pytest.fixture
def over_http():
    created = _OverHttp()
    yield created
    created.checkpoints.stop()


def test_a_deleted_agent_ends_the_route_after_one_refused_checkpoint(over_http) -> None:
    backend = over_http.backend
    backend.agent_deleted = True
    over_http.ticks(15)

    assert backend.calls == [
        "/api/v1/provider/checkpoints",
        "/api/v1/auth/agent/refresh",
        "/api/v1/auth/agent/connect",
    ]
    assert over_http.lost == [True]


def test_a_401_that_a_refresh_clears_keeps_the_route(over_http) -> None:
    backend = over_http.backend
    backend.stale_jwt = f"jwt{backend.issued}"
    over_http.ticks(3)

    assert backend.calls[:3] == [
        "/api/v1/provider/checkpoints",
        "/api/v1/auth/agent/refresh",
        "/api/v1/provider/checkpoints",
    ]
    assert backend.calls.count("/api/v1/provider/checkpoints") == 4
    assert over_http.lost == []


@pytest.mark.parametrize("status", [408, 503])
def test_a_refresh_that_fails_transiently_keeps_the_route(over_http, status: int) -> None:
    backend = over_http.backend
    backend.stale_jwt = f"jwt{backend.issued}"
    backend.refresh_status = status
    over_http.ticks(3)

    assert backend.calls.count("/api/v1/provider/checkpoints") == 3
    assert over_http.lost == []


def test_a_rate_limited_refresh_keeps_the_route_and_holds_checkpoints(over_http) -> None:
    backend = over_http.backend
    backend.stale_jwt = f"jwt{backend.issued}"
    backend.refresh_status = 429
    over_http.ticks(3)

    # The first post meets the 429; the next fails fast inside the hold and starts the
    # loop's own wait, so no third post goes out within the 30 s hold.
    assert backend.calls.count("/api/v1/auth/agent/refresh") == 1
    assert backend.calls.count("/api/v1/provider/checkpoints") == 2
    assert over_http.lost == []


def test_posts_no_checkpoint_until_a_direct_429_s_retry_after_passes(
    over_http, monkeypatch
) -> None:
    from blocks_network._session import provider_checkpoints

    monkeypatch.setattr(
        provider_checkpoints.time, "monotonic", lambda: over_http.scheduler.now_ms / 1000
    )
    over_http.backend.checkpoint_retry_after = 20.0
    over_http.ticks(1)
    assert over_http.backend.calls.count("/api/v1/provider/checkpoints") == 1

    over_http.ticks(9)
    assert over_http.backend.calls.count("/api/v1/provider/checkpoints") == 1
    over_http.ticks(1)
    assert over_http.backend.calls.count("/api/v1/provider/checkpoints") == 2
    assert over_http.lost == []


def test_a_checkpoint_still_refused_after_a_successful_refresh_ends_the_route(over_http) -> None:
    over_http.backend.refuses_every_jwt = True
    over_http.ticks(5)

    # The first post and its retry after the refresh, then no more.
    assert over_http.backend.calls.count("/api/v1/provider/checkpoints") == 2
    assert over_http.lost == [True]
