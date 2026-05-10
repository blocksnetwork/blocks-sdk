"""Cross-SDK parity test for the shared-affinity stream lifecycle work
(SHARED_STREAM_LIFECYCLE_IMPL Code Changes §11).

Parity claim: every assertion here has a Node mirror in
``blocks-sdk/sdks/node/tests/shared-stream-parity.test.ts``. Harness
specifics differ (pytest + unittest.mock vs vitest), but the
behavioral shape is identical.

Scenario (two concurrent pipe tasks on a shared-affinity outbound
stream):

- Both tasks' handlers receive a StreamObject.
- Each task publishes its OWN stream_setup to its OWN setup channel
  (``setup.{orgId}.{taskId}``). Task A publishes ``phase: 'embedded'``;
  task B publishes ``phase: 'activate'``. Distinct setup channels
  => distinct T7c KV slots (``streamtoken:{taskId}:{streamId}``) on
  the real Function => distinct per-task T7c tokens.
- Each setup message carries the OWNING task's ``durationMinutes``.
  On the real Function this becomes a per-task T7c TTL; asserting
  the ``durationMinutes`` on the setup is the correct SDK-side parity
  gate.
- ``stream.end()`` on either task's handler does NOT publish a
  ``stream_end`` marker to the shared channel (affinity gate).
- Consumer late-subscribe: with no cached marker on the shared
  channel, a later consumer would not be forced to exit on a stale
  marker from any prior task's cleanup.

The sibling tests at ``test_shared_up_consumer_writer.py`` (§12a)
and ``test_shared_stream_late_reader.py`` (§12b) cover the
consumer-writer and late-reader cases at the StreamClient /
descriptor level.
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
from blocks_network.task_session import TaskSession
from blocks_network.types import AgentInstanceOptions, ArtifactRef, StartTaskMessage, TaskContext


# ---------------------------------------------------------------------------
# Mock PubNub + capturing stream client, mirroring
# test_agent_instance_shared_stream.py so parity tests share the same
# harness shape.
# ---------------------------------------------------------------------------


def _make_mock_pubnub_with_abort_setup(
    captured_setups: List[Dict[str, Any]],
    captured_other: List[Dict[str, Any]],
) -> MagicMock:
    """Mock PubNub whose publish chain captures every ``stream_setup``
    payload (and raises a T7a JSON-payload abort so the setup
    handshake returns cleanly) and captures every OTHER publish too
    so tests can assert "no stream_end on the shared channel".
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
                captured_setups.append({"channel": channel, "message": dict(message)})
                payload = {
                    "streamSetupResponse": {
                        "token": (
                            None if message.get("phase") == "activate"
                            else f"T7A-{message.get('taskId')}"
                        ),
                        "taskId": message.get("taskId"),
                        "streamId": message.get("streamId"),
                        "channel": message.get("channel"),
                        "direction": message.get("direction", "outbound"),
                        "phase": message.get("phase", "embedded"),
                        "tokenTtlMinutes": 5,
                    }
                }
                raise RuntimeError(json.dumps(payload))
            # Non-setup publish — capture for cross-checks.
            if isinstance(message, dict):
                captured_other.append({"channel": channel, "message": dict(message)})
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
    agent_name: str = "parity_sh11",
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
        "instance": f"AG-{agent_name}-test",
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

    Captures every construction and every ``end()`` / bundle-publish
    event per instance so tests can assert the shared-affinity gate
    never publishes ``stream_end``.
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
        if not self.is_active:
            return
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
def _reset_capturing_stream_client() -> None:
    _CapturingStreamClient.captures.clear()
    _CapturingStreamClient.instances.clear()


