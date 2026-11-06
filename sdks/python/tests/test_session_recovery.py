"""Consumer recovery in the receiver, and the snapshot and error helpers it relies on.

Mirrors the Node SDK's recovery tests: recovery closes the gaps open when it began and those
below a replayed message, terminal messages wait for the recorded outcomes, a held artifact
reaches its exchange on any end, and recorded file refs merge on ``fileId``.
"""

from __future__ import annotations

import threading
import time
from typing import Any, Dict, List

import pytest

from blocks_network._session.consumer_receiver import (
    ConsumerReceiver,
    ExchangeArtifact,
    ExchangeOutcome,
)
from blocks_network._session.counterpart import consumer_counterparts
from blocks_network._session.errors import (
    SessionNoRouteError,
    SessionNotFoundError,
    session_error_from_response,
)
from blocks_network._session.sequencing import InOrderBuffer
from blocks_network._session.snapshot import (
    SessionSnapshot,
    SnapshotExchange,
    merge_artifacts,
    parse_snapshot,
)

from tests.manual_scheduler import ManualScheduler

SESSION = "s-1"
PROVIDER = "uuid-provider"
BACKEND = "uuid-backend"
INLINE = {"kind": "inline", "mimeType": "text/plain", "data": "aGk="}


def _message(message_type: str, seq: int, **fields: Any) -> Dict[str, Any]:
    return {"type": message_type, "sessionId": SESSION, "seq": seq, **fields}


def _progress(seq: int) -> Dict[str, Any]:
    return _message("exchange_progress", seq, exchangeId="ex-1", message=str(seq))


def _snapshot(state: str = "ready", *exchanges: SnapshotExchange, **fields: Any) -> SessionSnapshot:
    return SessionSnapshot(session_id=SESSION, state=state, exchanges=list(exchanges), **fields)


class _Consumer:
    def __init__(
        self,
        recover: Any = lambda: None,
        on_artifact: Any = lambda _: None,
        on_outcome: Any = None,
    ) -> None:
        self.scheduler = ManualScheduler()
        self.progress: List[str] = []
        self.outcomes: List[ExchangeOutcome] = []
        self.states: List[str] = []
        # Each state change with the number of outcomes reported before it.
        self.settled_at_state: List[tuple] = []
        self.receiver = ConsumerReceiver(
            counterparts=consumer_counterparts(
                session_id=SESSION, provider_uuid=PROVIDER, backend_uuid=BACKEND
            ),
            scheduler=self.scheduler,
            recover=recover,
            state="ready",
            on_progress=lambda p: self.progress.append(p.message),
            on_artifact=on_artifact,
            on_outcome=on_outcome or self.outcomes.append,
            on_state_change=self._state_changed,
        )
        self.receiver.track_exchange("ex-1")

    def _state_changed(self, state: str) -> None:
        self.states.append(state)
        self.settled_at_state.append((state, len(self.outcomes)))


def _artifact(seq: int) -> Dict[str, Any]:
    return _message("exchange_artifact", seq, exchangeId="ex-1", artifactRef=INLINE)


def _closed() -> Dict[str, Any]:
    return _message("session_closed", 0, reason="idle_timeout")


def _completed() -> SessionSnapshot:
    return _snapshot("ready", SnapshotExchange("ex-1", "completed"))


class TestHeldCutoffs:
    def test_each_publisher_reports_its_own_highest_held_seq(self) -> None:
        buffer: InOrderBuffer[str] = InOrderBuffer(lambda _: None)
        buffer.accept("a", 3, "a3")
        buffer.accept("a", 6, "a6")
        buffer.accept("b", 4, "b4")
        buffer.accept("c", 1, "c1")

        assert buffer.held_cutoffs() == {"a": 6, "b": 4}


