#!/bin/sh
# Entrypoint: forward container-local :{PORT} to the host-side backend,
# then run the agent. The Blocks CDM endpoint returns
# `api.baseUrl: http://localhost:{PORT}`, which the SDK uses verbatim —
# socat makes that URL reach the host from inside this container.
#
# docker-compose.yml sets HOST_BACKEND and LOCAL_BACKEND_PORT. The
# defaults below match this worktree's afui_mvp_backend/.env (PORT=3011).
# Mirrors examples/other/agent-docker/entrypoint.sh; the Python harness
# uses the SDK's `blocks-run` console_script as the agent command (set
# in docker-compose.yml).
set -eu

HOST_TARGET="${HOST_BACKEND:-host.docker.internal:3011}"
LISTEN_PORT="${LOCAL_BACKEND_PORT:-3011}"

echo "[entrypoint] forwarding localhost:${LISTEN_PORT} -> ${HOST_TARGET}"
socat -d "TCP-LISTEN:${LISTEN_PORT},fork,reuseaddr,bind=127.0.0.1" \
         "TCP:${HOST_TARGET}" &
SOCAT_PID=$!

cleanup() {
  if kill -0 "$SOCAT_PID" 2>/dev/null; then
    kill "$SOCAT_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

# Small delay so socat is listening before the agent's first fetch.
sleep 0.2

cd /agent

# Pipe agent stdout/stderr through log-prefix.mjs so each line is
# prefixed with `[HH:MM:ss.lll]` — matching the backend's pino-pretty
# clock format. Disable with AGENT_LOG_TIMESTAMPS=0 if interleaving
# with another aggregator that adds its own timestamps.
if [ "${AGENT_LOG_TIMESTAMPS:-1}" = "1" ]; then
  exec "$@" 2>&1 | node /usr/local/bin/log-prefix.mjs
fi

exec "$@"
