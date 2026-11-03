"""BlocksClient and Session against a fake PubNub and a fake backend.

Mirrors the Node SDK's blocks-client tests. The receiver's own ordering rules (gap skips,
deferred terminal notices, recorded file-ref merges) are owned by test_session_recovery.py
and test_session_receive.py; these tests cover what the client and session add on top.
"""

from __future__ import annotations

import io
import json
import threading
import time
import urllib.error
import urllib.request
from concurrent.futures import Future
from types import SimpleNamespace
from typing import Any, Callable, Dict, List, Optional, Tuple

import pytest

from blocks_network import (
    BlocksClient,
    SessionBackpressureError,
    SessionError,
    SessionFailedError,
    SessionNoRouteError,
    SessionPaidAgentError,
    SessionRecoveryError,
    SessionsDisabledError,
)
from blocks_network._session import client as client_module
from blocks_network._session import http as http_module
from blocks_network._session import session as session_module
from blocks_network._session.futures import spawn
from blocks_network.cdm_config import CdmApiConfig, CdmConfig, CdmKeyset

from tests.manual_scheduler import ManualScheduler

API = "https://api.test"
OPENED = {
    "sessionId": "ses1",
    "sessionChannel": "sess.rt1.ses1",
    "pamToken": "pam-token",
    "ttlMinutes": 60,
    "consumerTransportUuid": "consumer-uuid",
    "providerTransportUuid": "provider-uuid",
    "backendTransportUuid": "backend-uuid",
}
OPEN_URL = f"{API}/api/v1/sessions"
SESSION_URL = f"{API}/api/v1/sessions/ses1"
BEACON_URL = f"{SESSION_URL}/beacons"
TOKEN_URL = f"{SESSION_URL}/token"
TT_BASE = 17000000000000000
INLINE = {"kind": "inline", "mimeType": "text/plain", "data": "aGk="}


def wait_until(predicate: Callable[[], bool], timeout: float = 2.0) -> None:
    deadline = time.monotonic() + timeout
    while not predicate():
        if time.monotonic() > deadline:
            raise AssertionError("condition not met in time")
        time.sleep(0.002)


class Scheduler(ManualScheduler):
    """ManualScheduler, safe to arm from the SDK's threads while a test advances it."""

    def __init__(self) -> None:
        super().__init__()
        self._lock = threading.RLock()

    def call_later(self, delay_s: float, callback: Callable[[], None]) -> Any:
        with self._lock:
            return super().call_later(delay_s, callback)

    def advance(self, ms: float) -> None:
        with self._lock:
            super().advance(ms)

    def clock(self) -> float:
        return self.now_ms / 1000


# -- fake backend ------------------------------------------------------------------------


class _Response:
    def __init__(self, status: int, body: bytes) -> None:
        self.status = status
        self._body = body

    def read(self) -> bytes:
        return self._body

    def getheader(self, _name: str) -> None:
        return None

    def __enter__(self) -> "_Response":
        return self

    def __exit__(self, *_: Any) -> None:
        pass


Answer = Tuple[int, Any]


def json_answer(status: int, body: Any) -> Answer:
    return status, body


class Backend:
    """Answers urllib requests by URL; records every request it receives."""

    def __init__(self) -> None:
        self.requests: List[Dict[str, Any]] = []
        self.open: Callable[[], Answer] = lambda: (201, OPENED)
        self.snapshot: Callable[[], Answer] = lambda: (
            200,
            {"sessionId": "ses1", "state": "ready", "exchanges": []},
        )
        self.tokens: List[Callable[[], Answer]] = []
        self._hangs: List[threading.Event] = []

    def hang(self) -> Callable[[], Answer]:
        """An answer that never comes, until the test ends."""
        release = threading.Event()
        self._hangs.append(release)

        def answer() -> Answer:
            release.wait()
            raise urllib.error.URLError("abandoned")

        return answer

    def deferred(self) -> Tuple[Callable[[], Answer], Callable[[Answer], None]]:
        """An answer the test gives later."""
        given: "Future[Answer]" = Future()
        return (lambda: given.result()), given.set_result

    def release_hangs(self) -> None:
        for release in self._hangs:
            release.set()

    def urlopen(self, request: urllib.request.Request, **_: Any) -> _Response:
        url, method = request.full_url, request.get_method()
        body = json.loads(request.data) if request.data else None
        self.requests.append(
            {
                "url": url,
                "method": method,
                "body": body,
                "headers": {k.lower(): v for k, v in request.header_items()},
            }
        )
        if url == OPEN_URL:
            status, payload = self.open()
        elif url == SESSION_URL and method == "GET":
            status, payload = self.snapshot()
        elif url == TOKEN_URL:
            status, payload = (
                self.tokens.pop(0)() if self.tokens else (200, {"pamToken": "fresh", "ttlMinutes": 60})
            )
        else:
            status, payload = 204, None
        raw = b"" if payload is None else json.dumps(payload).encode()
        if status >= 400:
            raise urllib.error.HTTPError(url, status, "refused", {}, io.BytesIO(raw))  # type: ignore[arg-type]
        return _Response(status, raw)

    def calls(self, url: str, method: Optional[str] = None) -> List[Dict[str, Any]]:
        return [r for r in self.requests if r["url"] == url and method in (None, r["method"])]

    def beacons(self, beacon: Optional[str] = None) -> List[Dict[str, Any]]:
        return [r for r in self.calls(BEACON_URL) if beacon in (None, r["body"]["beacon"])]


# -- fake PubNub -------------------------------------------------------------------------


class _Builder:
    def __init__(self, run: Callable[[Dict[str, Any]], Any]) -> None:
        self._params: Dict[str, Any] = {}
        self._run = run

    def __getattr__(self, name: str) -> Callable[[Any], "_Builder"]:
        def setter(value: Any) -> "_Builder":
            self._params[name] = value
            return self

        return setter

    def sync(self) -> Any:
        return self._run(self._params)

    def execute(self) -> Any:
        return self._run(self._params)


class FakePubNub:
    def __init__(self, user_id: str) -> None:
        self.user_id = user_id
        self.listeners: List[Any] = []
        self.tokens: List[str] = []
        self.subscribes: List[List[str]] = []
        self.published: List[Dict[str, Any]] = []
        self.publish_impl: Callable[[Dict[str, Any]], None] = lambda _: None
        self.history_impl: Callable[[Dict[str, Any]], List[Any]] = lambda _: []
        self.history_calls: List[Dict[str, Any]] = []
        self.reconnects = 0
        self.stops = 0
        self.connected = False

    def set_token(self, token: str) -> None:
        self.tokens.append(token)

    def add_listener(self, listener: Any) -> None:
        self.listeners.append(listener)

    def remove_listener(self, listener: Any) -> None:
        self.listeners.remove(listener)

    def unsubscribe_all(self) -> None:
        pass

    def reconnect(self) -> None:
        self.reconnects += 1

    def stop(self) -> None:
        self.stops += 1

    def subscribe(self) -> _Builder:
        return _Builder(lambda p: self.subscribes.append(p["channels"]))

    def publish(self) -> _Builder:
        def run(params: Dict[str, Any]) -> None:
            self.published.append({**params, "after_connect": self.connected})
            self.publish_impl(params)

        return _Builder(run)

    def fetch_messages(self) -> _Builder:
        def run(params: Dict[str, Any]) -> Any:
            self.history_calls.append(params)
            items = self.history_impl(params)
            return SimpleNamespace(result=SimpleNamespace(channels={OPENED["sessionChannel"]: items}))

        return _Builder(run)

    # -- what the test drives --

    def types(self) -> List[str]:
        return [p["message"]["type"] for p in self.published]

    def messages(self) -> List[Dict[str, Any]]:
        return [p["message"] for p in self.published]

    def status(self, category: str) -> None:
        for listener in list(self.listeners):
            listener.status(self, SimpleNamespace(category=SimpleNamespace(name=category)))

    def connect(self) -> None:
        self.connected = True
        self.status("PNConnectedCategory")

    def deliver(self, publisher: str, message: Dict[str, Any], timetoken: Optional[int] = None) -> None:
        event = SimpleNamespace(
            channel=OPENED["sessionChannel"],
            publisher=publisher,
            timetoken=timetoken,
            message=message,
        )
        for listener in list(self.listeners):
            listener.message(self, event)


