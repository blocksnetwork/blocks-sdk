"""The session provider runtime at its route boundary: the provider API and PubNub are faked."""

from __future__ import annotations

import base64
import contextlib
import threading
from typing import Any, Callable, Dict, List, Optional

import pytest

from blocks_network import RateLimitedError
from blocks_network._session.provider_api import ProviderApiError, RouteToken
from blocks_network._session.provider_checkpoints import CHECKPOINT_FLUSH_S
from blocks_network._session.provider_route import SessionProviderRuntime
from blocks_network._session.provider_session import (
    MAX_PENDING_EXCHANGES,
    MAX_SETTLED_EXCHANGE_IDS,
    SESSION_OPEN_TIMEOUT_S,
)
from blocks_network._session.provider_session import spawn_daemon as real_spawn_daemon  # unpatched
from blocks_network._session.provider_types import SessionHandlers
from blocks_network.config import ARTIFACT_INLINE_LIMIT_BYTES
from blocks_network.protocol_version import CURRENT_PROTOCOL_VERSION

from tests.session_provider_fakes import (
    BACKEND_UUID,
    CONSUMER_UUID,
    INSTANCE_ID,
    ROUTE_CHANNEL,
    FakeApi,
    FakeRouteClient,
    FakeScheduler,
    Gate,
    from_consumer,
    manifest_body,
    route_grant,
    session_channel,
    Activity,
    text_parts,
    wait_for,
    wait_forever,
)

SESSION = "s1"
CHANNEL = session_channel(SESSION)
FLUSH_MS = CHECKPOINT_FLUSH_S * 1000


_forever = wait_forever
_activity: List[Activity] = []


@pytest.fixture(autouse=True)
def activity(monkeypatch):
    """Tracks the runtime's threads and publishes, so ``quiesce`` can wait for them to settle."""
    from blocks_network._session import provider_route, provider_session

    tracker = Activity()
    real_spawn = provider_session.spawn_daemon

    def spawn(fn):
        real_spawn(tracker.tracked(fn))

    real_submit = provider_session._Outbox.submit
    monkeypatch.setattr(provider_session, "spawn_daemon", spawn)
    monkeypatch.setattr(provider_route, "spawn_daemon", spawn)
    monkeypatch.setattr(
        provider_session._Outbox,
        "submit",
        lambda self, job: real_submit(self, tracker.tracked(job)),
    )
    _activity.append(tracker)
    yield tracker
    _activity.remove(tracker)


def quiesce() -> None:
    """Wait until the runtime has nothing left to do, so a test can assert nothing happened."""
    _activity[-1].wait_idle()


def _empty(*_: Any) -> Dict[str, Any]:
    return {}


def _exchange(seq: int, exchange_id: str, text: Optional[str] = None) -> Dict[str, Any]:
    return from_consumer(
        SESSION,
        seq,
        "exchange",
        exchangeId=exchange_id,
        requestParts=text_parts(text or exchange_id),
    )


class Harness:
    def __init__(self, handlers: SessionHandlers, max_sessions: Any = 4, auto_connect: bool = True):
        self.api = FakeApi()
        self.scheduler = FakeScheduler()
        self.clients: List[FakeRouteClient] = []
        # Raised by the next client's creation, or by its subscribe, then cleared.
        self.create_error: Optional[BaseException] = None
        self.subscribe_error: Optional[BaseException] = None
        self.token_error: Optional[BaseException] = None
        self.before_listen: Callable[[], None] = lambda: None
        self.after_listen: Callable[[], None] = lambda: None

        def create_client(user_id: str) -> FakeRouteClient:
            error, self.create_error = self.create_error, None
            if error is not None:
                raise error
            client = FakeRouteClient(user_id, auto_connect=auto_connect)
            client.subscribe_error, self.subscribe_error = self.subscribe_error, None
            client.token_error, self.token_error = self.token_error, None
            client.before_listen = self.before_listen
            client.after_listen = self.after_listen
            self.clients.append(client)
            return client

        self.runtime = SessionProviderRuntime(
            api=self.api,
            instance_id=INSTANCE_ID,
            max_sessions=max_sessions,
            handlers=handlers,
            create_client=create_client,
            scheduler=self.scheduler,
            clock=lambda: self.scheduler.now_ms / 1000,
        )

    @property
    def client(self) -> FakeRouteClient:
        return self.clients[-1]

    def deliver(self, message: Dict[str, Any], publisher: str = CONSUMER_UUID) -> None:
        self.client.deliver(session_channel(message["sessionId"]), publisher, message)

    def open(self, session_id: str = SESSION, **exchange: Any) -> None:
        fields = {"exchange": exchange} if exchange else {}
        self.deliver(from_consumer(session_id, 1, "session_open", **fields))

    def ready(self, count: int = 1) -> None:
        wait_for(lambda: len(self.client.of_type("session_ready")) == count)

    def backend_notice(self, message_type: str, **fields: Any) -> None:
        self.deliver(from_consumer(SESSION, 0, message_type, **fields), BACKEND_UUID)


@pytest.fixture
def start():
    harnesses: List[Harness] = []

    def _start(on_exchange=_empty, **kwargs: Any) -> Harness:
        handler_keys = ("on_session_open", "on_session_close")
        handlers = SessionHandlers(
            on_exchange=on_exchange, **{k: kwargs.pop(k) for k in handler_keys if k in kwargs}
        )
        harness = Harness(handlers, **kwargs)
        harnesses.append(harness)
        harness.runtime.start()
        return harness

    yield _start
    for harness in harnesses:
        harness.runtime.stop()


def test_subscribes_once_to_the_route_channel_as_the_issued_transport_uuid(start) -> None:
    h = start()
    assert h.client.user_id == "uuid-provider"
    assert h.client.tokens == ["token-route1"]
    assert h.client.subscribed == [ROUTE_CHANNEL]


def test_answers_the_inline_exchange_after_session_ready_and_keeps_context_across_turns(
    start,
) -> None:
    turns: Dict[str, int] = {}

    def on_exchange(ctx, exchange):
        turns[ctx.session_id] = turns.get(ctx.session_id, 0) + 1
        text = "".join(part.text for part in exchange.request_parts)
        return {
            "artifacts": [
                {"data": f"turn {turns[ctx.session_id]}: {text}", "mimeType": "text/plain"}
            ]
        }

    h = start(on_exchange, on_session_open=lambda ctx: turns.__setitem__(ctx.session_id, 0))
    h.open(exchangeId="ex1", requestParts=text_parts("hi"))
    wait_for(lambda: len(h.client.outcomes()) == 1)
    h.deliver(_exchange(2, "ex2", "again"))
    wait_for(lambda: len(h.client.outcomes()) == 2)

    sent = h.client.published
    assert [(p.message["type"], p.message["seq"]) for p in sent] == [
        ("session_ready", 1),
        ("exchange_artifact", 2),
        ("exchange_outcome", 3),
        ("exchange_artifact", 4),
        ("exchange_outcome", 5),
    ]
    for p in sent:
        assert p.channel == CHANNEL
        assert p.meta == {"sessionId": SESSION, "protocolVersion": CURRENT_PROTOCOL_VERSION}
        assert p.message["sessionId"] == SESSION
        assert p.message["protocolVersion"] == CURRENT_PROTOCOL_VERSION
        assert isinstance(p.message["ts"], int)
    refs = [m["artifactRef"] for m in h.client.of_type("exchange_artifact")]
    assert all(ref["kind"] == "inline" for ref in refs)
    assert [base64.b64decode(ref["data"]).decode() for ref in refs] == [
        "turn 1: hi",
        "turn 2: again",
    ]
    assert h.client.outcomes() == [("ex1", "completed", None), ("ex2", "completed", None)]


