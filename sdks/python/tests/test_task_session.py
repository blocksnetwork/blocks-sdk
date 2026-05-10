"""Tests for the TaskSession module."""

from __future__ import annotations

import threading
import time
from unittest.mock import MagicMock, patch

import pytest

from blocks_network.task_session import TaskSession, TaskEvent
from blocks_network.types import ArtifactRef


def _make_mock_pubnub() -> MagicMock:
    pn = MagicMock()
    pn._listeners = []

    def _add_listener(listener):
        pn._listeners.append(listener)

    pn.add_listener.side_effect = _add_listener
    pn.remove_listener.side_effect = lambda l: (
        pn._listeners.remove(l) if l in pn._listeners else None
    )

    sub_chain = MagicMock()
    sub_chain.channels.return_value = sub_chain
    sub_chain.with_timetoken.return_value = sub_chain
    sub_chain.execute.return_value = None
    pn.subscribe.return_value = sub_chain

    unsub_chain = MagicMock()
    unsub_chain.channels.return_value = unsub_chain
    unsub_chain.execute.return_value = None
    pn.unsubscribe.return_value = unsub_chain

    return pn


_sim_counter = [0]


def _simulate_message(
    pn: MagicMock,
    channel: str,
    message: dict,
    timetoken: str | None = None,
) -> None:
    """Simulate a PubNub message event.

    When ``timetoken`` is omitted an auto-incrementing unique token is
    supplied so existing tests (which don't care about dedup) still see
    distinct-looking events and the dedup layer doesn't suppress repeated
    fixture deliveries.
    """
    event = MagicMock()
    event.channel = channel
    event.message = message
    if timetoken is None:
        _sim_counter[0] += 1
        event.timetoken = f"sim-{_sim_counter[0]}"
    else:
        event.timetoken = timetoken
    for listener in list(pn._listeners):
        if hasattr(listener, "message"):
            listener.message(pn, event)


