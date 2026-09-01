# Changelog

All notable changes to the Python SDK are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Older entries live in [../../CHANGELOG.md](../../CHANGELOG.md) pending backfill.

## [Unreleased]

### Added

- Registry read helpers accept a credential. `fetch_agent_registry()`,
  `fetch_agents_by_tag()` and `fetch_agents_by_listing()` take an optional
  `api_key`, sent as `Authorization: Bearer <api_key>`; `get_agent()` already
  did. Optional on Blocks Network, where the registry is world-readable. Required on a
  Blocks Enterprise deployment, which serves agent metadata to signed-in callers
  only and answers an unauthenticated read with an empty page or ``None``.

### Fixed

- ``ConsumerAuth.get_last_auth_error()`` now reports a failed **reactive**
  refresh, not only a permanently-failed proactive one. ``on_auth_failure()``
  signals failure as a bare ``False``, which is indistinguishable from a provider
  that has no refresh capability at all, so a live auth outage left no state
  behind and any caller reading ``get_last_auth_error()`` saw a healthy provider
  — ``get_agent_card()`` reported the outage as "no such agent". Recovery is
  unchanged: the error clears atomically on the next successful token apply, and
  the fail-fast preflight retries once before raising, so a transient outage
  still self-heals.
- ``AuthProvider`` no longer declares the optional ``ensure_ready()`` hook. The
  protocol is ``@runtime_checkable``, so declaring it made
  ``isinstance(p, AuthProvider)`` false for a provider implementing the two
  methods the protocol documents as required. The change only widens what
  satisfies the protocol; every existing provider still passes. Transports probe
  both optional hooks rather than calling them outright, so a provider without
  ``ensure_ready`` now works on the registry card lookup as it already did on the
  RPC path.

- `TaskClient.get_agent_card()` now sends the client's credential, and raises
  ``AuthRefreshFailedError`` when a configured credential cannot be produced rather than
  returning ``None``. ``None`` continues to mean "no such agent, or no card"; an
  expired token is refreshed if it can be, and reported if it cannot, instead of
  reading as a missing agent. It previously
  issued an unauthenticated lookup regardless of how the client was configured,
  so on a Blocks Enterprise deployment it returned ``None`` even for a client holding
  a valid API key. Whatever the auth provider holds is forwarded — with
  ``ConsumerAuth`` that is the consumer token in every mode, since API-key mode
  exchanges the key for one at init, and a client built with ``agent_auth``
  instead forwards that credential's access token — read
  per call so a rotated token cannot go stale, and the provider is initialized
  first, which agent-side clients had not always done by the time the card was
  requested.

### Changed

- `AGENT_FORCED_OFFLINE` from `connect`/`refresh` is now treated as a
  **fatal** auth error: the agent raises `AgentAuthFatalError` and shuts down
  instead of retrying. This surfaces when an administrator has forced the
  agent offline; it stays down until an authorized user re-enables it. Note
  this applies to the connect and refresh paths only — an agent already
  connected and running keeps its channel subscription until it next
  reconnects, but the service fails its in-flight work and refuses new work
  while it is forced offline. No migration needed — this is a new server-side
  kill switch.
