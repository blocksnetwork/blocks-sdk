# Connections: an agent calling an agent (Node)

An agent that calls the [Salesforce connections agent](../connections-salesforce/) from inside its own task.
Blocks runs that call as the user who sent this task, so the Salesforce agent reads that user's Salesforce, not
the account of whoever runs this agent.

**Category:** Canonical -- integration connections

## How it works

- The handler calls `ctx.taskClient.sendMessage(...)`. The task client a handler receives names the task it runs
  in, so Blocks can carry that task's user down to the task it creates.
- The user approves the Salesforce agent **for this chain**: an approval given here covers the Salesforce agent
  only in tasks the user starts on this agent, whether this agent calls it or an agent this one calls does.
  Approving the Salesforce agent for direct use does not cover it here, and the other way round.
- Until they approve, the call to the Salesforce agent throws an `AuthRequiredError`. The handler does not
  catch it: the SDK fails this task with the refusal attached, and the dashboard offers **Connect**. An agent
  further up a chain gets the same refusal as a `RelayedAuthRequiredError` from `waitForTerminal()`, and passes
  it on the same way.

## Prerequisites

- Node.js 22.x from 22.23.4, 24.x from 24.22.1, or 26.12.1 and later
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env`.
- A Blocks deployment with agent-to-agent delegation turned on. It is off by default while it is in preview.
- The [Salesforce connections agent](../connections-salesforce/) registered and running in the same org, with its
  `salesforce` integration set up as its README describes.

## Install

```bash
cd examples/node/connections-chain
npm install
```

## Run the agent

```bash
blocks register
blocks run
```

## Try it

1. Sign in to the dashboard as a user who has not approved the Salesforce agent, open this agent's page, and
   click **Send task**.
2. The task fails, and the page shows **Connect salesforce to finish this task**.
3. Click **Connect salesforce**. The page that opens says a task you started on this agent asked for it. Click
   **Allow**, then sign in to Salesforce if asked.
4. The dashboard sends the task again. The result is the Salesforce agent's answer, with your Salesforce
   username.

To start over, open **Profile → Connections**. **Revoke** next to the Salesforce agent removes its approvals,
including the one for this chain.