class TestTaskSession:
    """TaskSession unit tests."""

    def test_constructor_subscribes(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
        )
        assert session.task_id == "task-1"
        assert session.owner_id == "alice"
        assert session.read_token == "t4"
        assert session.status_channel == "u.alice.task-1"
        assert not session.is_closed
        pn.subscribe.assert_called()
        # Verify cached-message retrieval via timetoken 1000
        # (SDK_CONTRACT §10.4.1a: 1000 replays everything still in the
        # channel's in-memory cache; 0 would mean "no catch-up").
        sub_chain = pn.subscribe.return_value
        sub_chain.with_timetoken.assert_called_with(1000)

    def test_on_progress(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        events = []
        session.on_progress(lambda e: events.append(e.raw))

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "progress": 50,
        })

        assert len(events) == 1
        assert events[0]["progress"] == 50

    def test_on_artifact(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        events = []
        session.on_artifact(lambda e: events.append(e.raw))

        _simulate_message(pn, "u.alice.task-1", {
            "type": "artifact",
            "taskId": "task-1",
            "artifactRef": {"kind": "inline"},
        })

        assert len(events) == 1

    def test_on_artifact_replays_preloaded_artifacts_on_registration(self) -> None:
        ref1 = ArtifactRef(kind="inline", mime_type="text/plain", size=5, data="aGVsbG8=")
        ref2 = ArtifactRef(
            kind="file",
            mime_type="image/png",
            size=1000,
            channel="u.alice.task-1",
            file_id="file-2",
            file_name="image.png",
        )
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
            preloaded_artifacts=[ref1, ref2],
        )
        cb = MagicMock()

        unsub = session.on_artifact(cb)

        assert callable(unsub)
        assert cb.call_count == 2
        first = cb.call_args_list[0][0][0]
        second = cb.call_args_list[1][0][0]
        assert first.type == "artifact"
        assert first.task_id == "task-1"
        assert first.artifact_ref == ref1
        assert second.artifact_ref == ref2

    def test_on_artifact_replays_preloaded_artifacts_when_terminal(self) -> None:
        ref = ArtifactRef(kind="inline", mime_type="text/plain", size=5, data="aGVsbG8=")
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
            state="completed",
            preloaded_artifacts=[ref],
        )
        cb = MagicMock()

        session.on_artifact(cb)

        assert cb.call_count == 1
        assert cb.call_args_list[0][0][0].artifact_ref == ref

    def test_on_artifact_replays_full_history_for_each_registration(self) -> None:
        ref = ArtifactRef(kind="inline", mime_type="text/plain", size=5, data="aGVsbG8=")
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
            preloaded_artifacts=[ref],
        )
        cb1 = MagicMock()
        cb2 = MagicMock()

        session.on_artifact(cb1)
        session.on_artifact(cb2)

        assert cb1.call_count == 1
        assert cb1.call_args_list[0][0][0].artifact_ref == ref
        assert cb2.call_count == 1
        assert cb2.call_args_list[0][0][0].artifact_ref == ref

    def test_on_artifact_live_delivery_after_history_replay(self) -> None:
        pn = _make_mock_pubnub()
        ref1 = ArtifactRef(kind="inline", mime_type="text/plain", size=5, data="aGVsbG8=")
        ref2 = ArtifactRef(
            kind="file",
            mime_type="image/png",
            size=1000,
            channel="u.alice.task-1",
            file_id="file-2",
            file_name="image.png",
        )
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
            preloaded_artifacts=[ref1],
        )
        cb = MagicMock()

        session.on_artifact(cb)
        _simulate_message(pn, "u.alice.task-1", {
            "type": "artifact",
            "taskId": "task-1",
            "artifactRef": ref2.to_dict(),
        })

        assert cb.call_count == 2
        assert cb.call_args_list[0][0][0].artifact_ref == ref1
        assert cb.call_args_list[1][0][0].artifact_ref == ref2

    def test_on_artifact_replay_errors_route_and_replay_continues(self) -> None:
        ref1 = ArtifactRef(kind="inline", mime_type="text/plain", size=5, data="aGVsbG8=")
        ref2 = ArtifactRef(kind="inline", mime_type="text/plain", size=6, data="d29ybGQ=")
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
            preloaded_artifacts=[ref1, ref2],
        )
        on_error = MagicMock()
        seen = []
        session.on_error(on_error)

        def cb(event: TaskEvent) -> None:
            seen.append(event.artifact_ref)
            if len(seen) == 1:
                raise RuntimeError("boom")

        unsub = session.on_artifact(cb)

        assert callable(unsub)
        assert seen == [ref1, ref2]
        assert on_error.call_count == 1
        ctx = on_error.call_args_list[0][0][1]
        assert ctx.entry_point == "taskSession"
        assert ctx.callback_type == "onArtifact"

    def test_on_artifact_replay_event_raw_shape_matches_live(self) -> None:
        ref = ArtifactRef(
            kind="file",
            mime_type="image/png",
            size=1000,
            channel="u.alice.task-1",
            file_id="file-2",
            file_name="image.png",
        )
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
            preloaded_artifacts=[ref],
        )
        events = []
        session.on_artifact(lambda e: events.append(e))

        assert len(events) == 1
        raw_ref = events[0].raw.get("artifactRef")
        assert isinstance(raw_ref, dict), "replay event.raw['artifactRef'] must be a dict, not an ArtifactRef instance"
        assert raw_ref["kind"] == "file"
        assert raw_ref["fileId"] == "file-2"
        assert raw_ref["fileName"] == "image.png"
        # dict-style access via event[] must also work
        assert isinstance(events[0]["artifactRef"], dict)

    def test_auto_close_on_terminal(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        terminal_events = []
        session.on_terminal(lambda e: terminal_events.append(e.raw))

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })

        assert len(terminal_events) == 1
        assert session.is_closed

    def test_close_idempotent(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        session.close()
        session.close()  # Should not raise
        assert session.is_closed

    def test_on_event_catchall(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        events = []
        session.on_event(lambda e: events.append(e.type))

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
        })
        _simulate_message(pn, "u.alice.task-1", {
            "type": "artifact",
            "taskId": "task-1",
        })

        assert events == ["progress", "artifact"]

    def test_list_events_returns_preloaded_history_in_order(self) -> None:
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
        ]
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
            preloaded_events=preloaded_events,
        )

        events = session.list_events()

        assert [event.type for event in events] == ["progress", "artifact", "terminal"]
        assert events[0].raw is preloaded_events[0]

    def test_list_events_empty_without_preloaded_events(self) -> None:
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
        )

        assert session.list_events() == []

    def test_list_events_returns_shallow_list_copy(self) -> None:
        progress = {"type": "progress", "taskId": "task-1", "message": "Working"}
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
            preloaded_events=[progress],
        )

        events = session.list_events()
        events.append(TaskEvent({"type": "terminal", "taskId": "task-1"}))

        assert [event.type for event in session.list_events()] == ["progress"]

    def test_list_events_includes_supported_event_shapes(self) -> None:
        preloaded_events = [
            {"type": "request", "taskId": "task-1", "requestParts": []},
            {"type": "progress", "taskId": "task-1", "progress": 50},
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
            {"type": "system", "taskId": "task-1", "status": "paused"},
            {"type": "log", "taskId": "task-1", "message": "line"},
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
                    },
                },
            },
        ]
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
            preloaded_events=preloaded_events,
        )

        assert [
            (event.type, event.get("streamEvent"))
            for event in session.list_events()
        ] == [
            ("request", None),
            ("progress", None),
            ("artifact", None),
            ("terminal", None),
            ("system", None),
            ("log", None),
            ("progress", "stream_started"),
        ]

    def test_append_history_event_deduplicates_by_timetoken(self) -> None:
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
        )
        session._append_history_event({"type": "progress", "taskId": "task-1", "message": "first"}, "300")
        session._append_history_event({"type": "progress", "taskId": "task-1", "message": "duplicate"}, "300")
        session._append_history_event({"type": "progress", "taskId": "task-1", "message": "second"}, "301")
        events = session.list_events()
        assert len(events) == 2
        assert events[0].raw["message"] == "first"
        assert events[1].raw["message"] == "second"

    def test_append_history_event_extends_list_events(self) -> None:
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
            preloaded_events=[{"type": "progress", "taskId": "task-1", "message": "from history"}],
        )
        session._append_history_event({"type": "progress", "taskId": "task-1", "message": "from buffer"})
        events = session.list_events()
        assert len(events) == 2
        assert events[0].raw["message"] == "from history"
        assert events[1].raw["message"] == "from buffer"

    def test_append_history_event_list_events_returns_copy(self) -> None:
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=_make_mock_pubnub(),
            sdk_options={},
        )
        session._append_history_event({"type": "progress", "taskId": "task-1", "message": "buf"})
        snap = session.list_events()
        snap.append(None)  # type: ignore[arg-type]
        assert len(session.list_events()) == 1

    def test_stream_started_parsing(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
        )
        stream_refs = []
        session.on_stream(lambda ref: stream_refs.append(ref))

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "video-out": {
                    "channel": "stream.echo.video-out",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t7c-abc",
                    "tokenTtlMinutes": 62,
                    "metadata": {"quality": "1080p"},
                },
            },
        })

        assert len(stream_refs) == 1
        ref = stream_refs[0]
        assert ref.descriptor.stream_id == "video-out"
        assert ref.descriptor.agent_direction == "outbound"
        assert ref.descriptor.local_direction == "inbound"
        assert ref.descriptor.format == "bytes"
        assert ref.descriptor.token == "t7c-abc"
        assert ref.descriptor.metadata == {"quality": "1080p"}

    def test_list_streams(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t1",
                },
                "s2": {
                    "channel": "stream.echo.s2",
                    "direction": "inbound",
                    "format": "events",
                    "affinity": "dedicated",
                    "token": "t2",
                },
            },
        })

        streams = session.list_streams()
        assert len(streams) == 2
        ids = {s.descriptor.stream_id for s in streams}
        assert ids == {"s1", "s2"}

    def test_wait_for_stream_already_known(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "video": {
                    "channel": "stream.echo.video",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t1",
                },
            },
        })

        ref = session.wait_for_stream("video", timeout=1.0)
        assert ref.descriptor.stream_id == "video"

    def test_wait_for_stream_single_no_id(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "only-one": {
                    "channel": "stream.echo.only-one",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t1",
                },
            },
        })

        ref = session.wait_for_stream(timeout=1.0)
        assert ref.descriptor.stream_id == "only-one"

    def test_wait_for_stream_multiple_no_id_raises(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t1",
                },
                "s2": {
                    "channel": "stream.echo.s2",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t2",
                },
            },
        })

        with pytest.raises(RuntimeError, match="Multiple streams exist"):
            session.wait_for_stream(timeout=1.0)

    def test_wait_for_stream_future(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        result = [None]

        def _waiter():
            result[0] = session.wait_for_stream("delayed", timeout=5.0)

        t = threading.Thread(target=_waiter)
        t.start()

        # Brief delay then announce the stream
        time.sleep(0.1)
        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "delayed": {
                    "channel": "stream.echo.delayed",
                    "direction": "outbound",
                    "format": "events",
                    "affinity": "dedicated",
                    "token": "t7c",
                },
            },
        })

        t.join(timeout=5.0)
        assert result[0] is not None
        assert result[0].descriptor.stream_id == "delayed"

    def test_wait_for_stream_closed_raises(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        session.close()

        with pytest.raises(RuntimeError, match="closed"):
            session.wait_for_stream(timeout=1.0)

    def test_wait_for_stream_timeout(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        with pytest.raises(TimeoutError, match="Timed out"):
            session.wait_for_stream("never", timeout=0.1)

    def test_wait_for_stream_where(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "text-out": {
                    "channel": "stream.echo.text-out",
                    "direction": "outbound",
                    "format": "events",
                    "affinity": "dedicated",
                    "token": "t1",
                    "metadata": {"type": "text"},
                },
            },
        })

        ref = session.wait_for_stream_where(
            lambda r: r.descriptor.format == "events",
            timeout=1.0,
        )
        assert ref.descriptor.stream_id == "text-out"

    def test_on_stream_fires_for_existing(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "existing": {
                    "channel": "stream.echo.existing",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t1",
                },
            },
        })

        refs = []
        session.on_stream(lambda ref: refs.append(ref))
        # Should fire immediately for the already-known stream
        assert len(refs) == 1
        assert refs[0].descriptor.stream_id == "existing"

    def test_unsubscribe_callback(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        events = []
        unsub = session.on_progress(lambda e: events.append(e))
        unsub()

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
        })

        assert len(events) == 0

    def test_ignores_wrong_channel(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        events = []
        session.on_progress(lambda e: events.append(e))

        _simulate_message(pn, "u.bob.task-2", {
            "type": "progress",
            "taskId": "task-2",
        })

        assert len(events) == 0

    def test_duplicate_stream_ignored(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        stream_event = {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "direction": "outbound",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t1",
                },
            },
        }

        _simulate_message(pn, "u.alice.task-1", stream_event)
        _simulate_message(pn, "u.alice.task-1", stream_event)

        assert len(session.list_streams()) == 1

    def test_invalid_format_skipped(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )

        _simulate_message(pn, "u.alice.task-1", {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "bad": {
                    "channel": "stream.echo.bad",
                    "direction": "outbound",
                    "format": "invalid",
                    "affinity": "dedicated",
                    "token": "t1",
                },
            },
        })

        assert len(session.list_streams()) == 0


