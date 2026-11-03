"""Fakes for the session provider runtime: the route client, the provider API and time."""

from __future__ import annotations

import threading
import time
from collections import deque
from dataclasses import dataclass, field
from typing import Any, Callable, Deque, Dict, List, Optional

from blocks_network._session.provider_api import RouteGrant, RouteToken, SessionManifest
from blocks_network.protocol_version import CURRENT_PROTOCOL_VERSION

ROUTE_ID = "route1"
ROUTE_CHANNEL = f"sess.{ROUTE_ID}.*"
PROVIDER_UUID = "uuid-provider"
CONSUMER_UUID = "uuid-consumer"
BACKEND_UUID = "uuid-backend"
INSTANCE_ID = "AG-echo_agent-1"


def session_channel(session_id: str, route_id: str = ROUTE_ID) -> str:
    return f"sess.{route_id}.{session_id}"


def route_grant_body(route_id: str = ROUTE_ID, ttl_minutes: float = 720) -> Dict[str, Any]:
    return {
        "routeId": route_id,
        "routeChannel": f"sess.{route_id}.*",
        "pamToken": f"token-{route_id}",
        "ttlMinutes": ttl_minutes,
        "providerTransportUuid": PROVIDER_UUID,
        "backendTransportUuid": BACKEND_UUID,
    }


def route_grant(route_id: str = ROUTE_ID, ttl_minutes: float = 720) -> RouteGrant:
    return RouteGrant(
        route_id=route_id,
        route_channel=f"sess.{route_id}.*",
        pam_token=f"token-{route_id}",
        ttl_minutes=ttl_minutes,
        provider_transport_uuid=PROVIDER_UUID,
        backend_transport_uuid=BACKEND_UUID,
    )


def manifest_body(session_id: str, route_id: str = ROUTE_ID) -> Dict[str, Any]:
    return {
        "sessionId": session_id,
        "routeId": route_id,
        "consumerTransportUuid": CONSUMER_UUID,
        "agentName": "echo_agent",
        "orgId": "org-1",
        "ownerId": "user-1",
        "callerClaims": {"sub": "user-1"},
    }


def manifest(session_id: str, route_id: str = ROUTE_ID) -> SessionManifest:
    return SessionManifest(
        session_id=session_id,
        route_id=route_id,
        consumer_transport_uuid=CONSUMER_UUID,
        agent_name="echo_agent",
        org_id="org-1",
        owner_id="user-1",
        caller_claims={"sub": "user-1"},
    )


def from_consumer(session_id: str, seq: int, message_type: str, **fields: Any) -> Dict[str, Any]:
    return {
        "type": message_type,
        "protocolVersion": CURRENT_PROTOCOL_VERSION,
        "sessionId": session_id,
        "seq": seq,
        "ts": 0,
        **fields,
    }


def text_parts(text: str) -> List[Dict[str, Any]]:
    return [{"text": text}]


def wait_for(predicate: Callable[[], Any], timeout: float = 3.0) -> None:
    deadline = time.monotonic() + timeout
    while not predicate():
        if time.monotonic() > deadline:
            raise AssertionError("condition not met in time")
        time.sleep(0.005)


_thread = threading.local()


class Activity:
    """Counts runtime threads and queued publishes still working; a thread waiting on a test
    ``Gate`` is parked, not working. ``wait_idle`` lets a test assert that nothing happened."""

    def __init__(self) -> None:
        self._idle = threading.Condition()
        self._working = 0

    def _change(self, delta: int) -> None:
        with self._idle:
            self._working += delta
            self._idle.notify_all()

    def tracked(self, fn: Callable[[], Any]) -> Callable[[], Any]:
        self._change(1)

        def run() -> Any:
            outer = getattr(_thread, "activity", None)
            _thread.activity = self
            try:
                return fn()
            finally:
                _thread.activity = outer
                self._change(-1)

        return run

    def park(self) -> None:
        self._change(-1)

    def unpark(self) -> None:
        self._change(1)

    def wait_idle(self, timeout: float = 3.0) -> None:
        with self._idle:
            if not self._idle.wait_for(lambda: self._working == 0, timeout):
                raise AssertionError(f"{self._working} runtime tasks still working")


def wait_forever(*_: Any) -> None:
    Gate().wait(timeout=None)


class Gate:
    """Blocks callers of ``wait`` until ``open``. A tracked runtime thread waiting here is
    parked, and ``open`` counts it as working again before it wakes, so ``wait_idle`` cannot
    slip in between."""

    def __init__(self) -> None:
        self._event = threading.Event()
        self._lock = threading.Lock()
        self._parked: List[Activity] = []

    def open(self) -> None:
        with self._lock:
            parked, self._parked = self._parked, []
            for activity in parked:
                activity.unpark()
            self._event.set()

    def wait(self, timeout: Optional[float] = 5.0) -> None:
        activity = getattr(_thread, "activity", None)
        with self._lock:
            if self._event.is_set():
                return
            if activity is not None:
                activity.park()
                self._parked.append(activity)
        if self._event.wait(timeout):
            return
        with self._lock:
            if activity is not None and activity in self._parked:
                self._parked.remove(activity)
                activity.unpark()
        raise AssertionError("gate never opened")


@dataclass
class _Timer:
    due_ms: float
    callback: Callable[[], None]
    canceled: bool = False

    def cancel(self) -> None:
        self.canceled = True


