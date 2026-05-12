# Python Agent Reference

Reference for building Blocks Network agents in Python. Use only when
the user explicitly requests Python. The default language is Node (TypeScript).

---

## Project Structure

```
my-agent/
  agent-card.json
  handler.py
  trigger.py
  pyproject.toml
  pip.conf
  .env
  .gitignore
  Dockerfile
```

---

## Handler Signature

Python handlers are **synchronous** and use `snake_case` APIs:

```python
from __future__ import annotations
from typing import Any, Dict, Optional
from blocks_network.types import StartTaskMessage, TaskContext

def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    parts = task.request_parts or []
    text = parts[0].text if parts and hasattr(parts[0], "text") else ""

    if ctx:
        ctx.report_status("Processing...")

    return {"artifacts": [{"data": f"Processed: {text}", "mimeType": "text/plain"}]}
```

Return a plain `dict` with `artifacts` (list of artifact entries).
Each entry has `data` (str or bytes) and `mimeType` (required).
Optional keys per entry: `fileName`, `outputId`.

---

## TaskContext

The `ctx` parameter provides handler APIs:

- `ctx.task_id` -- current task ID
- `ctx.has_stream` -- whether streaming is available
- `ctx.consumer_public_key` -- consumer's public key (E2E encryption)
- `ctx.report_status(message)` -- throttled status update (1/sec)
- `ctx.create_stream(...)` -- create a stream (see Streaming Handler)
- `ctx.task_client` -- `TaskClient` for agent-to-agent calls
- `ctx.download_input_artifact(part)` -- download file-type input artifact, returns `bytes`
- `ctx.publish_artifact(data, options?)` -- publish an artifact mid-handler
- `ctx.cancel_event` -- `threading.Event`, set on cancellation
- `ctx.is_cancelled` -- property: `True` if task was cancelled
- `ctx.is_expired` -- property: `True` if task duration expired

---

## Key Differences from Node

| Python | Node |
|--------|------|
| `task.request_parts` | `task.requestParts` |
| `ctx.report_status()` | `ctx.reportStatus()` |
| `ctx.create_stream()` | `ctx.createStream()` |
| Return `dict` with `artifacts` list | Return `HandlerResult` with `artifacts` array |
| `runtime.handlerExport`: `"handler"` | `runtime.handlerExport`: `"default"` |
| `handler.py` | `handler.ts` |
| Synchronous function | Async function |

---

## Streaming Handler

<!-- sync: streaming rules duplicated in SKILL.md, agent-card-reference.md, node-reference.md -->
**Requires** a top-level `streams` block in `agent-card.json`. Without it
`ctx.create_stream()` throws:
`"Streaming was not negotiated for this task."`

Add this to `agent-card.json` (peer of `identity`, `capabilities`, `io`):

```json
"streams": {
  "_default": {
    "direction": "outbound",
    "format": "bytes",
    "description": "Main output stream"
  }
}
```

**Critical rules:**
- `streams` is a **top-level** property -- NOT inside `capabilities`.
- `capabilities` only accepts `taskKinds` -- anything else fails `blocks check`.
- For request-only agents (`taskKinds: ["request"]`), streams must contain only `_default`.
- Both `direction` and `format` are required on each stream.
- A stream declared with `affinity: "shared"` is a cross-task broadcast and is **pipe-only** — `create_stream()` raises on request tasks, and per-task `stream.end()` does not publish a `stream_end` marker. See `dev_docs/SDK_CONTRACT.md` §4.4.2, §8.4.1a, §8.7.3a, §8.7.4 for the lifecycle contract.
- Re-publish (`blocks publish`) after adding `streams`.

`create_stream` accepts keyword-only options: `direction` (`"outbound"`|`"inbound"`|`"bidirectional"`, default `"outbound"`), `on_activate`, `metadata`, `external` (bool), `format` (`"bytes"`|`"events"`), `bundle_size_bytes`, `max_latency_ms`, `declared_stream` (omit when the card declares a single stream -- the SDK resolves it automatically; **required** when the card declares multiple streams), `subscribe_grace_ms`. The SDK derives the channel from `declared_stream` plus the card's affinity; handler code cannot specify the channel suffix directly.

### StreamObject (handler-side)

