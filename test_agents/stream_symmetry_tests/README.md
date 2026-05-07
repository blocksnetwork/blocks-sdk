# Stream Symmetry Tests

End-to-end live tests that prove the Blocks streaming wire format and SDK helpers behave consistently across every direction, format, and language pairing the platform supports. Two agents — a **provider** (a published Blocks agent that runs a handler) and a **consumer** (a driver script that opens a pipe task against the provider) — exchange known payloads on four concurrent streams over a real PubNub connection, hash everything independently on each side, and assert that all four hash pairs match byte-for-byte.

A passing run is positive evidence that:

- The wire format does not corrupt arbitrary binary or arbitrary structured data.
- The SDK's producer and consumer code paths agree on encoding, fragmentation, reorder, and flatten semantics.
- The agent-side and consumer-side stream APIs are interchangeable in shape and behavior — the heart of the symmetry claim.
- Wire-level compatibility holds across both supported SDK languages (Node and Python), in any provider/consumer pairing.

## What the tests exercise

Each run drives the following platform features end-to-end:

- **Pipe-task lifecycle.** The consumer dials a pipe-style agent task, the provider's handler runs for the task duration, and a final report artifact round-trips back. This is the multi-minute streaming workflow the platform is built for.
- **Bytes streams.** Five payload variants — empty, all-zero, all-high, exactly the multipart-boundary size (16 KB), and a 64 KB pseudo-random blob — flow in each direction. The large variants force the SDK's multipart fragmentation and reorder-buffer reassembly. The all-zero and all-high variants prove the wire's base64 path is engaged end-to-end; a UTF-8 path would corrupt those byte distributions.
- **Events streams.** Three structurally distinct events (a primitive bag, a deeply-nested object, a string with emoji and right-to-left Hebrew) plus a batch of 10 small events to exercise SDK producer-side batching and the consumer-side `events()` flatten path.
- **Both directions, simultaneously.** Each format runs provider→consumer **and** consumer→provider in parallel within the same task. A bug that only affects one direction is caught.
- **Bidirectional artifact reporting.** The provider's final task artifact carries hash digests for everything it sent and received; the consumer decodes it via the SDK's inline-artifact helper and compares against its own measurements.
- **Cross-language wire compatibility.** The shared helpers produce byte-identical SHA-256 digests for the same payloads in Node and Python, so any pairing — Node↔Node, Python↔Python, Node↔Python, Python↔Node — passes with the same expected digests.

## Why provider/consumer symmetry matters

In Blocks, an agent (handler-side) and the caller of that agent (consumer-side) talk to a stream through different SDK objects: `StreamObject` on the agent side, `StreamClient` on the consumer side. Those two objects are designed to expose the same shape of write, read, and observability methods, so a developer who learns one understands the other. They're also designed to interoperate cleanly: bytes one side writes must be byte-identical when the other side reads, an event written as one structure must arrive as that same structure, and a stream lifecycle event on one side must be observable on the other.

When that symmetry breaks — a method missing on one wrapper, a different decoding path on one side, a different event-flatten policy — the platform's worst class of bug emerges: no exception, no warning, just wrong answers flowing through agents and consumers that each believe they're correct.

These tests defend the symmetry property *by construction*. The producer code and the consumer code, on every side, import the **same helper functions** from a single shared file (`symmetry_shared/`). If the two SDK shapes ever stop satisfying the same structural contract, those helpers stop type-checking or importing on one side and the test breaks loudly before it can produce a misleading pass. The hash equality across every direction and every language pair is the runtime proof that today's code actually delivers on the symmetry promise.

## Layout

