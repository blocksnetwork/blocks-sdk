"""
Handler loader -- loads the handler registry and dynamically imports handlers.

Mirrors the loader logic in ``scripts/run-agent.ts``:
- Load ``registry.json`` from the package directory.
- Filter entries by ``runtime == "python"``.
- Dynamically import the handler function via ``importlib``.
"""

from __future__ import annotations

import importlib
import json
import os
from typing import Any, Callable, Dict, List, Optional


def load_registry(registry_path: Optional[str] = None) -> Dict[str, Any]:
    """Load the handler registry JSON file.

    Parameters
    ----------
    registry_path : str, optional
        Absolute path to the registry JSON file.  Defaults to
        ``registry.json`` in the ``blocks_network`` package directory.

    Returns
    -------
    dict
        Parsed registry with ``schemaVersion`` and ``handlers`` keys.

    Raises
    ------
    FileNotFoundError
        If the registry file does not exist.
    ValueError
        If the registry JSON is malformed or missing required fields.
    """
    if registry_path is None:
        registry_path = os.path.join(os.path.dirname(__file__), "registry.json")

    try:
        with open(registry_path, "r", encoding="utf-8") as f:
            registry = json.load(f)
    except FileNotFoundError:
        raise FileNotFoundError(
            f"Handler registry not found at {registry_path}.\n"
            f"Expected file: blocks_network/registry.json\n"
            f"Expected format: a JSON object mapping handler name to "
            f"module path."
        )
    except json.JSONDecodeError as exc:
        raise ValueError(
            f"Invalid JSON in handler registry at {registry_path}.\n"
            f"Parse error: {exc}\n"
            f"Please check the file for syntax errors."
        )

    # Validate structure
    schema_version = registry.get("schemaVersion")
    if not schema_version or not isinstance(schema_version, str):
        raise ValueError(
            'Invalid handler registry: missing or invalid "schemaVersion" field.\n'
            'Expected: { "schemaVersion": "1.0", "handlers": [...] }'
        )

    handlers = registry.get("handlers")
    if not isinstance(handlers, list) or len(handlers) == 0:
        raise ValueError(
            'Invalid handler registry: "handlers" must be a non-empty array.\n'
            'Expected: { "schemaVersion": "1.0", "handlers": [...] }'
        )

    return registry


def get_available_handlers(
    registry: Dict[str, Any],
    runtime: str = "python",
) -> List[str]:
    """Return handler names available for the given runtime.

    Parameters
    ----------
    registry : dict
        Parsed handler registry.
    runtime : str
        Runtime filter (default ``"python"``).

    Returns
    -------
    list[str]
        Handler names matching the runtime.
    """
    return [
        entry["name"]
        for entry in registry.get("handlers", [])
        if entry.get("runtime") == runtime
    ]


def find_handler_entry(
    registry: Dict[str, Any],
    handler_name: str,
    runtime: str = "python",
) -> Optional[Dict[str, Any]]:
    """Find a handler entry by name and runtime.

    Parameters
    ----------
    registry : dict
        Parsed handler registry.
    handler_name : str
        The handler ``name`` to look up.
    runtime : str
        Runtime filter (default ``"python"``).

    Returns
    -------
    dict or None
        The matching handler entry, or ``None`` if not found.
    """
    for entry in registry.get("handlers", []):
        if entry.get("name") == handler_name and entry.get("runtime") == runtime:
            return entry
    return None


def load_handler_function(entry: Dict[str, Any]) -> Callable:
    """Dynamically import and return the handler function from a registry entry.

    Resolves the ``entrypoint.path`` relative to the ``blocks_network`` package
    and imports the ``entrypoint.symbol`` from that module.

    Parameters
    ----------
    entry : dict
        A handler registry entry with ``entrypoint.path`` and
        ``entrypoint.symbol``.

    Returns
    -------
    Callable
        The handler function.

    Raises
    ------
    ImportError
        If the module cannot be imported.
    AttributeError
        If the symbol is not found in the module.
    """
    entrypoint = entry["entrypoint"]
    module_path: str = entrypoint["path"]  # e.g. "./handlers/echo.py"
    symbol: str = entrypoint["symbol"]     # e.g. "echo_handler"

    # Convert relative file path to Python dotted module path:
    #   "./handlers/echo.py" -> "handlers.echo" -> "blocks_network.handlers.echo"
    clean_path = module_path
    # Strip leading "./" or "/"
    while clean_path.startswith("./") or clean_path.startswith("/"):
        clean_path = clean_path.lstrip("./")
    # Remove .py extension
    if clean_path.endswith(".py"):
        clean_path = clean_path[:-3]
    # Convert path separators to dots
    dotted = clean_path.replace("/", ".").replace("\\", ".")
    module_name = f"blocks_network.{dotted}"

    try:
        module = importlib.import_module(module_name)
    except ImportError as exc:
        raise ImportError(
            f'Failed to import handler module for "{entry.get("name", "?")}"\n'
            f"Module path: {module_path}\n"
            f"Resolved to: {module_name}\n"
            f"Error: {exc}\n"
            f"Check that the file exists and has no syntax errors."
        ) from exc

    try:
        handler_fn = getattr(module, symbol)
    except AttributeError as exc:
        available = [
            name for name in dir(module) if not name.startswith("_")
        ]
        raise AttributeError(
            f'Handler symbol "{symbol}" not found in module "{module_name}".\n'
            f"Available exports: {', '.join(available)}\n"
            f'Check the registry entry for handler "{entry.get("name", "?")}".'
        ) from exc

    if not callable(handler_fn):
        raise TypeError(
            f'Symbol "{symbol}" in module "{module_name}" is not callable.\n'
            f"Got type: {type(handler_fn).__name__}"
        )

    return handler_fn
