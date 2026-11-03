"""start_agent_instance with session handlers: the backend's HTTP and both PubNub clients are
faked, and the route client goes through the real PubNub adapter."""

from __future__ import annotations

import json
import threading
from types import SimpleNamespace
from typing import Any, Dict, List

import pytest

from blocks_network import agent_instance as ai
from blocks_network.agent_auth import AgentAuth, _HttpError
from blocks_network.agent_registry import ConnectAgentResult
from blocks_network.types import AgentInstanceOptions

from tests.conftest import minimal_card
from tests.session_provider_fakes import (
    CONSUMER_UUID,
    PROVIDER_UUID,
    from_consumer,
    manifest_body,
    route_grant_body,
    session_channel,
    wait_for,
)

CONTROL_CHANNEL = "agent.cccccccc.control"
AGENT_JWT = "agent-jwt"


class _Chain:
    """One PubNub builder call: records its arguments and runs ``on_done`` at the end."""

    def __init__(self, on_done) -> None:
        self.args: Dict[str, Any] = {}
        self._on_done = on_done

    def __getattr__(self, name: str):
        if name in ("sync", "execute"):
            return lambda: self._on_done(self.args)
        return lambda value=None: (self.args.__setitem__(name, value), self)[1]


class FakePubNub:
    def __init__(
        self, user_id: str, subscribe_key: str = "sub-injected", publish_key: str = "pub-injected"
    ) -> None:
        self.user_id = user_id
        self.config = SimpleNamespace(publish_key=publish_key, subscribe_key=subscribe_key)
        self.listeners: List[Any] = []
        self.subscribed: List[List[str]] = []
        self.published: List[Dict[str, Any]] = []
        self.tokens: List[str] = []

    def add_listener(self, listener: Any) -> None:
        self.listeners.append(listener)

    def remove_listener(self, listener: Any) -> None:
        if listener in self.listeners:
            self.listeners.remove(listener)

    def set_token(self, token: str) -> None:
        self.tokens.append(token)

    def set_filter_expression(self, _expression: str) -> None:
        pass

    def subscribe(self) -> _Chain:
        def done(args):
            self.subscribed.append(args["channels"])
            self.status("PNConnectedCategory")

        return _Chain(done)

    def unsubscribe(self) -> _Chain:
        return _Chain(lambda _args: None)

    def publish(self) -> _Chain:
        return _Chain(lambda args: self.published.append(args["message"]))

    def set_state(self) -> _Chain:
        return _Chain(lambda _args: None)

    def stop(self) -> None:
        pass

    def status(self, category: str) -> None:
        for listener in list(self.listeners):
            listener.status(self, SimpleNamespace(category=category))

    def deliver(self, channel: str, publisher: str, message: Any) -> None:
        event = SimpleNamespace(
            channel=channel, publisher=publisher, message=message, user_metadata={}
        )
        for listener in list(self.listeners):
            listener.message(self, event)

    def of_type(self, message_type: str) -> List[Dict[str, Any]]:
        return [
            m for m in list(self.published) if isinstance(m, dict) and m.get("type") == message_type
        ]


@pytest.fixture
def backend(monkeypatch):
    """Fakes connect and the agent-JWT HTTP calls; ``route_answer`` is (status, body)."""
    state: Dict[str, Any] = {"calls": [], "route_answer": (201, route_grant_body())}

    def fake_connect(_agent_name, options):
        options.agent_auth._access_token = AGENT_JWT
        return ConnectAgentResult(agent_id="agent-1", control_channel=CONTROL_CHANNEL)

    def fake_request(_self, url, method="GET", body=None, headers=None):
        path = url.replace("http://backend.test", "")
        sent = json.loads(body) if body else None
        state["calls"].append(
            {
                "method": method,
                "path": path,
                "authorization": headers.get("Authorization"),
                "body": sent,
            }
        )
        if path == "/api/v1/provider/routes":
            status, answer = state["route_answer"]
            if status >= 300:
                raise _HttpError(status, answer)
            return answer, status
        if path == "/api/v1/provider/checkpoints":
            return {"routeId": sent["routeId"], "idempotencyKey": sent["idempotencyKey"]}, 200
        return {}, 200

    route_clients: List[FakePubNub] = []

    def fake_create_pubnub_client(**kwargs):
        client = FakePubNub(kwargs.get("user_id", ""), kwargs.get("subscribe_key", ""))
        client.created_with = kwargs
        route_clients.append(client)
        return client

    monkeypatch.setattr("blocks_network.agent_registry.connect_agent", fake_connect)
    monkeypatch.setattr(AgentAuth, "_request", fake_request)
    monkeypatch.setattr(ai, "create_pubnub_client", fake_create_pubnub_client)
    state["route_clients"] = route_clients
    handles: List[Any] = []
    state["handles"] = handles
    yield state
    for handle in handles:
        handle.stop()


