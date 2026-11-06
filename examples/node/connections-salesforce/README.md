# Connections: Salesforce (Node)

An agent that acts as the task's user in Salesforce. It asks Blocks for
a per-task OAuth token for the `salesforce` integration, reads the
user's identity, and lists the five newest Accounts in their org.

**Category:** Canonical -- integration connections

For one agent that checks Google, Microsoft and Salesforce together, see
[connections](../connections/).

## Prerequisites

- Node.js 22.x from 22.23.4, 24.x from 24.22.1, or 26.12.1 and later
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env`.
- A `blocks` CLI whose card schema includes `connections`.
- An org admin in the org you register the agent under, to add the
  integration below.
- A Salesforce org. A free [Developer Edition](https://developer.salesforce.com/signup)
  org comes with sample Accounts.

## Set up Salesforce

In Salesforce, open **Setup → Apps → External Client Apps → External
Client App Manager → New External Client App**. Older orgs offer **App
Manager → New Connected App** with the same settings.

- **Distribution State:** Local.
- **Enable OAuth:** on.
  - **Callback URL:** fill it in from the integration form in the next section.
  - **OAuth Scopes:** *Manage user data via APIs (api)* and *Perform
    requests at any time (refresh_token, offline_access)*. Without the
    second, Salesforce issues no refresh token.
- **Security:** require the secret for the Web Server Flow and the
  Refresh Token Flow, and require PKCE.

After you create it, open **Policies → Edit**: **Permitted Users** *All
users can self-authorize*, keep the default refresh token policy, and
**IP Relaxation** *Relax IP restrictions*. Then copy the **Consumer Key**
and **Consumer Secret** from **Settings → OAuth Settings**. A new app can
take about 10 minutes before sign-ins work.

## Add the integration in Blocks

1. In the dashboard, open **Manage → Integrations → Add integration → Salesforce**.
2. Set the name to exactly **`salesforce`**, so it matches
   `connections[0].integration` in `agent-card.json`.
3. Copy the **Redirect URI** the form shows into the app's Callback URL.
4. Paste the **Consumer Key** as the Client ID and the **Consumer Secret**
   as the Client secret, then save.

The template fills in everything else. For a sandbox, replace
`login.salesforce.com` with `test.salesforce.com` in the endpoints under
**Advanced**.

## Install

```bash
cd examples/node/connections-salesforce
npm install
```

## Run the agent

```bash
blocks register
blocks run
```

## Try it

1. Open the agent's page in the dashboard and click **Send task**.
2. Click **Connect**, then **Allow**, then sign in to Salesforce and
   click **Allow** on its consent screen.
3. The result reads:

```text
Signed in to Salesforce as you@example.com. Recent Accounts: Acme, Global Media, …
```

To connect again from scratch, open **Profile → Connections**.
**Disconnect** revokes the grant at Salesforce and deletes the connection.

## SDK concepts demonstrated

- One `connections` entry in `agent-card.json`, with the scopes the agent needs
- `required: true`: Blocks refuses the task, before the handler runs,
  until the user has connected Salesforce and approved the agent
- `ctx.connections.getToken('salesforce')` for a short-lived token
- `token.extra.instance_url`: Salesforce returns the org's API host with
  the token, so there is no fixed API URL
- `{ forceRefresh: true }` once when Salesforce answers `401`

## Troubleshooting

- **invalid_client_id** right after you create the app: wait up to 10
  minutes.
- **redirect_uri_mismatch**: the app's Callback URL does not match the
  Redirect URI on the integration form.
- **Reconnect needed after an hour**: the app lacks the `refresh_token,
  offline_access` scope, or its refresh token policy expires tokens
  immediately.
- **Salesforce token has no instance_url**: disconnect and connect again.
