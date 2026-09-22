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
**Requires** a top-level `streams` block in `agent-card.json`. `ctx.create_stream()`
raises `"Streaming was not negotiated for this task."` whenever `ctx.has_stream`
is false — either the card lacks `streams`, or (for request tasks) the consumer
didn't opt in via `extensions.blocks.stream`. Guard on
`ctx.has_stream` before calling it.

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
- A stream declared with `affinity: "shared"` is a cross-task broadcast and is **pipe-only** — `create_stream()` raises on request tasks, and per-task `stream.end()` does not publish a `stream_end` marker. See the SDK contract for the full stream lifecycle.
- Re-register or re-publish (`blocks register` / `blocks publish`) after adding `streams`. Use `blocks publish` here if the agent has already been promoted to public or paid — `blocks register` would reset the listing back to private+free.

`create_stream` accepts keyword-only options: `direction` (`"outbound"`|`"inbound"`|`"bidirectional"`, default `"outbound"`), `on_activate`, `metadata`, `external` (bool), `format` (`"bytes"`|`"events"`), `bundle_size_bytes`, `max_latency_ms`, `declared_stream` (omit when the card declares a single stream -- the SDK resolves it automatically; **required** when the card declares multiple streams), `subscribe_grace_ms`. The SDK derives the channel from `declared_stream` plus the card's affinity; handler code cannot specify the channel suffix directly.

### StreamObject (handler-side)

The wrapper returned by `ctx.create_stream(...)`. Mirrors the consumer-side `StreamClient` read/error/uuid surface so handler code uses the same iterators and callbacks consumers do (see the SDK contract for the normative definition).

Properties: `stream_id`, `channel`, `is_active`, `external`, `uuid` (read; same as `StreamClient.uuid`, useful for log correlation), `token` (external only).

Write / lifecycle: `write(data)` (raises on inbound-only and external streams), `end()`, `activate(opts=None)` (external only), `on_end(cb)`.

Read iterators (inbound and bidirectional streams):
- **Recommended:** `bytes()` for `format: "bytes"` streams (yields `bytes`, decodes base64/utf8 transparently). `events()` for `format: "events"` streams (yields one event per iteration; flattens producer-side batches). `as_file()` returns a `BufferedReader` for `shutil.copyfileobj` / subprocess pipe integration.
- **Low-level:** `inbound` is an `Iterator[InboundMessage]` over raw wire envelopes (`{ data, seq, ts, format, encoding }`). Reach for it only when you need raw envelope metadata; `bytes()` / `events()` are the everyday paths.

Errors: `on_error(cb)` subscribes to per-stream PubNub status errors. The callback receives a `StreamError` with `category`, `error`, `channel`, `timestamp`, `fatal`. **Append-only** — register before the read path activates; past errors do not replay. On `fatal=True` the underlying client auto-tears down so the iterator exits cleanly.

`on_inbound_done` is intentionally NOT part of `StreamObject` — it's an internal callback owned by `TaskSession`. For a "stream drained" signal, iterate `bytes()` / `events()` / `inbound` to completion.

Type exports for handler code: `from blocks_network import InboundMessage, StreamError`.

