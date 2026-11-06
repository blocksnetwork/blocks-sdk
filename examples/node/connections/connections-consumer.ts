/**
 * Connections consumer: sends one task to connections_node and prints the
 * per-integration result.
 *
 * Usage:
 *   npx tsx connections-consumer.ts
 */

import 'dotenv/config';
import { TaskClient } from '@blocks-network/sdk';

const AGENT_NAME = 'connections_node';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.");
  process.exit(1);
}

const client = await TaskClient.create({ billingMode: 'free', apiKey });
const session = await client.sendMessage({
  agentName: AGENT_NAME,
  requestParts: [{ partId: 'request', data: {} }],
});
console.log(`Task created: ${session.taskId}`);

session.onProgress((event) => console.log('Progress:', event.message ?? event));
session.onTerminal(async (event) => {
  for (const ref of session.listArtifacts()) {
    const { data } = await session.downloadArtifact(ref);
    console.log(`\n${new TextDecoder().decode(data)}`);
  }
  console.log(`\nTerminal: ${event.state}`);
  client.destroy();
});