def test_fetches_the_session_when_no_manifest_is_held_and_uses_a_held_one(start) -> None:
    h = start()
    h.open()
    h.ready()
    assert h.api.looked_up == [SESSION]

    h.runtime.accept_manifest({"type": "SessionManifest", **manifest_body("s2")})
    h.open("s2")
    h.ready(2)
    assert h.api.looked_up == [SESSION]


def test_serves_a_session_whose_control_channel_manifest_has_no_org(start) -> None:
    seen: List[Any] = []
    h = start(on_session_open=lambda ctx: seen.append(ctx.org_id))
    h.runtime.accept_manifest({"type": "SessionManifest", **manifest_body(SESSION), "orgId": None})
    h.open()
    h.ready()
    assert seen == [None]
    assert h.api.looked_up == []


def test_drops_session_open_silently_when_the_backend_does_not_know_the_session(start) -> None:
    opened: List[Any] = []
    h = start(on_session_open=opened.append)
    h.api.lookup_answers.append(ProviderApiError(404, "SESSION_NOT_FOUND", "nope"))
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: h.api.looked_up)
    quiesce()
    assert opened == []
    assert h.client.published == []


def test_retries_a_session_lookup_that_fails_transiently(start) -> None:
    h = start()
    h.api.lookup_answers.append(ProviderApiError(503, None, "unavailable"))
    timers = h.scheduler.pending()
    h.open()
    wait_for(lambda: h.scheduler.pending() > timers)
    h.scheduler.advance(250)
    h.ready()
    assert h.api.looked_up == [SESSION, SESSION]


@pytest.mark.parametrize(
    "retry_after,wait_ms", [(20.0, 20_000), (None, 30_000)], ids=["retry-after", "no-hint"]
)
def test_waits_out_a_rate_limited_lookup_without_spending_a_retry(
    start, retry_after: Optional[float], wait_ms: int
) -> None:
    h = start()
    transient = ProviderApiError(503, None, "unavailable")
    rate_limited = ProviderApiError(429, "RATE_LIMITED", "rate limited", retry_after)
    h.api.lookup_answers.extend([transient, rate_limited, transient, transient])
    timers = h.scheduler.pending()
    h.open()
    for looked_up, delay_ms in enumerate((250, wait_ms, 1_000, 3_000), start=1):
        wait_for(lambda: len(h.api.looked_up) == looked_up and h.scheduler.pending() > timers)
        h.scheduler.advance(delay_ms - 1)
        assert len(h.api.looked_up) == looked_up
        h.scheduler.advance(1)
    h.ready()
    assert len(h.api.looked_up) == 5


def test_drops_session_open_when_a_lookup_retry_after_outlasts_the_consumer(start) -> None:
    opened: List[Any] = []
    h = start(on_session_open=opened.append)
    h.api.lookup_answers.append(ProviderApiError(429, "RATE_LIMITED", "rate limited", 120.0))
    h.open()
    wait_for(lambda: h.api.looked_up)
    quiesce()
    h.scheduler.advance(200_000)
    quiesce()
    assert h.api.looked_up == [SESSION]
    assert opened == []
    assert h.client.published == []


def test_admits_no_session_for_a_session_open_from_anyone_but_the_manifest_consumer(start) -> None:
    h = start(max_sessions=1)
    h.client.deliver(session_channel("s1"), "uuid-intruder", from_consumer("s1", 1, "session_open"))
    wait_for(lambda: h.api.looked_up)
    quiesce()
    h.open("s2")
    h.ready()
    assert h.client.of_type("session_ready")[0]["sessionId"] == "s2"
    assert h.client.of_type("session_failed") == []


def test_runs_exchanges_one_at_a_time(start) -> None:
    first = Gate()
    started: List[str] = []

    def on_exchange(_ctx, exchange):
        started.append(exchange.exchange_id)
        if exchange.exchange_id == "ex1":
            first.wait()
        return {}

    h = start(on_exchange)
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: started == ["ex1"])
    h.deliver(_exchange(2, "ex2"))
    quiesce()
    assert started == ["ex1"]

    first.open()
    wait_for(lambda: len(h.client.outcomes()) == 2)
    assert started == ["ex1", "ex2"]


def test_publishes_failed_handler_error_when_the_handler_raises(start) -> None:
    def on_exchange(*_):
        raise RuntimeError("boom")

    h = start(on_exchange)
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: h.client.outcomes() == [("ex1", "failed", "handler_error")])


def test_answers_an_exchange_beyond_the_pending_bound_with_backpressure(start) -> None:
    hold = Gate()
    handled: List[str] = []

    def on_exchange(_ctx, exchange):
        handled.append(exchange.exchange_id)
        hold.wait()
        return {}

    h = start(on_exchange)
    h.open()
    h.ready()
    for i in range(1, MAX_PENDING_EXCHANGES + 2):
        h.deliver(_exchange(i + 1, f"ex{i}"))
    overflow = f"ex{MAX_PENDING_EXCHANGES + 1}"
    wait_for(lambda: h.client.outcomes() == [(overflow, "failed", "session_backpressure")])
    hold.open()
    wait_for(lambda: len(h.client.outcomes()) == MAX_PENDING_EXCHANGES + 1)
    assert overflow not in handled


def test_fails_the_session_once_backpressure_refusals_outrun_their_publishes(start) -> None:
    outcome_gate = Gate()
    h = start(_forever)
    h.open()
    h.ready()

    def hold_outcomes(message):
        if message["type"] == "exchange_outcome":
            outcome_gate.wait()

    h.client.before_publish = hold_outcomes
    flood = MAX_PENDING_EXCHANGES * 2 + 50
    for i in range(1, flood + 1):
        h.deliver(_exchange(i + 1, f"ex{i}"))
    outcome_gate.open()

    wait_for(lambda: len(h.client.of_type("session_failed")) == 1)
    assert h.client.of_type("session_failed")[0]["error"] == "provider_error"
    states = [f"{state}:{error or ''}" for _, state, error in h.client.outcomes()]
    assert states.count("failed:session_backpressure") == MAX_PENDING_EXCHANGES
    assert states.count("canceled:") == MAX_PENDING_EXCHANGES
    assert len(states) == MAX_PENDING_EXCHANGES * 2


