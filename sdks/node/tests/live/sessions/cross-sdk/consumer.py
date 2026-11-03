"""argv: <agent_name> <inputs_json>. Prints one JSON report line for cross-sdk.live.test.ts."""

import base64
import json
import os
import sys
import time

from blocks_network import BlocksClient

ROUTE_WAIT_S = 30.0
OUTCOME_TIMEOUT_S = 30.0


def _field(obj, name):
    return obj.get(name) if isinstance(obj, dict) else getattr(obj, name, None)


def _open_when_routed(client, agent_name):
    deadline = time.monotonic() + ROUTE_WAIT_S
    while True:
        try:
            return client.open_session(agent_name)
        except Exception as err:
            if getattr(err, "code", None) != "SESSION_NO_ROUTE" or time.monotonic() > deadline:
                raise
            time.sleep(0.5)


def _state(value):
    return getattr(value, "value", value)


def _echo(outcome):
    return json.loads(base64.b64decode(_field(outcome.artifacts[0], "data")))


def main() -> None:
    agent_name, inputs = sys.argv[1], json.loads(sys.argv[2])
    client = BlocksClient.create(
        billing_mode="free",
        api_key=os.environ["BLOCKS_API_KEY"],
        base_url=os.environ["BLOCKS_BACKEND_URL"],
    )
    try:
        session = _open_when_routed(client, agent_name)
        exchanges = []
        progress = {}
        for text in inputs:
            exchange = session.send(text)
            progress[exchange.exchange_id] = []
            exchange.on_progress(lambda p, log=progress[exchange.exchange_id]: log.append(p.message))
            exchanges.append((text, exchange))
        outcomes = [ex.wait_for_outcome(timeout=OUTCOME_TIMEOUT_S) for _, ex in exchanges]
        session.close()
        report = {
            "sent": [{"exchangeId": ex.exchange_id, "text": text} for text, ex in exchanges],
            "outcomes": [
                {
                    "exchangeId": o.exchange_id,
                    "state": _state(o.state),
                    "echo": _echo(o),
                    "progress": progress[o.exchange_id],
                }
                for o in outcomes
            ],
            "finalState": _state(session.state),
        }
        print(json.dumps(report), flush=True)
    finally:
        client.destroy()


if __name__ == "__main__":
    main()