```python
def handler(task: StartTaskMessage, ctx: Optional[TaskContext] = None) -> Dict[str, Any]:
    parts = task.request_parts or []
    text = parts[0].text if parts and hasattr(parts[0], "text") else "Hello!"

    # Guard on has_stream — request-task streaming is consumer opt-in, so
    # create_stream() raises when the consumer didn't opt in.
    if ctx and ctx.has_stream:
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

**send_message(\*, agent_name, request_parts, ...)** -- all keyword-only. `owner_id` is auto-populated from auth (optional override). Optional: `idempotency_key`, `task_kind` (`"request"`|`"pipe"`), `duration`, `consumer_public_key`, `stream` (request-task streaming opt-in — `True` requests streaming, `False` suppresses it; omitted uses the server default, now no-streaming, so pass `True` to stream; ignored for pipe; resolved `has_stream` still requires agent capability), `push_notification_config`, `retry_policy`, `auto_drain`, `drain_window_s` (default 30.0; overrides the per-session auto-drain window for already-open streams). Returns `TaskSession`.

**TaskClient.connect(task_id, auto_drain=True, drain_window_s=None, role="consumer")** -- returns a `TaskSession`. `drain_window_s` mirrors `send_message` so reconnecting consumers can tune the drain window for streams they open via `open_all_streams()` / `on_stream`. `role` defaults to `"consumer"` (task submitter — server checks `user_id == task.owner_id`); set to `"provider"` when the caller is the agent owner viewing a received task. Provider-role access follows the same rule as the Dashboard's received-tasks view: the agent's owner is always admitted. Otherwise the agent's org must match the active org resolved from the session `X-Active-Org` header or the credential's org claim, AND the caller must be a current member of that org; admins with an admin-typed active org get the cross-org bypass, and when no active org is resolved at all (legacy callers) the server falls back to admin-bypass / membership on the agent's org. For a private agent, membership alone is not enough: a non-admin must also hold an agent-management permission in the owning org and have been invited to the agent.

**TaskSession** -- properties: `task_id`, `owner_id`, `org_id`, `read_token`, `status_channel`, `state`, `is_closed`. Event listeners: `on_progress(cb)`, `on_artifact(cb)`, `on_terminal(cb)`, `on_cancel_requested(cb)`, `on_event(cb)`, `on_error(cb)`, `on_stream(cb)`. Blocking wait: `wait_for_terminal(timeout=60)` -- blocks until terminal event, returns `TaskEvent`; resolves immediately for already-terminal sessions. Typed event properties: `event.message`, `event.progress`, `event.state`, `event.artifact_ref`. History helpers: `list_events()` (all valid task events parsed by `connect()` history), `list_artifacts()`, `download_artifact(ref)`, `save_artifacts(directory)`. Stream helpers: `list_streams()`, `wait_for_stream(stream_id?, timeout?)`, `wait_for_stream_where(predicate, timeout?)`, `open_all_streams(**opts)` (active-session eager-open — returns `List[StreamClient]` for every readable ref, skipping outbound-only and already-ended refs). Card lookup: `client.get_agent_card(agent_name)` (forwards the client's credential, which a Blocks Enterprise deployment requires to return a card at all; raises `AuthRefreshFailedError` if a configured credential cannot be produced, so `None` only ever means "no such agent"). Control: `cancel()`, `terminate()`, `close()`. Context managers: `with client:` calls `destroy()`, `with session:` calls `close()`.

**At-most-once `on_terminal`.** `session.on_terminal`, `session.wait_for_terminal()`, `subscribe_to_task`'s `on_terminal`, and the synthetic re-emit on registration against an already-terminal session each fire at most once per task. First-terminal-wins; subsequent wire-level terminals are silently dropped (e.g. scanner force-cancel + agent's delayed terminal).

**`on_cancel_requested(cb)`** — backend-published acknowledgment of a cooperative cancel on `u.{org_id}.{task_id}`. Fires zero or once per session; suppressed once a terminal has been delivered. Event shape surfaced to the callback: `{"type": "cancel_requested", "taskId": ..., "ts": ...}`. (The wire payload also carries `protocolVersion` per the SDK task-event schema, but it is not surfaced to the callback.) Use to render an in-flight "cancel requested" UI signal before any terminal arrives. **Late registration:** callbacks registered after the wire event arrived still receive a synthetic replay of the first event, mirroring `on_terminal`'s sticky behavior — but only while no terminal has been delivered (a post-terminal registration gets nothing, preserving causality).

`on_artifact(cb)` replays pre-populated artifacts synchronously at registration time, in the same spirit as `on_stream()` and sticky `on_terminal()`. Replay events are minimal synthetic artifact events with `type`, `taskId`, and `artifactRef`; original history-only wire fields such as `outputId` and `protocolVersion` are not retained.

**Part helpers:** `text_part(text, part_id=)`, `file_part(path_or_data, **opts)` -- exported from `blocks_network`.

**Artifact helpers:** `build_artifact_ref(data, mime_type, file_name=None)` constructs an `ArtifactRef` from raw bytes. `should_inline_artifact(size_bytes)` returns `True` when the size is below the 16 KB inline threshold (matches the SDK's auto-routing decision). `decode_inline_artifact(ref)` synchronously decodes an inline `ArtifactRef` to `bytes` (no network). `download_artifact(ref)` is the standalone async-fetcher equivalent (the same logic `session.download_artifact()` calls). All four are exported from `blocks_network`.

**File uploads:** `presigned_upload_flow(api_key_or_client, file_path_or_bytes, mime_type, file_name=None)` uploads a file via the Blocks presigned-URL flow and returns an `ArtifactRef` suitable for `request_parts`. Use this when an input artifact is too large to inline as base64. Raises `FileUploadError` on presign-rejection or upload-stage failure (network, 4xx/5xx from object storage); the exception carries the original cause. Both are exported from `blocks_network`.

**Auth — `token_endpoint`:** accepts either a string URL or a `TokenEndpointConfig` TypedDict (`{ "url": str, "headers"?: dict, "body"?: Any }`). Unlike Node, Python's TypedDict has **no `credentials` field** — urllib has no fetch-equivalent. For cookie-based auth parity, pass the cookie explicitly in `headers={'Cookie': 'session=...'}`. `TokenEndpointConfig` is exported from `blocks_network`.

**Auth — surfacing refresh failures:** If proactive refresh fails 3 times in a row, or a reactive (on-401) refresh fails, the SDK records an `AuthRefreshFailedError` on the underlying `ConsumerAuth`. When the trigger was a reactive refresh, the call that hit the 401 -- or the auth-revoke RPC code that made it refresh-retryable -- fails immediately with that original error, untyped. The typed error is conditional: a later call's preflight retries the refresh once and raises only if that also fails, clearing the recorded error if it succeeds. The next authenticated `TaskClient` call (`send_message`, `connect`, `get_task`, `list_tasks`, `cancel_task`, file-upload helpers, etc.) runs through a shared preflight that first attempts one reactive-recovery refresh; if that recovery succeeds, the recorded error clears and the call proceeds, and if it fails, the typed `AuthRefreshFailedError` is raised (exported from `blocks_network`). Pass `on_auth_error=...` to `TaskClient.create(...)` if you want a proactive-path hook (it does not fire for reactive failures) for re-auth UX; the preflight is the safety net for callers who don't and the recovery path for transient outages that resolve before the next call. Note that the SDK logs the failure via `logging.getLogger("blocks_network.consumer_auth")` at WARNING level — this log is **silent unless your application has configured Python logging.**

**Stream consumer APIs:** `stream.bytes()` (decoded byte iterator), `stream.events()` (flattened event iterator), `stream.as_file()` (returns `BufferedReader`). Background helpers: `stream.consume_in_background(cb)`, `stream.write_periodic(interval, gen, stop?)`. All iterators deliver messages in sequence order via the SDK's reorder buffer. `ref.open(reorder_timeout_ms=)` configures the gap timeout (default 750ms; 0 disables reordering).

**Stream error surfaces:** `StreamClient.on_error(cb)` subscribes to per-stream PubNub status errors (PAM revocation, network failures, malformed payloads). Callback receives a `StreamError` with `category`, `error`, `channel`, `timestamp`, `fatal`; `category` is one of the neutral values `"connected"`, `"reconnected"`, `"network_down"`, `"network_issues"`, `"timeout"`, `"malformed_response"`, `"access_denied"`, `"bad_request"`, `"other"`. Fatal categories that force-terminate the stream are `"access_denied"` and `"bad_request"`; on `fatal=True` the client auto-tears down and signals iterator completion, so handlers typically only react to non-fatal errors. Distinct from `TaskSession.on_error` (which surfaces consumer-callback exceptions, not stream-level failures). `StreamUnavailableError` (exported from `blocks_network`) is raised synchronously by `StreamRef.open()` when the owning session is already terminal; it carries `.terminal_state` and `.stream_id` so callers can branch on `isinstance(err, StreamUnavailableError)`.

**Exported exception types:** all subclass `Exception` and are importable from `blocks_network`.

| Class | Raised by | Meaning |
|---|---|---|
| `RpcError` | any SDK call that contacts the backend | A non-2xx HTTP response from the Blocks REST API. Carries `status`, `code`, `details`. |
| `BillingModeMismatchError` | `TaskClient.send_message`, `TaskClient.connect` | The `billing_mode` passed to `TaskClient.create()` does not match the target agent's registered mode. Carries `expected` / `got`. |
| `AnonTaskAccessDeniedError` | `TaskClient.connect` (anon role) | A 403 from `/api/v1/auth/anon-task-read-token` — the anon-readable channel rejected the fingerprint. |
| `StreamUnavailableError` | `StreamRef.open()` | The owning session is already terminal; live stream data is gone (artifacts persist). Carries `.terminal_state` and `.stream_id`. |
| `AgentAuthFatalError` | `AgentAuth` connect/refresh path | Fatal, non-retryable — the API key was revoked/disabled (`API_KEY_INVALID`) or an administrator forced the agent offline (`AGENT_FORCED_OFFLINE`). The runtime terminates the process (`os._exit(1)` from the connect thread). Transient connect failures (network, 5xx, `404 not-published`) are NOT fatal and do not block startup. |
| `FileUploadError` | `presigned_upload_flow` | Presign rejection or object-storage upload failure. Carries the original cause. |

---

## Consumer Auth Patterns

The Python SDK is server-side: trigger scripts authenticate with an API
key passed to `TaskClient.create(api_key=...)`. **Never** check the
API key into a browser-shipped bundle.

For browser-based consumers (a chat UI, form, streaming page) two
options exist on top of the same per-task JWT model, both implemented
in the Node/JS bundle, not Python:

- `tokenEndpoint` (Mode 2) — the developer hosts a backend that mints
  short-lived JWTs from the API key; the browser calls it to refresh.
- Embedded auth (Mode 3) — drop-in `BlocksAuth` widget (JS only) served
  by Blocks runs a popup handshake; no partner backend needed. The page
  calls `BlocksAuth.signInAndGetClient(s)`. Authoring flow:
  `blocks init my-ui --mode webapp --agent <name>` (writes
  `blocks.config.json` with a required `backendBaseUrl` — the runtime backend
  API origin, distinct from the `--blocks-base-url` asset host — plus a
  generated `web/`; pipe-capable agents get a
  duration control), then `blocks dev` / `blocks deploy <partner>`
  (positional target; set `CLOUDFLARE_API_TOKEN` / `VERCEL_TOKEN` /
  `NETLIFY_AUTH_TOKEN` for non-interactive deploys). The widget is
  JS-only; the scaffolded page is not Python. See
  `blocks-sdk/embed-auth/README.md` and `docs/embed-getting-started.md`.

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

**InboundMessage** shape: `data` (`list[str]` for `bytes`, `list[Any]` for `events`, `dict[str, Any]` for `raw`), `seq`, `ts`, `format`, `encoding`. `data` is always an array/dict — the producer-side bundler coalesces `write()` calls; a single `write()` still yields a 1-element list. Prefer `bytes()` / `events()` for application code.

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

- `billing_mode` selects the keyset (`"free"` → playground, `"paid"` → network) and must match the target agent's server-derived billing_mode (exception: authenticated same-org callers are exempt from this check). Read it from the registry. `get_agent()` has no default backend URL and raises `ValueError` (`baseUrl is required`) without `base_url`, so take the Blocks Network backend URL from the CDM; pass `api_key` as well when the agent is private, otherwise the lookup returns `None`:

  ```python
  import os
  from blocks_network import fetch_cdm_config, get_agent

  base_url = fetch_cdm_config().api.base_url
  entry = get_agent(agent_name, base_url=base_url, api_key=os.environ.get("BLOCKS_API_KEY"))
  billing_mode = entry.billing_mode if entry else None  # None when the agent is unknown or not visible
  ```

  The list helpers (`fetch_agent_registry`, `fetch_agents_by_tag`, `fetch_agents_by_listing`) take the same `base_url` / `api_key` arguments.
- Loads the project `.env` first, anchoring the search on the working directory
  (`load_dotenv(find_dotenv(usecwd=True))`), then falls back to `BLOCKS_API_KEY` from
  the environment when no auth mode is provided
- Accepts `token_endpoint` or `token_provider` as alternative auth modes (skips `BLOCKS_API_KEY`)
- Extra `**kwargs` forwarded to `TaskClient.create()`

---

## TaskClient.create() Class Method

Lower-level factory with full control over CDM config resolution and auth:

```python
client = TaskClient.create(
    billing_mode="free",  # required: 'free' | 'paid'
    api_key="blocks-api-key",  # auth mode 1: API key -> JWT
    # token_endpoint="https://my-backend/token",  # auth mode 2: session-authenticated backend endpoint (customer proxy OR dashboard embedder -- see the SDK contract)
    # token_provider=my_func,  # auth mode 3: custom callback
)
```

- `billing_mode` is the first positional arg (required) and selects the keyset ('free' → playground, 'paid' → network)
- Exactly one auth mode: `api_key`, `token_endpoint`, or `token_provider`
- `rpc_headers: Optional[Dict[str, str]] = None` — optional extra request headers merged onto every RPC call (not the token mint). Merged UNDER SDK-owned headers, so a caller cannot override `Authorization`, `Content-Type`, `Blocks-Protocol-Version`, or `X-Write-Affinity` (case-insensitive). Request metadata, not auth; the canonical dashboard use is `X-Active-Org` to scope a multi-org user's submission to the agent's owning org.
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

The CLI surface is the same across Node and Python -- only the
scaffold language differs. See [node-reference.md](./node-reference.md)
→ CLI Commands for the shared surface, including the global `--profile`
and `--no-input` flags; the Python-specific entry points are below.

### Scaffold

```bash
blocks init <name> --yes --language python                  # Provider scaffold (handler.py + agent-card.json)
blocks init <name> --yes --language python --mode consumer  # Consumer scaffold (main.py using TaskClient)
```

`blocks init` defaults `--mode provider`. Consumer projects produce
`main.py` plus `pyproject.toml` -- no `agent-card.json`, no handler,
no publish step. Non-interactive mode requires the name argument.

### Authenticate

```bash
blocks login --network --write-env  # Blocks Network; write BLOCKS_API_KEY to .env
blocks login acme --write-env       # Enterprise short name -> https://acme.blocks.ai
blocks login https://blocks.acme.com --write-env       # Enterprise custom domain
blocks login https://blocks.acme.com --profile acme    # Store it under a custom profile name
blocks login --write-env --dir ./x  # Write .env to a specific directory
blocks login --no-write-env         # Authenticate without touching .env
blocks login --api-key "$KEY" --write-env       # Skip browser flow with a pre-issued key
echo "$KEY" | blocks login --api-key-stdin --write-env
blocks whoami                       # Print org, key id, expiry
blocks whoami --json                # Structured output (org_name, org_id, key_id, expires_at, days_remaining, expired)
blocks logout                       # Clear the profile's cached keys + remove BLOCKS_API_KEY from .env
```

`blocks login` prompts in a TTY for: which deployment to target (skipped once
the question is settled -- by any completed login, including one to Blocks
Network, which stores no deployment URL of its own; or by a deployment this
invocation resolves -- the active profile, `BLOCKS_BACKEND_URL`, or a project
`.env` pinning a deployment you have a profile for, so a bare `blocks login`
there returns to it without asking. Only a genuine first run is asked),
the instance URL or short name if you pick Enterprise, and `Write
credentials to project .env? (Y/n):`. In non-TTY shells those are
auto-skipped without hanging, but the deployment is then whatever the
profile resolves to and `.env` is not written; under `--no-input` they are
errors instead, whether or not stdin is a terminal. Answer them with flags:
`--network` (or an instance URL / short name argument, which `--network` is
mutually exclusive with) and `--write-env` / `--no-write-env` — a key passed
as `--api-key` / `--api-key-stdin` answers both instead, while
`BLOCKS_API_KEY` in the environment does not. An instance argument that
spells out an address is validated before anything is sent: `https://` is
required except for `localhost` / `127.0.0.1` / `[::1]`, the authority must
be a host with an optional port in 1–65535 (not `user@host`), a path prefix
is kept but a query string or a fragment is refused, and a short name must
be a single DNS label expanding to a hostname within the 253-character DNS
limit. An argument matching an existing profile name is not an address and
is not re-checked: it resolves to that profile's saved deployment and is
used as stored. It is still held to the origin rule, though: a stored URL that is not `https`
(or `http` to a loopback host), or that carries userinfo, a query or a fragment, is
refused by profile name rather than dialled. A login that names its target explicitly also unsets an
ambient `BLOCKS_CDM_URL` for that login, because the payload behind it names
both an API origin and an OAuth client id: `--network` (or picking Blocks
Network at the prompt) drops any such value, and an instance argument drops
any value that is not that instance's own CDM endpoint. Those drops do not
weigh provenance, so they apply to an exported value too, for that login
process only.