class TestAnExchangeResentWithANewSeq:
    def test_does_not_run_again_while_it_is_running(self, start) -> None:
        hold = Gate()
        handled: List[str] = []

        def on_exchange(_ctx, exchange):
            handled.append(exchange.exchange_id)
            hold.wait()
            return {}

        h = start(on_exchange)
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: handled == ["ex1"])
        h.deliver(_exchange(2, "ex1"))
        h.deliver(_exchange(3, "ex2"))
        hold.open()

        wait_for(lambda: len(h.client.outcomes()) == 2)
        assert handled == ["ex1", "ex2"]
        assert [o[0] for o in h.client.outcomes()] == ["ex1", "ex2"]

    def test_does_not_run_again_while_it_is_queued(self, start) -> None:
        hold = Gate()
        handled: List[str] = []

        def on_exchange(_ctx, exchange):
            handled.append(exchange.exchange_id)
            if exchange.exchange_id == "ex1":
                hold.wait()
            return {}

        h = start(on_exchange)
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: handled == ["ex1"])
        h.deliver(_exchange(2, "ex2"))
        h.deliver(_exchange(3, "ex2"))
        h.deliver(_exchange(4, "ex3"))
        hold.open()

        wait_for(lambda: len(h.client.outcomes()) == 3)
        assert handled == ["ex1", "ex2", "ex3"]
        assert [o[0] for o in h.client.outcomes()] == ["ex1", "ex2", "ex3"]

    def test_runs_nothing_and_publishes_nothing_once_it_has_settled(self, start) -> None:
        handled: List[str] = []

        def on_exchange(_ctx, exchange):
            handled.append(exchange.exchange_id)
            return {"artifacts": [{"data": "done", "mimeType": "text/plain"}]}

        h = start(on_exchange)
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: len(h.client.outcomes()) == 1)
        before = len(h.client.published)
        h.deliver(_exchange(2, "ex1"))
        h.deliver(_exchange(3, "ex2"))

        wait_for(lambda: len(h.client.outcomes()) == 2)
        assert handled == ["ex1", "ex2"]
        assert [p.message["exchangeId"] for p in h.client.published[before:]] == ["ex2", "ex2"]

    def test_draws_one_refusal_when_re_sent_while_that_refusal_is_publishing(self, start) -> None:
        outcome_gate = Gate()
        h = start(_forever)
        h.open()
        h.ready()
        for i in range(1, MAX_PENDING_EXCHANGES + 1):
            h.deliver(_exchange(i + 1, f"ex{i}"))

        def hold_outcomes(message):
            if message["type"] == "exchange_outcome":
                outcome_gate.wait()

        h.client.before_publish = hold_outcomes
        overflow = MAX_PENDING_EXCHANGES + 2
        h.deliver(_exchange(overflow, "extra"))
        h.deliver(_exchange(overflow + 1, "extra"))
        outcome_gate.open()

        wait_for(lambda: len(h.client.outcomes()) == 1)
        quiesce()
        assert h.client.outcomes() == [("extra", "failed", "session_backpressure")]

    def test_remembers_a_bounded_number_of_settled_exchanges(self, start) -> None:
        handled: List[str] = []

        def on_exchange(_ctx, exchange):
            handled.append(exchange.exchange_id)
            return {}

        h = start(on_exchange)
        h.open()
        h.ready()
        total = MAX_SETTLED_EXCHANGE_IDS + 1
        for i in range(1, total + 1):
            h.deliver(_exchange(i + 1, f"ex{i}"))
            wait_for(lambda i=i: len(h.client.outcomes()) == i)
        h.deliver(_exchange(total + 2, "ex2"))
        h.deliver(_exchange(total + 3, "ex1"))

        wait_for(lambda: len(h.client.outcomes()) == total + 1)
        assert handled[total:] == ["ex1"]


def test_settles_a_cooperatively_canceled_exchange_as_canceled(start) -> None:
    h = start(lambda _ctx, exchange: exchange.cancel_event.wait(5) and {})
    h.open(exchangeId="ex1", requestParts=[])
    h.ready()
    h.deliver(from_consumer(SESSION, 2, "exchange_cancel", exchangeId="ex1"))
    wait_for(lambda: h.client.outcomes() == [("ex1", "canceled", None)])


def test_settles_canceled_when_the_cancel_arrives_while_an_artifact_is_publishing(start) -> None:
    release = Gate()
    h = start(
        lambda *_: {
            "artifacts": [
                {"data": "one", "mimeType": "text/plain"},
                {"data": "two", "mimeType": "text/plain"},
            ]
        }
    )

    def hold_artifacts(message):
        if message["type"] == "exchange_artifact":
            release.wait()

    h.client.before_publish = hold_artifacts
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: len(h.client.publish_attempts) == 2)
    h.deliver(from_consumer(SESSION, 2, "exchange_cancel", exchangeId="ex1"))
    release.open()

    wait_for(lambda: h.client.outcomes() == [("ex1", "canceled", None)])
    assert len(h.client.of_type("exchange_artifact")) == 1


def test_session_close_cancels_the_exchange_runs_on_session_close_then_publishes_closed(
    start,
) -> None:
    order: List[str] = []
    h = start(_forever, on_session_close=lambda _ctx, reason: order.append(f"close:{reason}"))
    h.client.before_publish = lambda message: order.append(message["type"])
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: "session_ready" in order)
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    wait_for(lambda: "session_closed" in order)

    assert order == ["session_ready", "exchange_outcome", "close:consumer_closed", "session_closed"]
    assert h.client.outcomes() == [("ex1", "canceled", None)]
    assert h.client.of_type("session_closed")[0]["reason"] == "consumer_closed"

    h.deliver(_exchange(3, "ex2"))
    h.deliver(from_consumer(SESSION, 1, "session_open"))
    quiesce()
    assert len(h.client.published) == 3


def test_provider_close_from_the_handler_publishes_provider_closed(start) -> None:
    h = start(lambda ctx, _exchange: ctx.close())
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: h.client.of_type("session_closed"))
    assert h.client.of_type("session_closed")[0]["reason"] == "provider_closed"
    assert h.client.outcomes() == [("ex1", "canceled", None)]


def test_fails_the_session_with_provider_error_when_on_session_open_raises(start) -> None:
    calls: List[str] = []

    def on_session_open(_ctx):
        raise RuntimeError("no")

    h = start(
        lambda *_: calls.append("exchange"),
        on_session_open=on_session_open,
        on_session_close=lambda *_: calls.append("close"),
    )
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: h.client.of_type("session_failed"))
    assert h.client.of_type("session_failed")[0]["error"] == "provider_error"
    assert h.client.of_type("session_ready") == []
    # The inline exchange never started, so the consumer settles it failed/provider_error.
    assert h.client.outcomes() == []
    assert calls == []


