import time
from unittest.mock import MagicMock

import pytest

from blocks_network.write_affinity import (
    capture_affinity,
    inject_affinity,
    reset_affinity,
)


@pytest.fixture(autouse=True)
def _clean():
    reset_affinity()
    yield
    reset_affinity()


def _mock_response(headers: dict[str, str]) -> MagicMock:
    resp = MagicMock()
    resp.getheader = lambda name, default=None: headers.get(name, default)
    return resp


def test_injects_nothing_when_no_affinity():
    headers: dict[str, str] = {}
    inject_affinity(headers)
    assert headers == {}


def test_captures_and_injects_valid_header():
    future = str(int(time.time()) + 10)
    capture_affinity(_mock_response({"X-Write-Affinity": future}))

    headers: dict[str, str] = {}
    inject_affinity(headers)
    assert headers["X-Write-Affinity"] == future


def test_does_not_inject_expired_header():
    past = str(int(time.time()) - 10)
    capture_affinity(_mock_response({"X-Write-Affinity": past}))

    headers: dict[str, str] = {}
    inject_affinity(headers)
    assert headers == {}


def test_ignores_missing_header():
    capture_affinity(_mock_response({}))

    headers: dict[str, str] = {}
    inject_affinity(headers)
    assert headers == {}


def test_overwrites_older_with_newer():
    older = str(int(time.time()) + 5)
    newer = str(int(time.time()) + 15)
    capture_affinity(_mock_response({"X-Write-Affinity": older}))
    capture_affinity(_mock_response({"X-Write-Affinity": newer}))

    headers: dict[str, str] = {}
    inject_affinity(headers)
    assert headers["X-Write-Affinity"] == newer


@pytest.mark.parametrize("bad_value", ["inf", "-inf", "nan", "Infinity", "NaN", "not-a-number"])
def test_malformed_value_does_not_clobber_valid_state(bad_value: str):
    """Non-finite / unparseable values must not overwrite existing state.

    ``float()`` accepts ``"inf"``/``"nan"`` without raising; without a
    finite guard, ``inf`` would pin reads to primary forever.
    """
    valid = str(int(time.time()) + 10)
    capture_affinity(_mock_response({"X-Write-Affinity": valid}))
    capture_affinity(_mock_response({"X-Write-Affinity": bad_value}))

    headers: dict[str, str] = {}
    inject_affinity(headers)
    assert headers["X-Write-Affinity"] == valid


def test_capture_is_monotonic_older_after_newer_is_ignored():
    """Out-of-order response completions must not shorten the affinity window."""
    newer = str(int(time.time()) + 15)
    older = str(int(time.time()) + 5)
    capture_affinity(_mock_response({"X-Write-Affinity": newer}))
    capture_affinity(_mock_response({"X-Write-Affinity": older}))

    headers: dict[str, str] = {}
    inject_affinity(headers)
    assert headers["X-Write-Affinity"] == newer


def test_inject_removes_stale_header_from_reused_dict():
    future = str(int(time.time()) + 10)
    headers: dict[str, str] = {}

    capture_affinity(_mock_response({"X-Write-Affinity": future}))
    inject_affinity(headers)
    assert headers["X-Write-Affinity"] == future

    reset_affinity()
    inject_affinity(headers)
    assert "X-Write-Affinity" not in headers


def test_inject_strips_header_when_never_captured():
    headers = {"X-Write-Affinity": "1234567890"}
    inject_affinity(headers)
    assert "X-Write-Affinity" not in headers


def test_concurrent_capture_and_inject_is_thread_safe():
    """Parallel threads hammering capture/inject must not corrupt state.

    This is a liveness/absence-of-exception test. It's non-deterministic,
    but with a lock in place it must never raise.
    """
    import threading as _threading

    now = int(time.time())
    errors: list[BaseException] = []

    def worker(i: int) -> None:
        try:
            for n in range(50):
                capture_affinity(
                    _mock_response({"X-Write-Affinity": str(now + 10 + (i * 50) + n)})
                )
                headers: dict[str, str] = {}
                inject_affinity(headers)
        except BaseException as exc:  # noqa: BLE001 - collecting across threads
            errors.append(exc)

    threads = [_threading.Thread(target=worker, args=(i,)) for i in range(8)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    assert errors == []
