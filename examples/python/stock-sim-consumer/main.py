"""
Standalone CLI for running the stock-sim-consumer without the Blocks runner.

Usage:
    python main.py

Reads CDM config from BLOCKS_CDM_URL (or defaults to production CDN).
"""

from __future__ import annotations

import json
import os
import sys

from dotenv import load_dotenv

load_dotenv()

from blocks_network import TaskClient, fetch_cdm_config

from stock_sim_client import prompt_for_stock_request, run_stock_sim_task


def main() -> None:
    auth_token = os.environ.get("BLOCKS_TOKEN")
    if not auth_token:
        print('BLOCKS_TOKEN not set. Run "blocks login" first.', file=sys.stderr)
        sys.exit(1)

    cdm_config = fetch_cdm_config()
    keyset = cdm_config.playground
    base_url = cdm_config.api.base_url

    client = TaskClient(
        subscribe_key=keyset.subscribe_key,
        publish_key=keyset.publish_key,
        base_url=base_url,
        auth_token=auth_token,
    )

    try:
        request = prompt_for_stock_request()
        print(
            f"Requesting {', '.join(request.symbols)} from stock-sim for "
            f"{request.duration_minutes} minute{'s' if request.duration_minutes != 1 else ''}..."
        )
        print("---")

        result = run_stock_sim_task(
            task_client=client,
            request=request,
            log=lambda line: print(line),
        )

        print("---")
        print("Final summary:")
        print(json.dumps(
            {
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
        ))
    except KeyboardInterrupt:
        print("\nInterrupted")
    finally:
        client.destroy()


if __name__ == "__main__":
    main()
