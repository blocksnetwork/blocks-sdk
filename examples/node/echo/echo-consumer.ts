/**
 * Echo Consumer — submits a request task to the echo agent and prints the result.
 *
 * Usage:
 *   npx tsx echo-consumer.ts
 *
 * Reads CDM config from BLOCKS_CDM_URL (or defaults to production CDN).
 */

import 'dotenv/config';
import PubNub from 'pubnub';
import { fetchCdmConfig, TaskClient } from '@blocks-network/sdk';

const authToken = process.env.BLOCKS_TOKEN;
if (!authToken) {
  console.error('BLOCKS_TOKEN not set. Run "blocks login" first.');
  process.exit(1);
}

const cdmConfig = await fetchCdmConfig();
const { publishKey, subscribeKey } = cdmConfig.playground;
const baseUrl = cdmConfig.api.baseUrl;
const ownerId = `echo-consumer-${Date.now()}`;

const client = new TaskClient({
  subscribeKey,
  publishKey,
  baseUrl,
  authToken,
  createSessionPubNub: () =>
    new PubNub({ subscribeKey, publishKey, userId: ownerId, enableEventEngine: true }),
});

async function main() {
  console.log('Sending request task to echo agent...');

  const session = await client.sendMessage({
    agentName: 'echo',
    ownerId,
    requestParts: [{ partId: 'text', text: 'Hello from the echo consumer!' }],
  });

  console.log(`Task created: ${session.taskId}`);

  session.onProgress((event) => {
    console.log('Progress:', event);
  });

  session.onArtifact((event) => {
    console.log('Artifact:', event);
  });

  session.onTerminal((event) => {
    console.log('Terminal:', event);
    client.destroy();
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
