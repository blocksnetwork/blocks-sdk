# Auth Consumer (Python)

Two authentication modes for consumer clients, plus documentation of
the token endpoint concept. Each script demonstrates a different way
to acquire and refresh tokens.

**Category:** Consumer -- authentication

## Prerequisites

- Python 3.10+
- `BLOCKS_API_KEY` for api-key and custom-provider modes
- A running echo agent on the same keyset
- Optional: `python-dotenv` if you want `.env` file loading

## Install

```bash
cd examples/python/auth-consumer
pip install -e ../../sdks/python
# Optional, only if you want `.env` support:
pip install python-dotenv
```

## Authentication modes

### Mode 1: API key (`api_key.py`)

Server-side authentication. The SDK exchanges the API key for a
consumer JWT and refreshes it transparently.

```bash
BLOCKS_API_KEY=bk_... python api_key.py
```

Best for: backend services, scripts, cron jobs.

### Mode 2: Token endpoint (concept)

The Python SDK supports `token_endpoint` as a construction parameter.
The SDK POSTs an empty JSON body `{}` to the endpoint URL. The
developer's backend proxy identifies the caller, adds the Blocks API
key, calls the Blocks backend, and returns `{ token, expiresIn }`.

```python
client = TaskClient.create(
    billing_mode="free",
    token_endpoint="https://your-backend.example.com/api/blocks-token",
)
```

**Deployment shapes.** `token_endpoint` works for any
session-authenticated backend that mints a consumer JWT: either a
**customer-owned proxy** (what this example demonstrates) or a
**dashboard-embedder** path that points directly at the Blocks
backend's `/api/v1/auth/agent/consumer-token` endpoint with a
session cookie. The dashboard-embedder shape is browser-only because
it relies on `fetch` cookies. In Python server-side code, Mode 1
(API key) or Mode 3 (custom provider) are usually more natural than
either `token_endpoint` shape.

### Mode 3: Custom provider (`custom_provider.py`)

Maximum flexibility. The developer provides an arbitrary function that
returns a `TokenResult`. The SDK calls it on init and before each
token expiry.

```bash
BLOCKS_API_KEY=bk_... python custom_provider.py
```

Best for: OAuth2 flows, custom SSO, multi-tenant routing, non-standard
proxy endpoints requiring custom headers or credentials.

## Environment variables

| Variable         | Required by              | Description    |
| ---------------- | ------------------------ | -------------- |
| `BLOCKS_API_KEY` | api_key, custom_provider | Blocks API key |
| `BLOCKS_CDM_URL` | all (optional)           | CDM config URL |

## SDK concepts demonstrated

- `TaskClient.create()` with `api_key` (Mode 1)
- `TaskClient.create()` with `token_provider` (Mode 3)
- `on_auth_error` callback for permanent refresh failures
- Transparent token refresh across all modes
- `TokenResult` dataclass for custom providers