def test_fails_a_session_beyond_max_sessions_with_provider_at_capacity(start) -> None:
    h = start(max_sessions=1)
    h.open("s1")
    h.ready()
    h.open("s2")
    wait_for(lambda: h.client.of_type("session_failed"))
    failed = h.client.of_type("session_failed")[0]
    assert (failed["sessionId"], failed["error"]) == ("s2", "provider_at_capacity")
    assert [m["sessionId"] for m in h.client.of_type("session_ready")] == ["s1"]


def test_frees_a_closing_sessions_seat_before_its_slow_on_session_close_returns(start) -> None:
    slow_close = Gate()
    h = start(on_session_close=lambda *_: slow_close.wait(), max_sessions=1)
    h.open("s1")
    h.ready()
    h.deliver(from_consumer("s1", 2, "session_close"))
    h.open("s2")

    h.ready(2)
    assert h.client.of_type("session_failed") == []
    slow_close.open()


def test_on_a_backend_notice_ends_the_session_locally_and_publishes_nothing(start) -> None:
    reasons: List[str] = []
    h = start(_forever, on_session_close=lambda _ctx, reason: reasons.append(reason))
    h.open(exchangeId="ex1", requestParts=[])
    h.ready()
    h.backend_notice("session_closed", reason="idle_timeout")
    wait_for(lambda: reasons == ["idle_timeout"])
    assert [p.message["type"] for p in h.client.published] == ["session_ready"]


def test_a_backend_notice_still_ends_a_closing_session_which_then_publishes_nothing(start) -> None:
    slow_close = Gate()
    h = start(on_session_close=lambda *_: slow_close.wait())
    h.open()
    h.ready()
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    h.backend_notice("session_failed", error="route_lost")
    slow_close.open()
    quiesce()
    assert h.client.of_type("session_closed") == []


def test_drops_a_publish_queued_behind_a_slow_one_once_a_backend_notice_ends_it(start) -> None:
    first_artifact = Gate()
    held: List[bool] = []

    def on_exchange(_ctx, exchange):
        for data in ("one", "two"):
            threading.Thread(
                target=lambda d=data: _ignore_errors(lambda: exchange.publish_artifact(d)),
                daemon=True,
            ).start()
        wait_forever()

    def hold_first_artifact(message):
        if message["type"] == "exchange_artifact" and not held:
            held.append(True)
            first_artifact.wait()

    h = start(on_exchange)
    h.client.before_publish = hold_first_artifact
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: held)
    h.backend_notice("session_failed", error="route_lost")
    first_artifact.open()
    quiesce()
    assert len(h.client.of_type("exchange_artifact")) == 1


def _ignore_errors(fn) -> None:
    with contextlib.suppress(Exception):
        fn()


def test_echoes_a_backend_route_probe_in_the_next_checkpoint_and_no_one_elses(start) -> None:
    h = start()
    probe = lambda nonce: {
        "type": "route_probe",
        "protocolVersion": CURRENT_PROTOCOL_VERSION,
        "nonce": nonce,
    }  # noqa: E731
    h.client.deliver("sess.route1.probe", BACKEND_UUID, probe("n-backend"))
    h.client.deliver("sess.route1.probe", CONSUMER_UUID, probe("n-consumer"))
    wait_for(lambda: h.api.checkpoints)
    h.scheduler.advance(FLUSH_MS)

    wait_for(lambda: h.api.probe_echoes() == ["n-backend"])
    assert h.api.looked_up == []
    assert h.client.published == []


def test_checkpoints_a_session_end_at_once_without_waiting_for_a_tick(start) -> None:
    h = start()
    h.open()
    h.ready()
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    wait_for(lambda: any(e["type"] == "session_closed" for e in h.api.events()))


def test_checkpoints_lifecycle_facts_and_no_artifact_data(start) -> None:
    h = start(lambda *_: {"artifacts": [{"data": "small", "mimeType": "text/plain"}]})
    h.open(exchangeId="ex1", requestParts=text_parts("secret input"))
    wait_for(lambda: len(h.client.outcomes()) == 1)
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    wait_for(lambda: h.client.of_type("session_closed"))

    wait_for(lambda: any(e["type"] == "session_closed" for e in h.api.events()))
    batches = [b for b in h.api.checkpoints if b["events"]]
    assert all(b["routeId"] == "route1" and b["probeEchoes"] == [] for b in batches)
    assert batches[-1]["openSessions"] == []
    assert h.api.events() == [
        {"type": "session_ready", "sessionId": SESSION},
        {
            "type": "exchange_outcome",
            "sessionId": SESSION,
            "exchangeId": "ex1",
            "state": "completed",
            "artifactRefs": [],
        },
        {"type": "session_closed", "sessionId": SESSION, "reason": "consumer_closed"},
    ]


def test_fails_the_exchange_for_an_artifact_over_the_inline_limit(start) -> None:
    big = b"\x01" * (ARTIFACT_INLINE_LIMIT_BYTES + 1)
    h = start(lambda *_: {"artifacts": [{"data": big}]})
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: h.client.outcomes() == [("ex1", "failed", "handler_error")])
    assert h.client.of_type("exchange_artifact") == []


def test_defaults_a_mid_exchange_artifact_mime_type(start) -> None:
    h = start(lambda _ctx, exchange: exchange.publish_artifact("x", mime_type=None))
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: len(h.client.outcomes()) == 1)
    ref = h.client.of_type("exchange_artifact")[0]["artifactRef"]
    assert ref["mimeType"] == "application/octet-stream"


def test_on_route_lost_ends_the_route_sessions_locally_and_registers_a_new_route(start) -> None:
    reasons: List[str] = []
    h = start(_forever, on_session_close=lambda _ctx, reason: reasons.append(reason))
    h.open(exchangeId="ex1", requestParts=[])
    h.ready()
    wait_for(lambda: h.api.checkpoints)

    h.api.register_answers.append(route_grant("route2"))
    h.api.checkpoint_answers.append(ProviderApiError(409, "ROUTE_LOST", "lost"))
    h.scheduler.advance(FLUSH_MS)

    wait_for(lambda: len(h.clients) == 2)
    wait_for(lambda: reasons == ["route_lost"])
    assert h.clients[0].closed
    assert h.clients[1].subscribed == ["sess.route2.*"]
    assert h.clients[0].of_type("exchange_outcome") == []


@pytest.mark.parametrize("requested,sent", [(0, 1), (5000, 256), (float("nan"), 1), (3.7, 3)])
def test_registers_max_sessions_clamped_to_the_backend_range(start, requested, sent) -> None:
    h = start(max_sessions=requested)
    assert h.api.registered == [(INSTANCE_ID, sent)]


