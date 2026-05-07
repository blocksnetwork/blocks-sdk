"""
Stock-sim-consumer handler.

Consumer agent that submits a pipe task to stock-sim and consumes
the resulting stock-price stream. Returns a summary artifact with
quote counts and last prices.
"""

from __future__ import annotations

import json
import os
import sys
from typing import Any, Dict, Optional

# Ensure sibling modules are importable when loaded via importlib
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from blocks_network.types import StartTaskMessage, TaskContext

from stock_sim_client import (
    finalize_stock_request,
    parse_stock_request,
    prompt_for_stock_request,
    run_stock_sim_task,
)


def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    if ctx is None or ctx.task_client is None:
        return {
            "artifacts": [{"data": json.dumps({"error": "TaskClient not available"}), "mimeType": "application/json"}],
        }

    initial_request = parse_stock_request(task.request_parts)
    # Only prompt interactively if no input was provided AND running in a terminal.
    # When task input is present, always use it without prompting.
    has_input = bool(initial_request.get("symbols_input") or initial_request.get("duration_minutes"))
    request = (
        prompt_for_stock_request(initial_request)
        if not has_input and sys.stdin.isatty()
        else finalize_stock_request(initial_request)
    )

    ctx.report_status(f"Requesting {', '.join(request.symbols)} from stock-sim...")

    try:
        result = run_stock_sim_task(
            task_client=ctx.task_client,
            owner_id=task.owner_id,
            request=request,
            log=lambda line: print(f"[stock-sim-consumer] {line}"),
        )
    except Exception as err:
        return {
            "artifacts": [{"data": json.dumps({
                "ok": False,
                "error": str(err),
                "symbols": request.symbols,
                "provider": request.provider,
            }, indent=2), "mimeType": "application/json"}],
        }

    ctx.report_status("Stock simulation finished")

    return {
        "artifacts": [{
            "data": json.dumps(
                {
                    "ok": True,
                    "providerTaskId": result.provider_task_id,
                    "symbols": result.symbols,
                    "durationMinutes": result.duration_minutes,
                    "quotesReceived": result.quotes_received,
                    "lastQuotes": {
                        k: {
                            "type": v.type,
                            "symbol": v.symbol,
                            "price": v.price,
                            "change": v.change,
                            "tick": v.tick,
                            "at": v.at,
                        }
                        for k, v in result.last_quotes.items()
                    },
                },
                indent=2,
            ),
            "mimeType": "application/json",
        }],
    }