class _MockStreamClient:
    """Mock StreamClient with on_inbound_done support for auto-drain tests."""

    def __init__(self):
        self._is_active = True
        self._end_cbs = []
        self._inbound_done_cbs = []
        self._inbound_done_fired = False

    @property
    def is_active(self):
        return self._is_active

    def on_end(self, cb):
        self._end_cbs.append(cb)

    def on_inbound_done(self, cb):
        if self._inbound_done_fired:
            cb()
            return
        self._inbound_done_cbs.append(cb)

    def end(self):
        self._is_active = False
        if not self._inbound_done_fired:
            self._inbound_done_fired = True
            for cb in self._inbound_done_cbs:
                try:
                    cb()
                except Exception:
                    pass
            self._inbound_done_cbs.clear()
        for cb in self._end_cbs:
            cb()
        self._end_cbs.clear()

    def simulate_inbound_done(self):
        """Simulate stream_end arriving (inbound iterator completes)."""
        if self._inbound_done_fired:
            return
        self._inbound_done_fired = True
        for cb in self._inbound_done_cbs:
            try:
                cb()
            except Exception:
                pass
        self._inbound_done_cbs.clear()


_stream_started_event = {
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
}

_two_stream_started_event = {
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
        "s2": {
            "channel": "stream.echo.s2",
            "direction": "outbound",
            "format": "bytes",
            "affinity": "dedicated",
            "token": "t7c-2",
            "tokenTtlMinutes": 62,
        },
    },
}


