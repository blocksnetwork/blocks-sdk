/**
 * File Consumer -- submit a task with file input, receive and download artifacts.
 *
 * Demonstrates file exchange between consumer and agent:
 *   1. Create a TaskClient with API key authentication
 *   2. Send a task with a file attachment in requestParts
 *   3. Listen for artifact events as the agent produces output
 *   4. Download artifacts (both inline and file-based)
 *   5. List all artifacts from the session
 *
 * Usage:
 *   npx tsx index.ts
 *
 * Environment variables:
 *   BLOCKS_API_KEY  -- Blocks API key (required)
 *   BLOCKS_CDM_URL  -- CDM config URL (optional, defaults to production CDN)
 */

import 'dotenv/config';
import { readFileSync, existsSync } from 'node:fs';
import { basename } from 'node:path';
import { TaskClient } from '@blocks-network/sdk';
import type { SendMessageRequestPart } from '@blocks-network/sdk';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error("BLOCKS_API_KEY not set. Run 'blocks publish' or 'blocks login --write-env' first.");
  process.exit(1);
}

const ownerId = `file-consumer-${Date.now()}`;

async function main() {
  const client = await TaskClient.create({
    billingMode: 'free',
    apiKey,
  });

  // Build request parts: a text part and optionally a file part.
  // If a file path is provided as a CLI argument, attach it.
  const parts: SendMessageRequestPart[] = [
    { partId: 'text', text: 'Process this file and return results.' },
  ];

  const filePath = process.argv[2];
  if (filePath && existsSync(filePath)) {
    const fileData = readFileSync(filePath);
    const fileName = basename(filePath);
    console.log(`Attaching file: ${fileName} (${fileData.length} bytes)`);
    parts.push({
      partId: 'input_file',
      file: fileData,
      fileName,
      contentType: 'application/octet-stream',
    });
  } else {
    // Use a small inline sample when no file is provided
    const sampleData = Buffer.from('Sample file content for the agent to process.');
    parts.push({
      partId: 'input_file',
      file: sampleData,
      fileName: 'sample.txt',
      contentType: 'text/plain',
    });
  }

  console.log('Sending task with file attachment to echo agent...');

  const session = await client.sendMessage({
    agentName: 'echo',
    ownerId,
    requestParts: parts,
  });

  console.log(`Task created: ${session.taskId}`);

  session.onArtifact((event) => {
    console.log('Artifact event received:', JSON.stringify(event, null, 2));
  });

  const done = new Promise<void>((resolve) => {
    session.onTerminal(async (event) => {
      console.log('Terminal:', event);

      const artifacts = session.listArtifacts();
      console.log(`\nTotal artifacts: ${artifacts.length}`);

      for (const ref of artifacts) {
        console.log(`\nArtifact: kind=${ref.kind}, mimeType=${ref.mimeType ?? 'unknown'}`);

        try {
          const downloaded = await session.downloadArtifact(ref);
          console.log(`  Downloaded: ${downloaded.mimeType}, ${downloaded.data.length} bytes`);
          if (downloaded.fileName) {
            console.log(`  File name: ${downloaded.fileName}`);
          }
          // Print text content for text artifacts
          if (downloaded.mimeType?.startsWith('text/')) {
            const text = new TextDecoder().decode(downloaded.data);
            console.log(`  Content: ${text.slice(0, 200)}${text.length > 200 ? '...' : ''}`);
          }
        } catch (err) {
          console.error(`  Download failed:`, err);
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
