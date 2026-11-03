"""Receive-pipeline behavior the shared session fixtures cannot reach.

The fixtures (test_session_fixtures_parity.py) own the per-case protocol rules. These
cover multi-gap skipping, late gap fills, a recover() that actually supplies history,
failure isolation, envelope checks and the provider's close handshake.
"""

from __future__ import annotations

from typing import Any, Dict, List

import pytest

from blocks_network._session.consumer_receiver import ConsumerReceiver, ExchangeOutcome
from blocks_network._session.counterpart import consumer_counterparts, provider_counterparts
from blocks_network._session.provider_receiver import ProviderReceiver, StartedExchange
from blocks_network._session.sequencing import InOrderBuffer, SeqTracker, SeqVerdict

from tests.manual_scheduler import ManualScheduler

SESSION = "s-1"
CONSUMER = "uuid-consumer"
PROVIDER = "uuid-provider"
BACKEND = "uuid-backend"
MAX_SEQ = 2**53 - 1


def _message(message_type: str, seq: int, **fields: Any) -> Dict[str, Any]:
    return {"type": message_type, "sessionId": SESSION, "seq": seq, **fields}


def _outcome(seq: int, exchange_id: str) -> Dict[str, Any]:
    return _message("exchange_outcome", seq, exchangeId=exchange_id, state="completed")


def _progress(seq: int) -> Dict[str, Any]:
    return _message("exchange_progress", seq, exchangeId="ex-1", message=str(seq))


def _exchange(seq: int, exchange_id: Any) -> Dict[str, Any]:
    return _message("exchange", seq, exchangeId=exchange_id, requestParts=[])


def _buffer() -> "tuple[InOrderBuffer[int], List[int]]":
    released: List[int] = []
    return InOrderBuffer(released.append), released


class TestInOrderBuffer:
    def test_skip_gaps_releases_across_every_gap_in_order(self) -> None:
        buffer, released = _buffer()
        for seq in (3, 6, 4):
            assert buffer.accept("p", seq, seq) is True
        assert released == []
        assert buffer.skip_gaps("p", through=6) is True
        assert released == [3, 4, 6]
        assert not buffer.has_gap("p")

    def test_skip_gaps_leaves_gaps_above_the_ceiling_open(self) -> None:
        buffer, released = _buffer()
        for seq in (3, 6):
            buffer.accept("p", seq, seq)
        buffer.skip_gaps("p", through=3)
        assert released == [3]
        buffer.accept("p", 5, 5)
        assert released == [3]
        buffer.accept("p", 4, 4)
        assert released == [3, 4, 5, 6]

    def test_skipped_seq_arriving_late_is_dropped(self) -> None:
        buffer, released = _buffer()
        buffer.accept("p", 2, 2)
        buffer.skip_gaps("p", through=2)
        assert buffer.accept("p", 1, 1) is False
        assert buffer.accept("p", 3, 3) is True
        assert released == [2, 3]


class TestSeqTracker:
    def test_late_gap_fill_is_new_once(self) -> None:
        tracker = SeqTracker()
        assert tracker.observe("c", 1) is SeqVerdict.IN_ORDER
        assert tracker.observe("c", 3) is SeqVerdict.AFTER_GAP
        assert tracker.observe("c", 2) is SeqVerdict.IN_ORDER
        assert tracker.observe("c", 2) is SeqVerdict.DUPLICATE
        assert tracker.observe("c", 3) is SeqVerdict.DUPLICATE

    def test_large_seq_jump_is_tracked_as_one_range(self) -> None:
        tracker = SeqTracker()
        tracker.observe("c", 1)
        assert tracker.observe("c", 10**15) is SeqVerdict.AFTER_GAP
        assert tracker.observe("c", 500) is SeqVerdict.IN_ORDER
        assert tracker.observe("c", 500) is SeqVerdict.DUPLICATE
        assert tracker.observe("c", 499) is SeqVerdict.IN_ORDER
        assert tracker.observe("c", 501) is SeqVerdict.IN_ORDER

    def test_fill_touches_only_the_gap_holding_it(self) -> None:
        tracker = SeqTracker()
        for seq in (1, 4, 5, 8):
            tracker.observe("c", seq)
        assert tracker.observe("c", 5) is SeqVerdict.DUPLICATE
        for seq in (3, 6, 2, 7):
            assert tracker.observe("c", seq) is SeqVerdict.IN_ORDER
        for seq in range(1, 9):
            assert tracker.observe("c", seq) is SeqVerdict.DUPLICATE

    def test_publishers_are_tracked_independently(self) -> None:
        tracker = SeqTracker()
        tracker.observe("a", 1)
        assert tracker.observe("b", 1) is SeqVerdict.IN_ORDER