`--write-env` writes `BLOCKS_API_KEY`, and the deployment's own
`BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` alongside it when the login
targeted a specific deployment rather than stock Blocks Network, so a
directly-launched script (`python trigger.py`) sends its REST calls to the
deployment the key was minted at *and* subscribes on that deployment's
keysets. A `--network` login writes neither and removes stale values. All
three are applied in one edit, so an interrupted login does not leave a new
key beside another deployment's URLs — see [Environment Variables
(.env)](#environment-variables-env).

The key itself is stored in the deployment's profile in
`~/.config/blocks/contexts.json`, regardless of `--write-env`.
`blocks logout` clears that profile's cached org keys and strips
`BLOCKS_API_KEY` from the project `.env`, but **keeps the deployment
target** — it says so, and names `blocks profile remove <name>` for
forgetting the deployment entirely. It does not revoke the key on the
server. `blocks profile remove <name>` is the deliberate opposite: it drops each
of the project `.env`'s `BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` that names the
deployment being removed — the two are judged independently — and the
`BLOCKS_API_KEY` beside them when the backend URL was one of them (the backend pin
is what records where that key is spent) or when the key is one only the removed
profile had cached. A `.env` whose only match is the CDM endpoint therefore loses
that line and keeps the key. It touches the file only when the deployment is
genuinely being forgotten: if another profile still describes it — an alias
alongside its host — the removal forgets a name, not a deployment, and the `.env`
is left as it is. Values naming any other deployment are left alone; the cleanup
runs before the profile is deleted, so a `.env` that exists but cannot be read or
rewritten fails the command with the profile still in place, rather than being
skipped silently. No `.env` at all is a silent success.

### Register (recommended first step)

```bash
blocks register                                 # Private + free; no listing/billing/terms prompts
blocks register --api-key "$KEY"                # Inline auth (skip blocks login)
echo "$KEY" | blocks register --api-key-stdin   # Inline auth via stdin
```

`blocks register` registers the agent as **private and free** (usable by
the owner and invited organizations only). It exposes only `--api-key`,
`--api-key-stdin`, and `--org-name` (the last inert on an Enterprise deployment, whose
organizations are pre-seeded by its admin) — no visibility or billing flags — and
has no visibility / billing / terms prompts, so non-interactive and CI
invocations succeed with no required flags. (Interactive runs may still prompt for an organization name on the first agent an org
registers or publishes — the same prompt `blocks publish` shows, and Blocks Network
only: an Enterprise deployment pre-seeds organizations, so it skips both that prompt
and `--org-name`.) Run `blocks publish` later to go
public or set pricing; it can also promote an already-registered agent.

### Unregister (remove an agent)

```bash
blocks unregister                               # Name read from ./agent-card.json
blocks unregister <agentName>                   # Remove an agent from anywhere
blocks unregister <agentName> --yes             # Skip the confirmation (required in CI)
blocks unregister <agentName> --api-key "$KEY"  # Inline auth (also --api-key-stdin)
```

The inverse of `blocks register`: removes the agent from the deployment
you are currently targeting. With no argument the name comes from
`identity.agentName` in `agent-card.json`. The deployment is printed before
the removal, which **cannot be undone**; no organization is named, because
the removal is authorized by your rights over this agent rather than by the
organization the key belongs to, and the confirmation prompt names the agent
itself. A non-interactive session refuses to remove anything without `--yes`.

### Publish (go public / set pricing)

```bash
blocks publish                                  # Interactive (prompts for billing, listing, terms)
blocks publish --billing-mode free \
  --listing public --accept-terms               # Free public agent (non-interactive)
blocks publish --billing-mode paid --listing private \
  --price-per-task 0.05 --accept-terms          # Paid private agent
blocks publish --api-key "$KEY"                 # Inline auth
echo "$KEY" | blocks publish --api-key-stdin
```

Full publish flag set: `--billing-mode {free|paid}`, `--listing
{public|private}`, `--price`, `--price-per-task`, `--price-per-minute`,
`--free-units`, `--free-tasks`, `--free-minutes`, `--accept-terms`,
`--org-name`, `--api-key`, `--api-key-stdin`. Bare `blocks publish`
in CI does not prompt: a missing required value is an immediate error
naming the flag that supplies it -- include flags explicitly. On a
deployment with no marketplace, billing is off and `--billing-mode paid`
is rejected before anything is sent, with an error naming the value that
works: publish free there (omit the flag, or pass `--billing-mode free`).

`blocks publish` re-runs the same schema validation as `blocks check`,
so `check` is a fast pre-flight, not a hard gate.

### Validate, run, dashboard

```bash
blocks check                                  # Validate agent-card.json + handler file existence
blocks run                                    # Start the agent (delegates to the Python runner via venv walk-up)
blocks dashboard                              # Open the agent's dashboard page in a browser
blocks dashboard <agent-name>                 # Override the agent name (default: read from ./agent-card.json)
```

`blocks check` validates the JSON schema **and** the file referenced
by `runtime.handler` -- a missing handler produces `[FAIL]` even when
the JSON is valid. `blocks dashboard` resolves the dashboard URL from
`BLOCKS_APP_BASE_URL` / `BLOCKS_DASHBOARD_URL` (or the active profile's
dashboard origin) if set, otherwise from the active deployment
(`BLOCKS_BACKEND_URL`, the active profile's backend, or the CDM config),
so it targets the deployment you're on rather than always stock Blocks
Network. When `BLOCKS_BACKEND_URL` targets a different backend than the
active profile was logged into, the profile's cached dashboard origin is
skipped so the link follows `BLOCKS_BACKEND_URL`.

### Manage private-agent grants (`blocks invite`)

When an agent is published with `--listing private`, its owner and the
other members of the organization that owns it reach it already; for
everyone else access is gated by per-user / per-org grants:

```bash
blocks invite send <agentName> --email user@example.com   # invite a specific user
blocks invite send <agentName> --org consumer-org-slug    # invite an entire consumer org
blocks invite list <agentName>                            # list unaccepted invitations, including expired
blocks invite grants <agentName>                          # list active grants
blocks invite revoke <agentName> --email user@example.com # revoke a user grant
blocks invite revoke <agentName> --org consumer-org-slug  # revoke an org grant
blocks invite accept <token>                              # consumer-side: accept an invitation token
```

`--email` and `--org` are mutually exclusive on `send` / `revoke`. All
commands require prior `blocks login`.

### Maintenance

```bash
blocks upgrade                                # Self-update the CLI binary in place (POSIX installer flow)
blocks version                                # Print the installed CLI version
```

---

## Environment Variables (.env)

Auto-populated by `blocks init` or by `blocks login --write-env` (opt-in).
Use `--dir <path>` to target a specific directory. Contains
`BLOCKS_API_KEY`, plus `BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` when the
login targeted a specific deployment rather than stock Blocks Network (a
`--network` login writes neither and removes stale values). All three go in
as one rewrite. That rewrite leaves one uncommented assignment of each in
the file — under any spelling of the name, since for a variable whose name
uppercases to `BLOCKS_*` the CLI treats `blocks_api_key` and
`BLOCKS_API_KEY` as one variable, and `blocks logout` removes both. Two
limits belong with that: outside `BLOCKS_*` each spelling is its own
variable, so "assigned once" is a per-spelling statement for an application
variable; and the fold applies on every platform, so on macOS and Linux a
lowercase `blocks_...` line added by hand is one `blocks login` will
overwrite and `blocks logout` will delete. A value containing a line break
or a NUL byte — which would be read back as a second assignment — is refused
by name with nothing written. Note what a project `.env` does and does not
configure: the CLI imports only its own seven `BLOCKS_*` settings from that
file, and everything else in it is passed on to the agent process `blocks
run` starts rather than being applied to the CLI. Proxy, certificate-trust,
home/config-location and hosting-partner-token variables therefore have to
be exported in your shell to affect the CLI; the file prints a note on
stderr when it supplies one.

Additional env vars read by the Python SDK:

- `BLOCKS_CDM_URL` -- CDM config endpoint, which is what selects the
  real-time keysets. Unset, the SDK uses a hardcoded default serving
  **Blocks Network** keysets; `BLOCKS_BACKEND_URL` overrides only the REST
  API origin and does not change this. `blocks run` sets both for the
  delegated process whenever the invocation targets a named deployment
  (stock Blocks Network needs no injection — the SDK default already
  resolves it), so an agent started that way needs neither. It sets them as
  defaults: a non-empty value the child already carries for either variable
  wins. For a process
  you launch directly (`python trigger.py`) both come from `.env`, where
  `blocks login --write-env` writes them. Every deployment serves the
  endpoint at `<origin>/api/v1/cdm`, so a hand-written value is just that.
  Note that a `BLOCKS_BACKEND_URL` or `BLOCKS_CDM_URL` a project `.env`
  supplied -- as opposed to one exported in your shell, which these two
  gates leave alone -- is declined by the CLI unless it agrees with a
  deployment you chose: for the backend URL, any deployment some saved
  profile describes (not necessarily the active profile's); for the CDM URL,
  the CDM endpoint of the deployment this command is actually targeting, so
  on stock Blocks Network there is no such origin to compare against and a
  file-supplied value is declined there. The export precedence belongs to
  those two gates: naming a target on the `blocks login` command line drops
  an ambient `BLOCKS_CDM_URL` answering for a different deployment however
  you set it. So a `.env` from a cloned repository does not redirect a
  command at a deployment you never named
- `LOG_LEVEL` -- error/warn/info/debug (default info)
- `BLOCKS_DEBUG_INTERNAL` -- comma-separated debug subsystems. The Python SDK honors the `forward_transport` token (same name/format as the Node SDK). By default the SDK quiets the underlying transport's `httpx`/`httpcore` per-request INFO logging ("HTTP Request: GET https://ps.pndsn.com/... 200 OK"); set `BLOCKS_DEBUG_INTERNAL=forward_transport` to surface the raw request stream for connectivity debugging. Not implied by `LOG_LEVEL=debug`. A `httpx`/`httpcore` level your app sets explicitly is honored. See the SDK contract
- `BLOCKS_PROFILE` -- comma-separated opt-in local profilers. `timing` makes the runtime log one `dispatch timing` line per task with `received_to_running_ms` / `running_to_handler_ms` / `received_to_handler_ms` (single `time.monotonic()` clock, skew-free). **Local-only, no egress**; off by default; for bench use. At parity with the Node SDK. See the SDK contract
- `ARTIFACT_INLINE_LIMIT_BYTES` -- max artifact size for inline base64 encoding

Note: the Python SDK does not implement the `diagnostics` token (Node-only). Its retry / connectivity surface is always-on via `on_retry` callbacks (see the SDK contract). Mechanism divergence from Node: Node re-routes transport lines through its own `[Transport]`-tagged logger (so they obey `LOG_LEVEL`); Python's `forward_transport` simply stops suppressing the third-party `httpx`/`httpcore` loggers, which then emit at their own levels. Same opt-in contract, raw output.

---

## Transport-Layer Resilience

The agent's long-lived control client retries subscribe failures with an unbounded budget by default (~30 days at a 60s cap). Brief network outages — VPN reconnects, gateway flaps, transient ISP failures — no longer silently park the agent after the SDK's vanilla ~4-6 minute retry window exhausts. The Python SDK forwards per-attempt / recovered / failed retry messages from the underlying transport into the agent's structured logger as `transport_retry`, `transport_recovered`, and `transport_failed` events.

Per-task and per-stream PubNub clients keep the SDK's default short retry budget — a stuck task should fail fast rather than retry indefinitely. This split is enforced internally and not configurable from the handler.

This behavior is automatic; no agent-card or env-var changes are required.

---

## Install & Run

```bash
cd <name> && pip install -e . && pip install blocks-network --upgrade  # Install deps
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
