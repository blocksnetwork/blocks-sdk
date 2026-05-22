# Blocks Network Python SDK

Python SDK for running Blocks Network agent instances. This package provides a threaded runtime that subscribes to control channels, executes task handlers, publishes task events, and manages presence state -- feature parity with the Node.js runtime at `sdks/node/`.

## Installation

From the `sdks/python/` directory, install in editable mode:

```bash
cd sdks/python
pip install -e .
```

For development (includes pytest):

```bash
pip install -e ".[dev]"
```

**Requirements:** Python 3.10+, `pubnub>=10.6.0,<11`. CI tests against Python 3.10, 3.11, 3.12, 3.13, and 3.14 (every security-supported release per [python.org devguide](https://devguide.python.org/versions/)).

**Subscribe strategy:** The Python SDK uses PubNub's synchronous `NativeSubscriptionManager`. PubNub Event Engine is not available in the synchronous Python client -- the synchronous `PubNub` class does not expose `enable_event_engine` on `PNConfiguration`. The async client (`PubNubAsyncio`) does support Event Engine internally, but the Blocks Python SDK is built around a synchronous, thread-based runtime (blocking handlers, `threading.Event` cancellation, `ThreadPoolExecutor` concurrency, `queue.Queue` streams). Adopting Event Engine would require a runtime-model migration, not a config change.

## Quick Start

Set your CDM endpoint (optional override):

```bash
export BLOCKS_CDM_URL=http://localhost:3001/api/v1/cdm
```

If `BLOCKS_CDM_URL` is unset, the SDK uses the default CDM URL (`https://blocksnetwork.s3.us-west-2.amazonaws.com/config.json`). Raw `PUBNUB_PUBLISH_KEY` / `PUBNUB_SUBSCRIBE_KEY` env vars are not used for SDK startup.

Run the built-in echo handler:

```bash
blocks-run --handler echo
```

Run the built-in adder handler:

```bash
blocks-run --handler adder
```

The agent instance subscribes to `agent.{agentId}.control`, processes incoming tasks, and publishes results to `u.{ownerId}.{taskId}`.

## Creating Custom Handlers

A handler is a function that receives a `StartTaskMessage` and an optional `TaskContext`, and returns a dict with an `artifacts` array of `{data, mimeType}` entries:

```python
import json
from typing import Optional

from blocks_network.types import StartTaskMessage, TaskContext


def my_handler(
    task: StartTaskMessage, ctx: Optional[TaskContext] = None
) -> dict:
    input_part = task.request_parts[0] if task.request_parts else None

    result = {"ok": True, "received": input_part}

    return {
        "artifacts": [{"data": json.dumps(result, indent=2), "mimeType": "application/json"}],
    }
```

Register the handler in `blocks_network/registry.json` with `"runtime": "python"`, then run it with `blocks-run --handler my-handler`.

For the full walkthrough, see the [Getting Started Guide](../../docs/getting-started.md).

## Configuration

Provider/runtime configuration uses a minimal set of environment variables. Identity, scaling, and handler selection come from `agent-card.json` or explicit runtime options.

| Variable                      | Description                                        | Default                                                        |
| ----------------------------- | -------------------------------------------------- | -------------------------------------------------------------- |
| `BLOCKS_API_KEY`              | Required provider auth secret                      | None (fail fast if missing)                                    |
| `BLOCKS_CDM_URL`              | CDM config URL override used for keyset resolution | `https://blocksnetwork.s3.us-west-2.amazonaws.com/config.json` |
| `LOG_LEVEL`                   | Log verbosity (`error`, `warn`, `info`, `debug`)   | `info`                                                         |
| `ARTIFACT_INLINE_LIMIT_BYTES` | Max artifact size for inline base64 encoding       | `16384`                                                        |

## API Reference

### `start_agent_instance(options)`

Start an agent instance runtime. This is the primary entry point for the SDK.

```python
import json
from blocks_network import start_agent_instance
from blocks_network.types import AgentInstanceOptions

with open("agent-card.json") as f:
    card = json.load(f)

result = start_agent_instance(
    AgentInstanceOptions(
        agent_name="my_agent",
        description="My agent",
        capabilities=["example"],
        handler=my_handler,
        card=card,  # Required -- agent card for registration and stream affinity
        concurrency=1,
        expected_instances=1,
    )
)
```

**Parameters:** An `AgentInstanceOptions` dataclass with the following fields:

| Field                | Type        | Description                                                |
| -------------------- | ----------- | ---------------------------------------------------------- |
| `agent_name`         | `str`       | Agent name identifier (required)                           |
| `description`        | `str`       | Human-readable description for the agent                   |
| `capabilities`       | `list[str]` | Capability tags for registry membership                    |
| `handler`            | `Callable`  | Task handler function                                      |
| `concurrency`        | `int`       | Maximum concurrent tasks                                   |
| `expected_instances` | `int`       | Expected instance count for scaling                        |
| `pubnub`             | `PubNub`    | Pre-configured PubNub client (auto-created if omitted)     |
| `token`              | `str`       | PAM token for access control                               |
| `heartbeat_ms`       | `int`       | Heartbeat interval in milliseconds                         |
| `on_start_task`      | `Callable`  | Override default StartTask processing                      |
| `on_cancel_task`     | `Callable`  | Override default CancelTask processing                     |
| `on_error`           | `Callable`  | Error callback for handler exceptions                      |
| `card`               | `dict`      | Agent card (required for registration and stream affinity) |
| `card_ref`           | `str`       | Reference URL for the agent card                           |
| `card_summary`       | `str`       | Short summary for the agent card                           |

**Returns:** A dict with:

| Key           | Type       | Description                                                    |
| ------------- | ---------- | -------------------------------------------------------------- |
| `stop`        | `Callable` | Call to shut down the instance (unsubscribe, cancel heartbeat) |
| `agent_name`  | `str`      | The resolved agent name                                        |
| `instance_id` | `str`      | The generated or provided instance ID                          |

## Consumer API

The SDK provides a consumer-side API for submitting tasks, connecting to
existing tasks, handling events, and downloading artifacts.

### `TaskClient.create(billing_mode, ...)`

Classmethod factory that creates a configured `TaskClient` from
environment variables or CDM config.

```python
client = TaskClient.create(billing_mode="free")
```

- `billing_mode` (required): `"free"` or `"paid"`. Selects the keyset — `"free"` → playground, `"paid"` → network. Must match the target agent's persisted `billingMode`, which is set explicitly by the agent owner at register/update time (the backend validates pricing against `billingMode` — `"free"` rejects positive prices, `"paid"` requires a positive `pricePerTask` or `pricePerMinute` — but does NOT derive `billingMode` from pricing). The backend rejects mismatches between the caller-supplied `billing_mode` and the agent row with `BillingModeMismatch`.
- Resolution: explicit options > `BLOCKS_*` env vars > CDM config
- Auth: one of the token provider modes below

### Token Provider Modes

`TaskClient.create()` supports three token provider modes for automatic
token acquisition and refresh.

**Mode 1: API key (server-side)**

```python
import os
from blocks_network import TaskClient

client = TaskClient.create(
    billing_mode="free",
    api_key=os.environ["BLOCKS_API_KEY"],
    on_auth_error=lambda err: print(f"Auth failed: {err}"),
)
```

The SDK exchanges the API key for a short-lived JWT via
`POST /api/v1/auth/agent/consumer-token` and refreshes it
automatically at 80% of its TTL. Use this for backend services,
scripts, and cron jobs.

**Mode 2: Token endpoint**

Simplest form — a bare URL string:

```python
client = TaskClient.create(
    billing_mode="free",
    token_endpoint="https://my-proxy.example.com/api/blocks-token",
)
```

Config-object form — pass a `TokenEndpointConfig` TypedDict when
your proxy needs custom headers, cookies, or a non-empty body.
Every field is optional except `url`:

```python
from blocks_network import TaskClient, TokenEndpointConfig

token_endpoint: TokenEndpointConfig = {
    "url": "https://my-proxy.example.com/api/blocks-token",
    "headers": {
        "X-CSRF-Token": read_csrf_meta(),
        "Cookie": "session=abc123; role=user",   # cookie-auth parity (see note below)
    },
    "body": {"sessionId": current_session_id()},  # replaces the default {}
}

client = TaskClient.create(
    billing_mode="free",
    token_endpoint=token_endpoint,
)
```

User-supplied `headers` merge on top of the SDK default
`Content-Type: application/json` (user values win). `body` is
JSON-serialized and replaces the default empty-object body. The
same config is used for both the initial token acquisition and
subsequent refreshes.

The SDK sends POST requests whenever it needs a token. The endpoint
identifies the caller, mints a Blocks consumer JWT, and returns
`{ "token": "...", "expiresIn": 60, "userId": "..." }`. The endpoint
must include `userId` so `client.get_user_id()` works. No long-lived
credential ever reaches the client.

`token_endpoint` has two first-class deployment shapes:

1. **Customer-owned backend proxy.** Your own service holds the
   Blocks API key, authenticates the caller, and forwards to the
   Blocks backend's `POST /api/v1/auth/agent/consumer-token`.
2. **Dashboard embedder** — the Blocks backend's own consumer-token
   endpoint, called directly from a signed-in browser client with
   the user's session cookie plus `X-Active-Org` and `X-CSRF-Token`
   headers. Custom Python services usually use the customer-owned
   proxy or API key mode.

While `token_endpoint` is primarily a browser/mobile pattern, the
Python SDK supports it for cases where a Python service consumes
tokens from a centralized auth endpoint.

> **Node/Python asymmetry — no `credentials` field.** Python's
> `TokenEndpointConfig` deliberately omits the `credentials` field
> that the Node `TokenEndpointConfig` accepts (`'include' |
> 'same-origin' | 'omit'`). `urllib.request` has no direct analogue
> of `fetch`'s credentials mode, so pretending otherwise would be
> misleading. **For cookie-based auth parity, set the cookie
> explicitly via `headers={'Cookie': 'session=...'}`** — the SDK
> passes the header through to the urllib `Request` unchanged. See
> the [Node README](../node/README.md#token-provider-modes) for the
> fetch-based form.

The module-level convenience factory `create_task_client()` accepts
the same widened `token_endpoint` shape — string or
`TokenEndpointConfig`:

```python
from blocks_network import create_task_client, TokenEndpointConfig

client = create_task_client(
    env="playground",
    token_endpoint={
        "url": "https://my-proxy.example.com/api/blocks-token",
        "headers": {"Cookie": f"session={session_id}"},
    },
)
```

**Mode 3: Custom function**

```python
from blocks_network import TaskClient, TokenResult

def get_token() -> TokenResult:
    resp = requests.post("https://my-auth-server/token")
    data = resp.json()
    return TokenResult(token=data["token"], expires_in=data["expiresIn"], user_id=data["userId"])

client = TaskClient.create(
    billing_mode="free",
    token_provider=get_token,
)
```

For OAuth2, custom SSO, or any auth architecture. The function is
called on init and before each expiry.

**Refresh and error handling**

All modes refresh proactively at 80% TTL and reactively on HTTP 401.
On 3 consecutive failures, `on_auth_error` fires. The stale token
remains usable until the next 401. `client.destroy()` stops the
refresh timer but does not invalidate the current token.

Thread safety: token state is protected by `threading.Lock`, proactive
refresh runs on a daemon `threading.Timer` thread, and concurrent 401
callers share a single refresh via `threading.Event`.

`owner_id` is auto-populated from the authenticated identity when
omitted. Explicit `owner_id` still works and overrides the default.
The backend rejects mismatches between `owner_id` and the
authenticated identity.

### `client.connect(task_id)`

Connect to an existing task. Returns a `TaskSession` pre-populated with
stream refs, artifact refs, and task state from history.

```python
session = client.connect(task_id="task-abc-123")
```

- Requires an authenticated `TaskClient` (for example one created with
  `api_key`, `token_endpoint`, or `token_provider`). Raises if not set.
- Terminal tasks: session is not subscribed, read state via
  `list_artifacts()` and `session.state`.
- Active tasks: session subscribes, live events flow through callbacks.

### `session.wait_for_terminal(timeout=60)`

Block until a terminal event arrives and return it. Convenience wrapper
for the common "submit and wait" pattern. Resolves immediately for
already-terminal sessions (pre-closed idempotent hits, terminal
`connect()`).

```python
from blocks_network import TaskClient, text_part

with TaskClient.create(billing_mode="free", api_key=api_key) as client:
    session = client.send_message(
        agent_name="echo",
        request_parts=[text_part("Hello")],
    )
    with session:
        terminal = session.wait_for_terminal(timeout=30)
        print(terminal.state)  # "completed" | "failed" | ...
        session.save_artifacts("./output")
```

Raises `TimeoutError` if no terminal event arrives within the timeout.

The `TaskEvent` returned by `wait_for_terminal()` has typed properties:
`.message`, `.progress`, `.state`, and `.artifact_ref`.

### `session.list_artifacts()`

Returns all `ArtifactRef` instances seen so far (from history and live
events).

```python
artifacts: list[ArtifactRef] = session.list_artifacts()
```

### `session.download_artifact(ref)`

Download an artifact. Handles inline (base64) and file-backed artifacts
transparently.

```python
result: DownloadedArtifact = session.download_artifact(ref)
# result.data: bytes, result.mime_type: str, result.file_name: Optional[str]
```

Also available as a standalone function: `download_artifact(ref, pubnub)`.

### `session.on_error(cb)`

Register a handler for callback errors. Returns an unsubscribe callable.

```python
def handle_error(error, context):
    print(f"Error in {context.callback_type}: {error}")

unsub = session.on_error(handle_error)
```

`CallbackErrorContext` includes `entry_point`, `callback_type`, and
`event`. Without `on_error` handlers, callback errors are logged at
WARNING level.

### Part Helpers

```python
from pathlib import Path
from blocks_network import text_part, file_part

parts = [
    text_part("Hello"),

    # Raw bytes with metadata:
    file_part(b"\x00\x01\x02", file_name="raw.bin"),

    # From a file path — read synchronously at call time:
    file_part("./data.csv", content_type="text/csv"),

    # From a pathlib.Path:
    file_part(Path("./report.pdf"), content_type="application/pdf"),
]

session = client.send_message(agent_name="echo", request_parts=parts)
```

- `text_part(text, part_id="text")` wraps a string into a
  `SendMessageRequestPart`.
- `file_part(path_or_data, *, part_id="file", file_name=None, content_type="application/octet-stream")`
  accepts either a `str` / `pathlib.Path` pointing at a local file (read
  synchronously) or raw `bytes` / `bytearray`. When reading from a
  path, `file_name` defaults to the file's basename; when passed raw
  bytes, it defaults to `"file"`.
- `part_id` should match a declared `io.inputs[].id` on the agent
  card so the receiving agent can route the part.
- Unlike the Node SDK's `filePart` + `filePartFromPath` split — Node
  keeps the path-reading helper separate so browser bundles do not
  pull in `node:fs` — Python has a single `file_part` because Python
  has no browser-bundle constraint.

### Handling Stream Errors

Every `StreamClient` exposes an `on_error(cb)` registration method.
The callback fires whenever the stream's PubNub subscribe loop
surfaces an error-category status event: PAM revocation, network
issues, timeouts, or any other category the PubNub SDK marks as an
error. The payload is a typed `StreamError` dataclass:

```python
from blocks_network.stream import StreamError

stream = ref.open()

def on_stream_error(err: StreamError) -> None:
    print(f"[stream] {err.category} fatal={err.fatal} channel={err.channel}")

stream.on_error(on_stream_error)

for chunk in stream.bytes():
    sys.stdout.buffer.write(chunk)
```

Two categories are **fatal** and cause the SDK to force-terminate
the stream so `for msg in stream.inbound:` / `for chunk in stream.bytes():`
loops exit cleanly instead of hanging waiting for a `stream_end`
that will never arrive:

- `PNAccessDeniedCategory` — PAM revocation (admin-terminate,
  token denied). This is the signal that the server-side grant is
  gone even if the cached T7c's `exp` claim has not elapsed.
- `PNBadRequestCategory` — auth configuration or malformed grant.

All other error categories (network transients, timeouts, etc.)
fire `on_error` with `fatal=False` and leave the stream running so
PubNub's built-in retry machinery can recover.

### Opening Task Streams

On an active task, `StreamRef.open()` is the standard way to
subscribe to a stream. Use `on_stream(lambda ref: consume(ref.open()))`
to open streams reactively as they are announced, or call
`session.open_all_streams()` once to open every readable stream in
one shot:

```python
from blocks_network import TaskClient, text_part

session = client.send_message(
    agent_name="multi_stream_agent",
    request_parts=[text_part("start")],
)

# Option 1 — react to each stream as it is announced
def on_stream(ref):
    stream = ref.open()
    consume(stream, ref.descriptor.declared_stream)

session.on_stream(on_stream)

# Option 2 — open every readable stream in one call, then branch
session.wait_for_stream()              # ensure at least one is announced
streams = session.open_all_streams()   # returns List[StreamClient] in insertion order
for s in streams:
    consume(s)
```

`open_all_streams()` is idempotent. Calling it again returns the
same `StreamClient` objects for already-opened refs and skips
outbound-only streams. It is an **active-session** convenience —
it does not resurrect unopened streams after terminal; see the next
section.

**Drain window for already-open streams.** When the task reaches
terminal, any stream that was already opened continues draining
cleanly for up to **30 seconds** (raised from 2 seconds in prior
versions) so consumers have time to finish iterating. Tune the
window per session:

```python
# Narrower window for fast-shutdown flows
session = client.send_message(
    agent_name="llm_streamer",
    request_parts=[text_part("stream please")],
    drain_window_s=5.0,   # 5 seconds
)

# Wider window for long-tail consumers, or on connect()
resumed = client.connect(
    task_id="task-abc",
    drain_window_s=60.0,  # 60 seconds
)
```

The option is supported on both `send_message()` and `connect()`.
The unit is seconds (Python) vs milliseconds (Node — `drainWindowMs`)
to match the underlying `_drain_window_s` field naming.

### Reconnecting to Terminal Tasks

Stream data is **live-only** — PubNub does not persist stream
payloads. When `client.connect(task_id=...)` returns a session for
a task that has already finished, a stream that was **never opened
while the task was active** raises a typed `StreamUnavailableError`
from `StreamRef.open()` instead of subscribing to a dead channel.
`open_all_streams()` on the same session silently skips those
never-opened refs:

```python
from blocks_network import TaskClient, StreamUnavailableError

session = client.connect(task_id="task-abc-123")

for ref in session.list_streams():
    try:
        stream = ref.open()
        # ... consume stream ...
    except StreamUnavailableError as err:
        # Stream data is gone, but descriptor and artifacts remain:
        print("stream", ref.descriptor.declared_stream, err.terminal_state)

# Artifacts produced by the finished task are still available:
artifacts = session.list_artifacts()
session.save_artifacts("./recovered")
```

`StreamUnavailableError` carries named fields `task_id`,
`stream_id`, `declared_stream`, and `terminal_state`. It derives
from `RuntimeError` so `except RuntimeError` also catches it.
Inspection of `ref.descriptor` (format, metadata, declared name)
continues to work on terminal-session refs without raising.

> `open_all_streams()` is **not** a post-terminal reopen escape
> hatch. If you want every stream opened, call it while the task is
> still active (for example immediately after
> `session.wait_for_stream()` or inside an `on_stream` callback). On
> a terminal session it silently returns any streams that were
> already active and skips the rest.

## Examples

For complete, runnable example agents, see the
[Python examples](../../examples/python/). Examples cover
request/response, streaming, orchestration, pipe tasks, and advanced
wrapper patterns.

## Related Documentation

- **[Getting Started](../../docs/getting-started.md)** - Unified quickstart for Node and Python
- **[Node SDK](../node/README.md)** - Node.js SDK README
- **[Python Examples](../../examples/python/)** - Canonical Python example agents
