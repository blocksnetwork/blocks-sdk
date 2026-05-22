# Changelog

All notable changes to the Blocks Network SDK, Python SDK, and CLI are
documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Each component (Node SDK, Python SDK, CLI) is versioned independently.

---

## Node SDK

### [Unreleased]

#### Added
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

#### Changed
- ConsumerAuth is now used for `ctx.taskClient` A2A calls, ensuring consistent auth context for orchestrator agents
- PAM tokens are no longer visible in handler-accessible task objects
- Default CDM config URL updated to `https://config.blocks.ai/config.json`

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
- `session.list_events()` for seeding full task timelines from history
- Agent-side stream APIs now match consumer-side interface
- Agent owners can view full task details for received tasks
- Anonymous playground artifact visibility

#### Fixed
- `on_artifact` history replay — artifact events now replayed correctly from completed tasks
- `send_message` subscribe race resolved with history-based catch-up
- Cross-billing-mode fix for A2A calls
- Preserve `content_type` on uploaded-file wire parts
- PyPI project links corrected

#### Changed
- ConsumerAuth used for `ctx.task_client` A2A calls
- PAM tokens isolated from handler-visible task objects
- Default CDM config URL updated to `https://config.blocks.ai/config.json`

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
- Private agent invitations and grants — invite collaborators to private agents
- `blocks delete` command for removing agent registrations
- `blocks login --no-write-env` flag to opt out of writing `BLOCKS_API_KEY` to `.env` without seeing the interactive prompt. Use this in non-interactive sessions where a TTY is attached but no human is available to answer the prompt.

#### Fixed
- Windows TLS timeouts when fetching CDM config
- PowerShell 5.1 install script parse errors
- Dashboard URL updated from `/playground/agents/` to `/agents/`
- Windows and Linux install script fixes
- `blocks login` no longer hangs on the `Write BLOCKS_API_KEY to project .env? (Y/n):` prompt in non-interactive sessions where a TTY is attached. Pass `--write-env` to opt in or `--no-write-env` to opt out non-interactively.

#### Changed
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
