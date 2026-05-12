#!/usr/bin/env bash
# Simulate a network outage for the running agent container.
#
# Disconnects the container from its bridge network, waits N seconds,
# then reconnects. The host's browser and backend keep their
# connectivity throughout — this isolates the failure to the agent's
# PubNub path, which is what makes the heal flow observable.
#
# Usage:
#   ./blip.sh            # default 30s outage
#   ./blip.sh 60         # 60s outage

set -euo pipefail

DURATION="${1:-30}"
CONTAINER="blocks-agent-test"
NETWORK="blocks-agent-net"

if ! docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null | grep -q true; then
  echo "Container '$CONTAINER' is not running. Start it with: docker compose up" >&2
  exit 1
fi

# Match log-prefix.mjs format: HH:MM:ss.lll. BSD `date` on macOS lacks %N
# so we shell out to node (already required by the harness) for ms precision.
ts() {
  node -e 'const d=new Date();const p=(n,l=2)=>String(n).padStart(l,"0");process.stdout.write(`${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(),3)}`)'
}

echo "[$(ts)] [blip] cutting network for ${DURATION}s..."
docker network disconnect "$NETWORK" "$CONTAINER"
START=$(date +%s)

trap 'docker network connect "$NETWORK" "$CONTAINER" 2>/dev/null || true' EXIT INT TERM

sleep "$DURATION"

docker network connect "$NETWORK" "$CONTAINER"
ELAPSED=$(( $(date +%s) - START ))
echo "[$(ts)] [blip] network restored after ${ELAPSED}s — watch the agent log for heal cycle completion."
