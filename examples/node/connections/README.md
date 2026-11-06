# Connections (Node)

An agent that acts as the task's user in other services. It asks Blocks
for a per-task OAuth token for each integration the card declares, calls
that service's "who am I" endpoint, and reports the account it is signed
in as.

**Category:** Canonical -- integration connections

For one provider per agent, with `required: true` and a real API call
after sign-in, see [connections-google](../connections-google/),
[connections-microsoft](../connections-microsoft/) and
[connections-salesforce](../connections-salesforce/). Their READMEs also
cover setting up each provider's OAuth app.

## Prerequisites

- Node.js 22.x from 22.23.4, 24.x from 24.22.1, or 26.12.1 and later
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env`.
- A `blocks` CLI whose card schema includes `connections`.
- In the org you register the agent under, an admin has added the
  integrations under **Manage → Integrations**, named exactly `google`,
  `microsoft` and `salesforce`. Any of them can be missing: the agent
  reports it as unavailable and carries on.

## Install

```bash
cd examples/node/connections
npm install
```

## Run the agent

```bash
blocks register
blocks run
```

## Try it

Send a task from the agent's page in the dashboard, or run the consumer in
a separate terminal:

```bash
npx tsx connections-consumer.ts
```

The result has one line per integration:

```text
google: signed in as you@example.com
microsoft: not_connected, connect at https://…
salesforce: unavailable (409 IntegrationUnavailable)
```

Open the `connect at` link, approve the agent and sign in at the
provider, then send the task again.

## SDK concepts demonstrated

- `connections` in `agent-card.json`, each with the scopes the agent needs
- `required: false`: the task starts without the connection, and the
  handler degrades when it is missing
- `ctx.connections.getToken(integration)` for a short-lived token
- `{ forceRefresh: true }` once when the provider answers `401`
- `ConnectionRequiredError` (`reason`, `connectUrl`) and `ConnectionAccessError` (`status`, `code`)

With `required: true`, Blocks checks the connection when the task is
created and refuses the task, with `AuthRequiredError` on the consumer
side, before the handler runs.

## What to edit

- Add or remove entries in `SERVICES` in `handler.ts` and in
  `connections` in `agent-card.json`. The two lists must match.
- Ask for the scopes your calls need. Blocks treats the integration's
  own scope list as the ceiling.