@pytest.fixture(autouse=True)
def _patch_stream_client(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(
        "blocks_network.stream.StreamClient", _CapturingStreamClient
    )


def _shared_card() -> Dict[str, Any]:
    return {
        "streams": {
            "shared_down": {
                "direction": "outbound",
                "format": "bytes",
                "affinity": "shared",
            }
        }
    }


# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------


class TestSharedStreamParityTwoConcurrentPipeTasks:
    """§11 Cross-SDK parity: two concurrent pipe tasks on a shared stream.

    Mirrors
    ``blocks-sdk/sdks/node/tests/shared-stream-parity.test.ts`` →
    "distinct T7c slots per task, no stream_end on shared channel, late
    reader finds no cached marker".
    """

    def test_distinct_per_task_t7c_no_stream_end_late_reader_unaffected(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        captured_setups: List[Dict[str, Any]] = []
        captured_other: List[Dict[str, Any]] = []
        pn = _make_mock_pubnub_with_abort_setup(captured_setups, captured_other)
        monkeypatch.setattr(
            _ai_mod, "create_pubnub_client",
            lambda **_kw: _make_mock_pubnub_with_abort_setup(
                captured_setups, captured_other
            ),
        )

        done_a = threading.Event()
        done_b = threading.Event()
        release_a = threading.Event()
        release_b = threading.Event()
        ended_a = threading.Event()
        ended_b = threading.Event()

        def _handler(task: StartTaskMessage, ctx: TaskContext) -> Dict[str, Any]:
            obj = ctx.create_stream(
                format="bytes",
                declared_stream="shared_down",
                subscribe_grace_ms=0,
            )
            if task.task_id == "task-A":
                done_a.set()
                release_a.wait(timeout=3.0)
                obj.end()
                ended_a.set()
            else:
                done_b.set()
                release_b.wait(timeout=3.0)
                obj.end()
                ended_b.set()
            return {}

        result = start_agent_instance(
            AgentInstanceOptions(
                pubnub=pn, agent_name="parity_sh11",
                handler=_handler, card=_shared_card(),
                concurrency=4,
            )
        )
        try:
            time.sleep(0.1)
            _simulate_start_task(
                pn, task_id="task-A", task_kind="pipe",
                duration_minutes=15,
            )
            assert done_a.wait(timeout=3.0), "task-A handler did not reach create_stream"

            _simulate_start_task(
                pn, task_id="task-B", task_kind="pipe",
                duration_minutes=45,
            )
            assert done_b.wait(timeout=3.0), "task-B handler did not reach create_stream"

            # --- Assertion 1: both handlers discovered the shared stream ---
            assert done_a.is_set() and done_b.is_set()

            # --- Assertion 2: distinct setup channels (distinct T7c KV slots) ---
            setups = {
                entry["message"].get("taskId"): entry
                for entry in captured_setups
                if entry["message"].get("streamId") == "shared_down"
            }
            setup_a = setups.get("task-A")
            setup_b = setups.get("task-B")
            assert setup_a is not None, f"expected a stream_setup for task-A, got {setups}"
            assert setup_b is not None, f"expected a stream_setup for task-B, got {setups}"

            # Distinct setup channels == distinct per-task T7c KV slots
            # on the real Function.
            assert setup_a["channel"] != setup_b["channel"]
            assert "task-A" in setup_a["channel"]
            assert "task-B" in setup_b["channel"]
            # The setup's taskId field is the key into the KV slot.
            assert setup_a["message"]["taskId"] == "task-A"
            assert setup_b["message"]["taskId"] == "task-B"

            # --- Assertion 3: task-A is embedded (first-acquirer),
            # task-B is activate (second-and-later). ---
            assert setup_a["message"].get("phase") == "embedded"
            assert setup_b["message"].get("phase") == "activate"

            # --- Assertion 4: each setup carries OWNING task's durationMinutes ---
            assert setup_a["message"]["durationMinutes"] == 15
            assert setup_b["message"]["durationMinutes"] == 45

            # --- Assertion 5: both setups carry affinity: 'shared' +
            # taskKind: 'pipe' ---
            assert setup_a["message"]["affinity"] == "shared"
            assert setup_b["message"]["affinity"] == "shared"
            assert setup_a["message"]["taskKind"] == "pipe"
            assert setup_b["message"]["taskKind"] == "pipe"

            # --- Assertion 6: exactly ONE shared StreamClient — writer
            # reused across both tasks. ---
            shared_clients = [
                c for c in _CapturingStreamClient.instances
                if c.stream_id == "shared_down"
            ]
            assert len(shared_clients) == 1
            shared_writer = shared_clients[0]
            shared_channel = shared_writer.channel

            # --- Release task A. Writer stays alive (task B still holds ref). ---
            release_a.set()
            assert ended_a.wait(timeout=3.0)
            time.sleep(0.05)

            assert shared_writer.is_active is True
            assert shared_writer._end_calls == 0
            assert shared_writer.publish_end_marker_calls == 0
            # No publish of any kind hit the shared channel.
            ch_pubs = [p for p in captured_other if p["channel"] == shared_channel]
            assert ch_pubs == []

            # --- Release task B. Last ref-holder; writer tears down locally. ---
            release_b.set()
            assert ended_b.wait(timeout=3.0)
            time.sleep(0.1)

            # Teardown ran (registry called end()), but the affinity gate
            # suppressed publish_end_marker on the shared channel.
            assert shared_writer._end_calls == 1
            assert shared_writer.publish_end_marker_calls == 0

            # --- Assertion 7: no stream_end marker anywhere on the
            # shared channel. A third consumer subscribing after both
            # tasks released (within PubNub's cache window) would NOT
            # receive a cached stream_end marker because none was ever
            # published. ---
            shared_channel_end_markers = [
                p for p in captured_other
                if p["channel"] == shared_channel
                and p["message"].get("type") == "stream_end"
            ]
            assert shared_channel_end_markers == []
        finally:
            result["stop"]()


class TestOnArtifactHistoryReplay:
    """Cross-SDK parity: onArtifact/on_artifact history replay."""

    def test_replays_preloaded_artifacts_in_order_with_minimal_event_shape(self) -> None:
        ref1 = ArtifactRef(kind="inline", mime_type="text/plain", size=5, data="aGVsbG8=")
        ref2 = ArtifactRef(
            kind="file",
            mime_type="image/png",
            size=1000,
            channel="u.alice.task-1",
            file_id="file-2",
            file_name="image.png",
        )
        pn = _make_mock_pubnub_with_abort_setup([], [])
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="parity_artifacts",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub-key", "publish_key": "pub-key"},
            preloaded_artifacts=[ref1, ref2],
        )
        events: List[Any] = []

        session.on_artifact(lambda event: events.append(event))

        assert len(events) == 2
        assert events[0].type == "artifact"
        assert events[0].task_id == "task-1"
        assert events[0].artifact_ref == ref1
        assert "outputId" not in events[0].raw
        assert "protocolVersion" not in events[0].raw
        assert events[1].type == "artifact"
        assert events[1].task_id == "task-1"
        assert events[1].artifact_ref == ref2
        assert "outputId" not in events[1].raw
        assert "protocolVersion" not in events[1].raw


class TestListEventsHistoryParity:
    """Cross-SDK parity: listEvents/list_events history snapshots."""

    def test_returns_preloaded_history_events_in_insertion_order(self) -> None:
        preloaded_events = [
            {"type": "progress", "taskId": "task-1", "message": "Working"},
            {
                "type": "artifact",
                "taskId": "task-1",
                "artifactRef": {
                    "kind": "inline",
                    "mimeType": "text/plain",
                    "size": 5,
                    "data": "aGVsbG8=",
                },
            },
            {"type": "terminal", "taskId": "task-1", "state": "completed"},
            {
                "type": "progress",
                "taskId": "task-1",
                "streamEvent": "stream_started",
                "streams": {
                    "s1": {
                        "channel": "stream.echo.s1",
                        "direction": "outbound",
                        "format": "bytes",
                        "affinity": "dedicated",
                        "token": "t7c-1",
                        "tokenTtlMinutes": 62,
                    },
                },
            },
        ]
        pn = _make_mock_pubnub_with_abort_setup([], [])
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="parity_events",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub-key", "publish_key": "pub-key"},
            preloaded_events=preloaded_events,
        )

        listed = session.list_events()

        assert [(event.type, event.get("streamEvent")) for event in listed] == [
            ("progress", None),
            ("artifact", None),
            ("terminal", None),
            ("progress", "stream_started"),
        ]
        assert listed[0].raw is preloaded_events[0]
        assert listed is not preloaded_events
