/**
 * Stream Consumer -- submit a pipe task, discover and consume a stream.
 *
 * Demonstrates the pipe-task streaming consumer flow:
 *   1. Create a TaskClient with API key authentication
 *   2. Send a pipe task via sendMessage() with taskKind and duration
 *   3. Discover streams via onStream()
 *   4. Open the stream and iterate over decoded events via events<T>()
 *      (use bytes() for format: bytes streams)
 *   5. Handle terminal event and clean up
 *
 * Usage:
 *   npx tsx index.ts
 *
 * Environment variables:
 *   BLOCKS_API_KEY  -- Blocks API key (required)
 *   BLOCKS_CDM_URL  -- CDM config URL (optional, defaults to production CDN)
 */

import 'dotenv/config';
import { TaskClient } from '@blocks-network/sdk';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error("BLOCKS_API_KEY not set. Run 'blocks publish' or 'blocks login --write-env' first.");
  process.exit(1);
}

const ownerId = `stream-consumer-${Date.now()}`;

async function main() {
  const client = await TaskClient.create({
    billingMode: 'free',
    apiKey,
  });

  console.log('Sending pipe task to echo_stream agent...');

  const session = await client.sendMessage({
    agentName: 'echo_stream',
    ownerId,
    taskKind: 'pipe',
    duration: 1,
    requestParts: [{ partId: 'text', text: 'Stream this text back to me.' }],
  });

  console.log(`Task created: ${session.taskId}`);

  // Register terminal handler before awaiting the stream to avoid
  // missing events if the agent completes quickly.
  const done = new Promise<void>((resolve) => {
    session.onTerminal((event) => {
      console.log('Terminal:', event);
      client.destroy();
      resolve();
    });
  });

  session.onStream(async (streamRef) => {
    console.log(`Stream discovered: ${streamRef.descriptor.streamId} (${streamRef.descriptor.localDirection})`);

    const stream = streamRef.open();

    // echo_stream declares `format: events`; `events<T>()` flattens
    // batched event arrays into a single yield per event. Use `bytes()`
    // for `format: bytes` streams.
    let chunkCount = 0;
    for await (const ev of stream.events<{ text?: string } | string>()) {
      chunkCount++;
      const text =
        typeof ev === 'string'
          ? ev
          : typeof ev?.text === 'string'
            ? ev.text
            : JSON.stringify(ev);
      process.stdout.write(`[chunk ${chunkCount}] ${text}\n`);
    }

    console.log('--- Stream ended ---');
  });

  await done;
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
