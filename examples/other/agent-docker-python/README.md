# Agent network-outage test harness — Python edition (Docker)

Runs a **Python** Blocks agent inside a container on its own bridge
network so you can simulate PubNub connectivity loss **for the agent
only** — the host browser and backend keep their connectivity
throughout.

This is the Python sibling of [`../agent-docker/`](../agent-docker/).
For background on the harness design, network-isolation rationale, and
the four-fix mapping, read the Node README first; this file only
documents what differs.

## What this harness exists for

- BLOCKS-129 silent-park reproducer + fix verification on the **Python**
  SDK (`blocks-sdk/sdks/python/blocks_network/`).
- Side-by-side parity testing: run the same `./blip.sh` against the
  Node and Python harnesses and confirm both SDKs heal cleanly after
  network restoration.
- Any "what happens when the Python agent's uplink dies?" scenario.

The container name (`blocks-agent-py-test`) and bridge network
(`blocks-agent-py-net`) are namespaced separately from the Node
harness so both can run side-by-side.

## Implementation

The container runs the **Python SDK's `blocks-run` console_script**
directly. No Go toolchain inside the image.

- The Python SDK source (`blocks-sdk/sdks/python/`) is COPYed into the
  image and `pip install -e /sdk` registers `blocks-run` on PATH.
- Node 20 is also installed inside the image — used **only** by
  `log-prefix.mjs` to prefix agent stdout/stderr with millisecond
  timestamps. The agent itself runs Python.
- The agent project directory is bind-mounted at `/agent`. The
  entrypoint `cd`s into it before `exec blocks-run` so the SDK
  resolves the handler module relative to the project root.
- A socat forwarder maps container `localhost:{BACKEND_PORT}` →
  `host.docker.internal:{BACKEND_PORT}` so CDM-provided URLs work
  verbatim. `BACKEND_PORT` defaults to **3011**.

## Prerequisites

- Docker Desktop (macOS/Linux)
- Local backend running at `http://localhost:3011` (or wherever
  `BACKEND_PORT` points)
- `BLOCKS_API_KEY` set — run `blocks login` in any agent project to
  create one, then either `export BLOCKS_API_KEY=<key>` or drop it
  into the `.env` file next to `docker-compose.yml`
- UI open in a browser
- A Python agent project to run. The default (`AGENT_DIR` unset) is
  `blocks-sdk/examples/python/echo`. Anything with a valid
  `agent-card.json` + `handler.py` works.

**No host-side Python install is required** — the SDK lives entirely
inside the image. The host only needs Docker.

## Quickstart

```bash
cd examples/other/agent-docker-python

# Set your API key (or put it in a .env file next to docker-compose.yml)
export BLOCKS_API_KEY=your-key-here

docker compose build

# Launch the default Python echo agent.
docker compose up

# Point at a different backend port (e.g. if you run on 3001):
BACKEND_PORT=3001 docker compose up

# Override the agent project:
AGENT_DIR=../../../blocks-sdk/examples/python/echo-stream docker compose up
```

In a second terminal, simulate outages:

```bash
./blip.sh 30      # 30s outage — short enough to test transient recovery
./blip.sh 60      # 60s outage — crosses PubNub presenceTimeout (~20-40s)
./blip.sh 600     # 10min outage — exhausts the Python SDK's retry budget
```

**Why `./blip.sh 600` for silent-park reproduction.** The Python `pubnub`
SDK's native `NativeReconnectionManager` uses `ExponentialDelay.MAX_RETRIES = 6`
with `MAX_BACKOFF = 150` seconds (per `pubnub/managers.py`). Cumulative
worst-case delay across the six retries is roughly
`2 + 4 + 8 + 16 + 32 + 64 + 150 ≈ 4-6 minutes`, after which the
reconnection manager gives up. Anything shorter recovers cleanly on
its own and won't expose silent-park. Use ≥600 s to be confident the
budget is fully exhausted before the network restores.