class TestRouteReadiness:
    def test_sends_no_heartbeat_until_subscribed_then_one_at_once(self, start) -> None:
        h = start(auto_connect=False)
        h.client.status("network_down")
        h.scheduler.advance(FLUSH_MS * 3)
        quiesce()
        assert h.api.checkpoints == []

        h.client.connect()
        wait_for(lambda: len(h.api.checkpoints) == 1)

    def test_replaces_a_route_that_does_not_subscribe_in_time_backing_off(self, start) -> None:
        h = start(auto_connect=False)
        # Connect timeout (10 s), then the backoff: 1 s, 2 s, 4 s between registrations.
        h.scheduler.advance(10_000)
        assert h.clients[0].closed
        assert len(h.api.registered) == 1
        h.scheduler.advance(1_000)
        assert len(h.api.registered) == 2
        h.scheduler.advance(10_000 + 2_000)
        assert len(h.api.registered) == 3
        h.scheduler.advance(10_000 + 2_000)
        assert len(h.api.registered) == 3
        assert h.api.checkpoints == []


@pytest.fixture
def short_ttl_start():
    """A route whose token has a one-minute TTL."""
    harnesses: List[Harness] = []

    def _start() -> Harness:
        harness = Harness(SessionHandlers(on_exchange=_empty), max_sessions=1)
        harness.api.register_answers.append(route_grant(ttl_minutes=1))
        harnesses.append(harness)
        harness.runtime.start()
        return harness

    yield _start
    for harness in harnesses:
        harness.runtime.stop()


@pytest.mark.parametrize("status,registrations", [(403, 2), (404, 2), (429, 1)])
def test_on_a_route_token_refresh_refused_registers_a_new_route(
    short_ttl_start, status, registrations
) -> None:
    h = short_ttl_start()
    h.api.refresh_answers.append(ProviderApiError(status, None, "refused"))
    h.api.refresh_default = RouteToken("fresh", 60)
    # 80% of the 1-minute TTL, then the first retry backoff (5 s) for a transient answer.
    h.scheduler.advance(48_000 + 5_000)
    wait_for(lambda: len(h.api.registered) == registrations)
    if registrations == 1:
        assert len(h.api.refreshed) == 2


def test_replaces_the_route_when_a_token_refresh_answers_route_lost(short_ttl_start) -> None:
    h = short_ttl_start()
    h.api.refresh_answers.append(ProviderApiError(409, "ROUTE_LOST", "lost"))
    h.scheduler.advance(60_000)
    assert h.api.refreshed == ["route1"]
    assert len(h.api.registered) == 2
    assert h.clients[0].closed
    assert len(h.clients) == 2


def test_backs_off_between_failed_token_refreshes(short_ttl_start) -> None:
    h = short_ttl_start()
    h.api.refresh_default = RuntimeError("503")
    h.scheduler.advance(48_000)
    assert len(h.api.refreshed) == 1
    h.scheduler.advance(5_000)
    assert len(h.api.refreshed) == 2
    h.scheduler.advance(5_000)
    assert len(h.api.refreshed) == 2
    h.scheduler.advance(5_000)
    assert len(h.api.refreshed) == 3


RATE_LIMITED_FAILURES = [
    pytest.param(lambda wait: RateLimitedError("rate limited", wait), id="rate-limited-credential"),
    pytest.param(
        lambda wait: ProviderApiError(429, "RATE_LIMITED", "rate limited", wait), id="direct-429"
    ),
]


@pytest.mark.parametrize("failure", RATE_LIMITED_FAILURES)
def test_waits_out_a_rate_limited_token_refresh_retry_after(short_ttl_start, failure) -> None:
    h = short_ttl_start()
    h.api.refresh_answers.append(failure(30.0))
    h.api.refresh_default = RouteToken("fresh", 60)
    h.scheduler.advance(48_000)
    assert len(h.api.refreshed) == 1
    # The 5 s backoff alone would retry here.
    h.scheduler.advance(29_000)
    assert len(h.api.refreshed) == 1
    h.scheduler.advance(1_000)
    assert len(h.api.refreshed) == 2
    assert len(h.api.registered) == 1


@pytest.mark.parametrize("failure", RATE_LIMITED_FAILURES)
def test_waits_out_a_rate_limited_route_registration_retry_after(failure) -> None:
    h = Harness(SessionHandlers(on_exchange=_empty))
    h.api.register_answers.append(failure(20.0))
    h.runtime.start()
    try:
        assert len(h.api.registered) == 1
        h.scheduler.advance(19_000)
        assert len(h.api.registered) == 1
        h.scheduler.advance(1_000)
        assert len(h.api.registered) == 2
    finally:
        h.runtime.stop()


class TestOnARouteTokenDenial:
    def test_refreshes_once_and_resumes_the_same_route(self, start) -> None:
        h = start()
        h.api.refresh_answers.append(RouteToken("fresh", 60))
        h.client.status("access_denied")
        wait_for(lambda: h.client.resubscribes == 1)
        assert h.client.tokens[-1] == "fresh"
        assert h.client.active == {ROUTE_CHANNEL}
        assert len(h.api.registered) == 1
        assert len(h.clients) == 1

    def test_replaces_the_route_when_denied_again_before_reconnecting(self, start) -> None:
        h = start()
        h.api.refresh_default = RouteToken("fresh", 60)
        first = h.client
        first.status("access_denied")
        wait_for(lambda: first.resubscribes == 1)
        quiesce()
        first.status("access_denied")

        wait_for(lambda: len(h.api.registered) == 2)
        assert len(h.api.refreshed) == 1
        wait_for(lambda: first.closed)

    def test_refreshes_again_on_a_denial_after_the_route_reconnected(self, start) -> None:
        h = start()
        h.api.refresh_default = RouteToken("fresh", 60)
        h.client.status("access_denied")
        wait_for(lambda: h.client.resubscribes == 1)
        quiesce()
        h.client.status("connected")
        h.client.status("access_denied")

        wait_for(lambda: h.client.resubscribes == 2)
        assert len(h.api.registered) == 1

    def test_replaces_the_route_when_the_refresh_fails(self, start) -> None:
        reasons: List[str] = []
        h = start(on_session_close=lambda _ctx, reason: reasons.append(reason))
        h.open()
        h.ready()
        h.api.refresh_answers.append(ProviderApiError(403, None, "revoked"))
        first = h.client
        first.status("access_denied")

        wait_for(lambda: len(h.api.registered) == 2)
        assert first.closed
        wait_for(lambda: reasons == ["route_lost"])


