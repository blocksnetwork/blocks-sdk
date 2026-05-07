"""
Tests for blocks_network.handler_loader -- registry loading and handler resolution.

Uses tmp_path fixture for registry files; real imports for echo handler.
"""

from __future__ import annotations

import json

import pytest

from blocks_network.handler_loader import (
    find_handler_entry,
    get_available_handlers,
    load_handler_function,
    load_registry,
)


# ---------------------------------------------------------------------------
# load_registry
# ---------------------------------------------------------------------------


class TestLoadRegistry:
    def test_loads_valid_json(self, tmp_path) -> None:
        registry_data = {
            "schemaVersion": "1.0",
            "handlers": [
                {
                    "name": "echo",
                    "runtime": "python",
                    "entrypoint": {"path": "./handlers/echo.py", "symbol": "echo_handler"},
                }
            ],
        }
        path = tmp_path / "registry.json"
        path.write_text(json.dumps(registry_data))

        result = load_registry(str(path))
        assert result["schemaVersion"] == "1.0"
        assert len(result["handlers"]) == 1

    def test_file_not_found(self, tmp_path) -> None:
        with pytest.raises(FileNotFoundError, match="Handler registry not found"):
            load_registry(str(tmp_path / "missing.json"))

    def test_invalid_json(self, tmp_path) -> None:
        path = tmp_path / "bad.json"
        path.write_text("{broken json")

        with pytest.raises(ValueError, match="Invalid JSON"):
            load_registry(str(path))

    def test_missing_schema_version(self, tmp_path) -> None:
        path = tmp_path / "no-version.json"
        path.write_text(json.dumps({"handlers": [{"name": "x"}]}))

        with pytest.raises(ValueError, match="schemaVersion"):
            load_registry(str(path))

    def test_empty_handlers(self, tmp_path) -> None:
        path = tmp_path / "empty.json"
        path.write_text(json.dumps({"schemaVersion": "1.0", "handlers": []}))

        with pytest.raises(ValueError, match="non-empty array"):
            load_registry(str(path))


# ---------------------------------------------------------------------------
# get_available_handlers
# ---------------------------------------------------------------------------


class TestGetAvailableHandlers:
    def test_filters_by_python_runtime(self) -> None:
        registry = {
            "handlers": [
                {"name": "echo", "runtime": "python"},
                {"name": "node-echo", "runtime": "node"},
                {"name": "adder", "runtime": "python"},
            ]
        }
        result = get_available_handlers(registry)
        assert result == ["echo", "adder"]

    def test_excludes_other_runtimes(self) -> None:
        registry = {
            "handlers": [
                {"name": "node-only", "runtime": "node"},
                {"name": "go-handler", "runtime": "go"},
            ]
        }
        result = get_available_handlers(registry)
        assert result == []

    def test_custom_runtime_filter(self) -> None:
        registry = {
            "handlers": [
                {"name": "node-echo", "runtime": "node"},
                {"name": "py-echo", "runtime": "python"},
            ]
        }
        result = get_available_handlers(registry, runtime="node")
        assert result == ["node-echo"]


# ---------------------------------------------------------------------------
# find_handler_entry
# ---------------------------------------------------------------------------


class TestFindHandlerEntry:
    def test_finds_existing_handler(self) -> None:
        registry = {
            "handlers": [
                {"name": "echo", "runtime": "python", "description": "Echo"},
                {"name": "adder", "runtime": "python", "description": "Adder"},
            ]
        }
        entry = find_handler_entry(registry, "adder")
        assert entry is not None
        assert entry["name"] == "adder"
        assert entry["description"] == "Adder"

    def test_returns_none_for_missing(self) -> None:
        registry = {
            "handlers": [
                {"name": "echo", "runtime": "python"},
            ]
        }
        entry = find_handler_entry(registry, "nonexistent")
        assert entry is None

    def test_respects_runtime_filter(self) -> None:
        registry = {
            "handlers": [
                {"name": "echo", "runtime": "node"},
            ]
        }
        # "echo" exists but only for node runtime
        assert find_handler_entry(registry, "echo", "python") is None
        assert find_handler_entry(registry, "echo", "node") is not None


# ---------------------------------------------------------------------------
# load_handler_function
# ---------------------------------------------------------------------------


class TestLoadHandlerFunction:
    def test_loads_real_echo_handler(self) -> None:
        entry = {
            "name": "echo",
            "entrypoint": {
                "path": "./handlers/echo.py",
                "symbol": "echo_handler",
            },
        }
        fn = load_handler_function(entry)
        assert callable(fn)
        # Verify it's the actual echo_handler
        from blocks_network.handlers.echo import echo_handler

        assert fn is echo_handler

    def test_import_error_for_bad_module(self) -> None:
        entry = {
            "name": "bad",
            "entrypoint": {
                "path": "./handlers/nonexistent.py",
                "symbol": "handler",
            },
        }
        with pytest.raises(ImportError, match="Failed to import"):
            load_handler_function(entry)

    def test_attribute_error_for_bad_symbol(self) -> None:
        entry = {
            "name": "echo",
            "entrypoint": {
                "path": "./handlers/echo.py",
                "symbol": "nonexistent_function",
            },
        }
        with pytest.raises(AttributeError, match="not found"):
            load_handler_function(entry)