The wrapper returned by `ctx.create_stream(...)`. Mirrors the consumer-side `StreamClient` read/error/uuid surface so handler code uses the same iterators and callbacks consumers do (see `dev_docs/SDK_CONTRACT.md` §8.2.10 for the normative definition).

Properties: `stream_id`, `channel`, `is_active`, `external`, `uuid` (read; same as `StreamClient.uuid`, useful for log correlation), `token` (external only).

Write / lifecycle: `write(data)` (raises on inbound-only and external streams), `end()`, `activate(opts=None)` (external only), `on_end(cb)`.

Read iterators (inbound and bidirectional streams):
- **Recommended:** `bytes()` for `format: "bytes"` streams (yields `bytes`, decodes base64/utf8 transparently). `events()` for `format: "events"` streams (yields one event per iteration; flattens producer-side batches). `as_file()` returns a `BufferedReader` for `shutil.copyfileobj` / subprocess pipe integration.
- **Low-level:** `inbound` is an `Iterator[InboundMessage]` over raw wire envelopes (`{ data, seq, ts, format, encoding }`). Reach for it only when you need raw envelope metadata; `bytes()` / `events()` are the everyday paths.

Errors: `on_error(cb)` subscribes to per-stream PubNub status errors. The callback receives a `StreamError` with `category`, `error`, `channel`, `timestamp`, `fatal`. **Append-only** — register before the read path activates; past errors do not replay. On `fatal=True` the underlying client auto-tears down so the iterator exits cleanly.

`on_inbound_done` is intentionally NOT part of `StreamObject` — it's an internal callback owned by `TaskSession` (SDK_CONTRACT §8.3.8). For a "stream drained" signal, iterate `bytes()` / `events()` / `inbound` to completion.

Type exports for handler code: `from blocks_network import InboundMessage, StreamError`.

```python
def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    parts = task.request_parts or []
    text = parts[0].text if parts and hasattr(parts[0], "text") else "Hello!"

    if ctx:
        ctx.report_status("Streaming...")
        stream = ctx.create_stream(
            declared_stream="main-stream",
            bundle_size_bytes=2048,
            max_latency_ms=50,
        )
        for word in text.split():
            stream.write(word + " ")
        stream.end()

    return {"artifacts": [{"data": text, "mimeType": "text/plain"}]}
```

---

## Agent-to-Agent (Orchestrator)

Uses `ctx.task_client` (injected by the runtime — no extra imports needed).
Handler-level imports (`StartTaskMessage`, `TaskContext`) come from the
handler file's header shown in the Handler Signature section above.

```python
import json
import threading

def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    text = ""
    parts = task.request_parts or []
    if parts and hasattr(parts[0], "text"):
        text = parts[0].text

    if ctx:
        ctx.report_status("Delegating to sub-agent...")

    session = ctx.task_client.send_message(
        agent_name="summarizer",
        owner_id=task.owner_id,
        request_parts=[{"partId": "request", "text": text}],
    )

    result_holder = {}
    done = threading.Event()

    def on_artifact(event):
        result_holder["data"] = event
        done.set()

    def on_terminal(_event):
        done.set()

    session.on_artifact(on_artifact)
    session.on_terminal(on_terminal)
    done.wait()
    session.close()

    data = result_holder.get("data", "Sub-agent completed")
    return {"artifacts": [{"data": json.dumps(data), "mimeType": "application/json"}]}
```

---

## TaskClient & TaskSession

**send_message(\*, agent_name, request_parts, ...)** -- all keyword-only. `owner_id` is auto-populated from auth (optional override). Optional: `idempotency_key`, `task_kind` (`"request"`|`"pipe"`), `duration`, `consumer_public_key`, `push_notification_config`, `retry_policy`, `auto_drain`, `drain_window_s` (default 30.0; overrides the per-session auto-drain window for already-open streams). Returns `TaskSession`.

**TaskClient.connect(task_id, auto_drain=True, drain_window_s=None, role="consumer")** -- returns a `TaskSession`. `drain_window_s` mirrors `send_message` so reconnecting consumers can tune the drain window for streams they open via `open_all_streams()` / `on_stream`. `role` defaults to `"consumer"` (task submitter); set to `"provider"` when the caller is the agent owner viewing a received task.