def history_item(seq: int, uuid: str = "provider-uuid", **fields: Any) -> Any:
    return SimpleNamespace(
        timetoken=TT_BASE + seq,
        uuid=uuid,
        message={"protocolVersion": "2026-05-01", "sessionId": "ses1", "seq": seq, **fields},
    )


def history_of(items: List[Any]) -> Callable[[Dict[str, Any]], List[Any]]:
    """Serves ``items`` the way PubNub history does: newest page first, start exclusive, end inclusive."""

    def serve(params: Dict[str, Any]) -> List[Any]:
        start, end = params.get("start"), params.get("end")
        in_slice = [
            m
            for m in items
            if (start is None or m.timetoken < start) and (end is None or m.timetoken >= end)
        ]
        return in_slice[-params.get("maximum_per_channel", 100) :]

    return serve


# -- harness -----------------------------------------------------------------------------


class Harness:
    def __init__(self, monkeypatch: pytest.MonkeyPatch) -> None:
        self.backend = Backend()
        self.scheduler = Scheduler()
        self.pubnubs: List[FakePubNub] = []
        self.provider_seq = 0
        self.clients: List[BlocksClient] = []
        self.auth: Any = SimpleNamespace(
            get_auth_header=lambda: "Bearer consumer-jwt", on_auth_failure=lambda: False
        )
        keyset = CdmKeyset(publish_key="pub-cdm", subscribe_key="sub-cdm")
        monkeypatch.setattr(
            "blocks_network.cdm_config.fetch_cdm_config",
            lambda _url=None: CdmConfig(keyset, keyset, CdmApiConfig(base_url=API)),
        )
        monkeypatch.setattr(urllib.request, "urlopen", self.backend.urlopen)
        monkeypatch.setattr(client_module, "create_pubnub_client", self._create_pubnub)
        monkeypatch.setattr(
            client_module, "_default_timing", lambda: (self.scheduler, self.scheduler.clock)
        )
        monkeypatch.setattr(session_module, "SESSION_CLOSE_TIMEOUT_S", 0.2)
        monkeypatch.setattr(http_module, "SNAPSHOT_TIMEOUT_S", 0.2)

    def _create_pubnub(self, **kwargs: Any) -> FakePubNub:
        assert kwargs["subscribe_retry_unbounded"] is True
        fake = FakePubNub(kwargs["user_id"])
        self.pubnubs.append(fake)
        return fake

    @property
    def pn(self) -> FakePubNub:
        return self.pubnubs[-1]

    def client(self, **options: Any) -> BlocksClient:
        options.setdefault("auth_provider", self.auth)
        client = BlocksClient.create(
            "free",
            base_url=API,
            subscribe_key="sub-test",
            publish_key="pub-test",
            rpc_headers=options.pop("rpc_headers", {"X-CSRF-Token": "csrf"}),
            **options,
        )
        self.clients.append(client)
        return client

    def teardown(self) -> None:
        """Release every session and leave no SDK thread behind for later tests to trip on."""
        for client in self.clients:
            client.destroy()
        self.backend.release_hangs()
        wait_until(
            lambda: not any(t.name.startswith("blocks-session") for t in threading.enumerate()),
            timeout=5,
        )

    def from_provider(self, **fields: Any) -> None:
        self.provider_seq += 1
        message = {"protocolVersion": "2026-05-01", "sessionId": "ses1", "seq": self.provider_seq, **fields}
        self.pn.deliver("provider-uuid", message, TT_BASE + self.provider_seq)

    def from_backend(self, **fields: Any) -> None:
        message = {"protocolVersion": "2026-05-01", "sessionId": "ses1", "seq": 0, **fields}
        self.pn.deliver("backend-uuid", message)

    def opening(self, client: BlocksClient, **options: Any) -> "Future[Any]":
        return spawn(lambda: client.open_session("echo-agent", **options))

    def subscribed(self, count: int = 1) -> FakePubNub:
        wait_until(lambda: len(self.pubnubs) >= count and bool(self.pubnubs[count - 1].subscribes))
        return self.pubnubs[count - 1]

    def open_ready(self, client: Optional[BlocksClient] = None, **options: Any) -> Any:
        expected = len(self.pubnubs) + 1
        opening = self.opening(client or self.client(), **options)
        pn = self.subscribed(expected)
        pn.connect()
        wait_until(lambda: "session_open" in pn.types())
        self.from_provider(type="session_ready")
        return opening.result(timeout=2)

    def send(self, session: Any, text: str = "hi") -> Tuple[Any, str]:
        exchange = session.send(text)
        wait_until(lambda: exchange.exchange_id in [m.get("exchangeId") for m in self.pn.messages()])
        return exchange, exchange.exchange_id


@pytest.fixture
def h(monkeypatch: pytest.MonkeyPatch) -> Any:
    harness = Harness(monkeypatch)
    yield harness
    harness.teardown()


def error_of(future: "Future[Any]", timeout: float = 2) -> BaseException:
    err = future.exception(timeout=timeout)
    assert err is not None
    return err


# -- BlocksClient.open_session -----------------------------------------------------------


