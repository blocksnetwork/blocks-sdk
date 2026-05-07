from __future__ import annotations

import inspect
import json
from pathlib import Path
from unittest.mock import patch

from scripts import run_agent


def _write_handler(path: Path) -> None:
    path.write_text(
        "def handler(task, ctx=None):\n"
        "    return {}\n",
        encoding="utf-8",
    )


def _write_card(path: Path) -> None:
    path.write_text(
        json.dumps(
            {
                "identity": {
                    "displayName": "test_agent",
                    "agentName": "test_agent",
                    "description": "test",
                    "version": "1.0.0",
                    "provider": {"organization": "Test"},
                },
                "capabilities": {"taskKinds": ["request"]},
                "skills": [{"id": "main", "name": "Main"}],
                "runtime": {
                    "handler": "./handler.py",
                    "handlerExport": "handler",
                    "concurrency": 1,
                    "expectedInstances": 1,
                },
            }
        ),
        encoding="utf-8",
    )


def test_run_from_agent_card_reads_identity_block(tmp_path, monkeypatch) -> None:
    card_path = tmp_path / "agent-card.json"
    handler_path = tmp_path / "handler.py"
    _write_card(card_path)
    _write_handler(handler_path)

    with patch("scripts.run_agent._start_and_block") as start_mock:
        run_agent._run_from_agent_card(str(card_path))

    assert start_mock.call_args.kwargs["name"] == "test_agent"
    assert start_mock.call_args.kwargs["description"] == "test"
    assert start_mock.call_args.kwargs["agent_name"] == "test_agent"


def test_run_from_agent_card_reads_skills_from_top_level(tmp_path, monkeypatch) -> None:
    card_path = tmp_path / "agent-card.json"
    handler_path = tmp_path / "handler.py"
    _write_card(card_path)
    _write_handler(handler_path)

    with patch("scripts.run_agent._start_and_block") as start_mock:
        run_agent._run_from_agent_card(str(card_path))

    assert start_mock.call_args.kwargs["skills"] == [{"id": "main", "name": "Main"}]


def test_main_invokes_run_from_agent_card(tmp_path, monkeypatch) -> None:
    card_path = tmp_path / "agent-card.json"
    handler_path = tmp_path / "handler.py"
    _write_card(card_path)
    _write_handler(handler_path)
    monkeypatch.chdir(tmp_path)

    with patch("scripts.run_agent._run_from_agent_card") as run_from_card_mock:
        run_agent.main([])

    run_from_card_mock.assert_called_once_with(str(card_path))


def test_dotenv_loads_only_cwd_env() -> None:
    """Verify that main() loads only cwd/.env, not fallback paths."""
    source = inspect.getsource(run_agent.main)
    load_dotenv_count = source.count("load_dotenv(")
    assert load_dotenv_count == 1, (
        f"Expected exactly 1 load_dotenv() call in main(), found {load_dotenv_count}. "
        "Only cwd/.env should be loaded."
    )

    # Verify no references to __file__-derived fallback paths
    assert "_script_dir" not in source, "main() should not derive paths from __file__"
    assert "_python_dir" not in source, "main() should not derive paths from __file__"
    assert "_root_dir" not in source, "main() should not derive paths from __file__"


def test_dotenv_import_error_is_swallowed(tmp_path, monkeypatch) -> None:
    """Verify that ImportError from missing python-dotenv is silently caught."""
    card_path = tmp_path / "agent-card.json"
    handler_path = tmp_path / "handler.py"
    _write_card(card_path)
    _write_handler(handler_path)
    monkeypatch.chdir(tmp_path)

    # Simulate dotenv not being installed by making the import fail
    import builtins

    real_import = builtins.__import__

    def fake_import(name: str, *args: object, **kwargs: object) -> object:
        if name == "dotenv":
            raise ImportError("No module named 'dotenv'")
        return real_import(name, *args, **kwargs)

    with patch("builtins.__import__", side_effect=fake_import):
        # main() should proceed without error even if dotenv is missing
        with patch("scripts.run_agent._run_from_agent_card") as run_mock:
            run_agent.main([])

    run_mock.assert_called_once()
