"""Signature and channel-derivation tests for ctx.create_stream().

Mirrors the Node SDK suite at
``blocks-sdk/sdks/node/tests/agent-instance-create-stream.test.ts``.

Covers IMPL ``STREAM_ID_ARG_REMOVAL_IMPL.md`` acceptance criteria 1, 3, 5:
- ``create_stream`` is keyword-only; no positional ``stream_id`` parameter.
- Dedicated-affinity resolver auto-generates ``{task_id}-{counter}`` per task
  with a fresh counter for each task id.
- Shared-affinity resolver preserves ``stream.{agent}.{declared_key}``.
"""

from __future__ import annotations

import inspect
import json
import threading
import time
from typing import Any, Dict, List, Optional
from unittest.mock import MagicMock, patch

import pytest

from blocks_network import agent_instance as _ai_mod
from blocks_network.agent_instance import start_agent_instance
from blocks_network.types import AgentInstanceOptions, StartTaskMessage, TaskContext


# ---------------------------------------------------------------------------
# Helpers mirroring the pattern in test_agent_instance_p3.py /
# test_agent_instance_pipe.py.
# ---------------------------------------------------------------------------


def _simulate_start_task(
    pn: MagicMock,
    task_id: str,
    has_stream: bool = True,
    task_kind: str = "request",
    write_token: str = "wt-test",
    owner_id: str = "alice",
    agent_name: str = "echo",
    instance_id: Optional[str] = None,
) -> None:
    msg: Dict[str, Any] = {
        "type": "StartTask",
        "taskId": task_id,
        "agentName": agent_name,
        "ownerId": owner_id,
        "taskKind": task_kind,
        "hasStream": has_stream,
        "writeToken": write_token,
    }
    if task_kind == "pipe":
        msg["duration"] = 60
        msg["durationExpiresAtMs"] = int(time.time() * 1000) + 3_600_000
    meta = {
        "instance": instance_id or "AG-echo-test",
        "broadcast": "true",
    }
    event = MagicMock()
    event.message = msg
    event.user_metadata = meta
    for listener in list(pn._listeners):
        if hasattr(listener, "message"):
            listener.message(pn, event)


def _make_mock_pubnub_with_abort_setup(
    agent_name: str,
) -> MagicMock:
    """Build a mock PubNub whose publish raises a JSON-payload exception for
    ``setup.*`` channels (so the SDK's setup handshake succeeds via
    ``_extract_abort_response`` JSON fallback) and no-ops otherwise.
    """
    pn = MagicMock()

    def _make_chain():
        chain = MagicMock()
        record: Dict[str, Any] = {}

        def _channel(ch: str):
            record["channel"] = ch
            return chain

        def _message(msg: Any):
            record["message"] = msg
            return chain

        def _meta(m: Any):
            return chain

        def _should_store(v: Any):
            return chain

        def _use_post(v: Any):
            return chain

        def _sync():
            channel = record.get("channel", "")
            message = record.get("message", {}) or {}
            # Only the stream_setup publish raises; everything else (task
            # events, stream_started, progress, terminal, ...) is a no-op.
            if channel.startswith("setup.") and isinstance(message, dict) and message.get("type") == "stream_setup":
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
                # _extract_abort_response falls back to JSON parse of str(exc).
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


class _CapturingStreamClient:
    """Drop-in for ``blocks_network.stream.StreamClient`` used to capture
    the ``stream_id`` the SDK resolver chose, without needing real PubNub."""

    captures: List[Dict[str, Any]] = []

    def __init__(self, **kwargs: Any) -> None:
        self.captures.append(dict(kwargs))
        self.stream_id = kwargs.get("stream_id")
        self.agent_name = kwargs.get("agent_name")
        self.is_active = True
        self._ended = False

    def on_end(self, cb: Any) -> None:
        self._on_end = cb

    def end(self) -> None:
        self._ended = True
        self.is_active = False

    def write(self, *args: Any, **kwargs: Any) -> None:
        pass


@pytest.fixture(autouse=True)
def _patch_create_pubnub_client(monkeypatch: pytest.MonkeyPatch) -> None:
    """Return a permissive mock for any per-task PubNub client. The real
    ``pubnub`` package is not installed in the unit-test environment."""

    def _mock_create(**kwargs: Any) -> MagicMock:
        mock = _make_mock_pubnub_with_abort_setup("echo")
        return mock

    monkeypatch.setattr(_ai_mod, "create_pubnub_client", _mock_create)


@pytest.fixture(autouse=True)
def _patch_stream_client(monkeypatch: pytest.MonkeyPatch) -> None:
    _CapturingStreamClient.captures.clear()
    monkeypatch.setattr(
        "blocks_network.stream.StreamClient", _CapturingStreamClient
    )


# ---------------------------------------------------------------------------
# Signature introspection
# ---------------------------------------------------------------------------