class TestAutoDrain:
    """Auto-drain unit tests for TaskSession."""

    def _make_session(self, pn, auto_drain=True):
        return TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            auto_drain=auto_drain,
        )

    @patch("blocks_network.stream_ref.StreamClient")
    def test_terminal_and_stream_end_within_drain_window(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)

        _simulate_message(pn, "u.alice.task-1", _stream_started_event)
        ref = session.list_streams()[0]
        ref.open()
        client = mock_clients[0]

        # Terminal arrives
        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        assert not session.is_closed

        # stream_end arrives before drain timer
        client.simulate_inbound_done()
        assert session.is_closed

    @patch("blocks_network.stream_ref.StreamClient")
    def test_terminal_no_stream_end_drain_timer_fires(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        # Use a short drain window for faster tests
        session._drain_window_s = 0.1

        _simulate_message(pn, "u.alice.task-1", _stream_started_event)
        ref = session.list_streams()[0]
        ref.open()
        client = mock_clients[0]

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        assert not session.is_closed
        assert client.is_active

        # Wait for drain timer to fire
        time.sleep(0.3)
        assert not client.is_active
        assert session.is_closed

    @patch("blocks_network.stream_ref.StreamClient")
    def test_terminal_multiple_streams_partial_drain(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        session._drain_window_s = 0.1

        _simulate_message(pn, "u.alice.task-1", _two_stream_started_event)
        refs = session.list_streams()
        refs[0].open()
        refs[1].open()
        client1 = mock_clients[0]
        client2 = mock_clients[1]

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        assert not session.is_closed

        # Stream 1 drains naturally
        client1.simulate_inbound_done()
        assert not session.is_closed

        # Wait for drain timer to force-end stream 2
        time.sleep(0.3)
        assert not client2.is_active
        assert session.is_closed

    @patch("blocks_network.stream_ref.StreamClient")
    def test_stream_end_before_terminal(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)

        _simulate_message(pn, "u.alice.task-1", _stream_started_event)
        ref = session.list_streams()[0]
        ref.open()
        client = mock_clients[0]

        # stream_end before terminal
        client.simulate_inbound_done()
        assert not session.is_closed

        # Terminal arrives -- no open streams, closes immediately
        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        assert session.is_closed

    @patch("blocks_network.stream_ref.StreamClient")
    def test_session_close_during_drain_window(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        session._drain_window_s = 5.0  # Long window

        _simulate_message(pn, "u.alice.task-1", _stream_started_event)
        ref = session.list_streams()[0]
        ref.open()
        client = mock_clients[0]

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        assert not session.is_closed

        # Developer calls close() during drain window
        session.close()
        assert session.is_closed
        # close() now ends all open stream clients (Fix 6)
        assert not client.is_active

    def test_no_streams_terminal_immediate_close(self) -> None:
        pn = _make_mock_pubnub()
        session = self._make_session(pn)

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        assert session.is_closed

    @patch("blocks_network.stream_ref.StreamClient")
    def test_on_terminal_callbacks_fire_with_auto_drain(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)

        _simulate_message(pn, "u.alice.task-1", _stream_started_event)
        ref = session.list_streams()[0]
        ref.open()

        terminal_events = []
        session.on_terminal(lambda e: terminal_events.append(e.raw))

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        assert len(terminal_events) == 1
        assert terminal_events[0]["state"] == "completed"

    @patch("blocks_network.stream_ref.StreamClient")
    def test_stream_end_triggers_client_end_teardown(
        self, MockStreamClient
    ) -> None:
        """onInboundDone must call client.end() so PubNub is torn down."""
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)

        _simulate_message(pn, "u.alice.task-1", _stream_started_event)
        ref = session.list_streams()[0]
        ref.open()
        client = mock_clients[0]

        assert client.is_active

        # stream_end arrives
        client.simulate_inbound_done()

        # client.end() should have been called by the onInboundDone handler
        assert not client.is_active

    @patch("blocks_network.stream_ref.StreamClient")
    def test_close_from_on_terminal_prevents_auto_drain(
        self, MockStreamClient
    ) -> None:
        """close() from inside on_terminal must prevent startAutoDrain timer."""
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        session._drain_window_s = 0.1

        _simulate_message(pn, "u.alice.task-1", _stream_started_event)
        ref = session.list_streams()[0]
        ref.open()
        client = mock_clients[0]

        # Terminal callback closes the session
        session.on_terminal(lambda e: session.close())

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        assert session.is_closed

        # close() now ends all open stream clients (Fix 6),
        # so the drain timer is irrelevant
        time.sleep(0.3)
        assert not client.is_active

    @patch("blocks_network.stream_ref.StreamClient")
    def test_auto_drain_false_preserves_legacy_behavior(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn, auto_drain=False)

        _simulate_message(pn, "u.alice.task-1", _stream_started_event)
        ref = session.list_streams()[0]
        ref.open()
        client = mock_clients[0]

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        # close() now ends all open stream clients (Fix 6)
        assert session.is_closed
        assert not client.is_active


class TestConfigurableDrainWindow:
    """Family F: drain_window_s is configurable; default is 30.0s."""

    def _make_session(self, pn, **kwargs):
        return TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            **kwargs,
        )

    def test_default_drain_window_is_30_seconds(self) -> None:
        """Default drain window is 30.0s (raised from the old 2.0s)."""
        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        assert session._drain_window_s == 30.0

    def test_drain_window_s_override_honored(self) -> None:
        """drain_window_s kwarg overrides the 30.0s default."""
        pn = _make_mock_pubnub()
        session = self._make_session(pn, drain_window_s=60.0)
        assert session._drain_window_s == 60.0

    def test_drain_window_s_override_short(self) -> None:
        """A short drain_window_s value is preserved (useful for tests)."""
        pn = _make_mock_pubnub()
        session = self._make_session(pn, drain_window_s=0.1)
        assert session._drain_window_s == 0.1

    @patch("blocks_network.stream_ref.StreamClient")
    def test_already_open_stream_drains_within_custom_window(
        self, MockStreamClient
    ) -> None:
        """An already-open stream is not force-ended before the configured
        drain window expires; it IS force-ended when the window expires."""
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn, drain_window_s=0.2)

        _simulate_message(pn, "u.alice.task-1", _stream_started_event)
        ref = session.list_streams()[0]
        ref.open()
        client = mock_clients[0]

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })

        # Still active just after terminal arrives.
        assert client.is_active
        assert not session.is_closed

        # Wait past the 0.2s drain window.
        time.sleep(0.4)

        # Now force-ended by the drain timer.
        assert not client.is_active
        assert session.is_closed


