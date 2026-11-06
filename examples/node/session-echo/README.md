# Session Echo (Node)

Canonical session example. A session is one conversation with one agent
process: the consumer opens it once, sends many messages (exchanges)
over it, and closes it. The agent answers each exchange with an echo.

**Category:** Canonical -- sessions

> **Experimental.** Sessions are a preview in the Node SDK and may change
> before general availability. They serve free agents only, and the
> service refuses them with `SESSIONS_DISABLED` until sessions are enabled
> for your organization.

## Prerequisites

- Node.js 22.x from 22.23.4, 24.x from 24.22.1, or 26.12.1 and later
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env` (or accept the interactive prompt during `blocks login`).

## Install

```bash
cd examples/node/session-echo
npm install
```

## Register and run the agent

```bash
blocks register   # registers the agent privately and free
npm start
```

`npm start` runs `agent.ts`, which calls `startAgentInstance()` with the
session handlers. `blocks run` starts the task handler only, so the agent
would not serve sessions under it.

## Run the consumer

In a separate terminal:

```bash
npm run consumer
```

The consumer opens a session, sends three messages one after another,
prints each echo, and closes the session.

## SDK concepts demonstrated

- `onSessionOpen(ctx)`, `onExchange(ctx, exchange)` and `onSessionClose(ctx, reason)` provider handlers
- `exchange.requestParts` input parsing and `exchange.reportStatus()` for progress
- Returning `{ artifacts: [{ data, mimeType }] }` to complete an exchange
- `BlocksClient.create()` with `apiKey` for consumer authentication
- `client.openSession()`, `session.send()` and `exchange.waitForOutcome()`
- `decodeInlineArtifact()` to read an outcome's artifacts
- `session.close()` and `client.destroy()` for cleanup
- `SessionsDisabledError` and `SessionNoRouteError` typed errors

## What to edit

- Change `onExchange` in `handler.ts` to answer differently. Keep state
  for a conversation in a map keyed by `ctx.sessionId`, created in
  `onSessionOpen` and deleted in `onSessionClose`.
- Change `MESSAGES` in `session-consumer.ts` to send other input.
