# Agent network-outage test harness (Docker)

Runs a Blocks agent inside a container on its own bridge network so
you can simulate PubNub connectivity loss **for the agent only** — the
host browser and backend keep their connectivity throughout.

**Canonical uses:**
- BLOCKS-129 presence test plan (`dev_docs/initiative/05-04_presence_dot_fixes/TEST_PLAN.md`) §7.
- Verifying SDK heal loops against real PubNub without touching the host's network.
- Any "what happens when the agent's uplink dies?" scenario.

## What it lets you test

The four BLOCKS-129 fixes have different observable signatures under
network disruption; this harness lets you hit each one deliberately:

| Fix | Scenario | Expected observation |
|---|---|---|
| **1** — AgentCard live rebind | Long outage → timeout promoted to offline → reconnect | Dot flips grey then green without page refresh |
| **2** — Timeout debounce | ≤30 s outage | Dot **stays green** throughout; no flicker |
| **2** — Sweep promotion | >30 s outage + one cron tick | Dot flips grey after sweep fires |
| **3** — Reconcile grace window | Timeout during reconcile tick | Pending row survives (no silent delete) |
| **4** — `/agent-status` self-heal | Manual `DELETE FROM agent_instance` mid-test | Next page load reflects 0 regardless of denormalized column |

## Implementation

The container runs the **Node SDK** directly (`node /sdk/dist/cli/run.js`)
— the same `blocks-run` bin the Go CLI delegates to. No Go toolchain
needed inside the image.

- The built SDK (`blocks-sdk/sdks/node/dist`) is COPYed into the image
  and npm-installed for container-native binaries.
- The agent project directory is bind-mounted at `/agent`.
- A socat forwarder maps container `localhost:{BACKEND_PORT}` →
  `host.docker.internal:{BACKEND_PORT}` so CDM-provided URLs work
  verbatim. `BACKEND_PORT` defaults to **3011** (this worktree's
  `afui_mvp_backend/.env`) and can be overridden per-run.

## Prerequisites

- Docker Desktop (macOS/Linux)
- Workspace dependencies installed: `npm install` at repo root
- Node SDK built: `cd blocks-sdk && npm run build`
- `BLOCKS_API_KEY` set (run `blocks login` in any agent project to
  create one, then `export BLOCKS_API_KEY=<key>`; or drop it into the
  `.env` file next to `docker-compose.yml`)
- Local backend running at `http://localhost:3011` (or wherever
  `BACKEND_PORT` points)
- UI open in a browser

## Quickstart

```bash
cd examples/other/agent-docker

# Set your API key (or put it in a .env file next to docker-compose.yml)
export BLOCKS_API_KEY=your-key-here

docker compose build

# Launch the default agent (echo-stream). Backend port defaults to 3011.
docker compose up

# Point at a different backend port (e.g. if you run on 3001):
BACKEND_PORT=3001 docker compose up

# Override the agent project:
AGENT_DIR=../../../blocks-sdk/examples/node/echo docker compose up
```

In a second terminal, simulate outages:

```bash
# 25s blip — Fix 2 debounce absorbs it, dot stays green
./blip.sh 25

# 60s outage — crosses PubNub presenceTimeout (~20-40s) AND Fix 2
# debounce (30s), so the next sweep tick will promote to offline.
# Trigger the sweep manually from the backend dir:
#   cd ../../../afui_mvp_backend && npx tsx src/jobs/run-reconcile-presence.ts
./blip.sh 60
```

## Mapping the four fixes to concrete runs

Each run below corresponds to a TEST_PLAN §7 scenario. Read all three
log streams (container, backend, browser console) in parallel.

### Fix 2 §7.1 — transient blip, no flicker
```bash
./blip.sh 25
```
- Container log: `PNNetworkIssuesCategory` → eventual reconnect.
- Backend log: `POST /internal/presence/heartbeat` with `action:"timeout"`
  followed shortly by `action:"join"`.
- DB: `SELECT pending_timeout_at FROM agent_instance` — briefly set,
  then null again.
- UI dot: green throughout.

### Fix 2 §7.2 — long outage, sweep promotes
```bash
./blip.sh 60 &
# Wait for pending_timeout_at to age past 30s, then:
cd ../../../afui_mvp_backend
npx tsx src/jobs/run-reconcile-presence.ts
```
- Row is deleted; `vis_presence agent_offline` published.
- UI dot flips grey.

### Fix 3 §6 — reconcile grace window
Start the agent cleanly. Before any blip, trigger reconcile:
```bash
cd ../../../afui_mvp_backend && npx tsx src/jobs/run-reconcile-presence.ts
```
Row survives because hereNow returns the live instance. (To reproduce
the race deterministically, use the integration-test path instead.)

### Fix 1 §4 — live rebind
Observe the dot flipping without a page refresh during any of the
scenarios above. If the dot only updates on reload, Fix 1 regressed.

### Fix 4 §5 — Tier-1 self-heal
Mid-run, from a psql shell:
```sql
DELETE FROM agent_instance WHERE instance_id LIKE 'AG-<agentName>-%';
```
Then hit `/api/v1/agent-status?agentNames=<agentName>`: `onlineCount`
reflects 0 immediately, regardless of `agent.online_count`.

## Observing the flow

Watch three places in parallel:

- **Container log** (`docker compose up`): `PNNetworkIssuesCategory` →
  `heal cycle started` → `heal succeeded after N attempts`.
- **Backend log**: heartbeat handler logs, `[presence]` entries —
  `timeout` then heal upsert (or sweep promotion).
- **Browser DevTools console**: state transitions on the tracked agent.
  AgentCard's Tier-2 subscription fires on every `vis_presence`.

## Troubleshooting

- `BLOCKS_API_KEY` error on startup: run `blocks login` in an agent
  project, then `export BLOCKS_API_KEY=<key>`, or populate the `.env`
  file next to this compose file.
- `Cannot find module`: run `npm install` at repo root, then
  `cd blocks-sdk && npm run build`.
- Agent connects but `/agent-status` shows `onlineCount: 0`: wrong
  backend port. Set `BACKEND_PORT` or confirm `afui_mvp_backend/.env`.
- `host.docker.internal` not resolvable: update Docker Desktop; on
  Linux pass `--add-host=host.docker.internal:host-gateway` (already
  in the compose file).
- Blip disconnect happens but dot never flips: check whether the agent
  actually had time to register. PubNub's `presenceTimeout` needs 20 s
  of missed heartbeats before the `timeout` event fires.

## Cleaning up

```bash
docker compose down
docker network rm blocks-agent-net      # only if it lingers
```