```
stream_symmetry_tests/
├── README.md                    (this file)
├── symmetry_shared/             single source of truth for helpers + payloads
│   ├── helpers.ts                  produceBytes / consumeBytes / produceEvents /
│   │                               consumeEvents / canonicalJSON / sleep (Node)
│   ├── helpers.py                  same helpers, byte-identical hash semantics (Python)
│   ├── payloads.ts                 BYTES_VARIANTS × 5, EVENTS_VARIANTS × 13 (Node)
│   ├── payloads.py                 same payloads, deterministic across languages (Python)
│   └── package.json                "type": "module" — load-bearing for Node ESM
├── symmetry_provider/           Node provider agent (handler-side StreamObject)
├── symmetry_consumer/           Node test driver (consumer-side StreamClient)
├── symmetry_provider_py/        Python provider agent
└── symmetry_consumer_py/        Python test driver
```

The provider agents declare four streams in their `agent-card.json`:

| Stream | Direction (provider's POV) | Format |
|---|---|---|
| `p_to_c_bytes`  | outbound | bytes  |
| `p_to_c_events` | outbound | events |
| `c_to_p_bytes`  | inbound  | bytes  |
| `c_to_p_events` | inbound  | events |

The consumer driver discovers all four via `session.onStream` and exercises both producer and consumer roles.

## Symmetry-by-construction

`symmetry_shared/` is imported via plain relative path from both the provider and the consumer in each language:

- Node: `import { … } from '../symmetry_shared/helpers.js';`
- Python: `sys.path.insert(0, '../symmetry_shared')` then `from helpers import …`

If `StreamObject` (handler-side) and `StreamClient` (consumer-side) ever diverge structurally, this file fails to type-check or import on one of the sides and the test breaks loudly. The proof of "both APIs are interchangeable" is that the same function body works against both.

The cross-language hash equality is also by construction:
- The byte-payload LCG (`payloads.ts` / `payloads.py`) uses identical 32-bit arithmetic so payload #5 (64 KB pseudo-random) is byte-identical across languages.
- `canonicalJSON` (Node) / `canonical_json` (Python) walk objects with sorted keys, no whitespace, and `ensure_ascii=False` so the same event hashes to the same bytes in both runtimes. Verified against both Node and Python: `BYTES → e6dbed69…`, `EVENTS → b241aa20…`.

## Prerequisites

- An active Blocks API key. Run `blocks login --write-env` from each agent directory to fetch one, or paste an existing key into the `.env.example` and rename it to `.env`.
- Node 20+ and / or Python 3.10+ depending on which language(s) you're testing.
- For the Python flow, the SDK installed editable: `pip install -e blocks-sdk/sdks/python` from the repo root, OR `pip install blocks-network` from your registry.
- The `blocks` CLI on PATH (auto-installs into `~/.blocks/bin` on first SDK install).
- Network connectivity to PubNub. These are live tests; nothing about them is mocked.

## Running

Each test takes ~5–10 seconds inside a 1-minute task window. Two terminals are required: one runs the provider agent (long-lived; press Ctrl-C to stop after the consumer finishes), the other runs the consumer driver to completion.

The four provider/consumer combinations all use the same payloads and hash semantics, so any of them produce the same `e6dbed69…` / `b241aa20…` digests and the same PASS output.

### Node ↔ Node

```bash
# Terminal 1 — provider
cd blocks-sdk/test_agents/stream_symmetry_tests/symmetry_provider
cp .env.example .env && $EDITOR .env       # set BLOCKS_API_KEY
npm install
blocks publish                              # one-time per fresh registration
blocks run                                  # leave running

# Terminal 2 — consumer driver
cd blocks-sdk/test_agents/stream_symmetry_tests/symmetry_consumer
cp .env.example .env && $EDITOR .env       # use the SAME key
npm install
npm run start
```

### Python ↔ Python

```bash
# Terminal 1 — provider
cd blocks-sdk/test_agents/stream_symmetry_tests/symmetry_provider_py
cp .env.example .env && $EDITOR .env
pip install -e .
blocks publish
blocks run

# Terminal 2 — consumer driver
cd blocks-sdk/test_agents/stream_symmetry_tests/symmetry_consumer_py
cp .env.example .env && $EDITOR .env
pip install -e .
python main.py
```

`blocks run` for Python expects a `.venv` walking up from the agent dir. The standard place is `blocks-sdk/.venv` (created on first `pip install -e blocks-sdk/sdks/python`). The CLI walks up and finds it.

### Cross-language pairs

Run any provider against any consumer. The agent name the consumer dials defaults to its language sibling (`symmetry_provider` for the Node consumer, `symmetry_provider_py` for the Python consumer); override via the `AGENT_NAME` env var to swap targets:

```bash
# Node provider running, Python consumer dialing it
cd blocks-sdk/test_agents/stream_symmetry_tests/symmetry_consumer_py
AGENT_NAME=symmetry_provider python main.py

# Python provider running, Node consumer dialing it
cd blocks-sdk/test_agents/stream_symmetry_tests/symmetry_consumer
AGENT_NAME=symmetry_provider_py npm run start
```

A passing cross-language pair proves the wire format is language-agnostic for both bytes and events streams, with identical canonical-JSON encoding and identical base64 byte-stream encoding.

## Expected output

The consumer prints per-stream progress, the provider's report artifact, and a final block:

```
[stream] p_to_c_bytes (inbound/bytes)
[stream] p_to_c_events (inbound/events)
[stream] c_to_p_bytes (outbound/bytes)
[stream] c_to_p_events (outbound/events)
[c_to_p_bytes] sent 83968B / 5 writes / hash=e6dbed690bf6a1e1…
[c_to_p_events] sent 13 events / hash=b241aa20b4bcf2d1…
[p_to_c_bytes] got 83968B / 5 chunks / hash=e6dbed690bf6a1e1…
[p_to_c_events] got 13 events / hash=b241aa20b4bcf2d1…
[artifact] received provider report (~503B)

[terminal] completed

Hash comparison (expected = sender, actual = receiver):
  ✓ P->C bytes   expected=e6dbed690bf6a1e1…  actual=e6dbed690bf6a1e1…
  ✓ P->C events  expected=b241aa20b4bcf2d1…  actual=b241aa20b4bcf2d1…
  ✓ C->P bytes   expected=e6dbed690bf6a1e1…  actual=e6dbed690bf6a1e1…
  ✓ C->P events  expected=b241aa20b4bcf2d1…  actual=b241aa20b4bcf2d1…

SYMMETRY TEST PASSED
```

Volume numbers are deterministic:
- `83968 B` = `0 + 1024 + 1024 + 16384 + 65536` — the five `BYTES_VARIANTS` summed.
- `5 chunks / 5 writes` — one per byte payload.
- `13 events` — three distinct events plus a 10-event batch.

If you see those numbers and four ✓ marks with `e6dbed69…` and `b241aa20…` prefixes, the test passed and you can trust the provider-side path even without inspecting the provider log (the four ✓ checks compare against hashes the provider computed independently and reported via the artifact — a passing comparison can't be fabricated by the consumer alone).

## Failure modes and how to interpret them

| Symptom | Likely cause |
|---|---|
| `MISSING` in any "actual" / "expected" column | One side's stream never arrived (subscribe race, agent not running, PAM grant issue). Re-check `blocks publish` and that both terminals authenticate with the SAME `BLOCKS_API_KEY`. |
| ✗ on `P->C bytes` only | Consumer-side `StreamClient.bytes()` decoded incorrectly OR provider's `StreamObject.write()` produced wrong bytes. Check the provider log for the producer's reported hash. |
| ✗ on `C->P bytes` only | Mirror of the above on the handler side — `StreamObject.bytes()` decoded incorrectly. This is the BLOCKS-262 trap; pre-fix you'd see `e3b0c44…` (SHA-256 of empty input) on every chunk because `frame.data` was `string[]` not `Uint8Array`. |
| ✗ on `*events` checks only | Events-flatten path misbehaving (consumer-side `events()` returning the array envelope instead of one event per yield, or producer-side batching bug). |
| Hangs after `Sending pipe task to …` | API key is invalid or `BLOCKS_CDM_URL` points at an unreachable instance. |
| `[terminal] failed` instead of `completed` | Provider handler raised. Check provider log — the handler logs each phase under `[handler <task-id>]`. |
| Consumer doesn't return to shell | Known PubNub Python SDK background-thread issue; the Python consumer uses `os._exit(0)` to force the prompt back. If you're seeing the hang anyway, ensure you're on the post-fix `main.py`. |

## What this exercises

- Forwarded `StreamObject.bytes()` / `bytes()` (Python) — the BLOCKS-262 fix that started this whole effort.
- Forwarded `StreamObject.events<T>()` / `events()` — same fix, events flavor.
- Consumer-side `StreamClient.bytes()` and `StreamClient.events<T>()` — pre-existing, retested for parity.
- Multipart fragmentation + reorder buffer reassembly (payload #5 is 64 KB, well over the 16 KB `STREAM_MAX_MESSAGE_SIZE` boundary; payload #4 is exactly on the boundary).
- Empty-payload edge case (`Uint8Array(0)` / `b""`) — passes through as a chunk, isn't silently dropped.
- All-zero and all-high binary content — neither corrupts the base64 path.
- Special-character event content (emoji, RTL Hebrew) — UTF-8 round-trip.
- Producer-side batched writes (10 events written in tight succession) — the consumer's flatten path returns 10 events, not one batch.
- Bidirectional task lifecycle — handler runs both `produce` and `consume` halves concurrently, returns its artifact only after both complete.
- Artifact decode on the consumer side via `decodeInlineArtifact` / `decode_inline_artifact`.

## What this does NOT exercise

These belong to focused tests, not this symmetry pass:

- `uuid` forwarder — declared and reachable, but the test doesn't read its value. Add a `console.log(stream.uuid)` and assert the `{agent}-stream-NNNN` shape if you want explicit coverage.
- `onError(cb)` / `on_error(cb)` — the happy path doesn't fire errors. The Node live test covers a forwarding-only assertion via spies; T7a-revocation live coverage is gated behind a stub pending a deterministic revocation hook (see BLOCKS-262 Step 5 / 13 in the IMPL doc).
- `readable()` / `as_file()` adapter — neither side pipes through the file-stream adapter.
- Concurrent identical streams (per-stream isolation under load).
- Long-running tasks crossing token-TTL boundaries.
- Backpressure (producer outpaces consumer).
- Lifecycle / failure paths (terminal-during-active-stream, mid-stream cancel, etc.).

## Maintenance notes

The shared helpers + payloads are a single source of truth. **Edit `symmetry_shared/helpers.{ts,py}` and `symmetry_shared/payloads.{ts,py}` together** — diverging the two language ports breaks the cross-language hash equality and the test's symmetry claim becomes false advertising.

If you add a new payload variant or a new helper, keep the canonical-JSON / LCG semantics aligned. The verification pattern is:

```bash
# Both should print the same digest for the same input.
node -e "import('./symmetry_shared/helpers.ts').then(({ /* ... */ }) => /* hash a fixture */)"
python -c "from symmetry_shared.helpers import canonical_json; import hashlib; print(hashlib.sha256(canonical_json({...}).encode()).hexdigest())"
```

The agent cards declare four named streams with `dedicated` affinity. If the SDK's stream-affinity defaults change, update the cards alongside.

## Related

- `dev_docs/SDK_CONTRACT.md` §8.2.10 — `StreamObject` API (the public surface this test exercises).
- `dev_docs/initiative/05-02_sdk_blocks-262/` — the initiative this test was built to validate.
- BLOCKS-262 (Jira) — origin and acceptance criteria for the wrapper expansion.
