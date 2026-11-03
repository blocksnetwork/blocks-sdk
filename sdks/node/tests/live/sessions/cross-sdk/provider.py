"""argv: <agent_name> <card_json>. Prints {"event": "ready"}, then runs until stdin closes."""

import json
import os
import sys

from blocks_network import start_agent_instance
from blocks_network.types import AgentInstanceOptions


def _field(obj, name):
    return obj.get(name) if isinstance(obj, dict) else getattr(obj, name, None)


def on_exchange(_ctx, exchange):
    exchange.report_status("working")
    text = "".join(_field(p, "text") or "" for p in exchange.request_parts)
    payload = {"exchangeId": exchange.exchange_id, "text": text, "sdk": "python"}
    return {"artifacts": [{"data": json.dumps(payload), "mimeType": "application/json"}]}


def main() -> None:
    agent_name, card_json = sys.argv[1], sys.argv[2]
    runtime = start_agent_instance(
        AgentInstanceOptions(
            card=json.loads(card_json),
            agent_name=agent_name,
            base_url=os.environ["BLOCKS_BACKEND_URL"],
            on_exchange=on_exchange,
        )
    )
    print(json.dumps({"event": "ready"}), flush=True)
    try:
        sys.stdin.read()
    finally:
        runtime["stop"]()


if __name__ == "__main__":
    main()