class TestOpenAllStreams:
    """Family F: open_all_streams returns List[StreamClient] in insertion order."""

    def _make_session(self, pn, **kwargs):
        return TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            **kwargs,
        )

    @patch("blocks_network.stream_ref.StreamClient")
    def test_returns_clients_for_every_readable_stream(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        _simulate_message(pn, "u.alice.task-1", _two_stream_started_event)

        clients = session.open_all_streams()
        assert len(clients) == 2
        assert clients[0] is mock_clients[0]
        assert clients[1] is mock_clients[1]

    @patch("blocks_network.stream_ref.StreamClient")
    def test_skips_outbound_opens_inbound_and_bidirectional(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        mixed = {
            "type": "progress",
            "taskId": "task-1",
            "streamEvent": "stream_started",
            "streams": {
                "s1": {
                    "channel": "stream.echo.s1",
                    "direction": "outbound",  # inbound for consumer
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t7c-1",
                    "tokenTtlMinutes": 62,
                },
                "s2": {
                    "channel": "stream.echo.s2",
                    "direction": "inbound",  # outbound for consumer -- skipped
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t7c-2",
                    "tokenTtlMinutes": 62,
                },
                "s3": {
                    "channel": "stream.echo.s3",
                    "direction": "bidirectional",
                    "format": "bytes",
                    "affinity": "dedicated",
                    "token": "t7c-3",
                    "tokenTtlMinutes": 62,
                },
            },
        }
        _simulate_message(pn, "u.alice.task-1", mixed)

        clients = session.open_all_streams()
        # s1 (inbound) + s3 (bidirectional) = 2; s2 (outbound) skipped.
        assert len(clients) == 2

    @patch("blocks_network.stream_ref.StreamClient")
    def test_preserves_insertion_order_matching_list_streams(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        _simulate_message(pn, "u.alice.task-1", _two_stream_started_event)

        ref_order = session.list_streams()
        clients = session.open_all_streams()
        assert len(clients) == len(ref_order)
        # Preload order matches construction order of the stream_started map.
        stream_ids = [r.descriptor.stream_id for r in ref_order]
        assert stream_ids == ["s1", "s2"]

    @patch("blocks_network.stream_ref.StreamClient")
    def test_idempotent_second_call_returns_same_clients(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        _simulate_message(pn, "u.alice.task-1", _two_stream_started_event)

        first = session.open_all_streams()
        second = session.open_all_streams()
        assert len(second) == len(first)
        assert second[0] is first[0]
        assert second[1] is first[1]

    def test_returns_empty_list_when_no_streams(self) -> None:
        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        assert session.open_all_streams() == []

    @patch("blocks_network.stream_ref.StreamClient")
    def test_skips_streams_that_throw_on_open(
        self, MockStreamClient
    ) -> None:
        """open_all_streams silently skips refs whose open() raises."""
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = self._make_session(pn)
        _simulate_message(pn, "u.alice.task-1", _two_stream_started_event)

        refs = session.list_streams()
        # Patch the first ref to throw on open for its first call only.
        orig_open = refs[0].open
        call_count = {"n": 0}

        def _flaky_open(*args, **kwargs):
            call_count["n"] += 1
            if call_count["n"] == 1:
                raise RuntimeError("boom")
            return orig_open(*args, **kwargs)

        refs[0].open = _flaky_open  # type: ignore[assignment]

        clients = session.open_all_streams()
        # s1 threw on first call, s2 opened successfully.
        assert len(clients) == 1


class TestTerminalSessionStreamUnavailable:
    """Family F regression: unopened terminal-session ref.open() still raises
    StreamUnavailableError (merged t7c baseline), and open_all_streams must
    honor that short-circuit by silently skipping those refs."""

    @patch("blocks_network.stream_ref.StreamClient")
    def test_open_all_streams_skips_unopened_on_terminal(
        self, MockStreamClient
    ) -> None:
        mock_clients = []

        def _make_client(*args, **kwargs):
            c = _MockStreamClient()
            mock_clients.append(c)
            return c

        MockStreamClient.from_descriptor.side_effect = _make_client

        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
        )

        # Stream discovered while the task is still active.
        _simulate_message(pn, "u.alice.task-1", _stream_started_event)

        # Terminal arrives without the consumer opening the stream first.
        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })

        # ref.open() on the unopened ref throws StreamUnavailableError; the
        # convenience wrapper silently skips it, so the list is empty.
        clients = session.open_all_streams()
        assert clients == []


class TestPreClosedSession:
    """Tests for pre-closed TaskSession (terminal idempotent hits)."""

    def test_terminal_idempotent_hit_creates_pre_closed_session(self) -> None:
        """A pre-closed session starts closed with no PubNub subscription."""
        session = TaskSession(
            task_id="task-done",
            owner_id="alice",
            read_token="t4-read",
            agent_name="echo",
            pubnub=None,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            idempotent=True,
            pre_closed_state="completed",
        )

        assert session.is_closed
        assert session.task_id == "task-done"
        assert session.owner_id == "alice"
        assert session.idempotent is True
        assert session.state == "completed"
        assert session.read_token == "t4-read"

    def test_pre_closed_session_does_not_subscribe(self) -> None:
        """A pre-closed session must not call PubNub subscribe."""
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-failed",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
            idempotent=True,
            pre_closed_state="failed",
        )

        assert session.is_closed
        assert session.state == "failed"
        # PubNub subscribe should NOT have been called
        pn.subscribe.assert_not_called()
        pn.add_listener.assert_not_called()

    def test_pre_closed_session_close_is_idempotent(self) -> None:
        """Calling close() on a pre-closed session is safe (no-op)."""
        session = TaskSession(
            task_id="task-canceled",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=None,
            sdk_options={},
            idempotent=True,
            pre_closed_state="canceled",
        )

        assert session.is_closed
        session.close()  # Should not raise
        assert session.is_closed

    def test_pre_closed_session_wait_for_stream_raises(self) -> None:
        """wait_for_stream on a pre-closed session raises immediately."""
        session = TaskSession(
            task_id="task-done",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=None,
            sdk_options={},
            idempotent=True,
            pre_closed_state="completed",
        )

        with pytest.raises(RuntimeError, match="closed"):
            session.wait_for_stream(timeout=0.1)

    def test_pending_idempotent_hit_creates_normal_session(self) -> None:
        """An idempotent hit with no terminal state creates a live session."""
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-pending",
            owner_id="alice",
            read_token="t4-read",
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
            idempotent=True,
            queued=True,
        )

        assert not session.is_closed
        assert session.idempotent is True
        assert session.state is None
        pn.subscribe.assert_called()
        session.close()

    def test_running_idempotent_hit_creates_normal_session(self) -> None:
        """An idempotent hit with running state creates a live session."""
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-running",
            owner_id="alice",
            read_token="t4-read",
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
            idempotent=True,
        )

        assert not session.is_closed
        assert session.idempotent is True
        assert session.state is None
        pn.subscribe.assert_called()
        session.close()

    def test_all_terminal_states_create_pre_closed(self) -> None:
        """All three terminal states produce pre-closed sessions."""
        for state in ("completed", "failed", "canceled"):
            session = TaskSession(
                task_id=f"task-{state}",
                owner_id="alice",
                read_token=None,
                agent_name="echo",
                pubnub=None,
                sdk_options={},
                idempotent=True,
                pre_closed_state=state,
            )
            assert session.is_closed, f"Expected closed for state={state}"
            assert session.state == state


