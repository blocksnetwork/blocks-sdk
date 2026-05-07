"""
CLI runner for Blocks Network Python agent instances.

Port of ``scripts/run-agent.ts``.

Usage::

    # Via console script (after pip install -e .):
    blocks-run --handler echo

    # From an example directory with agent-card.json:
    cd examples/python/echo
    blocks-run

    # Via python -m:
    python -m scripts.run_agent --handler echo

The runner:
1. If no ``--handler`` is given and ``agent-card.json`` exists in the current
   directory, loads config from the card and dynamically imports the handler.
2. Otherwise loads ``blocks_network/registry.json``, finds the handler entry by
   name (filtered to ``runtime: "python"``), and dynamically imports it.
3. Calls ``start_agent_instance()`` with the resolved config.
4. Blocks until SIGINT/SIGTERM for graceful shutdown.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import signal
import sys
import threading
from typing import Optional


def _start_and_block(
    handler_fn: object,
    agent_name: str,
    name: str,
    description: Optional[str],
    skills: Optional[list],
    concurrency: int,
    expected_instances: int,
    card: Optional[dict] = None,
    max_pending_backlog: Optional[int] = None,
    max_running_time_sec: Optional[int] = None,
) -> None:
    """Start the agent instance and block until shutdown signal."""

    print(f'[agent] starting handler "{name}" on agent name "{agent_name}"')
    print(f"[agent] concurrency: {concurrency}, expectedInstances: {expected_instances}")

    from blocks_network.agent_instance import start_agent_instance
    from blocks_network.types import AgentInstanceOptions

    options = AgentInstanceOptions(
        handler=handler_fn,
        agent_name=agent_name,
        description=description,
        skills=skills,
        concurrency=concurrency,
        expected_instances=expected_instances,
        max_pending_backlog=max_pending_backlog,
        max_running_time_sec=max_running_time_sec,
        card=card,
    )

    result = start_agent_instance(options)

    instance_id_actual: str = getattr(result, "instance_id", "unknown")
    stop_fn = getattr(result, "stop", None)

    print(f"[agent] instance ID: {instance_id_actual}")

    shutdown_event = threading.Event()

    # Bounded grace period for stop_fn to run its cleanup code path
    # (task_client.destroy(), per-stream end(), control-channel unsubscribe,
    # executor shutdown). Tunable via env for ops who need a longer window
    # on slow machines; AGENT_SHUTDOWN_GRACE_SEC=0 hard-exits without
    # waiting (matches the previous fire-and-forget shape).
    _grace_raw = os.environ.get("AGENT_SHUTDOWN_GRACE_SEC", "3")
    try:
        _shutdown_grace_sec = max(0.0, float(_grace_raw))
    except ValueError:
        _shutdown_grace_sec = 3.0

    def _shutdown(signum: int, frame: object) -> None:
        sig_name = signal.Signals(signum).name if hasattr(signal, "Signals") else str(signum)
        print(f"\n[agent] received {sig_name}, shutting down")

        # Run cleanup OFF the signal handler so PubNub's subscribe-loop
        # join can't deadlock the main thread between bytecodes. Then
        # WAIT for stop_fn to complete (with a bounded timeout) so the
        # cleanup code path (task_client.destroy, per-task pubnub.stop,
        # stream end, control-channel unsubscribe, executor shutdown)
        # actually executes — matching Node's `instance.stop(); process.
        # exit(0)` semantics where stop runs to completion synchronously
        # before the hard exit. The timeout caps worst-case Ctrl-C
        # latency if pubnub.stop() blocks; healthy paths complete well
        # under the default 3s. Proper task-drain on SIGINT/SIGTERM is
        # tracked under the graceful-shutdown initiative
        # (dev_docs/initiative/sdk_graceful_shutdown/).
        if stop_fn is not None:
            t = threading.Thread(target=stop_fn, name="agent-stop", daemon=True)
            t.start()
            if _shutdown_grace_sec > 0:
                t.join(timeout=_shutdown_grace_sec)
        shutdown_event.set()

    signal.signal(signal.SIGINT, _shutdown)
    signal.signal(signal.SIGTERM, _shutdown)

    print("[agent] running (press Ctrl+C to stop)")
    shutdown_event.wait()
    # os._exit (not sys.exit) — sys.exit would wait on PubNub's non-daemon
    # subscription-manager / request-handler threads, which never join on
    # their own and would re-introduce the hang we're fixing here.
    os._exit(0)


def _run_from_agent_card(card_path: str) -> None:
    """Load config from an agent-card.json and start the agent."""

    with open(card_path, "r") as f:
        card = json.load(f)

    runtime = card.get("runtime", {})
    identity = card.get("identity", {})

    agent_name: str = identity.get("agentName", "")
    if not agent_name:
        print(
            "[agent] No agentName in agent-card.json identity.",
            file=sys.stderr,
        )
        sys.exit(1)

    handler_rel: str = runtime.get("handler", "")
    if not handler_rel:
        print("[agent] No runtime.handler in agent-card.json.", file=sys.stderr)
        sys.exit(1)

    card_dir = os.path.dirname(os.path.abspath(card_path))
    handler_abs = os.path.normpath(os.path.join(card_dir, handler_rel))

    if not os.path.isfile(handler_abs):
        print(f"[agent] Handler file not found: {handler_abs}", file=sys.stderr)
        sys.exit(1)

    handler_export: str = runtime.get("handlerExport", "handler")

    spec = importlib.util.spec_from_file_location("_agent_handler", handler_abs)
    if spec is None or spec.loader is None:
        print(f"[agent] Could not load handler from: {handler_abs}", file=sys.stderr)
        sys.exit(1)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    handler_fn = getattr(mod, handler_export)

    concurrency: int = int(runtime.get("concurrency", 1))

    expected_instances: int = int(runtime.get("expectedInstances", 1))

    max_pending_backlog_raw = runtime.get("maxPendingBacklog")
    max_pending_backlog: Optional[int] = (
        int(max_pending_backlog_raw) if max_pending_backlog_raw is not None else None
    )

    max_running_time_raw = runtime.get("maxRunningTimeSec")
    max_running_time_sec: Optional[int] = (
        int(max_running_time_raw) if max_running_time_raw is not None else None
    )

    print(f"[agent] using agent-card.json from {card_dir}")

    _start_and_block(
        handler_fn=handler_fn,
        agent_name=agent_name,
        name=identity.get("displayName", ""),
        description=identity.get("description", ""),
        skills=card.get("skills", []),
        concurrency=concurrency,
        expected_instances=expected_instances,
        max_pending_backlog=max_pending_backlog,
        max_running_time_sec=max_running_time_sec,
        card=card,
    )


def main(argv: Optional[list] = None) -> None:
    """Entry point for the ``blocks-run`` console script."""

    # -- Load .env file -----------------------------------------------------
    try:
        from dotenv import load_dotenv

        load_dotenv(os.path.join(os.getcwd(), ".env"))
    except ImportError:
        pass  # python-dotenv not installed; rely on real env vars

    # -- Parse arguments ----------------------------------------------------
    parser = argparse.ArgumentParser(
        prog="blocks-run",
        description="Start a Blocks Network Python agent instance.",
    )
    parser.add_argument(
        "--handler",
        type=str,
        default=None,
        help=(
            "Handler name from registry.json (e.g. 'echo', 'adder'). "
            "Falls back to agent-card.json in the current directory, "
            "then 'echo'."
        ),
    )
    parser.add_argument(
        "--registry",
        type=str,
        default=None,
        help="Path to a custom registry.json file.",
    )
    args = parser.parse_args(argv)

    # -- Try agent-card.json if no explicit handler -------------------------
    if args.handler is None:
        card_path = os.path.join(os.getcwd(), "agent-card.json")
        if os.path.isfile(card_path):
            _run_from_agent_card(card_path)
            return

    # -- Registry-based flow ------------------------------------------------
    # Resolve handler name: CLI arg > default "echo"
    handler_name: str = args.handler or "echo"

    from blocks_network.handler_loader import (
        find_handler_entry,
        get_available_handlers,
        load_handler_function,
        load_registry,
    )

    registry = load_registry(registry_path=args.registry)

    # -- Find handler entry -------------------------------------------------
    entry = find_handler_entry(registry, handler_name, runtime="python")
    if entry is None:
        available = get_available_handlers(registry, runtime="python")
        print(
            f'[agent] Unknown handler "{handler_name}".\n'
            f"[agent] Available handlers: {', '.join(available) or '(none)'}\n"
            f"[agent] Set --handler to select a handler.",
            file=sys.stderr,
        )
        sys.exit(1)

    # Validate entrypoint
    entrypoint = entry.get("entrypoint", {})
    if not entrypoint.get("path") or not entrypoint.get("symbol"):
        print(
            f'[agent] Invalid registry entry for handler "{handler_name}": '
            f"missing entrypoint.path or entrypoint.symbol.",
            file=sys.stderr,
        )
        sys.exit(1)

    # -- Check required environment variables --------------------------------
    required_env = entry.get("requiresEnv", [])
    missing_env = [var for var in required_env if not os.environ.get(var)]
    if missing_env:
        print(
            f'[agent] Handler "{handler_name}" requires environment variables: '
            f"{', '.join(missing_env)}",
            file=sys.stderr,
        )
        sys.exit(1)

    # -- Dynamically import the handler function ----------------------------
    handler_fn = load_handler_function(entry)

    # -- Resolve runtime configuration --------------------------------------
    agent_name: str = entry.get("agentName", "")
    if not agent_name:
        print(
            "[agent] No agent name configured. Ensure registry entry has agentName.",
            file=sys.stderr,
        )
        sys.exit(1)

    defaults = entry.get("defaults", {})

    concurrency: int = defaults.get("concurrency", 1)

    expected_instances: int = defaults.get("expectedInstances", 1)

    _start_and_block(
        handler_fn=handler_fn,
        agent_name=agent_name,
        name=entry["name"],
        description=entry.get("description"),
        skills=entry.get("skills"),
        concurrency=concurrency,
        expected_instances=expected_instances,
    )


if __name__ == "__main__":
    main()