class TestRecoveryCutoffs:
    def test_closes_a_gap_history_revealed_but_not_one_a_live_message_opens_after(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _progress(3), replayed=True)
        c.receiver.history_replayed()
        c.receiver.deliver(PROVIDER, _progress(5))

        c.receiver.finish_recovery()

        assert c.progress == ["3"]
        c.receiver.deliver(PROVIDER, _progress(4))
        assert c.progress == ["3", "4", "5"]

    def test_a_gap_left_above_the_cutoff_gets_its_own_reorder_window(self) -> None:
        recovers: List[int] = []
        c = _Consumer(recover=lambda: recovers.append(1))
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _progress(3), replayed=True)
        c.scheduler.advance(600)
        c.receiver.history_replayed()
        c.receiver.deliver(PROVIDER, _progress(6))

        c.receiver.finish_recovery()
        c.scheduler.advance(749)
        assert recovers == []
        c.scheduler.advance(1)
        assert recovers == [1]

    def test_delivers_an_artifact_behind_a_replay_revealed_gap_before_the_record_settles(
        self,
    ) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(
            PROVIDER,
            _message("exchange_artifact", 3, exchangeId="ex-1", artifactRef=INLINE),
            replayed=True,
        )
        c.receiver.history_replayed()

        c.receiver.finish_recovery(_snapshot("ready", SnapshotExchange("ex-1", "completed")))

        assert [(o.state, o.artifacts) for o in c.outcomes] == [("completed", [INLINE])]

    def test_leaves_a_gap_a_live_message_opens_during_the_replay_to_its_own_window(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _progress(2))
        c.receiver.history_replayed()

        c.receiver.finish_recovery()

        assert c.progress == []
        c.receiver.deliver(PROVIDER, _progress(1))
        assert c.progress == ["1", "2"]

    def test_closes_a_gap_below_a_live_message_that_history_also_returned(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _progress(2))
        c.receiver.deliver(PROVIDER, _progress(2), replayed=True)
        c.receiver.history_replayed()

        c.receiver.finish_recovery()

        assert c.progress == ["2"]

    def test_refuses_to_open_a_second_recovery_while_one_is_open(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()

        with pytest.raises(RuntimeError, match="already open"):
            c.receiver.begin_recovery()

    def test_releases_a_live_gap_before_a_deferred_terminal_ends_the_session(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _progress(2))
        c.receiver.deliver(BACKEND, _closed())
        c.receiver.history_replayed()

        c.receiver.finish_recovery()

        assert (c.progress, c.receiver.state) == (["2"], "closed")


class TestHeldSettlement:
    def test_settles_from_the_record_only_once_an_artifact_held_behind_a_live_gap_is_released(
        self,
    ) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _artifact(2))
        c.receiver.history_replayed()
        c.receiver.finish_recovery(_completed())
        assert c.outcomes == []

        c.receiver.deliver(PROVIDER, _progress(1))

        assert [(o.state, o.artifacts) for o in c.outcomes] == [("completed", [INLINE])]

    def test_waits_for_the_last_of_several_held_artifacts_before_settling_from_the_record(
        self,
    ) -> None:
        second = {**INLINE, "data": "Mg=="}
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _artifact(2))
        c.receiver.deliver(PROVIDER, {**_artifact(3), "artifactRef": second})
        c.receiver.history_replayed()
        c.receiver.finish_recovery(_completed())

        c.receiver.deliver(PROVIDER, _progress(1))

        assert [o.artifacts for o in c.outcomes] == [[INLINE, second]]

    def test_settles_from_the_record_once_an_ignored_held_outcome_is_released(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(
            PROVIDER, _message("exchange_outcome", 2, exchangeId="ex-1", state="done")
        )
        c.receiver.history_replayed()
        c.receiver.finish_recovery(_completed())

        c.receiver.deliver(PROVIDER, _progress(1))

        assert [o.state for o in c.outcomes] == ["completed"]

    def test_settles_from_the_record_once_any_held_message_of_its_exchange_is_released(
        self,
    ) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _message("stream_started", 2, exchangeId="ex-1"))
        c.receiver.history_replayed()
        c.receiver.finish_recovery(_completed())

        c.receiver.deliver(PROVIDER, _progress(1))

        assert [o.state for o in c.outcomes] == ["completed"]

    def test_skips_a_live_gap_when_the_record_ends_the_session_keeping_its_artifact(
        self,
    ) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _artifact(2))
        c.receiver.history_replayed()

        c.receiver.finish_recovery(_snapshot("closed", SnapshotExchange("ex-1", "completed")))

        assert [(o.state, o.artifacts) for o in c.outcomes] == [("completed", [INLINE])]
        assert c.receiver.state == "closed"

    def test_settles_a_waiting_exchange_from_the_record_when_the_session_ends_locally(
        self,
    ) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(PROVIDER, _artifact(2))
        c.receiver.history_replayed()
        c.receiver.finish_recovery(_completed())

        c.receiver.terminate("closed")

        assert [(o.state, o.artifacts) for o in c.outcomes] == [("completed", [INLINE])]