class TestWhenAPublishFails:
    @staticmethod
    def _failing(client: FakeRouteClient, message_type: str, times: int) -> None:
        left = [times]

        def fail(message):
            if message["type"] == message_type and left[0] > 0:
                left[0] -= 1
                raise RuntimeError("publish 503")

        client.before_publish = fail

    @staticmethod
    def _reply(*_: Any) -> Dict[str, Any]:
        return {"artifacts": [{"data": "hi", "mimeType": "text/plain"}]}

    def test_retries_once_and_still_delivers(self, start) -> None:
        h = start(self._reply)
        self._failing(h.client, "exchange_artifact", 1)
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: h.client.outcomes() == [("ex1", "completed", None)])
        assert len(h.client.of_type("exchange_artifact")) == 1

    def test_fails_the_exchange_when_its_artifact_cannot_be_published(self, start) -> None:
        h = start(self._reply)
        self._failing(h.client, "exchange_artifact", 2)
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: h.client.outcomes() == [("ex1", "failed", "handler_error")])

    def test_fails_the_session_recording_no_outcome_when_one_cannot_be_published(
        self, start
    ) -> None:
        h = start()
        self._failing(h.client, "exchange_outcome", 2)
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: h.client.of_type("session_failed"))
        assert h.client.of_type("session_failed")[0]["error"] == "provider_error"

        wait_for(lambda: any(e["type"] == "session_failed" for e in h.api.events()))
        assert {"type": "session_failed", "sessionId": SESSION, "error": "provider_error"} in (
            h.api.events()
        )
        assert "exchange_outcome" not in [e["type"] for e in h.api.events()]

    def test_fails_the_session_when_session_ready_cannot_be_published(self, start) -> None:
        handled: List[Any] = []
        h = start(lambda *args: handled.append(args))
        self._failing(h.client, "session_ready", 2)
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: h.client.of_type("session_failed"))
        assert h.client.of_type("session_failed")[0]["error"] == "provider_error"
        assert handled == []


def test_defers_a_close_that_arrives_during_on_session_open_until_it_returns(start) -> None:
    opening = Gate()
    order: List[str] = []
    h = start(
        on_session_open=lambda _ctx: opening.wait(),
        on_session_close=lambda _ctx, reason: order.append(f"close:{reason}"),
    )
    h.client.before_publish = lambda message: order.append(message["type"])
    h.open()
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    quiesce()
    assert order == []

    opening.open()
    wait_for(lambda: "session_closed" in order)
    assert order == ["close:consumer_closed", "session_closed"]


def test_fails_a_session_whose_on_session_open_never_returns_freeing_its_seat(start) -> None:
    opening = Gate()
    entered: List[str] = []
    closed: List[tuple] = []

    def on_session_open(ctx):
        entered.append(ctx.session_id)
        if ctx.session_id == "s1":
            opening.wait()

    h = start(
        on_session_open=on_session_open,
        on_session_close=lambda ctx, reason: closed.append((ctx.session_id, reason)),
        max_sessions=1,
    )
    h.open("s1")
    h.deliver(from_consumer("s1", 2, "session_close"))
    wait_for(lambda: entered == ["s1"])

    h.scheduler.advance(SESSION_OPEN_TIMEOUT_S * 1000)
    wait_for(lambda: h.client.of_type("session_failed"))
    failed = h.client.of_type("session_failed")[0]
    assert (failed["sessionId"], failed["error"]) == ("s1", "provider_error")
    assert closed == []
    h.open("s2")
    h.ready()
    assert [m["sessionId"] for m in h.client.of_type("session_ready")] == ["s2"]

    opening.open()
    wait_for(lambda: closed == [("s1", "provider_error")])
    assert h.client.of_type("session_closed") == []


def test_still_runs_on_session_close_when_a_notice_ends_a_session_whose_close_was_deferred(
    start,
) -> None:
    opening = Gate()
    reasons: List[str] = []
    h = start(
        on_session_open=lambda _ctx: opening.wait(),
        on_session_close=lambda _ctx, reason: reasons.append(reason),
    )
    h.open()
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    h.backend_notice("session_failed", error="route_lost")
    opening.open()

    wait_for(lambda: reasons == ["route_lost"])
    quiesce()
    assert reasons == ["route_lost"]
    assert h.client.of_type("session_closed") == []


def test_an_open_timeout_firing_as_on_session_open_returns_fails_nothing(start) -> None:
    opening = Gate()
    entered: List[str] = []

    def on_session_open(ctx):
        entered.append(ctx.session_id)
        opening.wait()

    h = start(on_session_open=on_session_open)
    h.open()
    wait_for(lambda: entered)
    open_timeout = h.scheduler.last()
    opening.open()
    h.ready()

    open_timeout.callback()
    quiesce()
    assert h.client.of_type("session_failed") == []


def test_records_an_outcome_published_before_a_close_ahead_of_the_close(start) -> None:
    outcome_gate = Gate()
    h = start()

    def hold_outcome(message):
        if message["type"] == "exchange_outcome":
            outcome_gate.wait()

    h.client.before_publish = hold_outcome
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: any(m["type"] == "exchange_outcome" for m in h.client.publish_attempts))
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    outcome_gate.open()

    wait_for(lambda: any(e["type"] == "session_closed" for e in h.api.events()))
    assert [(e["type"], e.get("state")) for e in h.api.events()] == [
        ("session_ready", None),
        ("exchange_outcome", "completed"),
        ("session_closed", None),
    ]


def test_runs_no_handler_for_an_exchange_a_close_settled_before_its_worker_started(
    start, monkeypatch
) -> None:
    from blocks_network._session.provider_session import ProviderSession

    worker_started = Gate()
    workers: List[str] = []
    run_then_pump = ProviderSession._run_then_pump

    def held_worker(self, running, request_parts):
        workers.append(running.exchange_id)
        worker_started.wait()
        run_then_pump(self, running, request_parts)

    monkeypatch.setattr(ProviderSession, "_run_then_pump", held_worker)
    handled: List[str] = []
    h = start(lambda _ctx, exchange: handled.append(exchange.exchange_id))
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: workers == ["ex1"])
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    wait_for(lambda: h.client.of_type("session_closed"))

    worker_started.open()
    quiesce()
    assert handled == []
    assert h.client.outcomes() == [("ex1", "canceled", None)]


def test_fails_a_closing_session_whose_canceled_outcome_cannot_be_published(start) -> None:
    h = start(_forever)

    def fail_outcomes(message):
        if message["type"] == "exchange_outcome":
            raise RuntimeError("publish 503")

    h.client.before_publish = fail_outcomes
    h.open(exchangeId="ex1", requestParts=[])
    h.ready()
    h.deliver(from_consumer(SESSION, 2, "session_close"))

    wait_for(lambda: h.client.of_type("session_failed") or h.client.of_type("session_closed"))
    assert h.client.of_type("session_closed") == []
    assert h.client.of_type("session_failed")[0]["error"] == "provider_error"


def test_replaces_the_route_when_resubscribing_after_a_denial_refresh_raises(start) -> None:
    h = start()
    h.api.refresh_answers.append(RouteToken("fresh", 60))
    first = h.client
    first.subscribe_error = RuntimeError("subscribe failed")
    first.status("access_denied")

    wait_for(lambda: len(h.api.registered) == 2)
    assert first.closed