class TestOpenSession:
    def test_opens_connects_as_the_issued_uuid_and_publishes_session_open_once_connected(
        self, h: Harness
    ) -> None:
        opening = h.opening(h.client(), idempotency_key="k1")
        pn = h.subscribed()

        [request] = h.backend.calls(OPEN_URL)
        assert request["body"] == {"agentName": "echo-agent", "idempotencyKey": "k1"}
        assert request["headers"]["authorization"] == "Bearer consumer-jwt"
        assert request["headers"]["blocks-protocol-version"] == "2026-05-01"
        assert request["headers"]["x-csrf-token"] == "csrf"
        assert pn.user_id == "consumer-uuid"
        assert pn.tokens == ["pam-token"]
        assert pn.subscribes == [["sess.rt1.ses1"]]

        pn.connect()
        wait_until(lambda: bool(pn.published) and bool(h.backend.beacons("opened")))
        [publish] = pn.published
        assert publish["after_connect"] is True
        assert publish["channel"] == "sess.rt1.ses1"
        assert publish["should_store"] is True
        assert publish["meta"] == {"sessionId": "ses1", "protocolVersion": "2026-05-01"}
        assert publish["message"]["type"] == "session_open"
        assert publish["message"]["seq"] == 1
        assert "exchange" not in publish["message"]
        assert not opening.done()

        h.from_provider(type="session_ready")
        session = opening.result(timeout=2)
        assert session.session_id == "ses1"
        assert session.state == "ready"

    @pytest.mark.parametrize(
        "status,code,error_class",
        [
            (404, "SESSIONS_DISABLED", SessionsDisabledError),
            (422, "SESSION_PAID_AGENT_UNSUPPORTED", SessionPaidAgentError),
            (503, "SESSION_NO_ROUTE", SessionNoRouteError),
        ],
    )
    def test_raises_a_refusal_as_its_typed_session_error(
        self, h: Harness, status: int, code: str, error_class: type
    ) -> None:
        h.backend.open = lambda: (status, {"error": "refused", "code": code})

        with pytest.raises(error_class) as raised:
            h.client().open_session("echo-agent")

        assert isinstance(raised.value, SessionError)
        assert raised.value.code == code
        assert h.pubnubs == []

    def test_raises_session_failed_when_the_session_fails_before_ready_and_releases(
        self, h: Harness
    ) -> None:
        opening = h.opening(h.client())
        pn = h.subscribed()
        pn.connect()
        wait_until(lambda: bool(pn.published))

        h.from_backend(type="session_failed", error="provider_at_capacity")

        err = error_of(opening)
        assert isinstance(err, SessionFailedError)
        assert err.code == "provider_at_capacity"
        wait_until(lambda: pn.stops == 1)
        assert pn.types() == ["session_open"]

    def test_cancels_the_session_when_the_provider_is_not_ready_in_time(self, h: Harness) -> None:
        opening = h.opening(h.client(), ready_timeout=0.1)
        h.subscribed().connect()

        err = error_of(opening)
        assert isinstance(err, SessionError) and err.code == "session_canceled"
        assert "not ready within 0.1 s" in str(err)
        wait_until(lambda: h.pn.stops == 1)
        assert h.pn.types() == ["session_open", "session_close"]

    def test_accepts_ready_timeout_at_its_two_minute_ceiling(self, h: Harness) -> None:
        client = h.client()
        opening = h.opening(client, ready_timeout=120)
        h.subscribed()

        assert len(h.backend.calls(OPEN_URL)) == 1
        client.destroy()
        error_of(opening)

    @pytest.mark.parametrize("ready_timeout", [0, 120.001, float("nan"), True, "1000"])
    def test_refuses_an_invalid_ready_timeout_before_asking_the_backend(
        self, h: Harness, ready_timeout: Any
    ) -> None:
        with pytest.raises(ValueError):
            h.client().open_session("echo-agent", ready_timeout=ready_timeout)
        assert h.backend.requests == []

    def test_refuses_an_idempotency_key_over_256_characters_before_asking_the_backend(
        self, h: Harness
    ) -> None:
        client = h.client()
        with pytest.raises(ValueError):
            client.open_session("echo-agent", idempotency_key="k" * 257)
        assert h.backend.requests == []

        opening = h.opening(client, idempotency_key="k" * 256)
        h.subscribed()
        client.destroy()
        error_of(opening)

    def test_counts_the_open_request_against_ready_timeout(self, h: Harness) -> None:
        h.backend.open = h.backend.hang()

        with pytest.raises(SessionError, match="not ready within 0.05 s") as raised:
            h.client().open_session("echo-agent", ready_timeout=0.05)

        assert raised.value.code == "session_canceled"
        assert h.pubnubs == []

    def test_counts_a_stalled_credential_refresh_against_ready_timeout(self, h: Harness) -> None:
        stalled = threading.Event()
        h.auth.ensure_ready = stalled.wait
        try:
            with pytest.raises(SessionError) as raised:
                h.client().open_session("echo-agent", ready_timeout=0.05)
        finally:
            stalled.set()
        assert raised.value.code == "session_canceled"
        assert h.backend.calls(OPEN_URL) == []

    def test_gives_up_on_an_open_whose_session_open_publish_never_completes(
        self, h: Harness
    ) -> None:
        stalled = threading.Event()
        opening = h.opening(h.client(), ready_timeout=0.1)
        pn = h.subscribed()
        pn.publish_impl = lambda _: stalled.wait()
        pn.connect()

        err = error_of(opening)
        assert "not ready within" in str(err)
        assert pn.stops == 0
        # session_close queues behind the stalled publish, so close ends once its bound passes.
        wait_until(lambda: pn.stops == 1)
        stalled.set()

    def test_keeps_the_sdk_credential_when_a_caller_header_differs_only_in_case(
        self, h: Harness
    ) -> None:
        client = h.client(
            rpc_headers={"authorization": "Bearer other", "blocks-protocol-version": "1999-01-01"}
        )
        opening = h.opening(client)
        h.subscribed()

        headers = h.backend.calls(OPEN_URL)[0]["headers"]
        assert headers["authorization"] == "Bearer consumer-jwt"
        assert headers["blocks-protocol-version"] == "2026-05-01"
        client.destroy()
        error_of(opening)

    def test_refuses_a_backend_url_that_is_not_http_before_sending_anything(
        self, h: Harness
    ) -> None:
        client = BlocksClient.create(
            "free", base_url="file:///etc", subscribe_key="s", publish_key="p", auth_provider=h.auth
        )
        h.clients.append(client)

        with pytest.raises(ValueError, match="http or https"):
            client.open_session("echo-agent")
        assert h.backend.requests == []

    def test_refuses_to_create_a_client_without_exactly_one_credential(self, h: Harness) -> None:
        with pytest.raises(ValueError, match="exactly one"):
            BlocksClient.create("free", base_url=API, subscribe_key="s", publish_key="p")
        with pytest.raises(ValueError, match="exactly one"):
            BlocksClient.create(
                "free", base_url=API, subscribe_key="s", publish_key="p",
                api_key="bk_x", auth_provider=h.auth,
            )


# -- idempotent open ---------------------------------------------------------------------


def replayed(state: str) -> Callable[[], Answer]:
    return lambda: (200, {**OPENED, "idempotent": True, "state": state})


