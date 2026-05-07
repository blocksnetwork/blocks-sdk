# symmetry_shared

Single source of truth for the BLOCKS-262 round-trip symmetry test. Both `symmetry_provider/` (the agent) and `symmetry_consumer/` (the test driver) import from this directory via plain relative path so the two sides literally call the same helper functions. If `StreamObject` and `StreamClient` ever diverge structurally, this file stops type-checking on one side and the test breaks loudly.

## Files

- `helpers.ts` — `produceBytes`, `consumeBytes`, `produceEvents`, `consumeEvents` plus a shared SHA-256 hasher and a sleep helper. Structurally typed against `BytesProducer`/`BytesConsumer`/`EventsProducer`/`EventsConsumer` so it works against either SDK shape.
- `payloads.ts` — `BYTES_VARIANTS` (5 byte payloads: empty, all-zero, all-high, multipart-boundary, 64 KB random) and `EVENTS_VARIANTS` (primitive, nested, special-chars, plus 10 batched events).

## How to run

Two terminals:

```bash
# Terminal 1 — provider (the agent)
cd ~/code/agents/symmetry_provider
npm install
blocks publish    # one-time, registers the agent
blocks run        # starts the handler

# Terminal 2 — consumer (the test driver)
cd ~/code/agents/symmetry_consumer
npm install
npm run start
```

The consumer sends a 1-minute pipe task, opens all four streams, and prints PASS/FAIL based on whether the four hash pairs (P->C bytes, P->C events, C->P bytes, C->P events) match between sides.

## Timing

- Both sides wait **2 seconds after task start** before publishing on their outbound streams. Gives the other side time to subscribe so no payload races the subscribe activation.
- The consumer uses a deadline of `(taskStart + duration) - 2 seconds` and skips publishing if the deadline is already past. Prevents the consumer's writes from arriving after the task has expired.
