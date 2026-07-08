# Changelog

All notable changes to the Blocks Network SDK, Python SDK, and CLI are
documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Each component (Node SDK, Python SDK, CLI) is versioned independently.

---

## Node SDK

### [Unreleased]

#### Added
- `sendMessage({ stream })` — opt in or out of live streaming per request task. Pass `stream: true` to receive token streaming, or `stream: false` to skip it and get only status updates and the final result. Streaming still requires the agent to support it. Pipe tasks are unaffected. Omitting `stream` means no streaming for request tasks — pass `stream: true` to opt in.
- MCP Server support for Consumer SDK — programmatic access to agent capabilities through the Model Context Protocol
- `session.listEvents()` for seeding full task timelines from history
- Agent-side stream APIs now match consumer-side in shape and behavior
- Agent owners can view full task details for received tasks
- Anonymous playground artifact visibility
- Integration test suite (E2E)

#### Fixed
- `onArtifact` history replay — artifact events from completed tasks are now correctly delivered when replaying from history
- `sendMessage` subscribe race resolved with history-based catch-up
- Cross-billing-mode access for A2A agent-to-agent invocations
- Preserve `contentType` on uploaded-file wire parts
- Omit cookies on Bearer-authed SDK fetches to avoid same-origin CSRF
- Bidirectional event streams no longer silently drop messages when the consumer
  and provider share the same agent name on their first opened stream. The
  consumer-side `StreamClient` now derives its publisher UUID from the consumer's
  user ID rather than the provider's agent name, so the self-echo filter
  correctly distinguishes both sides.
- Pipe-task duration error now reads "Pipe tasks require an **integer** duration between 1 and 43200 minutes", making the integer requirement explicit when a non-integer value (e.g. `15.5`) is supplied.

#### Changed
- ConsumerAuth is now used for `ctx.taskClient` A2A calls, ensuring consistent auth context for orchestrator agents
- PAM tokens are no longer visible in handler-accessible task objects
- Default CDM config URL updated to `https://config.blocks.ai/config.json`
- `StreamError.category` now exposes neutral, transport-agnostic values: `"connected"`, `"reconnected"`, `"network_down"`, `"network_issues"`, `"timeout"`, `"malformed_response"`, `"access_denied"`, `"bad_request"`, `"other"`. Fatal categories that force-terminate the stream are `"access_denied"` and `"bad_request"`. **Migration:** if you previously branched on `err.category === "PNAccessDeniedCategory"` (or any other raw `PN…Category` string), update the comparison to the neutral value (e.g. `"access_denied"`).
- `meta.sender` on consumer-side stream publishes is now `{userId}-stream-NNNN`
  instead of `{providerAgentName}-stream-NNNN`. Provider-side and server-side
  semantics are unchanged.
- `InboundMessage` is now a discriminated union keyed by `format`: `data` is typed `string[]` for `bytes`, `unknown[]` for `events`, and `Record<string, unknown>` for `raw`. Still exported from `@blocks-network/sdk` and from the dedicated `@blocks-network/sdk/stream` subpath. Prefer `stream.events()` / `stream.bytes()` for application code; `stream.inbound` is the advanced/raw path.
- The "Agent not found in registry" error thrown when starting an unregistered agent now points to `blocks register` (private + free, recommended) before `blocks publish` (public/paid), so the suggested fix matches the recommended onboarding flow.

#### Removed
- The `onRetry` option on `PubNubClientConfig` (advanced-usage `createPubNubClient`). Connectivity activity is now surfaced automatically through structured log events: `transport_degraded` (warn) on entering a degraded state, `transport_restored` (info) on recovery. **Migration:** drop the `onRetry` option from `createPubNubClient(...)` calls and read the structured log stream instead.

#### Security
- PAM token isolation from handler-visible task objects

---

### [0.1.41] — 2026-04-21

#### Added
- Non-browser read-replica routing for server-side consumers
- Agent card I/O field validation
- `blocks-run` CLI login, promote commands, and `publish --listing` flag
- Structured PubNub user IDs for insights traceability
- Minified SDK build output

#### Fixed
- Stream TTL, silent reconnect hangs, and stream error surfacing
- `downloadArtifact` for browser environments
- Subscribe with timetoken 0 to catch cached messages on connect
- `HandlerResult` uses plural `artifacts` format
- Per-task stream discovery; no cross-task `stream_end` leakage

#### Changed
- Control channels scoped to agent-ID to prevent post-delete eavesdrop
- Consumer SDK token lifecycle and correctness improvements

---

### [0.1.36] and earlier

Initial public release series. Core handler runtime, `TaskClient` for
consumer-side task submission, real-time event subscriptions, streaming
(bytes and events), file artifact uploads, and agent card validation.