class TestReplayedOpen:
    @pytest.mark.parametrize("state", ["ready", "closing", "closed", "failed", "canceled"])
    def test_refuses_a_replay_past_open_at_once_leaves_it_alone_and_frees_the_key(
        self, h: Harness, state: str
    ) -> None:
        h.backend.open = replayed(state)
        client = h.client()

        for _ in range(2):
            with pytest.raises(SessionError) as raised:
                client.open_session("echo-agent", idempotency_key="k1")
            assert raised.value.code == "session_already_open"

        assert h.pubnubs == []
        assert h.backend.beacons() == []
        assert len(h.backend.calls(OPEN_URL)) == 2

    def test_refuses_a_second_open_under_a_key_while_its_session_is_held_here(
        self, h: Harness
    ) -> None:
        client = h.client()
        first = h.opening(client, idempotency_key="k1")
        h.subscribed()

        with pytest.raises(SessionError, match="already names a session"):
            client.open_session("echo-agent", idempotency_key="k1")
        h.pn.connect()
        wait_until(lambda: bool(h.pn.published))
        h.from_provider(type="session_ready")
        session = first.result(timeout=2)
        with pytest.raises(SessionError, match="already names a session"):
            client.open_session("echo-agent", idempotency_key="k1")
        assert len(h.backend.calls(OPEN_URL)) == 1

        closing = spawn(session.close)
        wait_until(lambda: "session_close" in h.pn.types())
        h.from_provider(type="session_closed", reason="consumer_closed")
        closing.result(timeout=2)
        again = h.opening(client, idempotency_key="k1")
        wait_until(lambda: len(h.backend.calls(OPEN_URL)) == 2)
        client.destroy()
        error_of(again)

    def test_keeps_a_key_held_while_a_session_that_failed_to_open_tears_down(
        self, h: Harness
    ) -> None:
        stalled = threading.Event()
        client = h.client()
        first = h.opening(client, idempotency_key="k1", ready_timeout=0.05)
        pn = h.subscribed()
        pn.publish_impl = lambda _: stalled.wait()
        pn.connect()
        assert "not ready within" in str(error_of(first))

        with pytest.raises(SessionError, match="already names a session"):
            client.open_session("echo-agent", idempotency_key="k1")
        wait_until(lambda: pn.stops == 1)
        again = h.opening(client, idempotency_key="k1")
        wait_until(lambda: len(h.backend.calls(OPEN_URL)) == 2)
        client.destroy()
        error_of(again)
        stalled.set()

    def test_answers_session_canceled_not_already_open_once_the_client_is_destroyed(
        self, h: Harness
    ) -> None:
        h.backend.open = h.backend.hang()
        client = h.client()
        first = h.opening(client, idempotency_key="k1")
        wait_until(lambda: bool(h.backend.calls(OPEN_URL)))
        client.destroy()

        with pytest.raises(SessionError) as raised:
            client.open_session("echo-agent", idempotency_key="k1")
        assert raised.value.code == "session_canceled"
        assert getattr(error_of(first), "code", None) == "session_canceled"

    def test_keeps_a_key_held_until_an_abandoned_open_request_settles(self, h: Harness) -> None:
        answer_open, answer = h.backend.deferred()
        h.backend.open = answer_open
        client = h.client()
        with pytest.raises(SessionError) as raised:
            client.open_session("echo-agent", idempotency_key="k1", ready_timeout=0.02)
        assert raised.value.code == "session_canceled"

        with pytest.raises(SessionError) as raised:
            client.open_session("echo-agent", idempotency_key="k1")
        assert raised.value.code == "session_already_open"
        answer((201, OPENED))
        wait_until(lambda: bool(h.backend.beacons("closed")))
        h.backend.open = lambda: (201, OPENED)
        wait_until(lambda: "k1" not in client._keys_held)
        again = h.opening(client, idempotency_key="k1")
        wait_until(lambda: len(h.backend.calls(OPEN_URL)) == 2)
        client.destroy()
        error_of(again)

    def test_sends_no_closed_beacon_when_a_replay_answers_an_abandoned_open(
        self, h: Harness
    ) -> None:
        answer_open, answer = h.backend.deferred()
        h.backend.open = answer_open
        client = h.client()
        with pytest.raises(SessionError):
            client.open_session("echo-agent", idempotency_key="k1", ready_timeout=0.02)

        answer((200, {**OPENED, "idempotent": True, "state": "open"}))
        wait_until(lambda: "k1" not in client._keys_held)

        assert h.backend.beacons() == []

    def test_sends_no_closed_beacon_for_a_replay_answered_as_the_client_is_destroyed(
        self, h: Harness
    ) -> None:
        client = h.client()

        def destroyed_meanwhile() -> Answer:
            # destroy() lands after the request answered, before the client reads the answer.
            client._destroyed = True
            return 200, {**OPENED, "idempotent": True, "state": "open"}

        h.backend.open = destroyed_meanwhile

        with pytest.raises(SessionError) as raised:
            client.open_session("echo-agent", idempotency_key="k1")
        assert raised.value.code == "session_canceled"
        wait_until(lambda: not any(t.name == "blocks-session-beacon" for t in threading.enumerate()))
        assert h.backend.beacons() == []

    @pytest.mark.parametrize(
        "fields",
        [{"idempotent": "true", "state": "ready"}, {"idempotent": True, "state": "paused"}],
        ids=["non-boolean-idempotent", "unknown-replay-state"],
    )
    def test_refuses_an_invalid_open_response(self, h: Harness, fields: Dict[str, Any]) -> None:
        h.backend.open = lambda: (200, {**OPENED, **fields})

        with pytest.raises(ValueError, match="invalid session"):
            h.client().open_session("echo-agent", idempotency_key="k1")
        assert h.pubnubs == []

    @pytest.mark.parametrize(
        "fields",
        [{"idempotent": True, "state": "open"}, {"idempotent": False}],
        ids=["replay-never-ready", "explicit-first-open"],
    )
    def test_opens_normally(self, h: Harness, fields: Dict[str, Any]) -> None:
        h.backend.open = lambda: (200, {**OPENED, **fields})

        session = h.open_ready(idempotency_key="k1")

        assert session.state == "ready"
        assert "session_open" in h.pn.types()

    def test_refuses_call_on_a_replay_of_a_ready_session_sending_nothing(self, h: Harness) -> None:
        h.backend.open = replayed("ready")

        with pytest.raises(SessionError) as raised:
            h.client().call("echo-agent", "hi", idempotency_key="k1")
        assert raised.value.code == "session_already_open"
        assert h.pubnubs == []


# -- exchanges ---------------------------------------------------------------------------


