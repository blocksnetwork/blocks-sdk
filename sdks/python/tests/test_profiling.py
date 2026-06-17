import pytest

from blocks_network.profiling import is_profiling_enabled, log_dispatch_timing


@pytest.fixture(autouse=True)
def _clear_env(monkeypatch):
    monkeypatch.delenv("BLOCKS_PROFILE", raising=False)


def test_disabled_when_unset():
    assert is_profiling_enabled() is False


def test_enabled_when_token_present(monkeypatch):
    monkeypatch.setenv("BLOCKS_PROFILE", "timing")
    assert is_profiling_enabled() is True


def test_enabled_among_other_tokens(monkeypatch):
    monkeypatch.setenv("BLOCKS_PROFILE", "foo,timing,bar")
    assert is_profiling_enabled() is True


def test_disabled_for_unrelated_tokens(monkeypatch):
    monkeypatch.setenv("BLOCKS_PROFILE", "foo,bar")
    assert is_profiling_enabled() is False


def test_log_dispatch_timing_noop_when_disabled(monkeypatch):
    calls = []
    monkeypatch.setattr(
        "blocks_network.profiling.log_agent_instance_event",
        lambda *a, **k: calls.append((a, k)),
    )
    log_dispatch_timing("t1", received_ms=0, running_ms=3, handler_ms=5)
    assert calls == []


def test_log_dispatch_timing_emits_when_enabled(monkeypatch):
    monkeypatch.setenv("BLOCKS_PROFILE", "timing")
    calls = []
    monkeypatch.setattr(
        "blocks_network.profiling.log_agent_instance_event",
        lambda *a, **k: calls.append((a, k)),
    )
    # Chronological order: received(0) -> running(3) -> handler(5).
    log_dispatch_timing("t1", received_ms=0, running_ms=3, handler_ms=5)
    assert len(calls) == 1
    _, meta = calls[0]
    assert meta["received_to_running_ms"] == 3
    assert meta["running_to_handler_ms"] == 2
    assert meta["received_to_handler_ms"] == 5
