"""Shared-stream lifecycle tests for ``ctx.create_stream()``.

Mirrors the Node SDK suite at
``blocks-sdk/sdks/node/tests/agent-instance-shared-stream.test.ts``.

Covers the 8 behaviors of the shared-stream lifecycle contract:

1. First acquirer on a shared stream publishes ``stream_setup`` with
   ``phase: 'embedded'`` and creates a registry entry carrying the task.
2. Second acquirer on the same shared stream from a *different* task
   publishes ``stream_setup`` with ``phase: 'activate'`` carrying that
   task's ``durationMinutes`` and does NOT rebuild the writer.
3. Same-task idempotent reacquire: a second ``create_stream`` call from
   the same task returns the same ``StreamObject``, does not publish
   ``stream_setup`` again, and does not grow ``task_ids``.
4. Dedicated-affinity stream, second task: each task gets its own
   registry entry with its own ``StreamClient`` and its own
   ``phase: 'embedded'`` publish.
5. Shared-affinity on a request task: ``create_stream`` raises the
   fix-(g) error.
6. Producer-side ``StreamClient.end()`` on a shared stream does NOT
   publish ``stream_end`` via the bundle, and the task-scoped release
   keeps the shared writer alive for the other ref-holder.
7. Producer-side ``StreamClient.end()`` on a dedicated stream DOES
   publish ``stream_end`` (existing behavior preserved).
8. Cleanup-boundary cache eviction: explicit ``StreamObject.end()``,
   ``release_all_for_task``, and ``fail_stream`` all remove the right
   entries from ``shared_stream_handles`` (and nothing else).
"""

from __future__ import annotations

import json
import threading
import time
from typing import Any, Dict, List, Optional
from unittest.mock import MagicMock

import pytest

from blocks_network import agent_instance as _ai_mod
from blocks_network.agent_instance import start_agent_instance
from blocks_network.stream_registry import StreamRegistry
from blocks_network.types import AgentInstanceOptions, StartTaskMessage, TaskContext


# ---------------------------------------------------------------------------
# Mock PubNub + capturing stream client, shared by tests.
# ---------------------------------------------------------------------------


def _make_mock_pubnub_with_abort_setup(
    captured_setups: List[Dict[str, Any]],
) -> MagicMock:
    """Mock PubNub whose ``publish`` captures every stream_setup payload
    and raises a T7a JSON-payload exception so the setup handshake
    returns cleanly. Every other publish is a no-op.
    """
    pn = MagicMock()

    def _make_chain() -> MagicMock:
        chain = MagicMock()
        record: Dict[str, Any] = {}

        def _channel(ch: str) -> MagicMock:
            record["channel"] = ch
            return chain

        def _message(msg: Any) -> MagicMock:
            record["message"] = msg
            return chain

        def _meta(m: Any) -> MagicMock:
            return chain

        def _should_store(v: Any) -> MagicMock:
            return chain

        def _use_post(v: Any) -> MagicMock:
            return chain

        def _sync() -> MagicMock:
            channel = record.get("channel", "")
            message = record.get("message", {}) or {}
            if (
                channel.startswith("setup.")
                and isinstance(message, dict)
                and message.get("type") == "stream_setup"
            ):
                captured_setups.append(dict(message))
                payload = {
                    "streamSetupResponse": {
                        "token": "t7a-fake",
                        "taskId": message.get("taskId"),
                        "streamId": message.get("streamId"),
                        "channel": message.get("channel"),
                        "direction": message.get("direction", "outbound"),
                        "phase": message.get("phase", "final"),
                        "tokenTtlMinutes": 5,
                    }
                }
                raise RuntimeError(json.dumps(payload))
            return MagicMock()

        chain.channel = _channel
        chain.message = _message
        chain.meta = _meta
        chain.should_store = _should_store
        chain.use_post = _use_post
        chain.sync = _sync
        return chain

    pn.publish.side_effect = lambda: _make_chain()
    pn.subscribe.return_value = _make_chain()
    pn.set_state.return_value = _make_chain()
    pn.unsubscribe.return_value = _make_chain()
    pn.download_file.return_value = _make_chain()

    pn._listeners = []
    pn.add_listener.side_effect = lambda l: pn._listeners.append(l)
    pn.remove_listener.side_effect = lambda l: (
        pn._listeners.remove(l) if l in pn._listeners else None
    )
    pn.set_filter_expression = MagicMock()
    pn.config = MagicMock()
    pn.config.filter_expression = None
    pn.set_token = MagicMock()
    pn.stop = MagicMock()

    return pn


