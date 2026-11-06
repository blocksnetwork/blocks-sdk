/**
 * Session Consumer -- opens one session with the session_echo agent and
 * sends several messages over it.
 *
 * Demonstrates the session consumer flow:
 *   1. Create a BlocksClient with API key authentication
 *   2. Open a session with openSession()
 *   3. Send each message with send() and await its outcome
 *   4. Clean up with session.close() and client.destroy()
 *
 * Usage:
 *   npx tsx session-consumer.ts
 *
 * Environment variables:
 *   BLOCKS_API_KEY  -- Blocks API key (required)
 *   BLOCKS_CDM_URL  -- CDM config URL (optional, defaults to production CDN)
 */

import 'dotenv/config';
import {
  BlocksClient,
  SessionNoRouteError,
  SessionsDisabledError,
  decodeInlineArtifact,
} from '@blocks-network/sdk';

const AGENT_NAME = 'session_echo';
const OUTCOME_TIMEOUT_MS = 30_000;
const MESSAGES = ['Hello', 'Is anyone there?', 'Goodbye'];

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.");
  process.exit(1);
}

async function main() {
  const client = await BlocksClient.create({ billingMode: 'free', apiKey });
  try {
    const session = await client.openSession(AGENT_NAME);
    console.log(`Session ${session.sessionId} is ${session.state}`);
    session.onStateChange((state) => console.log(`Session state: ${state}`));

    try {
      for (const message of MESSAGES) {
        const exchange = session.send(message);
        exchange.onProgress((progress) => console.log('Progress:', progress.message));
        const outcome = await exchange.waitForOutcome(OUTCOME_TIMEOUT_MS);
        if (outcome.state !== 'completed') {
          console.error(`Exchange ${outcome.state}: ${outcome.error ?? 'no error given'}`);
          continue;
        }
        for (const ref of outcome.artifacts) {
          console.log(new TextDecoder().decode(decodeInlineArtifact(ref)));
        }
      }
    } finally {
      await session.close();
    }
  } finally {
    client.destroy();
  }
}

main().catch((err) => {
  if (err instanceof SessionsDisabledError) {
    console.error('Sessions are not enabled for your organization yet.');
  } else if (err instanceof SessionNoRouteError) {
    console.error(`No running ${AGENT_NAME} instance is serving sessions. Start it with 'npm start'.`);
  } else {
    console.error(err);
  }
  process.exit(1);
});