class FakeScheduler:
    """A thread-safe Scheduler whose time moves only on ``advance``; callbacks run on the
    caller of ``advance``."""

    def __init__(self) -> None:
        self.now_ms = 0.0
        self._lock = threading.Lock()
        self._timers: List[_Timer] = []

    def call_later(self, delay_s: float, callback: Callable[[], None]) -> _Timer:
        with self._lock:
            timer = _Timer(self.now_ms + delay_s * 1000, callback)
            self._timers.append(timer)
            return timer

    def last(self) -> _Timer:
        with self._lock:
            return self._timers[-1]

    def pending(self) -> int:
        with self._lock:
            return sum(1 for t in self._timers if not t.canceled)

    def advance(self, ms: float) -> None:
        target = self.now_ms + ms
        while True:
            with self._lock:
                due = [t for t in self._timers if not t.canceled and t.due_ms <= target]
                if not due:
                    self.now_ms = target
                    return
                timer = min(due, key=lambda t: t.due_ms)
                self._timers.remove(timer)
                self.now_ms = timer.due_ms
            timer.callback()


@dataclass
class Published:
    channel: str
    message: Dict[str, Any]
    meta: Dict[str, Any]


class FakeRouteClient:
    def __init__(self, user_id: str, auto_connect: bool = True) -> None:
        self.user_id = user_id
        self.auto_connect = auto_connect
        self.tokens: List[str] = []
        # Every subscribe call, and the channels currently subscribed.
        self.subscribed: List[str] = []
        self.active: set = set()
        self.closed = False
        self.closes = 0
        self.published: List[Published] = []
        self.subscribe_error: Optional[BaseException] = None
        self.token_error: Optional[BaseException] = None
        # "listen", "subscribe" and "close", in call order.
        self.lifecycle: List[str] = []
        self.before_listen: Callable[[], None] = lambda: None
        self.after_listen: Callable[[], None] = lambda: None
        self.before_subscribe: Callable[[], None] = lambda: None
        self.publish_attempts: List[Dict[str, Any]] = []
        # Called before a publish lands; raise to fail it, block to hold it.
        self.before_publish: Callable[[Dict[str, Any]], None] = lambda _message: None
        self._on_message: Any = None
        self._on_status: Any = None

    def set_token(self, token: str) -> None:
        if self.token_error is not None:
            raise self.token_error
        self.tokens.append(token)

    def listen(self, on_message: Any, on_status: Any) -> None:
        self.before_listen()
        self.lifecycle.append("listen")
        self._on_message, self._on_status = on_message, on_status
        self.after_listen()

    def subscribe(self, channel: str) -> None:
        """Only the first subscribe connects by itself; a test connects a later one."""
        self.before_subscribe()
        if self.subscribe_error is not None:
            raise self.subscribe_error
        self.lifecycle.append("subscribe")
        self.subscribed.append(channel)
        self.active.add(channel)
        if self.auto_connect and len(self.subscribed) == 1:
            self.connect()

    @property
    def resubscribes(self) -> int:
        return max(len(self.subscribed) - 1, 0)

    def connect(self) -> None:
        self.status("connected")

    def status(self, category: str) -> None:
        if category == "access_denied":
            # As pubnub-python does on a 403: every subscription is dropped.
            self.active.clear()
        self._on_status(category)

    def publish(self, channel: str, message: Dict[str, Any], meta: Dict[str, Any]) -> None:
        self.publish_attempts.append(message)
        self.before_publish(message)
        self.published.append(Published(channel, message, meta))

    def download_artifact(self, ref: Any) -> bytes:
        raise NotImplementedError

    def close(self, channel: str) -> None:
        self.lifecycle.append("close")
        self.closes += 1
        self.closed = True
        self.active.clear()

    def deliver(self, channel: str, publisher: str, message: Any) -> None:
        self._on_message(channel, publisher, message)

    def of_type(self, message_type: str) -> List[Dict[str, Any]]:
        return [p.message for p in list(self.published) if p.message["type"] == message_type]

    def outcomes(self) -> List[tuple]:
        return [
            (m["exchangeId"], m["state"], m.get("error")) for m in self.of_type("exchange_outcome")
        ]


@dataclass
class FakeApi:
    """Answers from per-method queues of results (an exception is raised), else a default."""

    registered: List[tuple] = field(default_factory=list)
    refreshed: List[str] = field(default_factory=list)
    looked_up: List[str] = field(default_factory=list)
    checkpoints: List[Dict[str, Any]] = field(default_factory=list)
    register_answers: Deque[Any] = field(default_factory=deque)
    refresh_answers: Deque[Any] = field(default_factory=deque)
    refresh_default: Any = None
    lookup_answers: Deque[Any] = field(default_factory=deque)
    checkpoint_answers: Deque[Any] = field(default_factory=deque)

    @staticmethod
    def _answer(answers: Deque[Any], default: Any) -> Any:
        answer = answers.popleft() if answers else default
        if callable(answer):
            answer = answer()
        if isinstance(answer, BaseException):
            raise answer
        return answer

    def register_route(self, instance_id: str, max_sessions: int) -> RouteGrant:
        self.registered.append((instance_id, max_sessions))
        return self._answer(self.register_answers, route_grant())

    def refresh_route_token(self, route_id: str) -> RouteToken:
        self.refreshed.append(route_id)
        return self._answer(self.refresh_answers, self.refresh_default)

    def get_session(self, session_id: str) -> SessionManifest:
        self.looked_up.append(session_id)
        return self._answer(self.lookup_answers, manifest(session_id))

    def post_checkpoint(self, batch: Dict[str, Any]) -> None:
        self.checkpoints.append(batch)
        self._answer(self.checkpoint_answers, None)

    def events(self) -> List[Dict[str, Any]]:
        return [event for batch in list(self.checkpoints) for event in batch["events"]]

    def probe_echoes(self) -> List[str]:
        return [nonce for batch in list(self.checkpoints) for nonce in batch["probeEchoes"]]