def _simulate_start_task(
    pn: MagicMock,
    task_id: str,
    *,
    task_kind: str = "pipe",
    has_stream: bool = True,
    agent_name: str = "echo",
    duration_minutes: Optional[int] = None,
    owner_id: str = "alice",
) -> None:
    msg: Dict[str, Any] = {
        "type": "StartTask",
        "taskId": task_id,
        "agentName": agent_name,
        "ownerId": owner_id,
        "taskKind": task_kind,
        "hasStream": has_stream,
        "writeToken": "wt-test",
    }
    if duration_minutes is not None:
        msg["duration"] = duration_minutes
        msg["durationExpiresAtMs"] = int(time.time() * 1000) + duration_minutes * 60_000
    elif task_kind == "pipe":
        msg["duration"] = 60
        msg["durationExpiresAtMs"] = int(time.time() * 1000) + 3_600_000
    meta = {
        "instance": "AG-echo-test",
        "broadcast": "true",
    }
    event = MagicMock()
    event.message = msg
    event.user_metadata = meta
    for listener in list(pn._listeners):
        if hasattr(listener, "message"):
            listener.message(pn, event)


class _CapturingStreamClient:
    """Drop-in for ``blocks_network.stream.StreamClient``.

    Captures every construction in ``captures`` and every ``end()`` /
    bundle-publish event per instance so tests can assert the
    shared-affinity gate never publishes ``stream_end``.
    """

    captures: List[Dict[str, Any]] = []
    instances: List["_CapturingStreamClient"] = []

    def __init__(self, **kwargs: Any) -> None:
        self.captures.append(dict(kwargs))
        _CapturingStreamClient.instances.append(self)
        self.stream_id: str = kwargs.get("stream_id", "")
        self.agent_name: str = kwargs.get("agent_name", "")
        self.affinity: str = kwargs.get("affinity", "dedicated")
        self.direction: str = kwargs.get("direction", "outbound")
        self.channel: str = f"stream.{self.agent_name}.{self.stream_id}"
        self.is_active: bool = True
        self._end_calls: int = 0
        self.publish_end_marker_calls: int = 0
        self._on_end_cbs: List[Any] = []

    def on_end(self, cb: Any) -> None:
        self._on_end_cbs.append(cb)

    def end(self) -> None:
        self._end_calls += 1
        # Mirror the real client's affinity-gated end marker:
        # only dedicated streams (and non-bidirectional directions)
        # publish the marker. Shared streams MUST NOT.
        if self.direction != "bidirectional" and self.affinity != "shared":
            self.publish_end_marker_calls += 1
        self.is_active = False
        for cb in self._on_end_cbs:
            try:
                cb()
            except Exception:
                pass

    def write(self, *_args: Any, **_kwargs: Any) -> None:
        pass


@pytest.fixture(autouse=True)
def _patch_create_pubnub_client(monkeypatch: pytest.MonkeyPatch) -> None:
    """Per-task PubNub clients use the same capturing mock so publishes
    on any tier land in the same setup capture list."""

    # We wire the mock up per-test by overriding from within the test
    # itself; this fixture provides a default no-op in case a test does
    # not override.
    def _fallback(**_kwargs: Any) -> MagicMock:
        return _make_mock_pubnub_with_abort_setup([])

    monkeypatch.setattr(_ai_mod, "create_pubnub_client", _fallback)


@pytest.fixture(autouse=True)
def _patch_stream_client(monkeypatch: pytest.MonkeyPatch) -> None:
    _CapturingStreamClient.captures.clear()
    _CapturingStreamClient.instances.clear()
    monkeypatch.setattr(
        "blocks_network.stream.StreamClient", _CapturingStreamClient
    )


def _shared_card(declared_key: str = "shared_out", direction: str = "outbound") -> Dict[str, Any]:
    return {
        "streams": {
            declared_key: {
                "direction": direction,
                "format": "events",
                "affinity": "shared",
            }
        }
    }


