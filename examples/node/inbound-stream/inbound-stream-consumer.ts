/**
 * Inbound stream consumer: opens a pipe task on inbound_stream_node, writes
 * bytes and events to the agent's two inbound streams, and prints its report.
 *
 * Usage:
 *   npx tsx inbound-stream-consumer.ts
 */

import 'dotenv/config';
import { createHash, randomBytes } from 'node:crypto';
import { type StreamRef, TaskClient } from '@blocks-network/sdk';

const AGENT_NAME = 'inbound_stream_node';
const DURATION_MIN = 2;
// Writes published before the agent's subscription is live are lost, so wait briefly.
const START_DELAY_MS = 2_000;

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.");
  process.exit(1);
}

const client = await TaskClient.create({ billingMode: 'free', apiKey });
const session = await client.sendMessage({
  agentName: AGENT_NAME,
  taskKind: 'pipe',
  duration: DURATION_MIN,
  requestParts: [{ partId: 'request', data: {} }],
});
console.log(`Task created: ${session.taskId}`);

session.onStream(async (ref: StreamRef) => {
  const stream = ref.open();
  await new Promise((resolve) => setTimeout(resolve, START_DELAY_MS));
  if (ref.descriptor.declaredStream === 'upload_bytes') {
    const payload = randomBytes(64 * 1024);
    stream.write(payload);
    const sha256 = createHash('sha256').update(payload).digest('hex');
    console.log(`upload_bytes: sent ${payload.byteLength} bytes, sha256 ${sha256}`);
  } else {
    for (const text of ['first', 'second', 'third']) stream.write({ text });
    console.log('upload_events: sent 3 events');
  }
  await stream.end();
});

session.onTerminal(async (event) => {
  for (const ref of session.listArtifacts()) {
    const { data } = await session.downloadArtifact(ref);
    console.log(`\nReport: ${new TextDecoder().decode(data)}`);
  }
  console.log(`Terminal: ${event.state}`);
  client.destroy();
});