def test_fails_a_closing_session_whose_in_flight_outcome_then_fails_to_publish(start) -> None:
    release = Gate()
    h = start()

    def hold_then_fail_outcome(message):
        if message["type"] == "exchange_outcome":
            release.wait()
            raise RuntimeError("publish 503")

    h.client.before_publish = hold_then_fail_outcome
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: any(m["type"] == "exchange_outcome" for m in h.client.publish_attempts))
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    release.open()

    wait_for(lambda: h.client.of_type("session_failed") or h.client.of_type("session_closed"))
    assert h.client.of_type("session_closed") == []
    assert h.client.of_type("session_failed")[0]["error"] == "provider_error"


@pytest.mark.parametrize("seq", [0, -1, "1", None])
def test_admits_no_session_for_a_session_open_with_an_invalid_seq(start, seq) -> None:
    h = start(max_sessions=1)
    h.deliver(from_consumer("s1", seq, "session_open"))
    wait_for(lambda: h.api.looked_up)
    quiesce()
    h.open("s2")
    h.ready()
    assert [m["sessionId"] for m in h.client.of_type("session_ready")] == ["s2"]
    assert h.client.of_type("session_failed") == []


def test_fails_a_close_when_an_outcome_failed_after_the_close_began(start, monkeypatch) -> None:
    from blocks_network._session.provider_session import ProviderSession

    close_started = Gate()
    closes: List[str] = []
    complete_close = ProviderSession._complete_close

    def held_close(self, reason):
        closes.append(reason)
        close_started.wait()
        complete_close(self, reason)

    monkeypatch.setattr(ProviderSession, "_complete_close", held_close)
    release = Gate()

    def hold_then_fail_outcome(message):
        if message["type"] == "exchange_outcome":
            release.wait()
            raise RuntimeError("publish 503")

    h = start()
    h.client.before_publish = hold_then_fail_outcome
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: any(m["type"] == "exchange_outcome" for m in h.client.publish_attempts))
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    wait_for(lambda: closes)
    release.open()
    quiesce()

    close_started.open()
    wait_for(lambda: h.client.of_type("session_failed") or h.client.of_type("session_closed"))
    assert h.client.of_type("session_closed") == []
    assert h.client.of_type("session_failed")[0]["error"] == "provider_error"


def test_does_not_retry_a_publish_once_a_backend_notice_ends_the_session(start) -> None:
    release = Gate()
    reasons: List[str] = []
    h = start(on_session_close=lambda _ctx, reason: reasons.append(reason))

    def hold_then_fail_outcome(message):
        if message["type"] == "exchange_outcome":
            release.wait()
            raise RuntimeError("publish 503")

    h.client.before_publish = hold_then_fail_outcome
    h.open(exchangeId="ex1", requestParts=[])
    wait_for(lambda: any(m["type"] == "exchange_outcome" for m in h.client.publish_attempts))
    h.backend_notice("session_failed", error="route_lost")
    wait_for(lambda: reasons == ["route_lost"])
    release.open()
    quiesce()

    attempts = [m for m in h.client.publish_attempts if m["type"] == "exchange_outcome"]
    assert len(attempts) == 1


class TestARouteThatCannotStart:
    def test_retries_when_the_route_client_cannot_be_created(self) -> None:
        h = Harness(SessionHandlers(on_exchange=_empty))
        h.create_error = ValueError("subscribe_key is required")
        h.runtime.start()
        assert h.clients == []

        h.scheduler.advance(1_000)
        assert len(h.api.registered) == 2
        assert h.client.subscribed == [ROUTE_CHANNEL]
        h.runtime.stop()

    def test_retries_when_subscribing_raises(self) -> None:
        h = Harness(SessionHandlers(on_exchange=_empty))
        h.subscribe_error = RuntimeError("subscribe failed")
        h.runtime.start()
        assert h.clients[0].closed

        h.scheduler.advance(1_000)
        assert len(h.api.registered) == 2
        assert h.client.subscribed == [ROUTE_CHANNEL]
        h.runtime.stop()


class TestReportStatus:
    @staticmethod
    def _progress(h: Harness) -> List[str]:
        return [m["message"] for m in h.client.of_type("exchange_progress")]

    def test_publishes_the_first_status_at_once(self, start) -> None:
        hold = Gate()

        def on_exchange(_ctx, exchange):
            exchange.report_status("working")
            hold.wait()

        h = start(on_exchange)
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: self._progress(h) == ["working"])
        hold.open()

    def test_coalesces_statuses_within_a_second_to_the_latest(self, start) -> None:
        reported = Gate()
        hold = Gate()

        def on_exchange(_ctx, exchange):
            for message in ("one", "two", "three"):
                exchange.report_status(message)
            reported.open()
            hold.wait()

        h = start(on_exchange)
        h.open(exchangeId="ex1", requestParts=[])
        reported.wait()
        wait_for(lambda: self._progress(h) == ["one"])

        h.scheduler.advance(1_000)
        wait_for(lambda: self._progress(h) == ["one", "three"])
        hold.open()

    def test_publishes_a_pending_status_before_the_completed_outcome(self, start) -> None:
        def on_exchange(_ctx, exchange):
            exchange.report_status("one")
            exchange.report_status("two")
            return {}

        h = start(on_exchange)
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: h.client.outcomes())
        types = [p.message["type"] for p in h.client.published]
        assert types == [
            "session_ready",
            "exchange_progress",
            "exchange_progress",
            "exchange_outcome",
        ]
        assert self._progress(h) == ["one", "two"]
        assert h.client.outcomes() == [("ex1", "completed", None)]

    @pytest.mark.parametrize("ending", ["exchange_cancel", "session_close"])
    def test_drops_a_pending_status_when_the_exchange_is_canceled(self, start, ending) -> None:
        reported = Gate()
        closing = Gate()

        def on_exchange(_ctx, exchange):
            if exchange.exchange_id != "ex1":
                return {}
            exchange.report_status("one")
            exchange.report_status("two")
            reported.open()
            exchange.cancel_event.wait(5)

        h = start(on_exchange, on_session_close=lambda *_: closing.wait())
        h.open(exchangeId="ex1", requestParts=[])
        reported.wait()
        fields = {"exchangeId": "ex1"} if ending == "exchange_cancel" else {}
        h.deliver(from_consumer(SESSION, 2, ending, **fields))
        wait_for(lambda: h.client.outcomes())
        # While the session is still open, so a status left pending would publish.
        h.scheduler.advance(1_000)
        if ending == "exchange_cancel":
            h.deliver(_exchange(3, "ex2"))
            wait_for(lambda: len(h.client.outcomes()) == 2)
        else:
            closing.open()
            wait_for(lambda: h.client.of_type("session_closed"))

        assert self._progress(h) == ["one"]
        assert h.client.outcomes()[0] == ("ex1", "canceled", None)

    def test_publishes_nothing_reported_after_the_exchange_settles(self, start) -> None:
        exchanges: List[Any] = []
        h = start(lambda _ctx, exchange: exchanges.append(exchange))
        h.open(exchangeId="ex1", requestParts=[])
        wait_for(lambda: h.client.outcomes())

        exchanges[0].report_status("late")
        h.scheduler.advance(1_000)
        # The outbox is FIFO, so a late status would publish before this outcome.
        h.deliver(_exchange(2, "ex2"))
        wait_for(lambda: len(h.client.outcomes()) == 2)
        assert self._progress(h) == []