class TestTerminalHandOver:
    @pytest.mark.parametrize(
        "end, state",
        [
            (lambda r: r.deliver(BACKEND, _closed()), "canceled"),
            (lambda r: r.terminate("failed", "boom"), "failed"),
        ],
        ids=["a live session_closed", "a local failure"],
    )
    def test_hands_a_held_artifact_to_its_exchange_before_the_state_turns_terminal(
        self, end: Any, state: str
    ) -> None:
        states_seen: List[str] = []
        c = _Consumer(on_artifact=lambda _: states_seen.append(c.receiver.state))
        c.receiver.deliver(PROVIDER, _artifact(2))

        end(c.receiver)

        assert [(o.state, o.artifacts) for o in c.outcomes] == [(state, [INLINE])]
        assert states_seen == ["ready"]

    def test_keeps_the_first_end_when_an_artifact_callback_ends_the_session_again(self) -> None:
        c = _Consumer(on_artifact=lambda _: c.receiver.terminate("failed", "from_callback"))
        c.receiver.deliver(PROVIDER, _artifact(2))

        c.receiver.terminate("closed")

        assert [(o.state, o.artifacts) for o in c.outcomes] == [("canceled", [INLINE])]
        assert (c.states, c.receiver.state) == (["closed"], "closed")

    def test_keeps_the_first_local_end_over_a_callback_fired_as_recovery_winds_down(
        self,
    ) -> None:
        c = _Consumer(on_artifact=lambda _: c.receiver.terminate("failed", "from_callback"))
        c.receiver.deliver(PROVIDER, _artifact(2))
        c.receiver.begin_recovery()

        c.receiver.terminate("closed")

        assert [(o.state, o.artifacts) for o in c.outcomes] == [("canceled", [INLINE])]
        assert (c.states, c.receiver.state) == (["closed"], "closed")

    @pytest.mark.parametrize("exchange_id", [[], {"id": "ex-1"}])
    def test_ends_the_session_though_a_held_artifact_carries_a_malformed_exchange_id(
        self, exchange_id: Any
    ) -> None:
        c = _Consumer()
        c.receiver.deliver(PROVIDER, {**_artifact(2), "exchangeId": exchange_id})

        c.receiver.deliver(BACKEND, _closed())

        assert (c.receiver.state, [o.state for o in c.outcomes]) == ("closed", ["canceled"])

    def test_a_local_end_during_recovery_completes_though_a_held_outcome_has_an_unhashable_state(
        self,
    ) -> None:
        c = _Consumer()
        c.receiver.deliver(
            PROVIDER, _message("exchange_outcome", 2, exchangeId="ex-1", state=[])
        )
        c.receiver.begin_recovery()

        c.receiver.terminate("failed", "session_access_revoked")

        assert c.receiver.state == "failed"
        assert [(o.state, o.error) for o in c.outcomes] == [("failed", "session_access_revoked")]

    def test_a_local_end_requested_during_recovery_applies_though_a_later_step_raises(
        self,
    ) -> None:
        def end_on_first_outcome(outcome: ExchangeOutcome) -> None:
            c.outcomes.append(outcome)
            c.receiver.terminate("closed")

        c = _Consumer(on_outcome=end_on_first_outcome)
        c.receiver.track_exchange("ex-2")
        c.receiver.begin_recovery()
        # parse_snapshot rejects refs that are not a list; here one stands in for any fault.
        faulty = SnapshotExchange("ex-2", "completed", artifact_refs=None)  # type: ignore[arg-type]

        with pytest.raises(TypeError):
            c.receiver.finish_recovery(
                _snapshot("ready", SnapshotExchange("ex-1", "completed"), faulty)
            )

        assert (c.receiver.state, c.receiver.is_ending) == ("closed", True)
        assert [(o.exchange_id, o.state) for o in c.outcomes] == [
            ("ex-1", "completed"),
            ("ex-2", "canceled"),
        ]

    def test_settles_every_exchange_before_reporting_the_terminal_state(self) -> None:
        c = _Consumer()

        c.receiver.terminate("closed")

        assert c.settled_at_state == [("closed", 1)]

    def test_drops_an_outcome_held_behind_a_gap_when_the_session_ends(self) -> None:
        c = _Consumer()
        c.receiver.deliver(
            PROVIDER, _message("exchange_outcome", 2, exchangeId="ex-1", state="completed")
        )

        c.receiver.terminate("closed")

        assert [o.state for o in c.outcomes] == ["canceled"]

    def test_a_local_end_during_recovery_applies_a_deferred_failure_first(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(BACKEND, _message("session_failed", 0, error="route_lost"))

        c.receiver.terminate("closed")

        assert (c.receiver.state, c.receiver.error) == ("failed", "route_lost")
        assert [(o.state, o.error) for o in c.outcomes] == [("failed", "route_lost")]


class TestNonStringWireText:
    @pytest.mark.parametrize("value", [[], {"code": 1}, 7])
    def test_a_non_string_error_or_reason_reads_as_absent(self, value: Any) -> None:
        c = _Consumer()
        c.receiver.track_exchange("ex-2")
        c.receiver.deliver(
            PROVIDER,
            _message(
                "exchange_outcome", 1, exchangeId="ex-2", state="failed", error=value, reason=value
            ),
        )

        c.receiver.deliver(BACKEND, _message("session_failed", 0, error=value))

        assert [(o.exchange_id, o.error, o.reason) for o in c.outcomes] == [
            ("ex-2", None, None),
            ("ex-1", None, None),
        ]
        assert (c.receiver.state, c.receiver.error) == ("failed", None)


class TestRecordedOutcomes:
    def test_settles_a_recorded_outcome_before_a_deferred_session_closed(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()
        c.receiver.deliver(BACKEND, _message("session_closed", 0, reason="idle_timeout"))
        assert c.receiver.state == "ready"

        c.receiver.finish_recovery(_snapshot("ready", SnapshotExchange("ex-1", "completed")))

        assert [o.state for o in c.outcomes] == ["completed"]
        assert c.receiver.state == "closed"

    def test_follows_a_recorded_failure_and_keeps_its_error(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()

        c.receiver.finish_recovery(_snapshot("failed", error="route_lost"))

        assert (c.receiver.state, c.receiver.error) == ("failed", "route_lost")
        assert [(o.state, o.error) for o in c.outcomes] == [("failed", "route_lost")]

    def test_ignores_a_snapshot_for_another_session(self) -> None:
        c = _Consumer()
        c.receiver.begin_recovery()

        c.receiver.finish_recovery(
            SessionSnapshot(
                session_id="other",
                state="closed",
                exchanges=[SnapshotExchange("ex-1", "completed")],
            )
        )

        assert (c.outcomes, c.receiver.state) == ([], "ready")

    def test_a_live_outcome_released_during_recovery_takes_the_recorded_file_refs(self) -> None:
        file_ref = {"kind": "file", "fileId": "f1", "fileName": "a.txt"}
        c = _Consumer()
        c.receiver.deliver(
            PROVIDER, _message("exchange_outcome", 2, exchangeId="ex-1", state="completed")
        )
        c.receiver.begin_recovery()
        c.receiver.history_replayed()

        c.receiver.finish_recovery(
            _snapshot("ready", SnapshotExchange("ex-1", "completed", artifact_refs=[file_ref]))
        )

        assert [o.artifacts for o in c.outcomes] == [[file_ref]]


class TestMergeArtifacts:
    def test_matches_a_reordered_file_ref_on_file_id(self) -> None:
        live = {"kind": "file", "fileId": "f1", "fileName": "a.txt"}
        recorded = {"fileName": "a.txt", "fileId": "f1", "kind": "file"}
        recorded_only = {"kind": "file", "fileId": "f2", "fileName": "b.txt"}

        assert merge_artifacts([live], [recorded, recorded_only]) == [live, recorded_only]

    def test_keeps_a_recorded_ref_without_a_file_id(self) -> None:
        unnamed = {"kind": "file", "fileName": "a.txt"}

        assert merge_artifacts([INLINE], [unnamed]) == [INLINE, unnamed]


class TestParseSnapshot:
    def test_parses_a_recorded_session(self) -> None:
        body = {
            "sessionId": SESSION,
            "state": "closed",
            "reason": "consumer_closed",
            "error": None,
            "exchanges": [
                {"exchangeId": "ex-1", "state": "failed", "error": "boom", "artifactRefs": []}
            ],
        }

        assert parse_snapshot(body) == SessionSnapshot(
            SESSION, "closed", "consumer_closed", None, [SnapshotExchange("ex-1", "failed", "boom")]
        )

    @pytest.mark.parametrize(
        "body",
        [
            None,
            {"sessionId": SESSION, "state": "paused", "exchanges": []},
            {"sessionId": SESSION, "state": "ready"},
            {"sessionId": SESSION, "state": ["ready"], "exchanges": []},
            {
                "sessionId": SESSION,
                "state": "ready",
                "exchanges": [{"exchangeId": "ex-1", "state": {}, "artifactRefs": []}],
            },
            {
                "sessionId": SESSION,
                "state": "ready",
                "exchanges": [{"exchangeId": "ex-1", "state": "running", "artifactRefs": []}],
            },
            {
                "sessionId": SESSION,
                "state": "ready",
                "exchanges": [{"exchangeId": "ex-1", "state": "completed"}],
            },
            {
                "sessionId": SESSION,
                "state": "ready",
                "exchanges": [
                    {"exchangeId": "ex-1", "state": "completed", "artifactRefs": [{"kind": "file"}]}
                ],
            },
            {
                "sessionId": SESSION,
                "state": "ready",
                "exchanges": [{"exchangeId": "ex-1", "state": "completed", "artifactRefs": ["f1"]}],
            },
        ],
    )
    def test_refuses_any_other_shape(self, body: Any) -> None:
        with pytest.raises(ValueError, match="invalid snapshot"):
            parse_snapshot(body)


class TestSessionErrorFromResponse:
    def test_maps_a_session_code_to_its_typed_error(self) -> None:
        error = session_error_from_response("SESSION_NO_ROUTE", "busy")

        assert isinstance(error, SessionNoRouteError)
        assert (error.code, str(error)) == ("SESSION_NO_ROUTE", "busy")

    def test_falls_back_to_the_default_message(self) -> None:
        error = session_error_from_response("SESSION_NOT_FOUND", None)

        assert isinstance(error, SessionNotFoundError)
        assert str(error) == "Session not found"

    @pytest.mark.parametrize("code", ["UNAUTHORIZED", None, 404])
    def test_returns_none_for_any_other_code(self, code: Any) -> None:
        assert session_error_from_response(code, "x") is None


class TestArtifactEvents:
    def test_reports_each_artifact_once_and_before_its_outcome(self) -> None:
        events: List[Any] = []
        receiver = ConsumerReceiver(
            counterparts=consumer_counterparts(
                session_id=SESSION, provider_uuid=PROVIDER, backend_uuid=BACKEND
            ),
            scheduler=ManualScheduler(),
            recover=lambda: None,
            state="ready",
            on_artifact=lambda a: events.append(("artifact", a)),
            on_outcome=lambda o: events.append(("outcome", o.artifacts)),
        )
        receiver.track_exchange("ex-1")
        artifact = _message(
            "exchange_artifact", 1, exchangeId="ex-1", artifactRef=INLINE, outputId="out-1"
        )

        receiver.deliver(PROVIDER, artifact)
        receiver.deliver(PROVIDER, artifact)
        receiver.deliver(
            PROVIDER, _message("exchange_outcome", 2, exchangeId="ex-1", state="completed")
        )

        assert events == [
            ("artifact", ExchangeArtifact("ex-1", INLINE, "out-1")),
            ("outcome", [INLINE]),
        ]


class TestConcurrentRecovery:
    def test_a_live_outcome_racing_finish_recovery_settles_its_exchange_once(self) -> None:
        releasing = threading.Event()
        delivering = threading.Event()
        outcomes: List[ExchangeOutcome] = []

        def on_progress(_: Any) -> None:
            # Hold finish_recovery mid-release until the live delivery is under way.
            releasing.set()
            delivering.wait(timeout=5)
            time.sleep(0.05)

        receiver = ConsumerReceiver(
            counterparts=consumer_counterparts(
                session_id=SESSION, provider_uuid=PROVIDER, backend_uuid=BACKEND
            ),
            scheduler=ManualScheduler(),
            recover=lambda: None,
            state="ready",
            on_progress=on_progress,
            on_outcome=outcomes.append,
        )
        receiver.track_exchange("ex-1")
        receiver.deliver(PROVIDER, _progress(2))
        receiver.begin_recovery()
        receiver.history_replayed()

        def live() -> None:
            releasing.wait(timeout=5)
            delivering.set()
            receiver.deliver(
                PROVIDER,
                _message(
                    "exchange_outcome", 3, exchangeId="ex-1", state="completed", reason="live"
                ),
            )

        thread = threading.Thread(target=live)
        thread.start()
        receiver.finish_recovery(_snapshot("ready", SnapshotExchange("ex-1", "completed")))
        thread.join(timeout=5)

        assert not thread.is_alive()
        # The live outcome waits for the recovery to finish, so the record settles the exchange.
        assert [(o.exchange_id, o.reason) for o in outcomes] == [("ex-1", None)]