## Mapping to BLOCKS-129 silent-park (Fix 5)

The Python harness's primary purpose is reproducing and verifying the
silent-park fix on the Python SDK. The Node harness already verified
this on the Node side; see
[`PRESENCE_DOT_FIXES_PLAN.md` §9a](../../../dev_docs/initiative/05-04_presence_dot_fixes/PRESENCE_DOT_FIXES_PLAN.md#9a-fix-5-added-during-testing--node-sdk-silent-park).

**Pre-fix** (today, before the Python SDK fix lands):
```bash
./blip.sh 360
```
- Agent log goes silent within ~3.5 minutes of the outage start.
- After network restoration, the agent does **not** re-register
  presence — PubNub's Event Engine has parked in
  `RECEIVE_FAILED`/`HEARTBEAT_FAILED` after exhausting its default
  6-attempt retry budget.
- The dashboard dot stays grey indefinitely until the container is
  restarted.

**Post-fix** (after Layer 1 of `PYTHON_PARITY_AND_PAM_PLAN.md` lands):
```bash
./blip.sh 360
```
- Container log shows `pubnub_transport_retry` warns at exponential
  delays throughout the outage.
- Within ~17-30 seconds of network restoration the agent re-registers
  presence and the dot returns to green without a page refresh.

The remaining Fix 1-4 mappings (transient blip absorbed by Fix 2,
sweep promotion, reconcile grace, Tier-1 self-heal) work the same as
the Node harness — see [the Node README](../agent-docker/README.md#mapping-the-four-fixes-to-concrete-runs).

## Observing the flow

Watch three places in parallel:

- **Container log** (`docker compose up`): the Python SDK's structured
  logger emits `[AgentInstance]` events; after the fix lands, also
  watch for `pubnub_transport_retry` warns during outages.
- **Backend log**: heartbeat handler logs, `[presence]` entries.
- **Browser DevTools console**: state transitions on the tracked
  agent. AgentCard's Tier-2 subscription fires on every
  `vis_presence`.

## Troubleshooting

- `BLOCKS_API_KEY` error on startup: run `blocks login` in any agent
  project, then `export BLOCKS_API_KEY=<key>`, or populate the `.env`
  file next to this compose file.
- `ModuleNotFoundError: No module named 'handler'`: the agent project
  bind-mounted at `/agent` is missing `handler.py` (or whatever its
  entry module is named). Check `AGENT_DIR` points at a valid
  example.
- Agent connects but `/agent-status` shows `onlineCount: 0`: wrong
  backend port. Set `BACKEND_PORT` or confirm `afui_mvp_backend/.env`.
- `host.docker.internal` not resolvable: update Docker Desktop; on
  Linux pass `--add-host=host.docker.internal:host-gateway` (already
  in the compose file).
- Blip disconnect happens but dot never flips: PubNub's
  `presenceTimeout` needs ~20-40 s of missed heartbeats before the
  `timeout` event fires. Try `./blip.sh 60` for a clearer signal.

## Cleaning up

```bash
docker compose down
docker network rm blocks-agent-py-net      # only if it lingers
```

## Relationship to the Node harness

| | Node | Python |
|---|---|---|
| Path | `examples/other/agent-docker/` | `examples/other/agent-docker-python/` |
| Container | `blocks-agent-test` | `blocks-agent-py-test` |
| Bridge network | `blocks-agent-net` | `blocks-agent-py-net` |
| Agent runtime | Node SDK (`node /sdk/dist/cli/run.js`) | Python SDK (`blocks-run`) |
| Default agent | `blocks-sdk/examples/node/echo-stream` | `blocks-sdk/examples/python/echo` |
| Base image | `node:24-slim` | `python:3.12-slim` (+ Node 20 for log-prefix) |

Both harnesses share `blip.sh` and `log-prefix.mjs` byte-for-byte
except for the container/network rename in `blip.sh`.
