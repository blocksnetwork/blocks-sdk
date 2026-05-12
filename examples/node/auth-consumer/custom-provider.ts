/**
 * Auth Consumer -- custom token provider mode.
 *
 * Demonstrates maximum-flexibility authentication using a custom async
 * function. The developer provides an arbitrary function that returns
 * a fresh token. The SDK calls it on init and before each token expiry.
 *
 * This mode covers any auth architecture: OAuth2, custom SSO,
 * multi-tenant routing, or wrapping a non-standard proxy endpoint
 * that requires custom headers or credentials.
 *
 * Usage:
 *   npx tsx custom-provider.ts
 *
 * Environment variables:
 *   BLOCKS_API_KEY  -- Blocks API key (used by the custom provider function)
 *   BLOCKS_CDM_URL  -- CDM config URL (optional)
 */

import 'dotenv/config';
import { TaskClient } from '@blocks-network/sdk';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.");
  process.exit(1);
}

const ownerId = `auth-custom-${Date.now()}`;

async function main() {
  // Mode 3: Custom provider -- you control the entire token acquisition.
  // This example wraps a hypothetical proxy endpoint with custom headers.
  const client = await TaskClient.create({
    billingMode: 'free',
    tokenProvider: async () => {
      // In a real application, this function would call your own
      // auth service, OAuth2 provider, or custom proxy endpoint.
      // Here we demonstrate the pattern by calling the Blocks
      // consumer-token endpoint directly (in production, this
      // would be behind your own proxy).
      const backendUrl = process.env.BLOCKS_BACKEND_URL || 'https://api.blocksnetwork.io';
      const resp = await fetch(`${backendUrl}/api/v1/auth/agent/consumer-token`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Custom-Header': 'custom-value',
        },
        body: JSON.stringify({ apiKey }),
      });

      if (!resp.ok) {
        throw new Error(`Token acquisition failed: HTTP ${resp.status}`);
      }

      const data = await resp.json() as {
        accessToken: string;
        expiresIn: number;
        userId: string;
      };

      return {
        token: data.accessToken,
        expiresIn: data.expiresIn,
        userId: data.userId,
      };
    },
    onAuthError: (err) => {
      console.error('Auth refresh failed permanently:', err.message);
    },
  });

  console.log('Authenticated with custom provider. Sending task...');

  const session = await client.sendMessage({
    agentName: 'echo',
    ownerId,
    requestParts: [{ partId: 'text', text: 'Hello from custom auth provider!' }],
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