**TaskSession** -- properties: `task_id`, `owner_id`, `org_id`, `read_token`, `status_channel`, `state`, `is_closed`. Event listeners: `on_progress(cb)`, `on_artifact(cb)`, `on_terminal(cb)`, `on_event(cb)`, `on_error(cb)`, `on_stream(cb)`. Blocking wait: `wait_for_terminal(timeout=60)` -- blocks until terminal event, returns `TaskEvent`; resolves immediately for already-terminal sessions. Typed event properties: `event.message`, `event.progress`, `event.state`, `event.artifact_ref`. History helpers: `list_events()` (all valid task events parsed by `connect()` history), `list_artifacts()`, `download_artifact(ref)`, `save_artifacts(directory)`. Stream helpers: `list_streams()`, `wait_for_stream(stream_id?, timeout?)`, `wait_for_stream_where(predicate, timeout?)`, `open_all_streams(**opts)` (active-session eager-open — returns `List[StreamClient]` for every readable ref, skipping outbound-only and already-ended refs). Card lookup: `client.get_agent_card(agent_name)`. Control: `cancel()`, `terminate()`, `close()`. Context managers: `with client:` calls `destroy()`, `with session:` calls `close()`.

`on_artifact(cb)` replays pre-populated artifacts synchronously at registration time, in the same spirit as `on_stream()` and sticky `on_terminal()`. Replay events are minimal synthetic artifact events with `type`, `taskId`, and `artifactRef`; original history-only wire fields such as `outputId` and `protocolVersion` are not retained.

**Part helpers:** `text_part(text, part_id=)`, `file_part(path_or_data, **opts)` -- exported from `blocks_network`.

**Auth — `token_endpoint`:** accepts either a string URL or a `TokenEndpointConfig` TypedDict (`{ "url": str, "headers"?: dict, "body"?: Any }`). Unlike Node, Python's TypedDict has **no `credentials` field** — urllib has no fetch-equivalent. For cookie-based auth parity, pass the cookie explicitly in `headers={'Cookie': 'session=...'}`. `TokenEndpointConfig` is exported from `blocks_network`.

**Stream consumer APIs:** `stream.bytes()` (decoded byte iterator), `stream.events()` (flattened event iterator), `stream.as_file()` (returns `BufferedReader`). Background helpers: `stream.consume_in_background(cb)`, `stream.write_periodic(interval, gen, stop?)`. All iterators deliver messages in sequence order via the SDK's reorder buffer. `ref.open(reorder_timeout_ms=)` configures the gap timeout (default 750ms; 0 disables reordering).

**Stream error surfaces:** `StreamClient.on_error(cb)` subscribes to per-stream PubNub status errors (PAM revocation, network failures, malformed payloads). Callback receives a `StreamError` with `category`, `error`, `channel`, `timestamp`, `fatal`; on `fatal=True` the client auto-tears down and signals iterator completion, so handlers typically only react to non-fatal errors. Distinct from `TaskSession.on_error` (which surfaces consumer-callback exceptions, not stream-level failures). `StreamUnavailableError` (exported from `blocks_network`) is raised synchronously by `StreamRef.open()` when the owning session is already terminal; it carries `.terminal_state` and `.stream_id` so callers can branch on `isinstance(err, StreamUnavailableError)`.

---

## Trigger Script

`trigger.py` is auto-generated by `blocks init --language python`. To send a task:

```bash
python trigger.py
```

Edit the `request_parts` list in `trigger.py` to change the input.

---

## Agent Card (runtime section)

```json
{
  "runtime": {
    "handler": "./handler.py",
    "handlerExport": "handler",
    "concurrency": 1,
    "expectedInstances": 1
  }
}
```

---

## Pipe Tasks (Provider Handler)

Pipe tasks are long-running streaming tasks with an explicit duration. The
handler loops until cancelled or expired, streaming data to consumers.

**Key differences from request tasks:**
- Handler voluntary return does NOT auto-publish a terminal event
- Streams continue running after handler returns
- `duration` (minutes) is set by the consumer; available as `task.duration`
- The runtime only publishes terminal on cancel, expire, or terminate

