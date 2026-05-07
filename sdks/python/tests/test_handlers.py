"""
Tests for blocks_network.handlers -- echo and adder handler functions.

These are pure function tests with no mocking required.
"""

from __future__ import annotations

import json

from blocks_network.handlers.echo import echo_handler
from blocks_network.handlers.adder import adder_handler
from blocks_network.types import RequestPart, StartTaskMessage, TaskContext


# ---------------------------------------------------------------------------
# Echo handler
# ---------------------------------------------------------------------------


class TestEchoHandler:
    def test_echoes_text_from_request_parts(self) -> None:
        task = StartTaskMessage(
            task_id="t-1",
            request_parts=[RequestPart(text="hello")],
        )
        result = echo_handler(task)
        assert result["artifacts"][0]["data"] == "Echoed request: hello"
        assert result["artifacts"][0]["mimeType"] == "text/plain"

    def test_falls_back_to_json_when_no_text_key(self) -> None:
        task = StartTaskMessage(
            task_id="t-2",
            request_parts=[RequestPart(extra={"data": 42})],
        )
        result = echo_handler(task)
        # Falls back to JSON-serialised parts
        assert "Echoed request:" in result["artifacts"][0]["data"]

    def test_handles_empty_request_parts(self) -> None:
        task = StartTaskMessage(task_id="t-3", request_parts=[])
        result = echo_handler(task)
        assert result["artifacts"][0]["data"] == "Echoed request: []"

    def test_accepts_optional_context(self) -> None:
        task = StartTaskMessage(
            task_id="t-4",
            request_parts=[RequestPart(text="ctx")],
        )
        ctx = TaskContext(task_id="t-4")
        result = echo_handler(task, ctx)
        assert result["artifacts"][0]["data"] == "Echoed request: ctx"


# ---------------------------------------------------------------------------
# Adder handler
# ---------------------------------------------------------------------------


class TestAdderHandler:
    def test_adds_two_numbers(self) -> None:
        task = StartTaskMessage(
            task_id="t-add",
            request_parts=[RequestPart(extra={"a": 2, "b": 3})],
        )
        result = adder_handler(task)
        payload = json.loads(result["artifacts"][0]["data"])
        assert payload["ok"] is True
        assert payload["sum"] == 5
        assert payload["a"] == 2
        assert payload["b"] == 3
        assert result["artifacts"][0]["mimeType"] == "application/json"

    def test_error_for_missing_inputs(self) -> None:
        task = StartTaskMessage(
            task_id="t-bad",
            request_parts=[RequestPart(text="not math")],
        )
        result = adder_handler(task)
        payload = json.loads(result["artifacts"][0]["data"])
        assert payload["ok"] is False
        assert "error" in payload

    def test_error_for_non_numeric_inputs(self) -> None:
        task = StartTaskMessage(
            task_id="t-str",
            request_parts=[RequestPart(extra={"a": "x", "b": "y"})],
        )
        result = adder_handler(task)
        payload = json.loads(result["artifacts"][0]["data"])
        assert payload["ok"] is False

    def test_parses_kind_math_add(self) -> None:
        task = StartTaskMessage(
            task_id="t-kind",
            request_parts=[RequestPart(extra={"kind": "math_add", "a": 10, "b": 20})],
        )
        result = adder_handler(task)
        payload = json.loads(result["artifacts"][0]["data"])
        assert payload["ok"] is True
        assert payload["sum"] == 30

    def test_rejects_wrong_kind(self) -> None:
        task = StartTaskMessage(
            task_id="t-wrong-kind",
            request_parts=[RequestPart(extra={"kind": "math_multiply", "a": 10, "b": 20})],
        )
        result = adder_handler(task)
        payload = json.loads(result["artifacts"][0]["data"])
        assert payload["ok"] is False

    def test_rejects_nan(self) -> None:
        task = StartTaskMessage(
            task_id="t-nan",
            request_parts=[RequestPart(extra={"a": float("nan"), "b": 1})],
        )
        result = adder_handler(task)
        payload = json.loads(result["artifacts"][0]["data"])
        assert payload["ok"] is False

    def test_rejects_inf(self) -> None:
        task = StartTaskMessage(
            task_id="t-inf",
            request_parts=[RequestPart(extra={"a": float("inf"), "b": 1})],
        )
        result = adder_handler(task)
        payload = json.loads(result["artifacts"][0]["data"])
        assert payload["ok"] is False

    def test_rejects_bool(self) -> None:
        task = StartTaskMessage(
            task_id="t-bool",
            request_parts=[RequestPart(extra={"a": True, "b": False})],
        )
        result = adder_handler(task)
        payload = json.loads(result["artifacts"][0]["data"])
        assert payload["ok"] is False

    def test_handles_float_addition(self) -> None:
        task = StartTaskMessage(
            task_id="t-float",
            request_parts=[RequestPart(extra={"a": 1.5, "b": 2.5})],
        )
        result = adder_handler(task)
        payload = json.loads(result["artifacts"][0]["data"])
        assert payload["ok"] is True
        assert payload["sum"] == 4.0
