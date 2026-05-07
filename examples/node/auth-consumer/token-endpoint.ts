/**
 * Auth Consumer -- token endpoint mode.
 *
 * Demonstrates client-side consumer authentication using a
 * customer-owned backend proxy endpoint. The SDK POSTs an empty JSON
 * body to this endpoint whenever it needs a token. The developer's
 * server-side handler identifies the caller (via cookies, session
 * tokens, etc.), adds the Blocks API key, calls the Blocks backend,
 * and returns { token, expiresIn, userId? }.
 *
 * The API key never appears in client-side code. For advanced cases
 * (custom headers, credentials, OAuth2), use the custom-provider mode.
 *
 * DEPLOYMENT SHAPES: `tokenEndpoint` works for any session-
 * authenticated backend endpoint that mints a consumer JWT — this
 * example shows the customer-owned proxy shape, where the developer
 * hosts the proxy and it holds the Blocks API key. The in-tree
 * `afui_mvp` dashboard uses the other first-class shape (points
 * directly at the Blocks backend's `/api/v1/auth/agent/consumer-token`
 * with a browser session cookie); see `dev_docs/SDK_CONTRACT.md`
 * §8.6.4g for that wiring.
 *
 * Usage:
 *   npx tsx token-endpoint.ts
 *
 * Environment variables:
 *   BLOCKS_TOKEN_ENDPOINT  -- URL of the customer proxy endpoint (required)
 *   BLOCKS_CDM_URL         -- CDM config URL (optional)
 */

import 'dotenv/config';
import { TaskClient } from '@blocks-network/sdk';

const tokenEndpoint = process.env.BLOCKS_TOKEN_ENDPOINT;
if (!tokenEndpoint) {
  console.error(
    'BLOCKS_TOKEN_ENDPOINT not set. This should be the URL of your ' +
    'backend proxy that wraps the Blocks consumer-token endpoint.',
  );
  process.exit(1);
}

const ownerId = `auth-endpoint-${Date.now()}`;

async function main() {
  // Mode 2: Token endpoint -- the SDK POSTs to this URL with an empty
  // JSON body. Your backend proxy adds the API key and returns a JWT.
  const client = await TaskClient.create({
    billingMode: 'free',
    tokenEndpoint,
    onAuthError: (err) => {
      console.error('Auth refresh failed permanently:', err.message);
    },
  });

  console.log('Authenticated via token endpoint. Sending task...');

  const session = await client.sendMessage({
    agentName: 'echo',
    ownerId,
    requestParts: [{ partId: 'text', text: 'Hello from token endpoint auth!' }],
  });

  console.log(`Task created: ${session.taskId}`);

  session.onTerminal((event) => {
    console.log('Terminal:', event);
    session.close();
    client.destroy();
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
