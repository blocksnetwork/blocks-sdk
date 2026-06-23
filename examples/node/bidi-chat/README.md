# bidi-chat (Node)

Minimal example of a **bidirectional** stream — both the agent (provider)
and the consumer write to and read from the same stream.

This example exists to exercise the consumer-side stream UUID fix from
[PR #835](https://github.com/pubnub/blocksnetwork/pull/835): when the
consumer and provider share the same `agentName` on their first opened
stream, the consumer-side `StreamClient` must derive its publisher UUID
from the consumer's user id (not the provider's agent name) so the
self-echo filter (`meta.sender !== myUuid`) does not silently drop one
side's messages.

## Behavior

- Agent: opens a `direction: bidirectional`, `format: events` stream,
  optionally writes one greeting echo, then loops on inbound messages
  and replies with `AGENT> <UPPERCASED>`. Exits the loop when it sees
  `bye` from the consumer.
- Consumer: connects, sends a few lines, reads each agent reply in real
  time, sends `bye`, then waits for the artifact + terminal events.

A 1:1 consumer/provider pair is exactly the configuration where this
bug is hardest to spot — both sides start with `agentName`-prefixed
UUIDs that begin at counter `0001`, so without the fix their UUIDs
collide and the self-echo filter drops every cross-side message
silently. The script exits with code `2` on collision (zero inbound
messages received), distinct from `1` for setup failure.

## Prerequisites

- Node.js 22+
- `BLOCKS_API_KEY` (run `blocks login --write-env` in the project dir)

## Run

In one terminal — start the agent:

```bash
cd blocks-sdk/examples/node/bidi-chat
blocks register   # first time only -- private + free (recommended first step)
blocks run
# Later, to make the agent public or set pricing: blocks publish
```

In another terminal — run the consumer:

```bash
cd blocks-sdk/examples/node/bidi-chat
npx tsx bidi-chat-consumer.ts
# or with custom messages:
npx tsx bidi-chat-consumer.ts "first" "second" "third"
```
