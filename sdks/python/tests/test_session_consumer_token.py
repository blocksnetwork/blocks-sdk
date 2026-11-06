"""ConsumerTokenRotation on its own: time moves only on ``advance``."""

from __future__ import annotations

from typing import Callable, List

from blocks_network._rate_limit import RateLimitedError
from blocks_network._session.consumer_token import ConsumerTokenRotation
from blocks_network._session.http import SessionToken

from tests.manual_scheduler import ManualScheduler

TTL_MINUTES = 60.0
ROTATE_AT_MS = 48 * 60_000


class Rotation:
    def __init__(self) -> None:
        self.scheduler = ManualScheduler()
        self.answers: List[Callable[[], SessionToken]] = []
        self.calls = 0
        self.tokens: List[str] = []
        self.resumes = 0
        self.revoked = 0
        self.rotation = ConsumerTokenRotation(
            session_id="ses1",
            refresh=self._refresh,
            set_token=self.tokens.append,
            resume=self._resume,
            on_revoked=self._revoke,
            scheduler=self.scheduler,
            clock=lambda: self.scheduler.now_ms / 1000,
        )
        self.rotation.start(TTL_MINUTES)

    def _refresh(self) -> SessionToken:
        self.calls += 1
        answer = self.answers.pop(0) if self.answers else (lambda: SessionToken("fresh", TTL_MINUTES))
        return answer()

    def _resume(self) -> None:
        self.resumes += 1

    def _revoke(self) -> None:
        self.revoked += 1


def rate_limited(retry_after: float) -> Callable[[], SessionToken]:
    def answer() -> SessionToken:
        raise RateLimitedError("rate limited", retry_after)

    return answer


def test_a_rotation_that_lands_during_a_rate_limited_denial_wait_resumes_the_subscription() -> None:
    r = Rotation()

    def rotation_with_a_denial_in_flight() -> SessionToken:
        # The denial arrives while this rotation is in flight, and its refresh is held 600 s.
        r.rotation.on_denied()
        r.scheduler.advance(0)
        assert r.resumes == 0
        return SessionToken("rotated", TTL_MINUTES)

    r.answers.extend([rotation_with_a_denial_in_flight, rate_limited(600.0)])
    r.scheduler.advance(ROTATE_AT_MS)

    assert r.tokens[-1] == "rotated"
    assert r.resumes == 1
    r.scheduler.advance(600_000)
    assert r.calls == 2
    assert r.revoked == 0


def test_a_rotation_that_fails_during_a_rate_limited_denial_wait_leaves_the_retries_to_it() -> None:
    r = Rotation()

    def failing_rotation_with_a_denial_in_flight() -> SessionToken:
        r.rotation.on_denied()
        r.scheduler.advance(0)
        raise RuntimeError("503")

    r.answers.extend([failing_rotation_with_a_denial_in_flight, rate_limited(20.0)])
    r.scheduler.advance(ROTATE_AT_MS)
    assert r.calls == 2

    # Rotation's own 5 s retry would land inside the 20 s hold.
    r.scheduler.advance(20_000 - 1)
    assert r.calls == 2
    r.scheduler.advance(1)
    assert r.calls == 3
    assert r.resumes == 1


def test_sends_no_denial_retry_once_stopped_during_the_wait() -> None:
    r = Rotation()
    r.answers.append(rate_limited(20.0))
    r.rotation.on_denied()
    r.scheduler.advance(0)
    assert r.calls == 1

    r.rotation.stop()
    r.scheduler.advance(60_000)
    assert r.calls == 1
    assert r.revoked == 0


def test_ignores_a_denial_refresh_that_fails_after_a_rotation_answered_the_denial() -> None:
    r = Rotation()

    def denial_refresh_outlived_by_a_rotation() -> SessionToken:
        # The rotation lands while this denial refresh is in flight.
        r.scheduler.advance(1)
        assert r.resumes == 1
        raise RuntimeError("503")

    r.answers.extend(
        [denial_refresh_outlived_by_a_rotation, lambda: SessionToken("rotated", TTL_MINUTES)]
    )
    r.scheduler.advance(ROTATE_AT_MS - 1)
    r.rotation.on_denied()
    r.scheduler.advance(0)

    assert r.tokens[-1] == "rotated"
    assert r.revoked == 0
