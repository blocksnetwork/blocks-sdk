# Connections: Google (Node)

An agent that acts as the task's user in Google. It asks Blocks for a
per-task OAuth token for the `google` integration, reads the user's
email, and lists their five most recent Google Drive files.

**Category:** Canonical -- integration connections

For one agent that checks Google, Microsoft and Salesforce together, see
[connections](../connections/).

## Prerequisites

- Node.js 22.x from 22.23.4, 24.x from 24.22.1, or 26.12.1 and later
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env`.
- A `blocks` CLI whose card schema includes `connections`.
- An org admin in the org you register the agent under, to add the
  integration below.

## Set up Google

1. In the [Google Cloud Console](https://console.cloud.google.com/),
   create or pick a project.
2. **APIs & Services → Library**: enable the **Google Drive API**.
3. **APIs & Services → OAuth consent screen**: user type **External**,
   publishing status **Testing**, and add each Google account that will
   connect as a test user.
4. **APIs & Services → Credentials → Create credentials → OAuth client ID**:
   application type **Web application**. Leave the redirect URI for now.

## Add the integration in Blocks

1. In the dashboard, open **Manage → Integrations → Add integration → Google**.
2. Set the name to exactly **`google`**, so it matches
   `connections[0].integration` in `agent-card.json`.
3. Copy the **Redirect URI** the form shows into the Google OAuth
   client's **Authorized redirect URIs**, and save the client. Google can
   take a few minutes to accept a new redirect URI.
4. Paste the client's **Client ID** and **Client secret** into the form
   and save.

## Install

```bash
cd examples/node/connections-google
npm install
```

## Run the agent

```bash
blocks register
blocks run
```

## Try it

1. Open the agent's page in the dashboard and click **Send task**.
2. Click **Connect**, then **Allow**, then sign in to Google and accept
   its consent screen. A Testing app shows an "unverified app" warning
   first; click **Continue**.
3. The result reads:

```text
Signed in to Google as you@example.com. Recent Drive files: Notes, Budget, …
```

To connect again from scratch, open **Profile → Connections**.
**Revoke** next to the agent removes only this agent's access.
**Disconnect** on the Google card revokes the token at Google and
deletes the connection.

## SDK concepts demonstrated

- One `connections` entry in `agent-card.json`, with the scopes the agent needs
- `required: true`: Blocks refuses the task, before the handler runs,
  until the user has connected Google and approved the agent
- `ctx.connections.getToken('google')` for a short-lived token
- `{ forceRefresh: true }` once when Google answers `401`

## Troubleshooting

- **redirect_uri_mismatch**: the OAuth client's redirect URI must match
  the one on the integration form exactly. Wait a few minutes after
  changing it.
- **Integration unavailable**: the org you registered the agent under
  has no integration named `google`.
- **Drive API has not been used in project**: enable the Google Drive
  API for the project that owns the OAuth client.
