# Session acceptance suite

Canonical cross-SDK fixtures for the session wire protocol. Node and Python
parity tests both load these files; neither SDK keeps its own copy.

## Scenario → pending feature

Every scenario lands before the code that satisfies it, wrapped in
`pendingUntil(feature)` (Node, `sdks/node/tests/live/sessions/pending.ts`).
Paths are relative to the repository root.

| Scenario | SDK | File | Pending feature |
|---|---|---|---|
| `call()` one-shot | Node | `sdks/node/tests/live/sessions/call.live.test.ts` | none: passes against a deployed backend |
| `openSession()` + 3 sends + close | Node | `sdks/node/tests/live/sessions/multi-exchange.live.test.ts` | none: passes against a deployed backend |
| Paid agent refused with `SESSION_PAID_AGENT_UNSUPPORTED` | Node | `sdks/node/tests/live/sessions/paid-agent-refusal.live.test.ts` | none: passes against a deployed backend |
| Sessions and tasks coexist on one instance | Node | `sdks/node/tests/live/sessions/task-coexistence.live.test.ts` | none: passes against a deployed backend |
| Flag off: every session call refused with `SESSIONS_DISABLED` | Node | `sdks/node/tests/live/sessions/sessions-disabled.live.test.ts` | session flag |
| Turn 2+ latency reported against a budget (not a hard gate) | Node | `sdks/node/tests/live/sessions/turn-latency.live.test.ts` | none: passes against a deployed backend |
| Fixture parity (both SDKs read and produce these fixtures) | Node + Python | `sdks/node/tests/session-fixtures-parity.test.ts`, `sdks/python/tests/test_session_fixtures_parity.py` | none: both SDKs replay every case |
| Provider SIGKILLed mid-exchange: `failed` / `route_lost` within the route-loss bound | Node | `sdks/node/tests/live/sessions/failure/provider-killed.live.test.ts` | none: passes against a deployed backend |
| Token grants: every minted consumer and route token is exact or one anchored pattern over its own route (decoded with `parseToken`), and PAM refuses each token on sibling sessions and routes | Node | `sdks/node/tests/live/sessions/gates/token-grants.live.test.ts` | none: passes against a deployed backend |
| Token revocation drill: per token class, the time from scope end (session closed, provider stopped, agent deleted) to PAM's first refusal, within the revocation bound, and the failed backend calls after it, under the storm limit for session closed and provider stopped (the agent-deleted case reports its failed calls; the next row holds them to the limit) | Node | `sdks/node/tests/live/sessions/gates/token-revocation.live.test.ts` | none: passes against a deployed backend |
| Revocation drill, agent deleted under a running provider: failed session calls in the 30 s after the first refusal stay under the storm limit | Node | `sdks/node/tests/live/sessions/gates/token-revocation.live.test.ts` | checkpoint backoff after the agent is deleted: the Node and Python provider runtimes end the route on a checkpoint 401, or back off |
| Zombie route (heartbeats alive, delivery dead): open session fails `route_unhealthy`, new opens get `SESSION_NO_ROUTE` | Node | `sdks/node/tests/live/sessions/failure/zombie-route.live.test.ts` | none: passes against a deployed backend |
| Consumer network blip: same session recovers, each progress event and outcome delivered once | Node | `sdks/node/tests/live/sessions/failure/consumer-blip.live.test.ts` | none: passes against a deployed backend |
| Node consumer ↔ Python provider, multi-exchange, correlated outcomes | Node + Python | `sdks/node/tests/live/sessions/cross-sdk/cross-sdk.live.test.ts` (spawns `provider.py`) | none: passes against a deployed backend |
| Python consumer ↔ Node provider, multi-exchange, correlated outcomes | Node + Python | `sdks/node/tests/live/sessions/cross-sdk/cross-sdk.live.test.ts` (spawns `consumer.py`; skips until the installed Python SDK has `BlocksClient`) | none |
| Python `call()` one-shot | Python | `sdks/python/tests/live/sessions/test_sessions_happy_path_live.py` (skips until the installed Python SDK has `BlocksClient`) | none |
| Python `open_session()` + 3 sends + close | Python | `sdks/python/tests/live/sessions/test_sessions_happy_path_live.py` (skips until the installed Python SDK has `BlocksClient`) | none |

## Running

Live scenarios need `PUBNUB_LIVE_TEST=1`, `BLOCKS_API_KEY` and
`BLOCKS_BACKEND_URL` pointing at a deployment with sessions. Set
`BLOCKS_SUBSCRIBE_KEY` / `BLOCKS_PUBLISH_KEY` when that deployment's keyset is
not the one the default CDM serves. `SESSIONS_FLAG_ON=1` declares that the
backend has sessions on: every scenario but the flag-off one needs it, and
the flag-off one skips under it. Pending scenarios skip unless
`SESSIONS_RUN_PENDING=1`, which runs them so you can watch them fail. The
scenarios under `gates/` write their measurements, one Markdown file per
gate, to the directory named by `SESSION_GATES_REPORT_DIR`, when it is set.

`connector.live.test.ts` is the managed-session live test. It runs against
agents and a keyset provisioned beforehand, once per keyset, and skips unless
its own variables are set; the header comment of the file lists them.

```sh
# Node (cross-SDK scenarios also need blocks_network importable by $PYTHON)
cd sdks/node
PUBNUB_LIVE_TEST=1 SESSIONS_RUN_PENDING=1 npx vitest run tests/live/sessions

# Python
cd sdks/python
PUBNUB_LIVE_TEST=1 SESSIONS_RUN_PENDING=1 pytest tests/live/sessions
```

## Rule for implementing changes

The change that makes a scenario pass deletes its pending wrapper in the same
pull request. It **MUST NOT** weaken the test to get there: no loosened bound,
dropped assertion, widened timeout, or retry around the behavior under test. If
the scenario is wrong against the session protocol, fix the protocol definition
first and change the test to match it, in the same pull request.
