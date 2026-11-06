# Connections: Microsoft 365 (Node)

An agent that acts as the task's user in Microsoft 365. It asks Blocks
for a per-task OAuth token for the `microsoft` integration, reads the
user's account from Microsoft Graph, and lists up to five items in the
root of their OneDrive. It works with personal Microsoft accounts and
with work and school accounts.

**Category:** Canonical -- integration connections

For one agent that checks Google, Microsoft and Salesforce together, see
[connections](../connections/).

## Prerequisites

- Node.js 22.x from 22.23.4, 24.x from 24.22.1, or 26.12.1 and later
- `BLOCKS_API_KEY` in the project `.env`. Get it by running `blocks login --write-env`.
- A `blocks` CLI whose card schema includes `connections`.
- An org admin in the org you register the agent under, to add the
  integration below.

## Set up Microsoft

Sign in at <https://entra.microsoft.com>. If **App registrations** is
missing for a personal account, sign up for a free Azure account first.

1. **App registrations → New registration.**
   - **Supported account types:** *Any Entra ID Tenant + Personal
     Microsoft accounts*. The integration's `common` endpoints need this
     to accept a personal account.
   - **Redirect URI:** platform **Web**. Fill it in from the integration
     form in the next section.
2. Copy the **Application (client) ID** from the Overview page.
3. **Certificates & secrets → Client secrets → New client secret.** Copy
   the secret's **Value**, not its Secret ID. Microsoft shows it once.

Microsoft asks the user for the Graph permissions the agent requests at
sign-in, so you do not have to add them under **API permissions**.

## Add the integration in Blocks

1. In the dashboard, open **Manage → Integrations → Add integration → Microsoft 365**.
2. Set the name to exactly **`microsoft`**, so it matches
   `connections[0].integration` in `agent-card.json`.
3. Copy the **Redirect URI** the form shows into the app registration's
   Web redirect URI.
4. Paste the **Application (client) ID** as the Client ID and the secret
   **Value** as the Client secret, then save.

The template fills in everything else. For an app registered as single
tenant, replace `common` with your tenant ID in both endpoints under
**Advanced**.

## Install

```bash
cd examples/node/connections-microsoft
npm install
```

## Run the agent

```bash
blocks register
blocks run
```

## Try it

1. Open the agent's page in the dashboard and click **Send task**.
2. Click **Connect**, then **Allow**, then pick your Microsoft account
   and accept its consent screen.
3. The result reads:

```text
Signed in to Microsoft as you@example.com. OneDrive files: Documents, Pictures, …
```

To connect again from scratch, open **Profile → Connections**. Microsoft
has no token revocation endpoint, so **Disconnect** deletes the
connection in Blocks but Microsoft keeps the app's consent. Remove it at
<https://account.live.com/consent/Manage> (personal accounts) or
<https://myapps.microsoft.com> (work and school accounts).

## SDK concepts demonstrated

- One `connections` entry in `agent-card.json`, with the scopes the agent needs
- `required: true`: Blocks refuses the task, before the handler runs,
  until the user has connected Microsoft and approved the agent
- `ctx.connections.getToken('microsoft')` for a short-lived token
- `{ forceRefresh: true }` once when Microsoft Graph answers `401`

## Troubleshooting

- **AADSTS50011**: the registration's Web redirect URI does not match
  the one on the integration form.
- **AADSTS50020**: the registration's supported account types exclude
  personal accounts.
- **AADSTS7000215**: you pasted the Secret ID instead of its Value, or
  the secret expired.
- **OneDrive call fails for a work account**: the account has no
  OneDrive licence.
