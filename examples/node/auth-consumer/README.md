# Auth Consumer (Node)

Three authentication modes for consumer clients. Each script
demonstrates a different way to acquire and refresh tokens.

**Category:** Consumer -- authentication

## Prerequisites

- Node.js 22+
- `BLOCKS_API_KEY` for api-key and custom-provider modes
- A running echo agent on the same keyset

## Authentication modes

### Mode 1: API key (`api-key.ts`)

Server-side authentication. The SDK exchanges the API key for a
consumer JWT and refreshes it transparently.

```bash
BLOCKS_API_KEY=bk_... npx tsx api-key.ts
```

Best for: backend services, scripts, cron jobs.

### Mode 2: Token endpoint (`token-endpoint.ts`)

Client-side authentication via a customer-owned backend proxy. The
SDK POSTs an empty JSON body `{}` to the endpoint URL. The proxy
identifies the caller (via cookies, session, etc.), adds the Blocks
API key, calls the Blocks backend, and returns `{ token, expiresIn }`.

```bash
BLOCKS_TOKEN_ENDPOINT=https://your-backend.example.com/api/blocks-token npx tsx token-endpoint.ts
```

**Deployment shapes.** `tokenEndpoint` works for any
session-authenticated backend endpoint that mints a consumer JWT:
either a **customer-owned proxy** (what this example demonstrates —
developer hosts it and it holds the Blocks API key) or the
**dashboard-embedder pattern** (the Blocks backend's own
`/api/v1/auth/agent/consumer-token` endpoint, called from a signed-in
browser dashboard with a session cookie). Custom apps usually use the
customer-owned proxy shape shown here.

Best for: browser apps, mobile apps, Electron -- environments where
the API key must not be bundled.

### Mode 3: Custom provider (`custom-provider.ts`)

Maximum flexibility. The developer provides an arbitrary async
function that returns `{ token, expiresIn }`. The SDK calls it on
init and before each token expiry.

```bash
BLOCKS_API_KEY=bk_... npx tsx custom-provider.ts
```

Best for: OAuth2 flows, custom SSO, multi-tenant routing, non-standard
proxy endpoints requiring custom headers or credentials.

## Environment variables

| Variable                | Required by              | Description                 |
| ----------------------- | ------------------------ | --------------------------- |
| `BLOCKS_API_KEY`        | api-key, custom-provider | Blocks API key              |
| `BLOCKS_TOKEN_ENDPOINT` | token-endpoint           | Customer proxy endpoint URL |
| `BLOCKS_CDM_URL`        | all (optional)           | CDM config URL              |

## SDK concepts demonstrated

- `TaskClient.create()` with `apiKey` (Mode 1)
- `TaskClient.create()` with `tokenEndpoint` (Mode 2)
- `TaskClient.create()` with `tokenProvider` (Mode 3)
- `onAuthError` callback for permanent refresh failures
- Transparent token refresh across all modes