class TestConsumerReceiverRecovery:
    def _receiver(self, recover: Any, **callbacks: Any) -> tuple:
        scheduler = ManualScheduler()
        receiver = ConsumerReceiver(
            counterparts=consumer_counterparts(
                session_id=SESSION, provider_uuid=PROVIDER, backend_uuid=BACKEND
            ),
            scheduler=scheduler,
            recover=recover,
            state="ready",
            **callbacks,
        )
        receiver.track_exchange("ex-1")
        return receiver, scheduler

    def test_recover_that_replays_history_fills_the_gap_in_order(self) -> None:
        delivered: List[str] = []
        holder: Dict[str, ConsumerReceiver] = {}

        def recover() -> None:
            holder["r"].deliver(PROVIDER, _progress(2))

        receiver, scheduler = self._receiver(
            recover, on_progress=lambda p: delivered.append(p.message)
        )
        holder["r"] = receiver
        receiver.deliver(PROVIDER, _progress(1))
        receiver.deliver(PROVIDER, _progress(3))
        scheduler.advance(751)
        assert delivered == ["1", "2", "3"]

    def test_gap_opened_during_recovery_gets_its_own_window(self) -> None:
        delivered: List[str] = []
        recover_calls: List[int] = []
        holder: Dict[str, ConsumerReceiver] = {}

        def recover() -> None:
            recover_calls.append(1)
            if len(recover_calls) == 1:
                holder["r"].deliver(PROVIDER, _progress(5))

        receiver, scheduler = self._receiver(
            recover, on_progress=lambda p: delivered.append(p.message)
        )
        holder["r"] = receiver
        receiver.deliver(PROVIDER, _progress(1))
        receiver.deliver(PROVIDER, _progress(3))
        scheduler.advance(751)
        assert delivered == ["1", "3"]
        scheduler.advance(751)
        assert (delivered, len(recover_calls)) == (["1", "3", "5"], 2)

    def test_failing_recover_still_skips_the_gap(self) -> None:
        delivered: List[str] = []

        def recover() -> None:
            raise RuntimeError("history unavailable")

        receiver, scheduler = self._receiver(
            recover, on_progress=lambda p: delivered.append(p.message)
        )
        receiver.deliver(PROVIDER, _progress(2))
        scheduler.advance(751)
        assert delivered == ["2"]

    def test_raising_callback_does_not_block_later_messages(self) -> None:
        outcomes: List[ExchangeOutcome] = []

        def on_progress(_: Any) -> None:
            raise RuntimeError("app bug")

        receiver, _ = self._receiver(
            lambda: None, on_progress=on_progress, on_outcome=outcomes.append
        )
        receiver.deliver(PROVIDER, _progress(1))
        receiver.deliver(PROVIDER, _outcome(2, "ex-1"))
        assert [o.state for o in outcomes] == ["completed"]

    def test_callback_may_reenter_the_receiver(self) -> None:
        holder: Dict[str, ConsumerReceiver] = {}
        outcomes: List[str] = []

        def on_outcome(outcome: ExchangeOutcome) -> None:
            outcomes.append(outcome.exchange_id)
            if outcome.exchange_id == "ex-1":
                holder["r"].track_exchange("ex-2")

        receiver, _ = self._receiver(lambda: None, on_outcome=on_outcome)
        holder["r"] = receiver
        receiver.deliver(PROVIDER, _outcome(1, "ex-1"))
        receiver.deliver(BACKEND, _message("session_closed", 0, reason="idle_timeout"))
        assert outcomes == ["ex-1", "ex-2"]


def _consumer() -> ConsumerReceiver:
    return ConsumerReceiver(
        counterparts=consumer_counterparts(
            session_id=SESSION, provider_uuid=PROVIDER, backend_uuid=BACKEND
        ),
        scheduler=ManualScheduler(),
        recover=lambda: None,
        state="ready",
    )


class _Provider:
    def __init__(self, state: str = "ready") -> None:
        self.started: List[StartedExchange] = []
        self.canceled: List[str] = []
        self.close_requests = 0
        self.receiver = ProviderReceiver(
            counterparts=provider_counterparts(
                session_id=SESSION, consumer_uuid=CONSUMER, backend_uuid=BACKEND
            ),
            state=state,
            on_exchange=self.started.append,
            on_cancel=self.canceled.append,
            on_close_requested=self._close_requested,
        )

    def _close_requested(self, _: None) -> None:
        self.close_requests += 1