class TestTaskEvent:
    """TaskEvent unit tests."""

    def test_properties(self) -> None:
        evt = TaskEvent({"type": "progress", "taskId": "t1", "progress": 42})
        assert evt.type == "progress"
        assert evt.task_id == "t1"
        assert evt.get("progress") == 42
        assert evt.get("missing", "default") == "default"
        assert "progress" in evt
        assert evt["progress"] == 42
        assert evt.raw == {"type": "progress", "taskId": "t1", "progress": 42}


class TestTerminalStateMutation:
    """Fix A: session.state is updated on live terminal events BEFORE callbacks fire."""

    def test_state_is_none_at_construction_for_live_session(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        assert session.state is None
        session.close()

    def test_state_assigned_from_terminal_event(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })
        assert session.state == "completed"

    def test_state_assigned_before_terminal_callbacks_fire(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        observed_states: list = []
        session.on_terminal(lambda e: observed_states.append(session.state))

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "failed",
        })

        # If mutation happened after callbacks, observed_states[0] would be None.
        # Fix A requires the assignment be the first statement in the branch.
        assert observed_states == ["failed"]

    def test_consumer_on_terminal_calling_ref_open_raises(self) -> None:
        from blocks_network.stream_ref import StreamUnavailableError

        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
        )

        # Mock StreamClient.from_descriptor so open() doesn't hit real PubNub
        # when it succeeds. Short-circuit happens BEFORE from_descriptor is
        # called, so this mock is only a safety net.
        with patch(
            "blocks_network.stream_ref.StreamClient"
        ) as MockStreamClient:
            MockStreamClient.from_descriptor.return_value = MagicMock(is_active=True)

            # Announce a stream while session is running
            _simulate_message(pn, "u.alice.task-1", {
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
                    },
                },
            })

            ref = session.list_streams()[0]

            thrown: list = []

            def _on_terminal(evt):
                try:
                    ref.open()
                except StreamUnavailableError as e:
                    thrown.append(e)

            session.on_terminal(_on_terminal)

            _simulate_message(pn, "u.alice.task-1", {
                "type": "terminal",
                "taskId": "task-1",
                "state": "canceled",
            })

            assert len(thrown) == 1
            err = thrown[0]
            assert err.task_id == "task-1"
            assert err.stream_id == "s1"
            assert err.terminal_state == "canceled"

    def test_descriptor_accessible_on_ref_after_terminal(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={},
        )
        _simulate_message(pn, "u.alice.task-1", {
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
                },
            },
        })
        ref = session.list_streams()[0]

        _simulate_message(pn, "u.alice.task-1", {
            "type": "terminal",
            "taskId": "task-1",
            "state": "completed",
        })

        # Descriptor access must not raise on a terminal-session ref
        assert ref.descriptor.stream_id == "s1"
        assert ref.descriptor.channel == "stream.echo.s1"
        assert ref.descriptor.token == "t7c-1"

    def test_preloaded_stream_honors_session_state_short_circuit(self) -> None:
        from blocks_network.stream_ref import StreamRef, StreamUnavailableError
        from blocks_network.stream import StreamDescriptor

        # Construct a terminal connect-like session with a preloaded stream.
        desc = StreamDescriptor(
            task_id="task-1",
            stream_id="preloaded-1",
            agent_name="echo",
            channel="stream.echo.preloaded-1",
            token="expired-t7c",
            agent_direction="outbound",
            local_direction="inbound",
            format="bytes",
            affinity="dedicated",
            metadata={"kind": "data"},
            declared_stream="audio",
        )
        raw_ref = StreamRef(desc, {"subscribe_key": "s", "publish_key": "p"})
        preloaded = {"preloaded-1": raw_ref}

        pn = _make_mock_pubnub()
        connected = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4-token",
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            skip_subscription=True,
            state="completed",
            preloaded_streams=preloaded,
        )

        ref = connected.list_streams()[0]
        with pytest.raises(StreamUnavailableError):
            ref.open()

        # Descriptor still inspectable
        assert ref.descriptor.declared_stream == "audio"
        assert ref.descriptor.stream_id == "preloaded-1"

        connected.close()


