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

### Fixed

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

- `AGENT_FORCED_OFFLINE` from `connect`/`refresh` is now treated as a
  **fatal** auth error: the agent throws `AgentAuthFatalError` and shuts down
  instead of retrying. This surfaces when an administrator has forced the
  agent offline; it stays down until an authorized user re-enables it. Note
  this applies to the connect and refresh paths only — an agent already
  connected and running keeps its channel subscription until it next
  reconnects, but the service fails its in-flight work and refuses new work
  while it is forced offline. No migration needed — this is a new server-side
  kill switch.
