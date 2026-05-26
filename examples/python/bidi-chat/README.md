# bidi-chat (Python)

Minimal example of a **bidirectional** stream — both the agent (provider)
and the consumer write to and read from the same stream. Mirrors the
Node example at `examples/node/bidi-chat/`.

This example exercises the consumer-side stream UUID fix from
[PR #835](https://github.com/pubnub/blocksnetwork/pull/835): when the
consumer and provider share the same `agent_name` on their first opened
stream, the consumer-side `StreamClient` must derive its publisher UUID
from the consumer's user id (not the provider's agent name) so the
self-echo filter does not silently drop one side's messages.

## Behavior

- Agent: opens a `direction="bidirectional"`, `format="events"` stream,
  optionally writes one greeting echo, then loops on inbound `{ text }`
  events and replies with `AGENT> <UPPERCASED>`. Exits the loop when it
  sees `bye` from the consumer.
- Consumer: connects, sends a few lines (in a producer thread because
  Python's `events()` iterator is blocking), reads each agent reply on
  the main thread, sends `bye`, then waits for the artifact + terminal
  events.

A 1:1 consumer/provider pair is exactly the configuration where this
bug is hardest to spot — both sides start with `agent_name`-prefixed
UUIDs that begin at counter `0001`, so without the fix their UUIDs
collide and the self-echo filter drops every cross-side message
silently. The script exits with code `2` on collision (zero inbound
messages received), distinct from `1` for setup failure.

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` (run `blocks login --write-env` in this directory)

## Run

```bash
cd blocks-sdk/examples/python/bidi-chat
pip install -e ../../../sdks/python    # if the SDK isn't installed yet
pip install -e .

# Terminal 1 — agent
blocks publish    # first time only
blocks run

# Terminal 2 — consumer
python main.py
# or with custom messages:
python main.py "first" "second" "third"
```
