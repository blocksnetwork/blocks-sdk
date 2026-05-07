/**
 * Auth Consumer -- API key mode.
 *
 * Demonstrates server-side consumer authentication using an API key.
 * The SDK exchanges the API key for a consumer JWT via the Blocks
 * backend and manages token refresh transparently.
 *
 * This is the recommended mode for backend services, scripts, and
 * cron jobs where the API key can be stored securely.
 *
 * Usage:
 *   npx tsx api-key.ts
 *
 * Environment variables:
 *   BLOCKS_API_KEY  -- Blocks API key (required)
 *   BLOCKS_CDM_URL  -- CDM config URL (optional)
 */

import 'dotenv/config';
import { TaskClient } from '@blocks-network/sdk';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error("BLOCKS_API_KEY not set. Run 'blocks publish' or 'blocks login --write-env' first.");
  process.exit(1);
}

const ownerId = `auth-api-key-${Date.now()}`;

async function main() {
  // Mode 1: API key -- the SDK handles JWT acquisition and refresh.
  const client = await TaskClient.create({
    billingMode: 'free',
    apiKey,
    onAuthError: (err) => {
      console.error('Auth refresh failed permanently:', err.message);
    },
  });

  console.log('Authenticated with API key. Sending task...');

  const session = await client.sendMessage({
    agentName: 'echo',
    ownerId,
    requestParts: [{ partId: 'text', text: 'Hello from API key auth!' }],
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