class TestCreateStreamSignature:
    """Keyword-only signature is enforced."""

    def test_create_stream_source_has_no_positional_stream_id(self) -> None:
        """Source-level assertion: the closure signature starts with the
        keyword-only ``*`` marker. No ``stream_id: Optional[str] = None``
        positional parameter may exist in ``_create_stream``."""
        source = inspect.getsource(start_agent_instance)
        # The keyword-only marker must be the first parameter line.
        assert "def _create_stream(\n            *,\n" in source, (
            "Expected keyword-only '*' to be the first parameter of "
            "_create_stream"
        )
        # The old positional parameter must be absent.
        assert "stream_id: Optional[str] = None" not in source, (
            "Positional `stream_id` parameter was not removed from "
            "_create_stream"
        )

    def test_positional_stream_id_raises_type_error(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Handler passing a positional string must raise ``TypeError`` from
        Python's keyword-only enforcement. This is the runtime analogue of
        Node's ``// @ts-expect-error`` compile-time check."""
        pn = _make_mock_pubnub_with_abort_setup("echo")
        captured_exc: List[BaseException] = []
        handler_done = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            try:
                ctx.create_stream("literal")  # type: ignore[misc]
            except TypeError as exc:
                captured_exc.append(exc)
            handler_done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn,
                agent_name="echo",
                handler=_handler,
                card={"streams": {"_default": {"direction": "outbound", "format": "bytes"}}},
                concurrency=4,
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(
                pn, task_id="task-type-1", has_stream=True, agent_name="echo",
            )
            assert handler_done.wait(timeout=3.0)
            assert len(captured_exc) == 1, "Expected TypeError from positional arg"
            msg = str(captured_exc[0])
            # Python's own keyword-only TypeError mentions the call name.
            assert "positional" in msg or "takes" in msg, (
                f"Unexpected TypeError message: {msg!r}"
            )
        finally:
            result["stop"]()


# ---------------------------------------------------------------------------
# Dedicated affinity counter
# ---------------------------------------------------------------------------


class TestDedicatedAffinityCounter:
    """Dedicated-affinity streams use ``{task_id}-{counter}`` per task,
    with fresh counters for each new task id."""

    def test_dedicated_counter_increments_per_task(self) -> None:
        pn = _make_mock_pubnub_with_abort_setup("echo")

        per_task_stream_ids: Dict[str, List[str]] = {}
        task_done: Dict[str, threading.Event] = {
            "task-A": threading.Event(),
            "task-B": threading.Event(),
        }

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            created: List[str] = []
            for _ in range(2):
                obj = ctx.create_stream(
                    format="bytes", declared_stream="_default",
                    subscribe_grace_ms=0,
                )
                created.append(obj.stream_id)
            per_task_stream_ids[task.task_id] = created
            task_done[task.task_id].set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn,
                agent_name="echo",
                handler=_handler,
                card={"streams": {"_default": {"direction": "outbound", "format": "bytes"}}},
                concurrency=4,
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(pn, task_id="task-A", has_stream=True, agent_name="echo")
            assert task_done["task-A"].wait(timeout=3.0)
            _simulate_start_task(pn, task_id="task-B", has_stream=True, agent_name="echo")
            assert task_done["task-B"].wait(timeout=3.0)

            assert per_task_stream_ids["task-A"] == ["task-A-1", "task-A-2"], (
                f"Expected fresh counter per task, got {per_task_stream_ids}"
            )
            assert per_task_stream_ids["task-B"] == ["task-B-1", "task-B-2"]
        finally:
            result["stop"]()

    def test_sequential_tasks_produce_different_channels_parity(self) -> None:
        """Cross-SDK parity: two sequential request tasks produce different
        stream channels (the whole-initiative bug class being fixed).

        Mirrors the Node SDK parity assertion in
        ``agent-instance-create-stream.test.ts``.
        """
        pn = _make_mock_pubnub_with_abort_setup("echo")

        channels: Dict[str, str] = {}
        done_a = threading.Event()
        done_b = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            obj = ctx.create_stream(format="bytes", subscribe_grace_ms=0)
            # Reconstruct the channel from the capture.
            captured = _CapturingStreamClient.captures[-1]
            channels[task.task_id] = f"stream.{captured['agent_name']}.{captured['stream_id']}"
            if task.task_id == "req-1":
                done_a.set()
            else:
                done_b.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn,
                agent_name="echo",
                handler=_handler,
                card={"streams": {"_default": {"direction": "outbound", "format": "bytes"}}},
                concurrency=4,
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(pn, task_id="req-1", has_stream=True, agent_name="echo")
            assert done_a.wait(timeout=3.0)
            time.sleep(0.2)
            _simulate_start_task(pn, task_id="req-2", has_stream=True, agent_name="echo")
            assert done_b.wait(timeout=3.0)

            assert channels["req-1"] != channels["req-2"], (
                f"Two sequential tasks produced the same channel: {channels}"
            )
            assert "req-1" in channels["req-1"]
            assert "req-2" in channels["req-2"]
        finally:
            result["stop"]()


# ---------------------------------------------------------------------------
# Shared affinity preservation
# ---------------------------------------------------------------------------


class TestSharedAffinityPreservation:
    """Shared-affinity streams preserve ``stream.{agent}.{declared_key}``."""

    def test_shared_affinity_uses_declared_key(self) -> None:
        pn = _make_mock_pubnub_with_abort_setup("echo")

        done = threading.Event()
        captured_channel: Dict[str, str] = {}

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            obj = ctx.create_stream(
                format="events", declared_stream="shared_out", subscribe_grace_ms=0,
            )
            last = _CapturingStreamClient.captures[-1]
            captured_channel["channel"] = (
                f"stream.{last['agent_name']}.{last['stream_id']}"
            )
            done.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn,
                agent_name="echo",
                handler=_handler,
                card={
                    "streams": {
                        "shared_out": {
                            "direction": "outbound",
                            "format": "events",
                            "affinity": "shared",
                        }
                    }
                },
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(
                pn, task_id="task-shared-1", has_stream=True,
                task_kind="pipe", agent_name="echo",
            )
            assert done.wait(timeout=3.0)

            assert captured_channel["channel"] == "stream.echo.shared_out", (
                f"Shared affinity did not preserve declared key: {captured_channel}"
            )
        finally:
            result["stop"]()