class TestExchanges:
    def test_publishes_each_exchange_with_the_next_seq_and_settles_with_every_artifact(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session, "hello")
        sent = h.pn.messages()[1]
        assert sent["type"] == "exchange" and sent["seq"] == 2
        assert sent["requestParts"] == [{"partId": "text", "text": "hello"}]

        progress: List[Any] = []
        exchange.on_progress(progress.append)
        h.from_provider(type="exchange_progress", exchangeId=exchange_id, message="working")
        h.from_provider(type="exchange_artifact", exchangeId=exchange_id, artifactRef=INLINE)
        late: List[Any] = []
        exchange.on_artifact(late.append)
        assert late == [INLINE]

        h.from_provider(type="exchange_outcome", exchangeId=exchange_id, state="completed")
        outcome = exchange.wait_for_outcome(1)
        assert (outcome.exchange_id, outcome.state, outcome.artifacts) == (
            exchange_id,
            "completed",
            [INLINE],
        )
        assert [(p.exchange_id, p.message) for p in progress] == [(exchange_id, "working")]

    def test_replays_received_artifacts_to_a_new_callback_before_a_live_one(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        first, second, live = ({**INLINE, "data": name} for name in ("QQ==", "Qg==", "Qw=="))
        for ref in (first, second):
            h.from_provider(type="exchange_artifact", exchangeId=exchange_id, artifactRef=ref)
        replaying = threading.Event()
        replay_may_continue = threading.Event()
        seen: List[Any] = []

        def record(ref: Any) -> None:
            seen.append(ref)
            if ref is first:
                replaying.set()
                replay_may_continue.wait(2)

        registering = spawn(lambda: exchange.on_artifact(record))
        replaying.wait(2)
        delivering = spawn(
            lambda: h.from_provider(type="exchange_artifact", exchangeId=exchange_id, artifactRef=live)
        )
        time.sleep(0.05)
        replay_may_continue.set()
        registering.result(timeout=2)
        delivering.result(timeout=2)

        assert seen == [first, second, live]

    def test_raises_backpressure_on_a_ninth_pending_exchange_without_publishing_it(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        exchanges = [session.send(f"m{i}") for i in range(8)]
        wait_until(lambda: len(h.pn.published) == 9)

        with pytest.raises(SessionBackpressureError):
            session.send("refused")

        h.from_provider(type="exchange_outcome", exchangeId=exchanges[0].exchange_id, state="completed")
        session.send("m8")
        # Publishes go out in order, so the refused one would have gone out before this.
        wait_until(lambda: len(h.pn.published) == 10)
        assert h.pn.messages()[-1]["requestParts"] == [{"partId": "text", "text": "m8"}]

    def test_settles_an_unsettled_exchange_canceled_when_close_completes(self, h: Harness) -> None:
        session = h.open_ready()
        exchange = session.send("hello")
        wait_until(lambda: len(h.pn.published) == 2)
        publishing = threading.Event()
        h.pn.publish_impl = lambda _: publishing.wait(2)
        closing = spawn(session.close)
        wait_until(lambda: "session_close" in h.pn.types())
        # Closing from the moment the close is asked for, before session_close is out.
        assert session.state == "closing"
        assert h.pn.messages()[-1]["seq"] == 3
        publishing.set()

        h.from_provider(type="session_closed", reason="consumer_closed")
        closing.result(timeout=2)
        assert exchange.wait_for_outcome(1).state == "canceled"
        assert session.state == "closed"
        assert h.pn.stops == 1
        wait_until(lambda: bool(h.backend.beacons("closed")))
        session.close()
        assert h.pn.types().count("session_close") == 1

    def test_refuses_a_send_made_right_after_close(self, h: Harness) -> None:
        session = h.open_ready()
        spawn(session.close)
        wait_until(lambda: "session_close" in h.pn.types())

        with pytest.raises(SessionError) as raised:
            session.send("late")
        assert raised.value.code == "session_not_ready"
        assert h.pn.types() == ["session_open", "session_close"]

    def test_a_state_callback_that_closes_again_publishes_one_session_close(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        session.on_state_change(lambda state: session.close() if state == "closing" else None)

        closing = spawn(session.close)
        wait_until(lambda: "session_close" in h.pn.types())
        h.from_provider(type="session_closed", reason="consumer_closed")
        closing.result(timeout=2)

        assert h.pn.types().count("session_close") == 1

    def test_a_message_callback_that_closes_does_not_hold_the_delivery_thread(
        self, h: Harness, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        exchange.on_progress(lambda _: session.close())
        monkeypatch.setattr(session_module, "SESSION_CLOSE_TIMEOUT_S", 5.0)

        started = time.monotonic()
        h.from_provider(type="exchange_progress", exchangeId=exchange_id, message="bye")
        assert time.monotonic() - started < 1

        # Back on this thread at once, so session_closed can still be delivered.
        wait_until(lambda: "session_close" in h.pn.types())
        h.from_provider(type="session_closed", reason="consumer_closed")
        session.close()
        assert session.state == "closed"

    def test_ends_the_session_locally_when_session_closed_does_not_arrive(self, h: Harness) -> None:
        session = h.open_ready()

        session.close()

        assert session.state == "closed"
        assert h.pn.stops == 1

    def test_reports_a_backend_failure_once_and_stops_publishing(self, h: Harness) -> None:
        session = h.open_ready()
        states: List[str] = []
        session.on_state_change(states.append)
        exchange = session.send("hello")

        h.from_backend(type="session_failed", error="route_lost")
        h.from_backend(type="session_failed", error="route_lost")

        assert states == ["failed"]
        outcome = exchange.wait_for_outcome(1)
        assert (outcome.state, outcome.error) == ("failed", "route_lost")
        wait_until(lambda: h.pn.stops == 1)
        with pytest.raises(SessionError):
            session.send("again")


    def test_settles_an_exchange_sent_as_the_session_fails(self, h: Harness) -> None:
        session = h.open_ready()
        track = session._receiver.track_exchange

        def fail_first(exchange_id: str) -> None:
            h.from_backend(type="session_failed", error="route_lost")
            track(exchange_id)

        session._receiver.track_exchange = fail_first

        outcome = session.send("hello").wait_for_outcome(1)

        assert (outcome.state, outcome.error) == ("failed", "route_lost")
        assert h.pn.types() == ["session_open"]


    def test_does_not_publish_an_exchange_sent_as_the_session_closes(self, h: Harness) -> None:
        session = h.open_ready()
        track = session._receiver.track_exchange

        def close_first(exchange_id: str) -> None:
            track(exchange_id)
            spawn(session.close)
            wait_until(lambda: "session_close" in h.pn.types())

        session._receiver.track_exchange = close_first

        outcome = session.send("hello").wait_for_outcome(2)

        assert outcome.state == "canceled"
        assert h.pn.types() == ["session_open", "session_close"]


# -- call --------------------------------------------------------------------------------


class TestCall:
    def test_sends_the_input_with_session_open_returns_the_outcome_and_closes(
        self, h: Harness
    ) -> None:
        calling = spawn(lambda: h.client().call("echo-agent", "hi"))
        pn = h.subscribed()
        pn.connect()
        wait_until(lambda: bool(pn.published))
        open_message = pn.messages()[0]
        inline = open_message["exchange"]
        assert open_message["type"] == "session_open"
        assert inline["requestParts"] == [{"partId": "text", "text": "hi"}]

        h.from_provider(type="session_ready")
        h.from_provider(type="exchange_outcome", exchangeId=inline["exchangeId"], state="completed")
        wait_until(lambda: "session_close" in pn.types())
        h.from_provider(type="session_closed", reason="consumer_closed")

        outcome = calling.result(timeout=2)
        assert (outcome.exchange_id, outcome.state, outcome.artifacts) == (
            inline["exchangeId"],
            "completed",
            [],
        )
        assert pn.types() == ["session_open", "session_close"]
        assert pn.stops == 1

    def test_rejects_input_it_cannot_send_before_opening_anything(self, h: Harness) -> None:
        with pytest.raises(TypeError):
            h.client().call("echo-agent", [{"partId": "doc", "file": b"\x01", "fileName": "a.bin"}])
        assert h.backend.requests == []
        assert h.pubnubs == []


# -- destroy -----------------------------------------------------------------------------


class TestDestroy:
    def test_fails_an_open_whose_ready_arrives_while_destroy_closes_it(self, h: Harness) -> None:
        stalled = threading.Event()
        client = h.client()
        opening = h.opening(client)
        pn = h.subscribed()
        pn.connect()
        wait_until(lambda: bool(pn.published))
        pn.publish_impl = lambda _: stalled.wait()

        client.destroy()
        wait_until(lambda: "session_close" in pn.types())
        h.from_provider(type="session_ready")

        assert getattr(error_of(opening), "code", None) == "session_canceled"
        stalled.set()

    def test_ends_a_session_still_subscribing_publishing_nothing(self, h: Harness) -> None:
        client = h.client()
        opening = h.opening(client)
        pn = h.subscribed()

        client.destroy()

        assert getattr(error_of(opening), "code", None) == "session_canceled"
        wait_until(lambda: pn.stops == 1 and len(h.backend.beacons("closed")) == 1)
        assert pn.published == []

    def test_ends_a_session_whose_open_request_is_still_in_flight(self, h: Harness) -> None:
        answer_open, answer = h.backend.deferred()
        h.backend.open = answer_open
        client = h.client()
        opening = h.opening(client)
        wait_until(lambda: bool(h.backend.calls(OPEN_URL)))

        client.destroy()
        answer((201, OPENED))

        assert getattr(error_of(opening), "code", None) == "session_canceled"
        wait_until(lambda: len(h.backend.beacons("closed")) == 1)
        assert h.pubnubs == []
        with pytest.raises(SessionError) as raised:
            client.open_session("echo-agent")
        assert raised.value.code == "session_canceled"


    def test_fails_an_open_whose_ready_lands_before_destroys_close_starts(
        self, h: Harness, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        client = h.client()
        opening = h.opening(client)
        pn = h.subscribed()
        pn.connect()
        wait_until(lambda: bool(pn.published))
        close_may_start = threading.Event()
        close = session_module.Session.close

        def gated_close(session: Any) -> None:
            close_may_start.wait(2)
            close(session)

        monkeypatch.setattr(session_module.Session, "close", gated_close)

        client.destroy()
        h.from_provider(type="session_ready")

        assert getattr(error_of(opening), "code", None) == "session_canceled"
        close_may_start.set()

    def test_publishes_no_session_open_once_destroy_has_asked_to_close(self, h: Harness) -> None:
        client = h.client()
        opening = h.opening(client)
        pn = h.subscribed()
        [session] = list(client._sessions)
        terminate = session._receiver.terminate
        closing = threading.Event()
        close_may_finish = threading.Event()

        def gated_terminate(*args: Any) -> None:
            closing.set()
            close_may_finish.wait(2)
            terminate(*args)

        session._receiver.terminate = gated_terminate
        client.destroy()
        closing.wait(2)
        pn.connect()

        assert getattr(error_of(opening, timeout=1), "code", None) == "session_canceled"
        assert pn.published == []
        close_may_finish.set()

    def test_sets_up_nothing_for_a_session_destroyed_before_it_subscribes(
        self, h: Harness, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        client = h.client()
        subscribe_may_start = threading.Event()
        subscribe = session_module.Session._subscribe

        def gated_subscribe(session: Any) -> None:
            subscribe_may_start.wait(2)
            subscribe(session)

        monkeypatch.setattr(session_module.Session, "_subscribe", gated_subscribe)
        opening = h.opening(client)
        wait_until(lambda: bool(client._sessions))

        client.destroy()
        wait_until(lambda: len(h.pubnubs) == 1 and h.pubnubs[0].stops == 1)
        subscribe_may_start.set()

        assert getattr(error_of(opening), "code", None) == "session_canceled"
        assert h.pn.subscribes == [] and h.pn.listeners == []
        h.scheduler.advance(60 * 60_000)
        assert h.backend.calls(TOKEN_URL) == []


# -- transport failures ------------------------------------------------------------------


class TestTransportFailures:
    def test_drops_a_publish_still_queued_when_the_session_is_released(self, h: Harness) -> None:
        session = h.open_ready()
        stalled = threading.Event()
        h.pn.publish_impl = lambda _: stalled.wait()
        session.send("stalled")
        queued = session.send("queued")
        wait_until(lambda: len(h.pn.published) == 2)

        h.from_backend(type="session_failed", error="route_lost")
        wait_until(lambda: h.pn.stops == 1)
        stalled.set()

        assert queued.wait_for_outcome(1).state == "failed"
        session._publisher.join(1)
        assert not session._publisher.is_alive()
        assert len(h.pn.published) == 2

    def test_publishes_nothing_more_once_terminal_though_a_callback_delays_the_release(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        callback_may_return = threading.Event()
        session.on_state_change(lambda state: callback_may_return.wait(2))
        stalled = threading.Event()
        h.pn.publish_impl = lambda _: stalled.wait(2)
        session.send("stalled")
        queued = session.send("queued")
        wait_until(lambda: len(h.pn.published) == 2)

        spawn(lambda: h.from_backend(type="session_failed", error="route_lost"))
        wait_until(lambda: session.state == "failed")
        stalled.set()
        # A window for the worker to reach the queued publish while the release still waits.
        time.sleep(0.05)
        assert h.pn.stops == 0
        callback_may_return.set()
        assert queued.wait_for_outcome(1).state == "failed"
        session._publisher.join(1)
        assert not session._publisher.is_alive()

        assert len(h.pn.published) == 2

    def test_publishes_no_queued_exchange_while_a_held_artifact_is_handed_over(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        in_callback = threading.Event()
        callback_may_return = threading.Event()

        def hold_the_end(_: Any) -> None:
            in_callback.set()
            callback_may_return.wait(2)

        exchange.on_artifact(hold_the_end)
        stalled = threading.Event()
        h.pn.publish_impl = lambda _: stalled.wait(2)
        session.send("stalled")
        session.send("queued")
        wait_until(lambda: len(h.pn.published) == 3)
        # Held behind the missing seq 2, so it is handed over while the session ends.
        h.provider_seq = 2
        h.from_provider(type="exchange_artifact", exchangeId=exchange_id, artifactRef=INLINE)

        spawn(lambda: h.from_backend(type="session_closed", reason="idle_timeout"))
        in_callback.wait(2)
        stalled.set()
        wait_until(lambda: session._publishes.empty())
        callback_may_return.set()
        wait_until(lambda: session.state == "closed")
        session._publisher.join(1)
        assert not session._publisher.is_alive()

        assert [m["message"].get("requestParts") for m in h.pn.published[1:]] == [
            [{"partId": "text", "text": "hi"}],
            [{"partId": "text", "text": "stalled"}],
        ]

    def test_settles_an_exchange_failed_when_its_publish_fails(self, h: Harness) -> None:
        session = h.open_ready()

        def fail(_: Any) -> None:
            raise RuntimeError("network down")

        h.pn.publish_impl = fail
        outcome = session.send("hello").wait_for_outcome(1)

        assert (outcome.state, outcome.error) == ("failed", "publish_failed")
        assert session.state == "ready"


# -- token rotation ----------------------------------------------------------------------


class TestTokenRotation:
    def token_calls(self, h: Harness) -> int:
        return len(h.backend.calls(TOKEN_URL))

    def test_rotates_at_80_percent_of_the_ttl_without_resubscribing(self, h: Harness) -> None:
        session = h.open_ready()
        h.backend.tokens.append(lambda: (200, {"pamToken": "rotated", "ttlMinutes": 10}))

        h.scheduler.advance(48 * 60_000 - 1)
        assert self.token_calls(h) == 0
        h.scheduler.advance(1)
        assert self.token_calls(h) == 1
        assert h.pn.tokens[-1] == "rotated"
        assert len(h.pn.subscribes) == 1

        # The next rotation follows the TTL the refresh answered.
        h.scheduler.advance(8 * 60_000)
        assert self.token_calls(h) == 2
        assert session.state == "ready"

    @pytest.mark.parametrize("status", [503, 429])
    def test_retries_a_rotation_that_failed_in_transit_with_backoff(
        self, h: Harness, status: int
    ) -> None:
        h.open_ready()
        h.backend.tokens.append(lambda: (status, {"error": "later"}))

        h.scheduler.advance(48 * 60_000)
        assert self.token_calls(h) == 1
        h.scheduler.advance(5_000)
        assert self.token_calls(h) == 2
        assert h.pn.tokens[-1] == "fresh"

    def test_stops_retrying_a_failing_rotation_once_the_token_has_expired(self, h: Harness) -> None:
        session = h.open_ready()
        h.backend.tokens.extend([lambda: (503, {"error": "unavailable"})] * 50)

        h.scheduler.advance(60 * 60_000)
        attempts = self.token_calls(h)
        assert attempts > 1
        h.scheduler.advance(60 * 60_000)
        assert self.token_calls(h) == attempts
        assert session.state == "ready"

    @pytest.mark.parametrize(
        "answer",
        [(404, {"code": "SESSION_NOT_FOUND", "error": "gone"}), (403, {"error": "forbidden"})],
        ids=["session-not-found", "forbidden"],
    )
    def test_does_not_retry_a_refused_rotation(self, h: Harness, answer: Answer) -> None:
        session = h.open_ready()
        h.backend.tokens.append(lambda: answer)

        h.scheduler.advance(48 * 60_000)
        assert self.token_calls(h) == 1
        h.scheduler.advance(12 * 60_000)
        assert self.token_calls(h) == 1
        assert session.state == "ready"

    def test_refreshes_once_on_a_denial_and_resumes_the_parked_subscription(
        self, h: Harness
    ) -> None:
        session = h.open_ready()

        h.pn.status("PNAccessDeniedCategory")
        h.pn.status("PNAccessDeniedCategory")
        h.scheduler.advance(0)

        assert self.token_calls(h) == 1
        assert h.pn.tokens[-1] == "fresh"
        assert h.pn.reconnects == 1
        assert session.state == "ready"

    @pytest.mark.parametrize(
        "answer",
        [(404, {"code": "SESSION_NOT_FOUND", "error": "gone"}), (403, {"error": "forbidden"})],
        ids=["session-not-found", "forbidden"],
    )
    def test_fails_the_session_when_the_denial_refresh_is_refused(
        self, h: Harness, answer: Answer
    ) -> None:
        session = h.open_ready()
        exchange = session.send("hello")
        h.backend.tokens.append(lambda: answer)

        h.pn.status("PNAccessDeniedCategory")
        h.scheduler.advance(0)

        assert session.state == "failed"
        outcome = exchange.wait_for_outcome(1)
        assert (outcome.state, outcome.error) == ("failed", "session_access_revoked")
        wait_until(lambda: h.pn.stops == 1)

    def test_fails_the_session_when_the_refreshed_token_is_denied_before_it_reconnects(
        self, h: Harness
    ) -> None:
        session = h.open_ready()

        h.pn.status("PNAccessDeniedCategory")
        h.scheduler.advance(0)
        h.pn.status("PNAccessDeniedCategory")

        assert self.token_calls(h) == 1
        assert session.state == "failed"

    def test_fails_the_open_when_its_token_is_denied_and_cannot_be_refreshed(
        self, h: Harness
    ) -> None:
        h.backend.tokens.append(lambda: (404, {"code": "SESSION_NOT_FOUND", "error": "gone"}))
        opening = h.opening(h.client())
        pn = h.subscribed()

        pn.status("PNAccessDeniedCategory")
        h.scheduler.advance(0)

        err = error_of(opening)
        assert isinstance(err, SessionFailedError)
        assert err.code == "session_access_revoked"

    def test_stops_rotating_once_the_session_is_closed(self, h: Harness) -> None:
        session = h.open_ready()
        session.close()

        h.scheduler.advance(60 * 60_000)

        assert self.token_calls(h) == 0


# -- recovery ----------------------------------------------------------------------------


class TestRecovery:
    def test_refills_a_gap_from_history_once_it_persists(self, h: Harness) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        h.pn.history_impl = lambda _: [
            history_item(2, type="exchange_artifact", exchangeId=exchange_id, artifactRef=INLINE),
            history_item(3, type="exchange_outcome", exchangeId=exchange_id, state="completed"),
        ]
        h.provider_seq = 3
        h.from_provider(type="exchange_progress", exchangeId=exchange_id, message="later")

        h.scheduler.advance(750)

        outcome = exchange.wait_for_outcome(1)
        assert (outcome.state, outcome.artifacts) == ("completed", [INLINE])
        # From session_ready, the last message before the gap, not the progress held behind it.
        assert h.pn.history_calls[0]["end"] == TT_BASE + 1

    def test_settles_from_the_backend_record_after_a_reconnect(self, h: Harness) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        h.backend.snapshot = lambda: (
            200,
            {
                "sessionId": "ses1",
                "state": "ready",
                "exchanges": [
                    {"exchangeId": exchange_id, "state": "completed", "error": None, "artifactRefs": []}
                ],
            },
        )

        h.pn.status("PNReconnectedCategory")

        assert exchange.wait_for_outcome(2).state == "completed"
        assert h.pn.history_calls

    def test_still_settles_from_the_backend_record_when_history_fails(self, h: Harness) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)

        def unavailable(_: Any) -> List[Any]:
            raise RuntimeError("history 503")

        h.pn.history_impl = unavailable
        h.backend.snapshot = lambda: (
            200,
            {
                "sessionId": "ses1",
                "state": "ready",
                "exchanges": [
                    {"exchangeId": exchange_id, "state": "completed", "error": None, "artifactRefs": []}
                ],
            },
        )

        session.recover()

        assert exchange.wait_for_outcome(1).state == "completed"

    def test_holds_a_live_terminal_notice_that_arrives_during_the_snapshot_read(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        answer_snapshot, answer = h.backend.deferred()
        h.backend.snapshot = answer_snapshot

        recovering = spawn(session.recover)
        wait_until(lambda: bool(h.backend.calls(SESSION_URL, "GET")))
        h.from_backend(type="session_closed", reason="idle_timeout")
        assert session.state == "ready"
        answer(
            (
                200,
                {
                    "sessionId": "ses1",
                    "state": "closed",
                    "reason": "idle_timeout",
                    "exchanges": [
                        {"exchangeId": exchange_id, "state": "completed", "error": None, "artifactRefs": []}
                    ],
                },
            )
        )
        recovering.result(timeout=2)

        assert exchange.wait_for_outcome(1).state == "completed"
        assert session.state == "closed"

    def test_replays_history_in_full_before_a_live_terminal_notice_during_recovery(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        replaying = threading.Event()
        resume = threading.Event()

        def history(_: Any) -> List[Any]:
            replaying.set()
            resume.wait(2)
            return [
                history_item(2, type="exchange_artifact", exchangeId=exchange_id, artifactRef=INLINE),
                history_item(3, type="exchange_outcome", exchangeId=exchange_id, state="completed"),
            ]

        h.pn.history_impl = history
        h.backend.snapshot = lambda: (200, {"sessionId": "ses1", "state": "closed", "exchanges": []})

        recovering = spawn(session.recover)
        replaying.wait(2)
        h.from_backend(type="session_closed", reason="idle_timeout")
        resume.set()
        recovering.result(timeout=2)

        outcome = exchange.wait_for_outcome(1)
        assert (outcome.state, outcome.artifacts) == ("completed", [INLINE])
        assert session.state == "closed"

    def test_does_not_move_the_replay_point_on_a_dropped_message_such_as_its_own_echo(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        h.pn.deliver(
            "consumer-uuid", {"type": "exchange", "sessionId": "ses1", "seq": 1}, TT_BASE + 5
        )

        session.recover()

        assert h.pn.history_calls[0]["end"] == TT_BASE + 1

    def test_replays_from_where_recovery_began_though_a_live_message_arrives_meanwhile(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        answer_snapshot, answer = h.backend.deferred()
        h.backend.snapshot = answer_snapshot

        first = spawn(session.recover)
        wait_until(lambda: bool(h.backend.calls(SESSION_URL, "GET")))
        h.from_provider(type="exchange_progress", exchangeId="x", message="live")
        answer((200, {"sessionId": "ses1", "state": "ready", "exchanges": []}))
        first.result(timeout=2)
        h.backend.snapshot = lambda: (200, {"sessionId": "ses1", "state": "ready", "exchanges": []})
        session.recover()

        assert [c["end"] for c in h.pn.history_calls] == [TT_BASE + 1, TT_BASE + 2]

    def test_runs_overlapping_recoveries_one_after_another(self, h: Harness) -> None:
        session = h.open_ready()
        in_flight = 0
        most = 0
        lock = threading.Lock()

        def history(_: Any) -> List[Any]:
            nonlocal in_flight, most
            with lock:
                in_flight += 1
                most = max(most, in_flight)
            time.sleep(0.05)
            with lock:
                in_flight -= 1
            return []

        h.pn.history_impl = history

        both = [spawn(session.recover), spawn(session.recover)]
        for recovery in both:
            recovery.result(timeout=2)

        assert len(h.pn.history_calls) == 2
        assert most == 1

    def test_replays_every_history_page_in_order_each_message_once(self, h: Harness) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        progress = [
            history_item(i, type="exchange_progress", exchangeId=exchange_id, message=f"p{i}")
            for i in range(2, 252)
        ]
        h.pn.history_impl = history_of(
            [*progress, history_item(252, type="exchange_outcome", exchangeId=exchange_id, state="completed")]
        )
        seen: List[str] = []
        exchange.on_progress(lambda p: seen.append(p.message))

        session.recover()

        assert len(h.pn.history_calls) == 3
        assert all(call["include_uuid"] is True for call in h.pn.history_calls)
        assert seen == [m.message["message"] for m in progress]
        assert exchange.wait_for_outcome(1).state == "completed"

    def test_stops_after_ten_history_pages(self, h: Harness) -> None:
        session = h.open_ready()
        _, exchange_id = h.send(session)
        h.pn.history_impl = history_of(
            [
                history_item(i, type="exchange_progress", exchangeId=exchange_id, message="p")
                for i in range(2, 1502)
            ]
        )

        session.recover()

        assert len(h.pn.history_calls) == 10

    def test_raises_both_errors_when_history_and_the_snapshot_both_fail(self, h: Harness) -> None:
        session = h.open_ready()

        def unavailable(_: Any) -> List[Any]:
            raise RuntimeError("history 503")

        h.pn.history_impl = unavailable
        h.backend.snapshot = lambda: (500, {"error": "boom"})

        with pytest.raises(SessionRecoveryError, match="history 503") as raised:
            session.recover()
        assert len(raised.value.errors) == 2

    def test_counts_a_snapshot_of_another_session_as_a_failed_read(self, h: Harness) -> None:
        session = h.open_ready()

        def unavailable(_: Any) -> List[Any]:
            raise RuntimeError("history 503")

        h.pn.history_impl = unavailable
        h.backend.snapshot = lambda: (200, {"sessionId": "ses2", "state": "ready", "exchanges": []})

        with pytest.raises(SessionRecoveryError, match="another session"):
            session.recover()

    def test_delivers_an_artifact_behind_a_gap_only_history_reveals_before_the_record_settles(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        h.backend.snapshot = lambda: (
            200,
            {
                "sessionId": "ses1",
                "state": "ready",
                "exchanges": [
                    {"exchangeId": exchange_id, "state": "completed", "error": None, "artifactRefs": []}
                ],
            },
        )
        # Nothing is held live; history holds seq 3 but has lost seq 2.
        h.pn.history_impl = lambda _: [
            history_item(3, type="exchange_artifact", exchangeId=exchange_id, artifactRef=INLINE)
        ]

        session.recover()

        outcome = exchange.wait_for_outcome(0)
        assert (outcome.state, outcome.artifacts) == ("completed", [INLINE])

    def test_refuses_a_send_from_an_artifact_callback_while_the_session_is_ending(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        refusals: List[BaseException] = []

        def send_more(_: Any) -> None:
            try:
                session.send("more")
            except SessionError as err:
                refusals.append(err)

        exchange.on_artifact(send_more)
        # seq 2 is missing, so the artifact waits behind a gap until the session ends.
        h.provider_seq = 2
        h.from_provider(type="exchange_artifact", exchangeId=exchange_id, artifactRef=INLINE)

        h.from_backend(type="session_closed", reason="idle_timeout")

        assert [getattr(err, "code", None) for err in refusals] == ["session_not_ready"]
        assert h.pn.types().count("exchange") == 1
        assert exchange.wait_for_outcome(0).artifacts == [INLINE]

    def test_refuses_a_send_from_an_artifact_released_as_a_revocation_ends_a_recovery(
        self, h: Harness, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        monkeypatch.setattr(http_module, "SNAPSHOT_TIMEOUT_S", 5.0)
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        refusals: List[BaseException] = []

        def send_more(_: Any) -> None:
            try:
                session.send("more")
            except SessionError as err:
                refusals.append(err)

        exchange.on_artifact(send_more)
        # Held behind the missing seq 2 when recovery begins, so winding it down releases it.
        h.provider_seq = 2
        h.from_provider(type="exchange_artifact", exchangeId=exchange_id, artifactRef=INLINE)
        h.backend.snapshot = h.backend.hang()
        recovering = spawn(session.recover)
        wait_until(lambda: bool(h.backend.calls(SESSION_URL, "GET")))
        h.backend.tokens.append(lambda: (403, {"code": "SESSION_FORBIDDEN"}))

        h.pn.status("PNAccessDeniedCategory")
        h.scheduler.advance(0)

        assert session.state == "failed"
        assert [getattr(err, "code", None) for err in refusals] == ["session_not_ready"]
        assert h.pn.types().count("exchange") == 1
        assert exchange.wait_for_outcome(0).artifacts == [INLINE]
        h.backend.release_hangs()
        recovering.result(timeout=2)

    def test_ends_failed_not_closed_when_close_outlasts_a_recovery_holding_a_session_failed(
        self, h: Harness, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        # Longer than close()'s wait, so the recovery is still open when close() ends locally.
        monkeypatch.setattr(http_module, "SNAPSHOT_TIMEOUT_S", 5.0)
        session = h.open_ready()
        exchange, _ = h.send(session)
        h.backend.snapshot = h.backend.hang()
        recovering = spawn(session.recover)
        wait_until(lambda: bool(h.backend.calls(SESSION_URL, "GET")))
        h.from_backend(type="session_failed", error="route_lost")
        assert session.state == "ready"

        session.close()

        assert session.state == "failed"
        outcome = exchange.wait_for_outcome(0)
        assert (outcome.state, outcome.error) == ("failed", "route_lost")
        h.backend.release_hangs()
        recovering.result(timeout=2)

    def test_gives_up_on_history_that_stalls_and_settles_from_the_snapshot_alone(
        self, h: Harness, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        monkeypatch.setattr(session_module, "HISTORY_TIMEOUT_S", 0.1)
        session = h.open_ready()
        exchange, exchange_id = h.send(session)
        # Left unsettled by the record, so a late replay would reach its progress callback.
        still_open, still_open_id = h.send(session)
        late: List[Any] = []
        still_open.on_progress(late.append)
        stalled = threading.Event()

        def history(_: Any) -> List[Any]:
            stalled.wait(2)
            return [
                history_item(2, type="exchange_progress", exchangeId=still_open_id, message="late")
            ]

        h.pn.history_impl = history
        h.backend.snapshot = lambda: (
            200,
            {
                "sessionId": "ses1",
                "state": "ready",
                "exchanges": [
                    {"exchangeId": exchange_id, "state": "completed", "error": None, "artifactRefs": []}
                ],
            },
        )
        started = time.monotonic()
        session.recover()

        assert time.monotonic() - started < 1
        assert exchange.wait_for_outcome(0).state == "completed"
        stalled.set()
        wait_until(lambda: not any(t.name == "blocks-session-history" for t in threading.enumerate()))
        assert late == []

    def test_stops_paging_history_once_its_deadline_has_passed(
        self, h: Harness, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        monkeypatch.setattr(session_module, "HISTORY_TIMEOUT_S", 0.1)
        session = h.open_ready()
        _, exchange_id = h.send(session)
        full_page = [
            history_item(i, type="exchange_progress", exchangeId=exchange_id, message="p")
            for i in range(2, 102)
        ]
        page_may_answer = threading.Event()
        fetched_after_stall = threading.Event()

        def history(_: Any) -> List[Any]:
            if page_may_answer.is_set():
                fetched_after_stall.set()
                return []
            page_may_answer.wait(2)
            return full_page

        h.pn.history_impl = history

        session.recover()
        # Once recovery has given up, the stalled page answers full, which asks for the next.
        page_may_answer.set()
        wait_until(lambda: not any(t.name == "blocks-session-history" for t in threading.enumerate()))

        assert not fetched_after_stall.is_set()
        assert len(h.pn.history_calls) == 1

    def test_times_out_a_snapshot_whose_auth_preparation_stalls_and_applies_a_held_close(
        self, h: Harness
    ) -> None:
        session = h.open_ready()
        stalled = threading.Event()
        h.auth.ensure_ready = stalled.wait

        recovering = spawn(session.recover)
        wait_until(lambda: bool(h.pn.history_calls))
        h.from_backend(type="session_closed", reason="idle_timeout")
        assert session.state == "ready"
        recovering.result(timeout=2)

        assert session.state == "closed"
        stalled.set()