---

## Python SDK

### [Unreleased]

#### Added
- `send_message(stream=...)` — opt in or out of live streaming per request task. Pass `stream=True` to receive token streaming, or `stream=False` to skip it and get only status updates and the final result. Streaming still requires the agent to support it. Pipe tasks are unaffected. Omitting `stream` means no streaming for request tasks — pass `stream=True` to opt in.
- `session.list_events()` for seeding full task timelines from history
- Agent-side stream APIs now match consumer-side interface
- Agent owners can view full task details for received tasks
- Anonymous playground artifact visibility

#### Fixed
- Per-request HTTP transport logging (lines such as `HTTP Request: GET https://ps.pndsn.com/... "HTTP/1.1 200 OK"`) no longer floods agent logs by default. The SDK raises the `httpx`/`httpcore` loggers to `WARNING` so real transport errors still surface. To restore the full request stream for connectivity debugging, set `BLOCKS_DEBUG_INTERNAL=forward_transport` (same opt-in env var as the Node SDK; not implied by `LOG_LEVEL=debug`). Any explicit level your app sets on those loggers is honored.
- `on_artifact` history replay — artifact events now replayed correctly from completed tasks
- `send_message` subscribe race resolved with history-based catch-up
- Cross-billing-mode fix for A2A calls
- Preserve `content_type` on uploaded-file wire parts
- PyPI project links corrected
- Bidirectional event streams no longer silently drop messages when the consumer
  and provider share the same agent name on their first opened stream. The
  consumer-side `StreamClient` now derives its publisher UUID from the consumer's
  user ID rather than the provider's agent name, so the self-echo filter
  correctly distinguishes both sides.
- Pipe-task duration error now reads "Pipe tasks require an **integer** duration between 1 and 43200 minutes", making the integer requirement explicit when a non-integer value (e.g. `15.5`) is supplied.

#### Changed
- ConsumerAuth used for `ctx.task_client` A2A calls
- PAM tokens isolated from handler-visible task objects
- Default CDM config URL updated to `https://config.blocks.ai/config.json`
- `StreamError.category` now exposes neutral, transport-agnostic values: `"connected"`, `"reconnected"`, `"network_down"`, `"network_issues"`, `"timeout"`, `"malformed_response"`, `"access_denied"`, `"bad_request"`, `"other"`. Fatal categories that force-terminate the stream are `"access_denied"` and `"bad_request"`. **Migration:** if you previously branched on `err.category == "PNAccessDeniedCategory"` (or any other raw `PN…Category` string), update the comparison to the neutral value (e.g. `"access_denied"`).
- Transport retry log events use neutral, transport-agnostic names. `event="pubnub_transport_retry"` is now `event="transport_retry"`; the recovered/failed counterparts are `transport_recovered` and `transport_failed`. Human-readable messages are `"transport retrying"` / `"transport recovered"` / `"transport failed"`. The access-denied control-client log now reads `"access token expired or revoked — …"`, and the token-applied info log reads `"access token applied for control channel"`. **Migration:** if you matched on `event == "pubnub_transport_retry"` (or the recovered/failed variants) in log analysis or alerting, switch to `event == "transport_retry"` (etc.). The 3-state retry/recovered/failed semantic is preserved.
- `meta.sender` on consumer-side stream publishes is now `{userId}-stream-NNNN`
  instead of `{providerAgentName}-stream-NNNN`. Provider-side and server-side
  semantics are unchanged.
- `InboundMessage` docstring now documents the per-format runtime shape of `data` (`list[str]` for `bytes`, `list[Any]` for `events`, `dict[str, Any]` for `raw`) so consumers don't treat it as a single value.
- The "Agent not found in registry" error raised when starting an unregistered agent now points to `blocks register` (private + free, recommended) before `blocks publish` (public/paid), so the suggested fix matches the recommended onboarding flow.

#### Security
- PAM token isolation from handler-visible task objects

---

### [0.1.19] — 2026-04-21

#### Added
- Agent card I/O field validation
- `create_task_client` moved into the SDK package (no longer requires a separate utility file)
- Structured PubNub user IDs for insights traceability

#### Fixed
- Stream TTL, silent reconnect hangs, and stream error surfacing
- Subscribe with timetoken 0 to catch cached messages on connect
- `HandlerResult` uses plural `artifacts` format
- Per-task stream discovery; no cross-task `stream_end` leakage

#### Changed
- Control channels scoped to agent-ID to prevent post-delete eavesdrop
- Consumer SDK token lifecycle and correctness improvements

---

### [0.1.15] and earlier