class TestSubscribeCacheReplayDedup:
    """Family E: timetoken-based dedup at the TaskSession dispatch layer.

    Cache replay + live delivery can surface the same PubNub message twice.
    TaskSession._handle_event drops repeats by timetoken before any dispatch
    happens, so ``on_artifact``, ``on_progress``, ``on_terminal``, and
    ``on_event`` callbacks fire exactly once per unique event and
    ``list_artifacts()`` contains each artifact exactly once
    (SDK_CONTRACT §10.4.1a).
    """

    def _make_session(self) -> tuple[TaskSession, MagicMock]:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-1",
            owner_id="alice",
            read_token="t4",
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
        )
        return session, pn

    def test_duplicate_artifact_dropped(self) -> None:
        session, pn = self._make_session()
        events: list = []
        session.on_artifact(lambda e: events.append(e.raw))

        artifact_ref = {
            "kind": "inline",
            "mimeType": "text/plain",
            "size": 3,
            "data": "Zm9v",
        }
        _simulate_message(
            pn, "u.alice.task-1",
            {"type": "artifact", "taskId": "task-1", "artifactRef": artifact_ref},
            timetoken="17000000000000001",
        )
        # Same event, same timetoken -> dedup.
        _simulate_message(
            pn, "u.alice.task-1",
            {"type": "artifact", "taskId": "task-1", "artifactRef": artifact_ref},
            timetoken="17000000000000001",
        )

        assert len(events) == 1
        assert len(session.list_artifacts()) == 1

    def test_distinct_timetokens_both_dispatch(self) -> None:
        session, pn = self._make_session()
        events: list = []
        session.on_progress(lambda e: events.append(e.raw))

        _simulate_message(
            pn, "u.alice.task-1",
            {"type": "progress", "taskId": "task-1", "progress": 25},
            timetoken="17000000000000001",
        )
        _simulate_message(
            pn, "u.alice.task-1",
            {"type": "progress", "taskId": "task-1", "progress": 50},
            timetoken="17000000000000002",
        )

        assert len(events) == 2

    def test_no_timetoken_bypasses_dedup(self) -> None:
        """Defensive: events dispatched without a timetoken never dedup."""
        session, _pn = self._make_session()
        events: list = []
        session.on_progress(lambda e: events.append(e.raw))

        # Drive _handle_event directly with no timetoken; simulates pre-existing
        # call sites that don't thread it.
        session._handle_event(
            {"type": "progress", "taskId": "task-1", "progress": 1}
        )
        session._handle_event(
            {"type": "progress", "taskId": "task-1", "progress": 2}
        )

        assert len(events) == 2

    def test_duplicate_terminal_fires_once(self) -> None:
        session, pn = self._make_session()
        terms: list = []
        session.on_terminal(lambda e: terms.append(e.raw))

        _simulate_message(
            pn, "u.alice.task-1",
            {"type": "terminal", "taskId": "task-1", "state": "completed"},
            timetoken="17000000000000100",
        )
        _simulate_message(
            pn, "u.alice.task-1",
            {"type": "terminal", "taskId": "task-1", "state": "completed"},
            timetoken="17000000000000100",
        )

        assert len(terms) == 1

    def test_seen_timetokens_bounded_at_200(self) -> None:
        session, pn = self._make_session()
        events: list = []
        session.on_progress(lambda e: events.append(e.raw))

        # 201 unique timetokens all dispatch; oldest is evicted once the cap
        # is exceeded.
        for i in range(201):
            _simulate_message(
                pn, "u.alice.task-1",
                {"type": "progress", "taskId": "task-1", "progress": i},
                timetoken=f"tt-{i:04d}",
            )
        assert len(events) == 201

        # Re-deliver the OLDEST timetoken (tt-0000). After eviction it is no
        # longer in the seen set and dispatches again.
        _simulate_message(
            pn, "u.alice.task-1",
            {"type": "progress", "taskId": "task-1", "progress": 0},
            timetoken="tt-0000",
        )
        assert len(events) == 202

        # Re-deliver a RECENT timetoken (tt-0200). Still in the seen set, so
        # dedup suppresses it.
        _simulate_message(
            pn, "u.alice.task-1",
            {"type": "progress", "taskId": "task-1", "progress": 200},
            timetoken="tt-0200",
        )
        assert len(events) == 202


class TestOnTerminalImmediateFire:
    """on_terminal fires immediately when registered on already-terminal sessions."""

    def test_fires_immediately_for_skip_subscription_terminal(self) -> None:
        session = TaskSession(
            task_id="task-term",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=None,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            skip_subscription=True,
            state="failed",
        )
        events = []
        session.on_terminal(lambda e: events.append(e))
        assert len(events) == 1
        assert events[0].type == "terminal"
        assert events[0].state == "failed"

    def test_fires_immediately_for_pre_closed_terminal(self) -> None:
        session = TaskSession(
            task_id="task-done",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=None,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
            pre_closed_state="completed",
        )
        events = []
        session.on_terminal(lambda e: events.append(e))
        assert len(events) == 1
        assert events[0].type == "terminal"
        assert events[0].state == "completed"

    def test_does_not_fire_for_non_terminal_session(self) -> None:
        pn = _make_mock_pubnub()
        session = TaskSession(
            task_id="task-active",
            owner_id="alice",
            read_token=None,
            agent_name="echo",
            pubnub=pn,
            sdk_options={"subscribe_key": "sub", "publish_key": "pub"},
        )
        events = []
        session.on_terminal(lambda e: events.append(e))
        assert len(events) == 0
        session.close()
