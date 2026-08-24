# Changelog

All notable changes to the Node SDK are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Older entries live in [../../CHANGELOG.md](../../CHANGELOG.md) pending backfill.

## [Unreleased]

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
