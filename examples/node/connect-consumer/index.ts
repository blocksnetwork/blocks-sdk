/**
 * Connect Consumer -- connect to an existing task and inspect its state.
 *
 * Demonstrates reconnecting to a task that is already in progress or
 * has completed:
 *   1. Create a TaskClient with API key authentication
 *   2. Connect to an existing task by taskId via connect()
 *   3. For terminal tasks: list streams, list artifacts, download results
 *   4. For active tasks: receive live events through callbacks
 *
 * Usage:
 *   npx tsx index.ts <taskId>
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

const taskId = process.argv[2];
if (!taskId) {
  console.error('Usage: npx tsx index.ts <taskId>');
  process.exit(1);
}

async function main() {
  const client = await TaskClient.create({
    billingMode: 'free',
    apiKey,
  });

  console.log(`Connecting to task: ${taskId}`);

  const session = await client.connect({ taskId });

  console.log(`Connected. State: ${session.state}`);

  // List streams discovered from history
  const streams = session.listStreams();
  console.log(`Streams found: ${streams.length}`);
  for (const ref of streams) {
    console.log(`  Stream: ${ref.descriptor.streamId} (${ref.descriptor.localDirection})`);
  }

  // List artifacts discovered from history
  const artifacts = session.listArtifacts();
  console.log(`Artifacts found: ${artifacts.length}`);

  for (const ref of artifacts) {
    try {
      const downloaded = await session.downloadArtifact(ref);
      const text = new TextDecoder().decode(downloaded.data);
      console.log(`  Artifact (${downloaded.mimeType}): ${text.slice(0, 200)}${text.length > 200 ? '...' : ''}`);
    } catch (err) {
      console.error(`  Download failed:`, err);
    }
  }

  // For active tasks, register live event callbacks
  if (!session.isClosed) {
    console.log('\nTask is active. Listening for live events...');

    session.onProgress((event) => {
      console.log('Progress:', event);
    });

    session.onArtifact((event) => {
      console.log('Artifact:', event);
    });

    session.onStream(async (streamRef) => {
      const { streamId, format } = streamRef.descriptor;
      console.log(`Live stream: ${streamId} (format=${format})`);
      const stream = streamRef.open();

      // Generic consumer: branch on the descriptor's format so we
      // exercise the right decoded iterator for each stream shape.
      // `bytes()` yields decoded `Uint8Array` per chunk; `events<T>()`
      // yields one event per yield. Iterating `.inbound` directly would
      // give raw wire envelopes (`{ data: string[], encoding, seq, ts }`)
      // — only reach for it if you need that metadata.
      if (format === 'bytes') {
        let total = 0;
        for await (const chunk of stream.bytes()) {
          total += chunk.byteLength;
          process.stdout.write(`[stream:bytes] +${chunk.byteLength}B (total ${total}B)\n`);
        }
      } else {
        for await (const ev of stream.events()) {
          const text = typeof ev === 'string' ? ev : JSON.stringify(ev);
          process.stdout.write(`[stream:events] ${text}\n`);
        }
      }
    });

    await new Promise<void>((resolve) => {
      session.onTerminal((event) => {
        console.log('Terminal:', event);
        resolve();
      });
    });
  }

  session.close();
  client.destroy();
  console.log('Done.');
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