def test_a_notice_during_a_close_reports_the_notice_reason(start, monkeypatch) -> None:
    from blocks_network._session.provider_session import ProviderSession

    reasons: List[str] = []
    notice_handled = Gate()
    close_done = threading.Event()
    after_local_end = ProviderSession._after_local_end
    complete_close = ProviderSession._complete_close

    def held_local_end(self, reason, state):
        notice_handled.wait()
        after_local_end(self, reason, state)

    def tracked_close(self, reason):
        complete_close(self, reason)
        close_done.set()

    monkeypatch.setattr(ProviderSession, "_after_local_end", held_local_end)
    monkeypatch.setattr(ProviderSession, "_complete_close", tracked_close)
    release = Gate()

    def hold_then_fail_outcome(message):
        if message["type"] == "exchange_outcome":
            release.wait()
            raise RuntimeError("publish 503")

    h = start(_forever, on_session_close=lambda _ctx, reason: reasons.append(reason))
    h.client.before_publish = hold_then_fail_outcome
    h.open(exchangeId="ex1", requestParts=[])
    h.ready()
    h.deliver(from_consumer(SESSION, 2, "session_close"))
    wait_for(lambda: any(m["type"] == "exchange_outcome" for m in h.client.publish_attempts))
    h.backend_notice("session_failed", error="route_lost")
    release.open()
    assert close_done.wait(3)
    notice_handled.open()

    wait_for(lambda: reasons)
    assert reasons == ["route_lost"]


def test_runs_no_on_session_open_for_a_session_a_notice_ended_first(start, monkeypatch) -> None:
    from blocks_network._session.provider_session import ProviderSession

    open_started = Gate()
    opens: List[str] = []
    real_open = ProviderSession._open

    def held_open(self):
        opens.append(self.session_id)
        open_started.wait()
        real_open(self)

    monkeypatch.setattr(ProviderSession, "_open", held_open)
    opened: List[str] = []
    h = start(on_session_open=lambda ctx: opened.append(ctx.session_id))
    h.open()
    wait_for(lambda: opens)
    h.backend_notice("session_failed", error="route_lost")
    quiesce()

    open_started.open()
    quiesce()
    assert opened == []
    assert h.client.published == []


def test_replaces_the_route_when_denied_again_while_resubscribing(start) -> None:
    h = start()
    h.api.refresh_answers.append(RouteToken("fresh", 60))
    first = h.client
    first.before_subscribe = lambda: first.status("access_denied")
    first.status("access_denied")

    wait_for(lambda: len(h.api.registered) == 2)
    assert len(h.api.refreshed) == 1
    wait_for(lambda: first.closed)


def test_a_worker_that_raises_is_logged(caplog) -> None:
    raised = threading.Event()

    def fail() -> None:
        raised.set()
        raise RuntimeError("boom")

    with caplog.at_level("ERROR", logger="blocks_network._session.provider_session"):
        real_spawn_daemon(fail)
        assert raised.wait(3)
        wait_for(lambda: any("fail raised" in r.getMessage() for r in caplog.records))


@pytest.mark.parametrize("hook", ["before_listen", "after_listen"])
def test_closes_a_route_stopped_while_it_starts_without_subscribing(hook) -> None:
    h = Harness(SessionHandlers(on_exchange=_empty))
    setattr(h, hook, h.runtime.stop)
    h.runtime.start()

    client = h.clients[0]
    assert "subscribe" not in client.lifecycle
    assert client.lifecycle[-1] == "close"
    assert client.active == set()


def test_one_denial_refresh_finishing_does_not_free_a_second_to_run_beside_it(start) -> None:
    h = start()
    second_refresh = Gate()
    h.api.refresh_answers.extend(
        [RouteToken("fresh", 60), lambda: (second_refresh.wait(), RouteToken("fresh", 60))[1]]
    )
    first = h.client

    def reconnect_then_deny_again() -> None:
        first.before_subscribe = lambda: None
        first.status("connected")
        first.status("access_denied")
        wait_for(lambda: len(h.api.refreshed) == 2)

    first.before_subscribe = reconnect_then_deny_again
    first.status("access_denied")
    wait_for(lambda: first.resubscribes == 1)
    first.status("access_denied")
    quiesce()

    assert len(h.api.refreshed) == 2
    second_refresh.open()


def test_the_status_throttle_measures_time_on_the_runtime_clock(start) -> None:
    reported = Gate()
    waited = Gate()
    finish = Gate()

    def on_exchange(_ctx, exchange):
        exchange.report_status("one")
        reported.open()
        waited.wait()
        exchange.report_status("two")
        finish.wait()
        return {}

    h = start(on_exchange)
    h.open(exchangeId="ex1", requestParts=[])
    reported.wait()
    h.scheduler.advance(1_000)
    waited.open()
    # A second has passed on the runtime clock, so "two" goes out while the handler still runs.
    wait_for(
        lambda: [m["message"] for m in h.client.of_type("exchange_progress")] == ["one", "two"]
    )
    finish.open()


def test_closes_a_route_client_whose_token_cannot_be_set_and_retries() -> None:
    h = Harness(SessionHandlers(on_exchange=_empty))
    h.token_error = ValueError("bad token")
    h.runtime.start()
    assert h.clients[0].closed

    h.scheduler.advance(1_000)
    assert h.client is not h.clients[0]
    assert h.client.subscribed == [ROUTE_CHANNEL]
    h.runtime.stop()


def test_a_timer_callback_that_raises_is_logged(caplog) -> None:
    from blocks_network._session.scheduler import ThreadingScheduler

    raised = threading.Event()

    def fail() -> None:
        raised.set()
        raise RuntimeError("boom")

    with caplog.at_level("ERROR", logger="blocks_network._session.scheduler"):
        ThreadingScheduler().call_later(0, fail)
        assert raised.wait(3)
        wait_for(lambda: any("timer" in r.getMessage() for r in caplog.records))


def test_a_route_stopped_while_it_resubscribes_after_a_denial_keeps_no_subscription(start) -> None:
    h = start()
    h.api.refresh_answers.append(RouteToken("fresh", 60))
    first = h.client
    first.before_subscribe = h.runtime.stop
    first.status("access_denied")

    wait_for(lambda: first.resubscribes == 1)
    quiesce()
    assert first.lifecycle[-1] == "close"
    assert first.active == set()
