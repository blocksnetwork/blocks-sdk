# Changelog

All notable changes to the Node SDK are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Older entries live in [../../CHANGELOG.md](../../CHANGELOG.md) pending backfill.

## [Unreleased]

### Added

- Registry read helpers accept a credential. `fetchAgentRegistry()`,
  `fetchAgentsByTag()` and `fetchAgentsByListing()` take an optional `apiKey`,
  sent as `Authorization: Bearer <apiKey>`; `getAgent()` already did. Optional
  on Blocks Network, where the registry is world-readable. Required on a Blocks
  Enterprise deployment, which serves agent metadata to signed-in callers only and
  answers an unauthenticated read with an empty page or `null`.

- `removeAgent()` accepts `reason`, recorded in the audit log. A blank `reason`
  throws before any request is sent.

### Fixed

- **Node SDK:** A stream created with `ctx.createStream()` by a task that is still running when the
  agent switches environments now uses the same keys as the rest of that task. Before,
  the stream was built with the keys of the environment the agent had just switched to,
  so it could fail to connect. Tasks received after the switch are unaffected.

- `ConsumerAuth.getLastAuthError()` now reports a failed **reactive** refresh, not
  only a permanently-failed proactive one. `onAuthFailure()` signals failure as a
  bare `false`, which is indistinguishable from a provider that has no refresh
  capability at all, so a live auth outage left no state behind and any caller
  reading `getLastAuthError()` saw a healthy provider — `getAgentCard()` reported
  the outage as "no such agent". Recovery is unchanged: the error clears
  atomically on the next successful token apply, and the fail-fast preflight
  retries once before raising, so a transient outage still self-heals.

- `TaskClient.getAgentCard()` now sends the client's credential, and raises
  `AuthRefreshFailedError` when a configured credential cannot be produced rather
  than returning `null`. `null` continues to mean "no such agent, or no card"; an
  expired token is refreshed if it can be, and reported if it cannot, instead of
  reading as a missing agent. It previously
  issued an unauthenticated lookup regardless of how the client was configured,
  so on a Blocks Enterprise deployment it returned `null` even for a client holding a
  valid API key. Whatever the auth provider holds is forwarded — with
  `ConsumerAuth` that is the consumer token in every mode, since API-key mode
  exchanges the key for one at init, and a client built with `agentAuth` instead
  forwards that credential's access token — read
  per call so a rotated token cannot go stale, and the provider is initialized
  first, which agent-side clients had not always done by the time the card was
  requested.

### Changed

- `BLOCKS_MAX_UPLOAD_BYTES` is now 1 GiB (`1_073_741_824`), up from 25 MB. To send a large file from `TaskClient` without loading it into memory, pass a `Blob` or `File` as the part's `file` (for example from `fs.openAsBlob()`); byte inputs and `publishArtifact` data are copied into a `Blob`, peaking at about twice their size, and downloaded artifacts are held in memory in full.

- Starting an agent without `BLOCKS_API_KEY` now explains that `blocks run`
  supplies the key stored by `blocks login`, or that you can set `BLOCKS_API_KEY`
  in `.env`, instead of pointing only at `blocks login --write-env`.
- `AGENT_FORCED_OFFLINE` from `connect`/`refresh` is now treated as a
  **fatal** auth error: the agent throws `AgentAuthFatalError` and shuts down
  instead of retrying. This surfaces when an administrator has forced the
  agent offline; it stays down until an authorized user re-enables it. Note
  this applies to the connect and refresh paths only — an agent already
  connected and running keeps its channel subscription until it next
  reconnects, but the service fails its in-flight work and refuses new work
  while it is forced offline. No migration needed — this is a new server-side
  kill switch.

- `removeAgent()` no longer takes an `agentAuth`. Removing an agent is a management
  action, and an agent's own runtime credential carries no management standing, so
  the previous `agentAuth` path now answers `403`. The function authenticates with
  an API key from `BLOCKS_API_KEY`, as it already did without `agentAuth`.
  **Migration:** drop the `agentAuth` option and make sure `BLOCKS_API_KEY` is set
  to a key whose owner owns the agent or holds Agent Management in its
  organization. TypeScript callers get a compile error at the removed option; a
  plain-JavaScript caller that keeps passing it has the option ignored and
  authenticates with the API key as above.

### Deprecated

- Calling `removeAgent()` without `reason` logs a warning; it will be required
  from 2027-04-01. **Migration:** pass `{ reason }` to `removeAgent()`.

### Security

- The SDK now resolves a patched build of one of its dependencies. Nothing about
  how the SDK behaves changes. A project that pins that dependency itself, in its
  own lockfile or an `overrides` entry, keeps whatever its pin resolves.
