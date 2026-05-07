"""
Fix B (t7c_token_lifecycle) -- ``max_running_time_sec`` resolver and
request-task TTL derivation tests (Python parity with Node).

Covers three pure helpers exported from ``blocks_network.agent_instance``:

    - ``_resolve_max_running_time_sec(opts, card)`` -- single source of truth
      for the instance-scoped max-running-time; logs on divergence.
    - ``_extract_card_max_running_time_sec(card)`` -- duck-typed read of
      ``card.runtime.maxRunningTimeSec`` that tolerates dict and
      dataclass-shaped cards.
    - ``_compute_stream_duration_minutes(task_duration, is_pipe, effective)``
      -- derives the ``durationMinutes`` passed to the ``streamSetup``
      Function for each task.
"""

from __future__ import annotations

from dataclasses import dataclass
from types import SimpleNamespace

from blocks_network.agent_instance import (
    _compute_stream_duration_minutes,
    _extract_card_max_running_time_sec,
    _resolve_max_running_time_sec,
)


# ---------------------------------------------------------------------------
# _resolve_max_running_time_sec
# ---------------------------------------------------------------------------


class TestResolveMaxRunningTimeSec:
    def _capture_logs(self, monkeypatch) -> list[tuple[str, str]]:
        """Patch ``log_agent_instance_event`` to capture (level, message) pairs."""
        captured: list[tuple[str, str]] = []

        def _capture(level: str, message: str, **_meta) -> None:
            captured.append((level, message))

        monkeypatch.setattr(
            "blocks_network.agent_instance.log_agent_instance_event",
            _capture,
        )
        return captured

    def test_returns_opts_when_only_opts_is_set(self, monkeypatch) -> None:
        logs = self._capture_logs(monkeypatch)
        assert _resolve_max_running_time_sec(900, None) == 900
        assert logs == []

    def test_returns_card_when_only_card_is_set(self, monkeypatch) -> None:
        logs = self._capture_logs(monkeypatch)
        assert _resolve_max_running_time_sec(None, 1800) == 1800
        assert logs == []

    def test_both_set_and_equal_no_log(self, monkeypatch) -> None:
        logs = self._capture_logs(monkeypatch)
        assert _resolve_max_running_time_sec(900, 900) == 900
        assert logs == []

    def test_both_set_and_divergent_returns_opts_with_info_log(
        self, monkeypatch
    ) -> None:
        logs = self._capture_logs(monkeypatch)
        assert _resolve_max_running_time_sec(900, 1800) == 900
        assert len(logs) == 1
        level, msg = logs[0]
        assert level == "info"
        assert "opts.max_running_time_sec (900)" in msg
        assert "card.runtime.maxRunningTimeSec (1800)" in msg

    def test_neither_set_returns_none(self, monkeypatch) -> None:
        logs = self._capture_logs(monkeypatch)
        assert _resolve_max_running_time_sec(None, None) is None
        assert logs == []


# ---------------------------------------------------------------------------
# _extract_card_max_running_time_sec
# ---------------------------------------------------------------------------


@dataclass
class _FakeRuntime:
    maxRunningTimeSec: int


@dataclass
class _FakeCard:
    runtime: _FakeRuntime


class TestExtractCardMaxRunningTimeSec:
    def test_dict_card_with_dict_runtime(self) -> None:
        card = {"runtime": {"maxRunningTimeSec": 1800}}
        assert _extract_card_max_running_time_sec(card) == 1800

    def test_dict_card_missing_runtime(self) -> None:
        assert _extract_card_max_running_time_sec({}) is None

    def test_dict_card_with_empty_runtime(self) -> None:
        assert _extract_card_max_running_time_sec({"runtime": {}}) is None

    def test_dict_card_with_non_numeric_value(self) -> None:
        card = {"runtime": {"maxRunningTimeSec": "not-a-number"}}
        assert _extract_card_max_running_time_sec(card) is None

    def test_dataclass_card(self) -> None:
        card = _FakeCard(runtime=_FakeRuntime(maxRunningTimeSec=1800))
        assert _extract_card_max_running_time_sec(card) == 1800

    def test_simple_namespace_card(self) -> None:
        card = SimpleNamespace(runtime=SimpleNamespace(maxRunningTimeSec=900))
        assert _extract_card_max_running_time_sec(card) == 900

    def test_dataclass_card_with_snake_case_runtime_attr(self) -> None:
        ns = SimpleNamespace(max_running_time_sec=600)
        card = SimpleNamespace(runtime=ns)
        assert _extract_card_max_running_time_sec(card) == 600

    def test_none_card(self) -> None:
        assert _extract_card_max_running_time_sec(None) is None