class TestEnvelopeChecks:
    @pytest.mark.parametrize(
        "publisher, message",
        [
            (PROVIDER, _progress(MAX_SEQ + 1)),
            (PROVIDER, {**_progress(1), "sessionId": "s-other"}),
            (PROVIDER, {k: v for k, v in _progress(1).items() if k != "sessionId"}),
            (BACKEND, _message("session_failed", 1, error="route_lost")),
            (PROVIDER, {"type": "route_probe", "nonce": "n-1"}),
            (PROVIDER, _message("route_probe", 1, nonce="n-1")),
            (PROVIDER, {**_progress(1), "type": ["exchange_progress"]}),
        ],
        ids=[
            "seq-beyond-safe-integer",
            "other-session",
            "no-session-id",
            "backend-notice-nonzero-seq",
            "route-probe",
            "route-probe-with-session-envelope",
            "unhashable-type",
        ],
    )
    def test_consumer_drops(self, publisher: str, message: Dict[str, Any]) -> None:
        receiver = _consumer()
        assert receiver.deliver(publisher, message) is False
        assert receiver.state == "ready"

    def test_consumer_accepts_the_largest_safe_seq_after_a_gap(self) -> None:
        assert _consumer().deliver(PROVIDER, _progress(MAX_SEQ)) is True

    def test_provider_drops_a_message_for_another_session(self) -> None:
        provider = _Provider()
        message = {**_exchange(1, "ex-1"), "sessionId": "s-other"}
        assert provider.receiver.deliver(CONSUMER, message) is False
        assert provider.started == []

    @pytest.mark.parametrize("exchange_id", ["", "x" * 65, 7], ids=["empty", "too-long", "not-str"])
    def test_provider_ignores_an_invalid_exchange_id(self, exchange_id: Any) -> None:
        provider = _Provider(state="open")
        inline = {"exchangeId": exchange_id, "requestParts": []}
        provider.receiver.deliver(CONSUMER, _message("session_open", 1, exchange=inline))
        provider.receiver.deliver(CONSUMER, _exchange(2, exchange_id))
        provider.receiver.deliver(CONSUMER, _message("exchange_cancel", 3, exchangeId=exchange_id))
        assert (provider.started, provider.canceled) == ([], [])

    @pytest.mark.parametrize(
        "exchange",
        [{"exchangeId": "ex-1"}, {"exchangeId": "ex-1", "requestParts": "hello"}],
        ids=["missing", "not-a-list"],
    )
    def test_provider_ignores_an_exchange_without_a_request_parts_list(
        self, exchange: Dict[str, Any]
    ) -> None:
        provider = _Provider(state="open")
        provider.receiver.deliver(CONSUMER, _message("session_open", 1, exchange=exchange))
        provider.receiver.deliver(CONSUMER, _message("exchange", 2, **exchange))
        assert provider.started == []

    def test_provider_accepts_a_64_character_exchange_id(self) -> None:
        provider = _Provider()
        provider.receiver.deliver(CONSUMER, _exchange(1, "x" * 64))
        assert [e.exchange_id for e in provider.started] == ["x" * 64]


class TestOutcomeState:
    def test_an_unknown_state_settles_nothing_so_a_later_valid_outcome_still_settles(self) -> None:
        outcomes: List[ExchangeOutcome] = []
        receiver = ConsumerReceiver(
            counterparts=consumer_counterparts(
                session_id=SESSION, provider_uuid=PROVIDER, backend_uuid=BACKEND
            ),
            scheduler=ManualScheduler(),
            recover=lambda: None,
            state="ready",
            on_outcome=outcomes.append,
        )
        receiver.track_exchange("ex-1")
        receiver.deliver(PROVIDER, {**_outcome(1, "ex-1"), "state": "done"})
        assert outcomes == []
        receiver.deliver(PROVIDER, _outcome(2, "ex-1"))
        assert [(o.exchange_id, o.state) for o in outcomes] == [("ex-1", "completed")]


class TestAfterTerminal:
    def test_consumer_drops_counterpart_messages_but_not_backend_notices(self) -> None:
        receiver = _consumer()
        receiver.deliver(BACKEND, _message("session_failed", 0, error="route_lost"))
        assert receiver.deliver(PROVIDER, _progress(1)) is False
        assert receiver.deliver(BACKEND, _message("session_closed", 0, reason="idle_timeout"))

    def test_provider_drops_counterpart_messages_but_not_backend_notices(self) -> None:
        provider = _Provider()
        provider.receiver.terminate("closed")
        assert provider.receiver.deliver(CONSUMER, _exchange(1, "ex-1")) is False
        assert provider.receiver.deliver(BACKEND, _message("session_failed", 0, error="route_lost"))


class TestProviderClose:
    def test_session_close_waits_in_closing_for_terminate(self) -> None:
        provider = _Provider()
        provider.receiver.deliver(CONSUMER, _message("session_close", 1))
        provider.receiver.deliver(CONSUMER, _exchange(2, "ex-1"))
        assert (provider.receiver.state, provider.close_requests, provider.started) == (
            "closing",
            1,
            [],
        )
        provider.receiver.terminate("closed")
        assert provider.receiver.state == "closed"

    def test_backend_failure_during_close_wins_over_terminate(self) -> None:
        provider = _Provider()
        provider.receiver.deliver(CONSUMER, _message("session_close", 1))
        provider.receiver.deliver(BACKEND, _message("session_failed", 0, error="route_lost"))
        provider.receiver.terminate("closed")
        assert provider.receiver.state == "failed"
