/**
 * Request Consumer -- submit a request task, wait for terminal, download artifacts.
 *
 * Demonstrates the simplest consumer flow:
 *   1. Create a TaskClient with API key authentication
 *   2. Send a request task via sendMessage()
 *   3. Listen for artifacts and terminal events
 *   4. List and download artifacts from the completed task
 *   5. Clean up with session.close() and client.destroy()
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
  console.error("BLOCKS_API_KEY not set. Run 'blocks login --write-env' first.");
  process.exit(1);
}

const ownerId = `request-consumer-${Date.now()}`;

async function main() {
  const client = await TaskClient.create({
    billingMode: 'free',
    apiKey,
  });

  console.log('Sending request task to echo agent...');

  const session = await client.sendMessage({
    agentName: 'echo',
    ownerId,
    requestParts: [{ partId: 'text', text: 'Hello from the request consumer!' }],
  });

  console.log(`Task created: ${session.taskId}`);

  session.onProgress((event) => {
    console.log('Progress:', event);
  });

  session.onArtifact((event) => {
    console.log('Artifact event:', event);
  });

  const done = new Promise<void>((resolve) => {
    session.onTerminal(async (event) => {
      console.log('Terminal:', event);

      const artifacts = session.listArtifacts();
      console.log(`Artifacts found: ${artifacts.length}`);

      for (const ref of artifacts) {
        try {
          const downloaded = await session.downloadArtifact(ref);
          const text = new TextDecoder().decode(downloaded.data);
          console.log(`Downloaded artifact (${downloaded.mimeType}): ${text}`);
        } catch (err) {
          console.error('Failed to download artifact:', err);
        }
      }

      session.close();
      client.destroy();
      resolve();
    });
  });

  await done;
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
