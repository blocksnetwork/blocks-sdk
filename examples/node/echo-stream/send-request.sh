#!/usr/bin/env bash
# Send a SendMessage request to the echo_stream agent the same way the
# dashboard UI would: a JSON-RPC POST to /api/v1/rpc. Useful for poking
# the backend without spinning up the Node SDK consumer.
#
# Usage:
#   ./send-request.sh                              # uses the default text
#   ./send-request.sh "custom text to echo"        # one-off payload
#   ./send-request.sh --poll                       # poll GetTask until terminal
#   ./send-request.sh --poll "some text"
#
# Environment:
#   BLOCKS_BACKEND_URL     Backend base URL (default: http://localhost:3001)
#   BLOCKS_SESSION_COOKIE  better-auth.session_token value (URL-encoded, as
#                          copied from the dashboard's browser devtools).
#                          This matches how the UI authenticates.
#   BLOCKS_TOKEN           Alternative: a Bearer API key (bk_...) or agent JWT.
#                          Used only when BLOCKS_SESSION_COOKIE is unset.
#   If neither is set, the request is anonymous (works only for public+free
#   agents).
#
# Requires: curl, jq.

set -euo pipefail

BACKEND_URL="${BLOCKS_BACKEND_URL:-http://localhost:3001}"
PROTOCOL_VERSION="2026-05-01"
AGENT_NAME="echo_stream"

POLL=0
TEXT=""
while (( $# )); do
  case "$1" in
    --poll) POLL=1 ;;
    -h|--help)
      sed -n '2,20p' "$0" | sed 's/^# //; s/^#//'
      exit 0
      ;;
    *) TEXT="$1" ;;
  esac
  shift
done

if [[ -z "$TEXT" ]]; then
  TEXT=$'Hello from send-request.sh!\nLine two.\nLine three.'
fi

command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

auth_header=()
if [[ -n "${BLOCKS_SESSION_COOKIE:-}" ]]; then
  auth_header=(-H "Cookie: better-auth.session_token=${BLOCKS_SESSION_COOKIE}")
elif [[ -n "${BLOCKS_TOKEN:-}" ]]; then
  auth_header=(-H "Authorization: Bearer ${BLOCKS_TOKEN}")
fi

# JSON-RPC 2.0 envelope matching what the dashboard sends.
# We intentionally omit ownerId — the backend derives it from the auth
# session. Sending a mismatched ownerId is rejected with 403.
request_body=$(jq -nc \
  --arg agent "$AGENT_NAME" \
  --arg text  "$TEXT" \
  '{
    jsonrpc: "2.0",
    id: 1,
    method: "SendMessage",
    params: {
      agentName: $agent,
      requestParts: [ { partId: "text", text: $text } ]
    }
  }')

echo "POST ${BACKEND_URL}/api/v1/rpc  (SendMessage / ${AGENT_NAME})"
echo "Input: $(printf '%s' "$TEXT" | head -c 80)"
echo "---"

response=$(curl -sS -X POST "${BACKEND_URL}/api/v1/rpc" \
  -H "Content-Type: application/json" \
  -H "Blocks-Protocol-Version: ${PROTOCOL_VERSION}" \
  ${auth_header[@]+"${auth_header[@]}"} \
  --data "$request_body")

echo "$response" | jq .

task_id=$(echo "$response" | jq -r '.result.taskId // empty')
if [[ -z "$task_id" ]]; then
  echo "No taskId in response — bailing out." >&2
  exit 1
fi

if (( POLL == 0 )); then
  exit 0
fi

echo
echo "Polling GetTask for ${task_id} (Ctrl-C to stop)..."

terminal_states='"succeeded" "failed" "canceled" "rejected" "expired"'
last_state=""
while true; do
  poll_body=$(jq -nc \
    --arg tid "$task_id" \
    '{ jsonrpc: "2.0", id: 1, method: "GetTask", params: { taskId: $tid } }')

  poll_response=$(curl -sS -X POST "${BACKEND_URL}/api/v1/rpc" \
    -H "Content-Type: application/json" \
    -H "Blocks-Protocol-Version: ${PROTOCOL_VERSION}" \
    ${auth_header[@]+"${auth_header[@]}"} \
    --data "$poll_body")

  state=$(echo "$poll_response" | jq -r '.result.task.state // .error.message // "unknown"')

  if [[ "$state" != "$last_state" ]]; then
    echo "[$(date +%H:%M:%S)] state=$state"
    last_state="$state"
  fi

  for t in $terminal_states; do
    if [[ "\"$state\"" == "$t" ]]; then
      echo
      echo "Final GetTask response:"
      echo "$poll_response" | jq '.result.task'
      exit 0
    fi
  done

  sleep 1
done