def _start(backend, control: FakePubNub, **options: Any):
    handle = ai.start_agent_instance(
        AgentInstanceOptions(
            card=minimal_card(),
            pubnub=control,
            agent_name="echo_agent",
            base_url="http://backend.test",
            **options,
        )
    )
    backend["handles"].append(handle)
    return handle


def _route_calls(backend) -> List[Dict[str, Any]]:
    return [c for c in backend["calls"] if c["path"] == "/api/v1/provider/routes"]


def _route_client(backend) -> FakePubNub:
    return next(c for c in backend["route_clients"] if c.user_id == PROVIDER_UUID)


def _wait_for_route(backend) -> FakePubNub:
    """The route listens once it has subscribed; its client exists slightly earlier."""
    wait_for(
        lambda: any(c.user_id == PROVIDER_UUID and c.subscribed for c in backend["route_clients"])
    )
    return _route_client(backend)


def test_opens_a_route_with_the_agent_jwt_and_serves_a_session_from_a_control_manifest(
    backend,
) -> None:
    control = FakePubNub("control")
    _start(
        backend, control, instance_id="AG-echo_agent-1", concurrency=3, on_exchange=lambda *_: {}
    )

    _wait_for_route(backend)
    assert _route_calls(backend) == [
        {
            "method": "POST",
            "path": "/api/v1/provider/routes",
            "authorization": f"Bearer {AGENT_JWT}",
            "body": {"instanceId": "AG-echo_agent-1", "maxSessions": 3},
        }
    ]

    control.deliver(
        CONTROL_CHANNEL, "uuid-backend", {"type": "SessionManifest", **manifest_body("s1")}
    )
    route = _route_client(backend)
    route.deliver(session_channel("s1"), CONSUMER_UUID, from_consumer("s1", 1, "session_open"))

    wait_for(lambda: len(route.of_type("session_ready")) == 1)
    assert not any(c["path"].startswith("/api/v1/provider/sessions/") for c in backend["calls"])


def test_keeps_serving_tasks_when_the_backend_refuses_the_route_with_sessions_disabled(
    backend,
) -> None:
    backend["route_answer"] = (404, {"error": "off", "code": "SESSIONS_DISABLED"})
    control = FakePubNub("control")
    handled = threading.Event()
    handle = _start(
        backend,
        control,
        handler=lambda *_: handled.set(),
        on_exchange=lambda *_: {},
    )

    wait_for(lambda: _route_calls(backend))
    start_task = {
        "type": "StartTask",
        "taskId": "t1",
        "agentName": "echo_agent",
        "ownerId": "user-1",
        "taskKind": "request",
        "hasStream": False,
    }
    event = SimpleNamespace(message=start_task, user_metadata={"instance": handle.instance_id})
    control.listeners[0].message(control, event)

    assert handled.wait(3)
    assert not any(c.user_id == PROVIDER_UUID for c in backend["route_clients"])
    assert len(_route_calls(backend)) == 1


def test_opens_no_route_when_stopped_before_connect_returns(backend, monkeypatch) -> None:
    connected = threading.Event()
    release = threading.Event()
    connect_threads: List[threading.Thread] = []

    def slow_connect(_agent_name, options):
        options.agent_auth._access_token = AGENT_JWT
        connect_threads.append(threading.current_thread())
        connected.set()
        release.wait(3)
        return ConnectAgentResult(agent_id="agent-1", control_channel=CONTROL_CHANNEL)

    monkeypatch.setattr("blocks_network.agent_registry.connect_agent", slow_connect)
    control = FakePubNub("control")
    handle = ai.start_agent_instance(
        AgentInstanceOptions(
            card=minimal_card(),
            pubnub=control,
            agent_name="echo_agent",
            base_url="http://backend.test",
            on_exchange=lambda *_: {},
        )
    )
    assert connected.wait(3)
    handle.stop()
    release.set()

    # The connect thread subscribes and then would start the provider, so wait for it to end.
    connect_threads[0].join(3)
    assert control.subscribed
    assert _route_calls(backend) == []


def test_builds_the_route_client_from_an_injected_clients_keys(backend) -> None:
    control = FakePubNub("control")
    _start(backend, control, on_exchange=lambda *_: {})

    _wait_for_route(backend)
    created = _route_client(backend).created_with
    assert (created["publish_key"], created["subscribe_key"]) == ("pub-injected", "sub-injected")


@pytest.mark.parametrize("missing", ["subscribe_key", "publish_key"])
def test_serves_tasks_only_when_an_injected_client_lacks_a_key(
    backend, monkeypatch, missing
) -> None:
    warnings: List[str] = []

    def capture(level, message, **fields):
        if fields.get("event") == "session_provider_unavailable":
            warnings.append(message)

    monkeypatch.setattr(ai, "log_agent_instance_event", capture)
    control = FakePubNub("control", **{missing: ""})
    _start(backend, control, on_exchange=lambda *_: {})

    wait_for(lambda: warnings)
    assert _route_calls(backend) == []