```python
import json
import time

def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    if ctx is None:
        raise ValueError("TaskContext required for pipe tasks")

    ctx.report_status("Starting stream...")
    stream = ctx.create_stream(
        declared_stream="data-stream",
        format="events",
        bundle_size_bytes=4096,
        max_latency_ms=100,
    )

    # Long-running loop -- exits on cancel or duration expiry
    while not ctx.cancel_event.is_set():
        stream.write(json.dumps({"ts": time.time(), "value": 42}) + "\n")
        ctx.cancel_event.wait(timeout=1)  # cancellation-aware sleep
        if ctx.is_expired:
            break

    stream.end()

    reason = ("duration_expired" if ctx.is_expired
              else "canceled" if ctx.is_cancelled
              else "stopped")
    ctx.report_status(f"Stream ended: {reason}")

    return {"artifacts": [{"data": json.dumps({"reason": reason}), "mimeType": "application/json"}]}
```

---

## Sending Pipe Tasks (Consumer)

Consumers create pipe tasks by setting `task_kind='pipe'` and providing a
`duration` (required, 1-43200 minutes). They then discover and consume
the provider's stream.

```python
from blocks_network import text_part

with client:
    session = client.send_message(
        agent_name="my-pipe-agent",
        task_kind="pipe",
        duration=5,  # minutes (required for pipe tasks)
        request_parts=[text_part("start streaming")],
    )
    with session:
        # Discover the stream (blocks until stream_started event)
        stream_ref = session.wait_for_stream(timeout=30)
        stream = stream_ref.open()

        # Consume inbound data using high-level API
        for event in stream.events():
            print("Received:", event)

        # Or consume bytes:
        # for chunk in stream.bytes():
        #     output_file.write(chunk)

        # Or use file-like interface:
        # import shutil
        # with open("output.bin", "wb") as f:
        #     shutil.copyfileobj(stream.as_file(), f)

        terminal = session.wait_for_terminal(timeout=300)
        print("Task ended:", terminal.state)
```

**InboundMessage** shape: `data`, `seq`, `ts`, `format`, `encoding`.

**Auto-drain**: When the terminal event arrives, the session waits up to
the configured drain window (default **30 seconds**; override per session
via `drain_window_s` on `TaskClient.send_message()` and
`TaskClient.connect()`) for open streams to deliver remaining data before
closing.

---

## create_task_client() Convenience Factory

The recommended way to create a consumer client. Loads `.env` automatically
and reads `BLOCKS_API_KEY` from the environment:

```python
from blocks_network import create_task_client

client = create_task_client()                              # billing_mode='free' (default, playground keyset)
client = create_task_client(billing_mode="paid")           # network keyset
client = create_task_client(api_key="explicit")            # explicit key
client = create_task_client(token_endpoint="https://my-backend/token")  # proxy auth
```

- `billing_mode` selects the keyset (`"free"` → playground, `"paid"` → network) and must match the target agent's server-derived billing_mode (exception: authenticated same-org callers are exempt from this check)
- When no auth mode is provided, falls back to `BLOCKS_API_KEY` from the environment
- Accepts `token_endpoint` or `token_provider` as alternative auth modes (skips `BLOCKS_API_KEY`)
- Extra `**kwargs` forwarded to `TaskClient.create()`

---

## TaskClient.create() Class Method

Lower-level factory with full control over CDM config resolution and auth:

```python
client = TaskClient.create(
    billing_mode="free",  # required: 'free' | 'paid'
    api_key="blocks-api-key",  # auth mode 1: API key -> JWT
    # token_endpoint="https://my-backend/token",  # auth mode 2: session-authenticated backend endpoint (customer proxy OR dashboard embedder -- see SDK_CONTRACT §8.6.4b/g)
    # token_provider=my_func,  # auth mode 3: custom callback
)
```

- `billing_mode` is the first positional arg (required) and selects the keyset ('free' → playground, 'paid' → network)
- Exactly one auth mode: `api_key`, `token_endpoint`, or `token_provider`
- Does not call `load_dotenv()` -- caller manages env vars
- Synchronous (no `await`)

---

## TaskClient.connect() -- Reconnecting to Existing Tasks

Reconnect to an active or completed task by ID:

```python
session = client.connect(task_id="task-abc-123")

# For terminal tasks: history is preloaded, no live events
events = session.list_events()
artifacts = session.list_artifacts()
streams = session.list_streams()

# For active tasks: history preloaded + live subscription
session.on_artifact(lambda event: print("Artifact:", event))
session.on_stream(lambda ref: print("Stream:", ref))

# Provider (agent owner) viewing a received task:
provider_session = client.connect(task_id="task-abc-123", role="provider")
```