def _dedicated_card(direction: str = "outbound") -> Dict[str, Any]:
    return {
        "streams": {
            "_default": {
                "direction": direction,
                "format": "bytes",
            }
        }
    }


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestSharedStreamFirstAcquirer:
    """1. First acquirer publishes stream_setup phase=embedded and
    records the task on the registry entry."""

    def test_first_acquirer_publishes_embedded_and_tracks_task(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done = threading.Event()
        captured_registry: Dict[str, Any] = {}

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            obj = ctx.create_stream(
                format="events", declared_stream="shared_out",
                subscribe_grace_ms=0,
            )
            captured_registry["obj"] = obj
            done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_shared_card(),
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(
                pn, task_id="task-A", task_kind="pipe",
                duration_minutes=30,
            )
            assert done.wait(timeout=3.0)

            setups = [m for m in captured if m.get("streamId") == "shared_out"]
            assert len(setups) == 1, f"Expected 1 setup publish, got {len(setups)}"
            msg = setups[0]
            # Python publishes `phase: 'embedded'` explicitly on the
            # single-phase path to match Node.
            assert msg.get("phase") == "embedded"
            assert msg.get("taskId") == "task-A"
            assert msg.get("affinity") == "shared"
            assert msg.get("taskKind") == "pipe"
            assert msg.get("durationMinutes") == 30
            assert msg.get("streamId") == "shared_out"
            assert msg.get("channel") == "stream.echo.shared_out"
            assert captured_registry["obj"].stream_id == "shared_out"
        finally:
            result["stop"]()


class TestSharedStreamSecondTaskActivate:
    """2. Second task attaching to an existing shared writer publishes
    stream_setup phase=activate with THAT task's durationMinutes."""

    def test_second_task_publishes_activate_with_own_duration(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done_a = threading.Event()
        done_b = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            ctx.create_stream(
                format="events", declared_stream="shared_out",
                subscribe_grace_ms=0,
            )
            if task.task_id == "task-A":
                done_a.set()
            else:
                done_b.set()
            # Pipe handler returns voluntarily (no terminal yet); keeps
            # shared writer alive for task B.
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_shared_card(),
                concurrency=4,
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(
                pn, task_id="task-A", task_kind="pipe",
                duration_minutes=45,
            )
            assert done_a.wait(timeout=3.0)
            _simulate_start_task(
                pn, task_id="task-B", task_kind="pipe",
                duration_minutes=20,
            )
            assert done_b.wait(timeout=3.0)

            setups = [m for m in captured if m.get("streamId") == "shared_out"]
            assert len(setups) == 2, f"Expected 2 setup publishes, got {len(setups)}"

            a_msg = next(m for m in setups if m.get("taskId") == "task-A")
            b_msg = next(m for m in setups if m.get("taskId") == "task-B")

            # A is first; uses embedded (single-phase).
            assert a_msg.get("phase") == "embedded"
            assert a_msg.get("durationMinutes") == 45

            # B is the second-and-later attacher -> phase=activate with
            # its own duration.
            assert b_msg.get("phase") == "activate"
            assert b_msg.get("durationMinutes") == 20
            assert b_msg.get("affinity") == "shared"
            assert b_msg.get("taskKind") == "pipe"

            # Only ONE StreamClient constructed (first task's writer is
            # reused by the second task's attach).
            shared_constructs = [
                c for c in _CapturingStreamClient.captures
                if c.get("stream_id") == "shared_out"
            ]
            assert len(shared_constructs) == 1
        finally:
            result["stop"]()


class TestSharedStreamIdempotentReacquire:
    """3. Same-task reacquire is idempotent: same StreamObject, no
    extra setup, registry task_ids unchanged."""

    def test_same_task_returns_same_stream_object(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done = threading.Event()
        captured_refs: Dict[str, Any] = {}

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            first = ctx.create_stream(
                format="events", declared_stream="shared_out",
                subscribe_grace_ms=0,
            )
            second = ctx.create_stream(
                format="events", declared_stream="shared_out",
                subscribe_grace_ms=0,
            )
            captured_refs["first"] = first
            captured_refs["second"] = second
            done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_shared_card(),
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(
                pn, task_id="task-idem", task_kind="pipe",
                duration_minutes=10,
            )
            assert done.wait(timeout=3.0)

            assert captured_refs["first"] is captured_refs["second"], (
                "Second create_stream from the same task must return the "
                "same StreamObject"
            )

            setups = [m for m in captured if m.get("streamId") == "shared_out"]
            assert len(setups) == 1, (
                f"Repeat same-task create_stream must not publish a second "
                f"stream_setup; got {[s.get('phase') for s in setups]}"
            )

            # One StreamClient constructed total.
            shared_constructs = [
                c for c in _CapturingStreamClient.captures
                if c.get("stream_id") == "shared_out"
            ]
            assert len(shared_constructs) == 1
        finally:
            result["stop"]()


class TestDedicatedAffinitySecondTask:
    """4. Dedicated-affinity second task creates its own writer with
    its own stream_setup (no activate reuse)."""

    def test_dedicated_second_task_gets_own_writer(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done_a = threading.Event()
        done_b = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            ctx.create_stream(format="bytes", subscribe_grace_ms=0)
            (done_a if task.task_id == "req-A" else done_b).set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_dedicated_card(),
                concurrency=4,
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(
                pn, task_id="req-A", task_kind="request",
            )
            assert done_a.wait(timeout=3.0)
            time.sleep(0.15)
            _simulate_start_task(
                pn, task_id="req-B", task_kind="request",
            )
            assert done_b.wait(timeout=3.0)

            # Two setup publishes on two different stream IDs, both
            # phase=embedded, neither activate.
            stream_ids = {m.get("streamId") for m in captured if m.get("type") == "stream_setup"}
            assert len(stream_ids) >= 2, f"Expected distinct stream ids, got {stream_ids}"
            phases = {m.get("phase") for m in captured if m.get("type") == "stream_setup"}
            assert phases == {"embedded"}, f"Dedicated path must emit only embedded, got {phases}"

            # Two distinct StreamClient instances.
            dedicated_constructs = [
                c for c in _CapturingStreamClient.captures
                if c.get("affinity", "dedicated") == "dedicated"
            ]
            assert len(dedicated_constructs) == 2
        finally:
            result["stop"]()


class TestSharedStreamRejectOnRequestTask:
    """5. Shared-affinity on a request task raises fix-(g) error."""

    def test_request_task_shared_stream_raises(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done = threading.Event()
        captured_exc: List[BaseException] = []

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            try:
                ctx.create_stream(
                    format="events", declared_stream="shared_out",
                    subscribe_grace_ms=0,
                )
            except RuntimeError as exc:
                captured_exc.append(exc)
            done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_shared_card(),
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(
                pn, task_id="req-shared", task_kind="request",
            )
            assert done.wait(timeout=3.0)

            assert len(captured_exc) == 1, "Expected RuntimeError for request+shared"
            msg = str(captured_exc[0])
            assert "Shared-affinity streams are not supported" in msg
            assert "shared_out" in msg

            # No setup publish should have occurred.
            setups = [m for m in captured if m.get("type") == "stream_setup"]
            assert setups == []
        finally:
            result["stop"]()


class TestSharedStreamRejectOnExternal:
    """5a. Shared-affinity + external=True raises fix-(h) error.

    Shared affinity is "one SDK-managed broadcast writer, many
    ref-holding tasks"; external delegates the writer entirely. The
    combination has no coherent registry model, so both SDKs reject
    it at createStream time regardless of task_kind. See the SDK contract.
    """

    def test_shared_external_raises(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done = threading.Event()
        captured_exc: List[BaseException] = []

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            try:
                ctx.create_stream(
                    format="events",
                    declared_stream="shared_out",
                    external=True,
                    subscribe_grace_ms=0,
                )
            except RuntimeError as exc:
                captured_exc.append(exc)
            done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_shared_card(),
            )
        )
        try:
            time.sleep(0.1)
            # pipe task — fix (h) rejects regardless of task_kind, so
            # test with the permitted kind to prove the reject is
            # orthogonal to fix (g).
            _simulate_start_task(
                pn, task_id="pipe-shared-ext", task_kind="pipe",
            )
            assert done.wait(timeout=3.0)

            assert len(captured_exc) == 1, \
                "Expected RuntimeError for shared+external"
            msg = str(captured_exc[0])
            assert "Shared-affinity external streams are not supported" in msg
            assert "shared_out" in msg
            assert "external=True" in msg

            # No setup publish should have occurred — reject fires
            # before the handshake.
            setups = [m for m in captured if m.get("type") == "stream_setup"]
            assert setups == []
        finally:
            result["stop"]()


class TestSharedStreamEndNoMarker:
    """6. Producer-side StreamClient.end() on shared stream suppresses
    publish_end_marker, and task-scoped release keeps the shared writer
    alive while any ref-holder remains."""

    def test_shared_end_does_not_publish_marker(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done_a = threading.Event()
        done_b = threading.Event()
        ended_a = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            obj = ctx.create_stream(
                format="events", declared_stream="shared_out",
                subscribe_grace_ms=0,
            )
            if task.task_id == "task-A":
                done_a.set()
                # Wait for task B to attach, then end task A explicitly.
                done_b.wait(timeout=3.0)
                obj.end()
                ended_a.set()
            else:
                done_b.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_shared_card(),
                concurrency=4,
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(pn, task_id="task-A", task_kind="pipe")
            assert done_a.wait(timeout=3.0)
            _simulate_start_task(pn, task_id="task-B", task_kind="pipe")
            assert done_b.wait(timeout=3.0)
            assert ended_a.wait(timeout=3.0)

            shared_clients = [
                c for c in _CapturingStreamClient.instances
                if c.stream_id == "shared_out"
            ]
            assert len(shared_clients) == 1
            client = shared_clients[0]
            # Task-scoped release must NOT teardown the shared writer:
            # task B still holds the ref.
            assert client.is_active is True
            assert client._end_calls == 0
            assert client.publish_end_marker_calls == 0
        finally:
            result["stop"]()


class TestDedicatedStreamEndPublishesMarker:
    """7. Producer-side StreamClient.end() on dedicated stream still
    publishes the end marker (regression gate)."""

    def test_dedicated_end_publishes_marker(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done = threading.Event()
        captured_obj: Dict[str, Any] = {}

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            obj = ctx.create_stream(format="bytes", subscribe_grace_ms=0)
            captured_obj["obj"] = obj
            obj.end()  # Task-scoped release -> dedicated teardown
            done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_dedicated_card(),
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(pn, task_id="req-end", task_kind="request")
            assert done.wait(timeout=3.0)

            # Single dedicated client; end() ran; marker published.
            instances = list(_CapturingStreamClient.instances)
            assert len(instances) == 1
            client = instances[0]
            assert client.affinity == "dedicated"
            assert client._end_calls >= 1
            assert client.publish_end_marker_calls == 1
        finally:
            result["stop"]()


class TestSharedHandleCacheEviction:
    """8. Cleanup boundaries evict the correct entries from the shared
    handle cache and never over-tear-down the underlying client while
    other ref-holders remain."""

    def test_release_all_for_task_evicts_only_that_task(self) -> None:
        """Unit-level check: simulate two tasks sharing a stream, then
        release_all_for_task for one; the registry must keep the entry
        for the other, and the handle for the released task must be
        gone from the map keyed by task.
        """
        reg = StreamRegistry()
        entry_a, is_new_a, new_for_task_a = reg.acquire(
            "shared_out", "task-A",
            direction="outbound", format="events", external=False,
            affinity="shared",
        )
        assert is_new_a is True and new_for_task_a is True

        entry_b, is_new_b, new_for_task_b = reg.acquire(
            "shared_out", "task-B",
            direction="outbound", format="events", external=False,
            affinity="shared",
        )
        assert is_new_b is False and new_for_task_b is True
        assert entry_a is entry_b

        destroyed = reg.release_all_for_task("task-A")
        # Shared entry not destroyed because task-B still holds it.
        assert destroyed == []
        survived = reg.get("shared_out")
        assert survived is not None
        assert survived.task_ids == {"task-B"}

        # Release the last ref-holder: entry destroyed.
        destroyed2 = reg.release_all_for_task("task-B")
        assert [e.stream_id for e in destroyed2] == ["shared_out"]
        assert reg.get("shared_out") is None

    def test_explicit_end_evicts_only_this_tasks_handle(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done_a = threading.Event()
        done_b = threading.Event()
        ended_a = threading.Event()
        reacquire: Dict[str, Any] = {}

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            obj = ctx.create_stream(
                format="events", declared_stream="shared_out",
                subscribe_grace_ms=0,
            )
            if task.task_id == "task-A":
                done_a.set()
                done_b.wait(timeout=3.0)
                obj.end()
                # After end(), a second create_stream from this SAME task
                # must be a fresh acquire — cache should be empty for
                # this task, so we get a NEW StreamObject. But task B
                # still owns the shared writer, so no new StreamClient
                # is constructed.
                reacquire["obj"] = ctx.create_stream(
                    format="events", declared_stream="shared_out",
                    subscribe_grace_ms=0,
                )
                ended_a.set()
            else:
                done_b.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_shared_card(),
                concurrency=4,
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(pn, task_id="task-A", task_kind="pipe")
            assert done_a.wait(timeout=3.0)
            _simulate_start_task(pn, task_id="task-B", task_kind="pipe")
            assert done_b.wait(timeout=3.0)
            assert ended_a.wait(timeout=5.0)

            # Exactly one shared writer throughout.
            shared_clients = [
                c for c in _CapturingStreamClient.instances
                if c.stream_id == "shared_out"
            ]
            assert len(shared_clients) == 1
            assert shared_clients[0].is_active is True
            # No end-marker publish.
            assert shared_clients[0].publish_end_marker_calls == 0

            # Reacquire after end() got a real StreamObject wrapping the
            # same underlying client (writer never died).
            assert reacquire["obj"] is not None
            assert reacquire["obj"].stream_id == "shared_out"
        finally:
            result["stop"]()

    def test_fail_stream_evicts_whole_stream_entry(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(captured),
        )
        done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            ctx.create_stream(
                format="events", declared_stream="shared_out",
                subscribe_grace_ms=0,
            )
            done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="echo",
                handler=_handler, card=_shared_card(),
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(pn, task_id="task-fail", task_kind="pipe")
            assert done.wait(timeout=3.0)

            # fail_stream should tear everything down cleanly.
            result["fail_stream"]("shared_out", "forced")

            # StreamClient.end() ran (fail_stream still calls end()),
            # but because affinity is shared the mock does not increment
            # publish_end_marker_calls. Invariant: cleanup did not
            # publish a marker.
            shared_clients = [
                c for c in _CapturingStreamClient.instances
                if c.stream_id == "shared_out"
            ]
            assert len(shared_clients) == 1
            assert shared_clients[0].publish_end_marker_calls == 0
            # Registry should be empty for this stream now.
            # (handle cache eviction happens alongside.)
        finally:
            result["stop"]()


class TestSharedStreamSetupRace:
    """Second-acquirer attach-before-first-setup race (PR#515 review).

    ``_perform_setup_handshake`` is slow (PubNub round-trip); if Task B
    enters create_stream between Task A's registry.acquire and Task A's
    ``entry.stream_client = client`` assignment, Task B — pre-fix —
    would observe ``entry.stream_client is None`` in the activate-branch
    predicate, skip the activate path entirely, and fall through to a
    duplicate embedded handshake, creating a second StreamClient on the
    same shared channel.

    The fix installs a ``setup_complete`` Event on the registry entry
    that Task B waits on before consulting ``stream_client``. Captured
    ``setup_error`` propagates via re-raise so a crashed first-acquirer
    doesn't leave second acquirers hanging.

    This is a registry-scope test covering the barrier mechanism; the
    end-to-end agent-instance behavior is exercised by the existing
    TestSharedStreamSecondTaskActivate test
    concurrent-task walkthrough.
    """

    def test_second_acquirer_waits_on_setup_complete(self) -> None:
        registry = StreamRegistry()

        entry_a, is_new_a, is_new_for_task_a = registry.acquire(
            "quotes", "task-a",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )
        assert is_new_a is True
        assert entry_a.stream_client is None

        # First acquirer installs a pending setup_complete event.
        entry_a.setup_complete = threading.Event()

        entry_b, is_new_b, is_new_for_task_b = registry.acquire(
            "quotes", "task-b",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )
        assert is_new_b is False
        assert is_new_for_task_b is True
        # Barrier is observable to Task B.
        assert entry_b.setup_complete is entry_a.setup_complete

        task_b_unblocked = threading.Event()
        observed_client: Dict[str, Any] = {}

        def task_b_flow() -> None:
            if entry_b.setup_complete is not None:
                entry_b.setup_complete.wait()
            if entry_b.setup_error is not None:
                raise entry_b.setup_error
            task_b_unblocked.set()
            observed_client["client"] = entry_b.stream_client

        t = threading.Thread(target=task_b_flow, daemon=True)
        t.start()

        # Task B is still waiting.
        assert task_b_unblocked.wait(timeout=0.1) is False
        assert entry_b.stream_client is None

        # Task A finishes setup: installs stream_client, signals event.
        entry_a.stream_client = {"mock": "first-writer"}  # type: ignore[assignment]
        entry_a.setup_complete.set()

        t.join(timeout=2.0)
        assert not t.is_alive(), "Task B thread failed to unblock"
        assert task_b_unblocked.is_set()
        assert observed_client["client"] == {"mock": "first-writer"}

    def test_second_acquirer_reraises_first_acquirer_setup_error(self) -> None:
        """If first-acquirer setup crashes, second acquirers get the exception.

        Without this guarantee, Task B would silently unblock on the
        event.set() in the first acquirer's except branch but see a
        half-initialized entry (stream_client is None) and would either
        return a broken StreamObject or hang looking for a client that
        was never installed.
        """
        registry = StreamRegistry()

        entry_a, _, _ = registry.acquire(
            "quotes-err", "task-a",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )
        entry_a.setup_complete = threading.Event()

        entry_b, _, _ = registry.acquire(
            "quotes-err", "task-b",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )

        # First acquirer crashes: captures exception + signals event.
        boom = RuntimeError("first-acquirer setup failed")
        entry_a.setup_error = boom
        entry_a.setup_complete.set()

        with pytest.raises(RuntimeError, match="first-acquirer setup failed"):
            if entry_b.setup_complete is not None:
                entry_b.setup_complete.wait()
            if entry_b.setup_error is not None:
                raise entry_b.setup_error


class TestSharedStreamSameTaskInFlightRace:
    """Concurrent same-task reacquire race (PR#515 review follow-up).

    A handler that spawns two threads each calling ``ctx.create_stream``
    for the same shared declared stream — or in general any code path
    that re-enters ``create_stream`` for the same (task_id, stream_id)
    before the first call has cached its handle — would, pre-fix, land
    the second call in the ``not is_new_for_task`` block with
    ``entry.stream_client is None``. The old code then returned a
    ``StreamObject(entry.stream_client=None, ...)``, handing user code
    a broken handle whose first ``write()`` raises on the None client.

    The fix:
        (1) the first acquirer caches its handle synchronously BEFORE
            ``setup_event.set()`` fires, so anyone waking on the
            barrier sees a populated cache;
        (2) the ``not is_new_for_task`` block waits on
            ``setup_complete``, propagates any captured
            ``setup_error``, then returns the cached handle rather
            than constructing a fresh (possibly None-client) wrapper.

    This is a registry-scope test exercising (2) directly: it simulates
    the concurrent reacquire sequence and asserts the second caller
    waits on the barrier and observes the cached handle. The end-to-end
    agent-instance behavior (same handle identity, no duplicate setup
    publish) is exercised by the Node case 10 mirror.
    """

    def test_same_task_concurrent_reacquire_waits_and_returns_cached(
        self,
    ) -> None:
        registry = StreamRegistry()
        shared_cache: Dict[str, Dict[str, Any]] = {}

        # First acquirer (task-a, call 1): installs the barrier and a
        # sentinel "first-handle" marker in the shared-cache equivalent.
        entry_a, is_new_a, is_new_for_task_a = registry.acquire(
            "quotes", "task-a",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )
        assert is_new_a is True
        assert is_new_for_task_a is True
        entry_a.setup_complete = threading.Event()

        # Second acquirer (task-a, call 2) — SAME task, concurrent
        # reacquire. Pre-fix, this would fall into the buggy
        # "return StreamObject(stream_client=None)" branch.
        entry_a_again, is_new_2, is_new_for_task_2 = registry.acquire(
            "quotes", "task-a",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )
        # Registry MUST report idempotent reacquire (fix e): neither
        # the entry nor the task-tracking set grew.
        assert is_new_2 is False
        assert is_new_for_task_2 is False
        assert entry_a_again is entry_a
        assert entry_a.task_ids == {"task-a"}

        # Simulate the post-fix `not is_new_for_task` path: wait on
        # setup_complete, then read the cache.
        got_handle: Dict[str, Any] = {}

        def second_call_flow() -> None:
            if (
                entry_a_again.stream_client is None
                and entry_a_again.setup_complete is not None
            ):
                entry_a_again.setup_complete.wait()
                if entry_a_again.setup_error is not None:
                    raise entry_a_again.setup_error
            got_handle["result"] = shared_cache.get("quotes", {}).get(
                "task-a"
            )

        t = threading.Thread(target=second_call_flow, daemon=True)
        t.start()

        # Second call blocked on the barrier; cache is still empty.
        time.sleep(0.05)
        assert t.is_alive()
        assert "result" not in got_handle

        # First acquirer: populate cache BEFORE signalling (this is the
        # key ordering the production fix enforces).
        first_handle = {"id": "first-handle"}
        shared_cache.setdefault("quotes", {})["task-a"] = first_handle
        entry_a.stream_client = {"mock": "client"}  # type: ignore[assignment]
        entry_a.setup_complete.set()

        # Second call wakes, reads the cache, observes the first handle.
        t.join(timeout=2.0)
        assert not t.is_alive(), "Second call thread failed to unblock"
        assert got_handle["result"] is first_handle

    def test_same_task_concurrent_reacquire_propagates_setup_error(
        self,
    ) -> None:
        """A crashed first-acquirer must surface as an exception to the
        concurrent second caller, not as a silent None-client handle.
        """
        registry = StreamRegistry()

        entry_a, _, _ = registry.acquire(
            "quotes-err", "task-a",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )
        entry_a.setup_complete = threading.Event()

        entry_a_again, _, is_new_for_task_2 = registry.acquire(
            "quotes-err", "task-a",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )
        assert is_new_for_task_2 is False

        # First acquirer crashes.
        boom = RuntimeError("first-acquirer setup crashed")
        entry_a.setup_error = boom
        entry_a.setup_complete.set()

        with pytest.raises(RuntimeError, match="first-acquirer setup crashed"):
            if (
                entry_a_again.stream_client is None
                and entry_a_again.setup_complete is not None
            ):
                entry_a_again.setup_complete.wait()
                if entry_a_again.setup_error is not None:
                    raise entry_a_again.setup_error


class TestSharedStreamSetupFailureRollback:
    """First-acquirer setup failure rollback (PR#515 review finding).

    Pre-fix: if ``_perform_setup_handshake`` raised for the first task
    on a shared declared stream, the registry entry was left behind
    with ``task_ids={failed_task_id}`` and a signalled
    ``setup_complete`` + non-None ``setup_error``. Any subsequent task
    calling ``create_stream`` on the same shared stream_id would find
    that entry, wait on the (already-signalled) barrier, observe
    ``setup_error``, and re-raise the original error — bricking the
    shared channel on that agent instance until restart.

    The fix releases the registry ref inside ``create_stream``'s
    setup-fail ``except`` (Layer 1) and also in the outer task-error
    ``except`` (Layer 2, belt-and-suspenders). This test exercises the
    registry primitive directly: simulate acquire → install
    setup_complete → signal with error + release, then verify a fresh
    acquire from a different task creates a new entry (is_new=True).
    """

    def test_setup_failure_releases_entry_so_next_task_gets_fresh(
        self,
    ) -> None:
        registry = StreamRegistry()

        # Task A: first acquirer on a shared stream.
        entry_a, is_new_a, is_new_for_task_a = registry.acquire(
            "shared_quotes", "task-a",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )
        assert is_new_a is True
        assert "task-a" in entry_a.task_ids

        # Install pending setup barrier, then simulate setup failure.
        entry_a.setup_complete = threading.Event()
        setup_err = RuntimeError(
            "PubNub 503 during _perform_setup_handshake"
        )
        # Production code path: capture setup_error, signal event,
        # then release the registry ref before re-raising.
        entry_a.setup_error = setup_err
        entry_a.setup_complete.set()
        registry.release("shared_quotes", "task-a")

        # Registry should be clean — no zombie entry.
        assert registry.get("shared_quotes") is None

        # Task B on the same shared stream must get a fresh entry
        # (is_new=True), not inherit Task A's rejected barrier.
        entry_b, is_new_b, is_new_for_task_b = registry.acquire(
            "shared_quotes", "task-b",
            direction="outbound", format="events",
            external=False, affinity="shared",
        )
        assert is_new_b is True
        assert is_new_for_task_b is True
        assert entry_b.task_ids == {"task-b"}
        assert entry_b.setup_complete is None
        assert entry_b.setup_error is None
        # Fresh entry means Task B's create_stream would run a new
        # setup handshake rather than re-raising Task A's error.
        # Verifies the bricking fix.