# ---------------------------------------------------------------------------
# _compute_stream_duration_minutes
# ---------------------------------------------------------------------------


class TestComputeStreamDurationMinutes:
    def test_task_duration_wins_for_request_task(self) -> None:
        assert _compute_stream_duration_minutes(45, False, 1800) == 45

    def test_task_duration_wins_for_pipe_task(self) -> None:
        assert _compute_stream_duration_minutes(120, True, 1800) == 120

    def test_pipe_task_no_duration_falls_back_to_60(self) -> None:
        assert _compute_stream_duration_minutes(None, True, None) == 60
        assert _compute_stream_duration_minutes(None, True, 1800) == 60

    def test_request_task_with_effective_1800_is_30_minutes(self) -> None:
        assert _compute_stream_duration_minutes(None, False, 1800) == 30

    def test_request_task_with_no_effective_defaults_to_60(self) -> None:
        assert _compute_stream_duration_minutes(None, False, None) == 60

    def test_request_task_30_seconds_is_1_minute(self) -> None:
        # ceil(30 / 60) = 1
        assert _compute_stream_duration_minutes(None, False, 30) == 1

    def test_request_task_59_seconds_is_1_minute(self) -> None:
        # ceil(59 / 60) = 1
        assert _compute_stream_duration_minutes(None, False, 59) == 1

    def test_request_task_61_seconds_is_2_minutes(self) -> None:
        # ceil(61 / 60) = 2
        assert _compute_stream_duration_minutes(None, False, 61) == 2

    def test_request_task_3600_seconds_is_60_minutes(self) -> None:
        assert _compute_stream_duration_minutes(None, False, 3600) == 60


# ---------------------------------------------------------------------------
# Cross-helper integration: resolver + extractor + derivation
# ---------------------------------------------------------------------------


class TestEndToEndDerivation:
    """Exercise the pipeline the same way ``start_agent_instance`` does."""

    def test_card_only_900_produces_15_minutes_for_request_task(
        self, monkeypatch
    ) -> None:
        # Simulates: opts.max_running_time_sec=None, card has 900.
        card = {"runtime": {"maxRunningTimeSec": 900}}
        effective = _resolve_max_running_time_sec(
            None, _extract_card_max_running_time_sec(card)
        )
        assert effective == 900
        assert _compute_stream_duration_minutes(None, False, effective) == 15

    def test_no_card_no_opts_produces_60_minutes_for_request_task(self) -> None:
        card = {}
        effective = _resolve_max_running_time_sec(
            None, _extract_card_max_running_time_sec(card)
        )
        assert effective is None
        assert _compute_stream_duration_minutes(None, False, effective) == 60

    def test_opts_override_propagates(self, monkeypatch) -> None:
        # Capture the divergence info log.
        captured: list[tuple[str, str]] = []

        def _capture(level: str, message: str, **_meta) -> None:
            captured.append((level, message))

        monkeypatch.setattr(
            "blocks_network.agent_instance.log_agent_instance_event",
            _capture,
        )
        card = {"runtime": {"maxRunningTimeSec": 1800}}
        effective = _resolve_max_running_time_sec(
            900, _extract_card_max_running_time_sec(card)
        )
        assert effective == 900
        assert _compute_stream_duration_minutes(None, False, effective) == 15
        assert len(captured) == 1
        assert captured[0][0] == "info"