- Requires JWT-based auth (`api_key`, `token_endpoint`, or `token_provider`
  via `TaskClient.create()`). `AgentAuth` is not supported for `connect()`.
- `role` defaults to `"consumer"` (task submitter). Set to `"provider"`
  when the caller owns the agent that received the task.
- Terminal tasks: preloads events/artifacts/streams from history, no live events
- Active tasks: preloads history, then subscribes from cursor (no gap)

---

## Consumer Stream Discovery

Multiple ways to discover and consume streams on a `TaskSession`:

```python
# Wait for any stream (blocks until stream_started event)
stream_ref = session.wait_for_stream(timeout=30)

# Wait for a specific stream by declared key or runtime ID
stream_ref = session.wait_for_stream(stream_id="data-stream", timeout=30)

# Wait for a stream matching a predicate
stream_ref = session.wait_for_stream_where(
    lambda ref: ref.descriptor.format == "events",
    timeout=30,
)

# High-level consumption
stream = stream_ref.open()
for chunk in stream.bytes():       # decoded byte iterator
    output_file.write(chunk)

for event in stream.events():      # flattened event iterator
    print(event)

# File-like interface
import shutil
with open("output.bin", "wb") as f:
    shutil.copyfileobj(stream.as_file(), f)

# Background consumption (daemon thread)
thread = stream.consume_in_background(lambda item: print(item))

# List already-discovered streams
all_streams = session.list_streams()
```

- `stream_ref.open()` returns a `StreamClient`; idempotent while active
- `stream.inbound` is still available as the low-level `Iterator[InboundMessage]`
- `stream.descriptor.declared_stream` carries the card-declared stream key

---

## Task Lifecycle Methods

### On TaskClient

```python
info = client.get_task(task_id)            # returns TaskInfo
result = client.list_tasks(ListTasksParams(
    owner_id=..., agent_name=..., state=..., limit=..., cursor=...,
))                                         # returns ListTasksResult
client.cancel_task(task_id)
client.pause_task(task_id)
client.resume_task(task_id)
client.retry_task(task_id)
client.terminate_task(task_id)
```

### On TaskSession

```python
session.cancel()     # request cancellation
session.terminate()  # force terminate
session.close()      # unsubscribe and clean up
```

---

## CLI Commands

```bash
blocks init <name> --yes --language python   # Scaffold Python agent
blocks check                                  # Validate agent-card.json + handler
blocks run                                    # Start agent
```

---

## Environment Variables (.env)

Auto-populated by `blocks init` or by `blocks login --write-env` (opt-in). Contains `BLOCKS_API_KEY`.

Additional env vars read by the Python SDK:

- `BLOCKS_CDM_URL` -- CDM config endpoint (defaults to production S3-hosted endpoint)
- `LOG_LEVEL` -- error/warn/info/debug (default info)
- `ARTIFACT_INLINE_LIMIT_BYTES` -- max artifact size for inline base64 encoding

---

## Transport-Layer Resilience

The agent's long-lived PubNub control client retries subscribe failures with an unbounded budget by default (~30 days at a 60s cap). Brief network outages — VPN reconnects, gateway flaps, transient ISP failures — no longer silently park the agent after the SDK's vanilla ~4-6 minute retry window exhausts. The Python SDK forwards PubNub's per-attempt / recovered / failed retry messages into the agent's structured logger as `pubnub_transport_retry`, `pubnub_transport_recovered`, and `pubnub_transport_failed` events.

Per-task and per-stream PubNub clients keep the SDK's default short retry budget — a stuck task should fail fast rather than retry indefinitely. This split is enforced internally and not configurable from the handler.

This behavior is automatic; no agent-card or env-var changes are required.

---

## Install & Run

```bash
cd <name> && PIP_CONFIG_FILE=pip.conf pip install -e .           # Install deps
blocks run                                                      # Local run
python trigger.py                                                # Send test task
```

---

## Package Config (pyproject.toml)

```toml
[build-system]
requires = ["setuptools>=68.0"]
build-backend = "setuptools.build_meta"

[project]
name = "my-agent"
version = "1.0.0"
requires-python = ">=3.12"
dependencies = ["blocks-network"]

[tool.setuptools]
py-modules = ["handler"]
```
