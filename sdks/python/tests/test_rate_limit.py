"""Retry-After parsing for the backend's per-API-key rate limit."""

from __future__ import annotations

from email.utils import formatdate

import pytest

import blocks_network
from blocks_network._rate_limit import RETRY_AFTER_MAX_S, parse_retry_after

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
        ("nan", None),
        ("86400", RETRY_AFTER_MAX_S),
        (formatdate(NOW + 86400, usegmt=True), RETRY_AFTER_MAX_S),
    ],
)
def test_parse_retry_after(value, expected) -> None:
    assert parse_retry_after(value, now=NOW) == expected


def test_rate_limited_error_is_exported() -> None:
    assert "RateLimitedError" in blocks_network.__all__
    err = blocks_network.RateLimitedError("rate limited", 12.0)
    assert err.status == 429
    assert err.retry_after == 12.0


def test_a_concurrent_shorter_hold_cannot_shorten_a_longer_one(monkeypatch) -> None:
    import threading

    from blocks_network import RateLimitedError
    from blocks_network import _rate_limit

    gate = _rate_limit.RateLimitGate()
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
    short = threading.Thread(target=gate.hold, args=(RateLimitedError("x", None),), name="short")
    long_ = threading.Thread(target=gate.hold, args=(RateLimitedError("x", 120.0),), name="long")
    short.start()
    assert in_short_hold.wait(5)
    long_.start()
    long_.join(0.2)
    release.set()
    short.join(5)
    long_.join(5)

    with pytest.raises(RateLimitedError) as held:
        gate.check("refresh")
    assert held.value.retry_after == 120.0