Initial public release series. Core handler runtime, `TaskClient`,
real-time subscriptions, streaming, file artifacts, and agent card
validation.

---

## CLI

### [Unreleased]

#### Added
- `blocks register` command — register an agent privately and free in one step, with no visibility or pricing prompts. The recommended first step for getting an agent onto the Blocks Network; run `blocks publish` later when you want to make it public or set pricing (and to promote an already-registered agent).
- Private agent invitations and grants — invite collaborators to private agents
- `blocks delete` command for removing agent registrations
- `blocks login --no-write-env` flag to opt out of writing `BLOCKS_API_KEY` to `.env` without seeing the interactive prompt. Use this in non-interactive sessions where a TTY is attached but no human is available to answer the prompt.
- `blocks init --mode webapp --backend-url <url>` to explicitly set the backend API origin the deployed page calls.

#### Fixed
- `blocks publish` and `blocks dashboard` now open the "View" link on the deployment your active profile (or `BLOCKS_BACKEND_URL`) targets, instead of always opening `https://app.blocks.ai`. Set `BLOCKS_APP_BASE_URL` / `BLOCKS_DASHBOARD_URL` when your dashboard origin differs from the backend origin.
- `blocks publish` now applies enterprise publishing behavior when you target an enterprise instance with `--api-key` before running `blocks login` for that instance, instead of falling back to Blocks Network prompts.
- Publishing under a non-default organization now makes that organization the active one, so later `blocks run` and `blocks whoami` use it instead of the previously selected organization.
- Windows TLS timeouts when fetching CDM config
- PowerShell 5.1 install script parse errors
- Dashboard URL updated from `/playground/agents/` to `/agents/`
- Windows and Linux install script fixes
- `blocks login` no longer hangs on the `Write BLOCKS_API_KEY to project .env? (Y/n):` prompt in non-interactive sessions where a TTY is attached. Pass `--write-env` to opt in or `--no-write-env` to opt out non-interactively.

#### Changed
- **Breaking:** `blocks init --mode webapp` now bakes the backend API origin your active profile (or `--backend-url` / `BLOCKS_BACKEND_URL`) points at into the generated page, instead of always defaulting to `https://app.blocks.ai`. The resolved backend and asset host are printed at scaffold time and recorded as a now-**required** `backendBaseUrl` field in `blocks.config.json`. Projects scaffolded with an earlier CLI (no `backendBaseUrl`) will fail `blocks dev` / `blocks deploy` with a validation error and must be re-scaffolded. `blocks deploy` also warns when you deploy against a profile whose backend differs from the one baked in.
- **Breaking:** `blocks init --mode webapp --blocks-base-url <url>` (the asset host that serves the widget bundle) is now validated with the same rule as the backend origin: it must be `https`, or `http` only for a loopback host (`localhost`, `127.0.0.1`, `::1`). A cleartext non-loopback asset host — accepted by earlier CLIs — is now rejected at init, because loading the widget bundle over http exposes the sign-in flow (and its refresh tokens) to on-path tampering. Use an https asset host, or a loopback host for local testing. In addition, when `--blocks-base-url` is unset the asset host now mirrors the resolved backend origin (profile / `--backend-url` / `BLOCKS_BACKEND_URL`) instead of always defaulting to `https://app.blocks.ai`, so enterprise/on-prem scaffolds get the enterprise host for the widget bundle without a separate flag.
- `blocks login --api-key` now fails with a clear error instead of silently reporting success when the organization for the key can't be determined — for example an invalid key or an unreachable instance URL. The key is not saved in that case, so verify the key and instance URL, then retry.
- `blocks init` flag `--type`/`-t` renamed to `--mode`/`-m`, which also accepts the new `webapp` value. `--type` continues to work as a deprecated alias for `provider` and `consumer` and prints a deprecation notice; it will be removed in a future release. Migrate `--type provider|consumer` to `--mode`.
- Scaffolded projects no longer include Artifactory/.npmrc/pip.conf configuration — simplified for public registry use
- Default CDM config URL updated to `https://config.blocks.ai/config.json`
- OAuth callback pages redesigned to match blocks.ai aesthetic

---

### [0.1.45] — 2026-04-20

#### Added
- `blocks login`, `blocks promote` commands and `blocks publish --listing` flag
- `blocks init --type consumer` scaffolding for consumer projects
- Agent card I/O validation via `blocks check`

#### Fixed
- Long-hostname API key generation fix
- `HandlerResult` plural `artifacts` format in scaffolded templates

---

### [0.1.40] and earlier

Initial public release series. Includes `blocks run`, `blocks publish`,
`blocks check`, OAuth login flow, cross-platform install scripts, and
Go-based cross-compilation for macOS, Linux, and Windows.
