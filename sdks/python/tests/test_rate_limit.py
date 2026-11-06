"""Retry-After parsing for the backend's per-API-key rate limit."""

from __future__ import annotations

import json
from email.utils import formatdate
from pathlib import Path

import pytest

import blocks_network
from blocks_network import _rate_limit
from blocks_network._rate_limit import (
    RETRY_AFTER_MAX_S,
    connect_retry_delay_s,
    parse_retry_after,
    rate_limit_backoff_s,
)

NOW = 1_800_000_000.0


@pytest.mark.parametrize(
    "value,expected",
    [
        ("120", 120.0),
        (" 7 ", 7.0),
        ("0", 0.0),
        (formatdate(NOW + 90, usegmt=True), 90.0),
        (formatdate(NOW - 90, usegmt=True), 0.0),
        (None, None),
        ("", None),
        ("soon", None),
        ("-5", None),
        ("1.5", None),
        ("1e3", None),
        ("\u0661\u0662", None),  # non-ASCII digits
        ("nan", None),
        ("86400", RETRY_AFTER_MAX_S),
        (formatdate(NOW + 86400, usegmt=True), RETRY_AFTER_MAX_S),
    ],
)
def test_parse_retry_after(value, expected) -> None:
    assert parse_retry_after(value, now=NOW) == expected


def test_constants_match_the_shared_fixture() -> None:
    """Node asserts its own constants against the same file."""
    fixture = json.loads(
        (Path(__file__).resolve().parents[3] / "fixtures" / "rate-limit.json").read_text()
    )
    assert {
        "retryAfterMaxSeconds": _rate_limit.RETRY_AFTER_MAX_S,
        "noHintHoldSeconds": _rate_limit.RATE_LIMIT_NO_HINT_HOLD_S,
        "backoffBaseSeconds": _rate_limit.RATE_LIMIT_BACKOFF_BASE_S,
        "backoffMaxSeconds": _rate_limit.RATE_LIMIT_BACKOFF_MAX_S,
        "minWaitSeconds": _rate_limit.RATE_LIMIT_MIN_WAIT_S,
        "jitterFraction": _rate_limit.RATE_LIMIT_JITTER_FRACTION,
    } == {key: value for key, value in fixture.items() if key != "about"}


@pytest.mark.parametrize(
    "attempt,retry_after,expected",
    [
        (0, 7.0, 7.0),
        (5, 7.0, 7.0),
        (0, 0.0, 1.0),
        (0, None, 30.0),
        (1, None, 60.0),
        (10, None, 600.0),
        (2000, None, 600.0),
    ],
)
def test_backoff_honors_retry_after_else_doubles_to_the_cap(
    attempt, retry_after, expected
) -> None:
    assert rate_limit_backoff_s(attempt, retry_after) == expected


@pytest.mark.parametrize("draw,expected", [(0.0, 20.0), (0.5, 21.0), (1.0, 22.0)])
def test_connect_delay_adds_up_to_a_tenth_of_jitter(draw, expected) -> None:
    assert connect_retry_delay_s(0, 20.0, lambda: draw) == pytest.approx(expected)


def test_rate_limited_error_is_exported() -> None:
    assert "RateLimitedError" in blocks_network.__all__
    err = blocks_network.RateLimitedError("rate limited", 12.0)
    assert err.status == 429
    assert err.retry_after == 12.0


def test_a_concurrent_shorter_hold_cannot_shorten_a_longer_one(monkeypatch) -> None:
    import threading

    from blocks_network import RateLimitedError

    hold = _rate_limit.RateLimitHold()
    in_short_hold = threading.Event()
    release = threading.Event()
    calls = []

    def monotonic() -> float:
        calls.append(threading.current_thread().name)
        if threading.current_thread().name == "short" and len(calls) == 1:
            in_short_hold.set()
            release.wait(5)
        return 0.0

    monkeypatch.setattr(_rate_limit.time, "monotonic", monotonic)
    short = threading.Thread(target=hold.record, args=(RateLimitedError("x", None),), name="short")
    long_ = threading.Thread(target=hold.record, args=(RateLimitedError("x", 120.0),), name="long")
    short.start()
    assert in_short_hold.wait(5)
    long_.start()
    long_.join(0.2)
    release.set()
    short.join(5)
    long_.join(5)

    with pytest.raises(RateLimitedError) as held:
        hold.check("refresh")
    assert held.value.retry_after == 120.0


def test_only_a_blocks_endpoint_429_is_rate_limited() -> None:
    from blocks_network._rate_limit import BlocksHttpError, RateLimitedError, is_rate_limited

    class ProviderError(Exception):
        status = 429

    assert is_rate_limited(RateLimitedError("held", 1.0))
    assert is_rate_limited(BlocksHttpError(429, "slow down"))
    wrapped = RuntimeError("refresh failed")
    wrapped.__cause__ = BlocksHttpError(429, "slow down")
    assert is_rate_limited(wrapped)
    # A custom token provider's error is not a Blocks endpoint's, whatever its status.
    assert not is_rate_limited(ProviderError())
    assert not is_rate_limited(BlocksHttpError(503, "unavailable"))
