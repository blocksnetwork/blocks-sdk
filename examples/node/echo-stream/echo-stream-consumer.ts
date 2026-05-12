/**
 * Echo-Stream Consumer — submits a request task to the echo-stream agent,
 * reads streamed chunks in real time, then prints the final artifact.
 *
 * Usage:
 *   npx tsx echo-stream-consumer.ts
 *   npx tsx echo-stream-consumer.ts "custom text to echo"
 *
 * Authentication:
 *   BLOCKS_API_KEY   API key (bk_...) — from `blocks login --write-env`.
 *   BLOCKS_CDM_URL   CDM config URL (optional — falls back to default CDN).
 */

import 'dotenv/config';
import { TaskClient, fetchCdmConfig, getAgent } from '@blocks-network/sdk';

const AGENT_NAME = 'echo_stream';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error('BLOCKS_API_KEY not set. Run `blocks login --write-env` first.');
  process.exit(1);
}

// Resolve baseUrl from BLOCKS_* env or CDM so getAgent() knows where to look.
const cdmUrl = process.env.BLOCKS_CDM_URL;
const cdm = await fetchCdmConfig(cdmUrl);
const baseUrl = process.env.BLOCKS_BACKEND_URL ?? cdm.api.baseUrl;

// Read the agent's registered billingMode so we pick the matching keyset.
// free → playground, paid → network. Mismatch = PubNub messages on different
// keys and the consumer silently hangs waiting on streams.
const entry = await getAgent(AGENT_NAME, { baseUrl });
if (!entry) {
  console.error(`Agent "${AGENT_NAME}" not found in the registry at ${baseUrl}.`);
  process.exit(1);
}
const billingMode = entry.billingMode ?? 'free';
console.log(`Registry says ${AGENT_NAME} is billingMode=${billingMode}; using ${billingMode === 'paid' ? 'network' : 'playground'} keyset.`);

const client = await TaskClient.create({
  billingMode,
  apiKey,
  cdmUrl,
  baseUrl,
});

async function main() {
  const inputText = process.argv[2] || 'Hello from the echo-stream consumer!\nThis is line two.\nAnd line three.';

  console.log(`Sending to echo-stream agent: "${inputText}"`);
  console.log('---');

  const session = await client.sendMessage({
    agentName: AGENT_NAME,
    requestParts: [{ partId: 'text', text: inputText }],
  });

  console.log(`Task created: ${session.taskId}`);

  const done = new Promise<void>((resolve) => {
    session.onArtifact((event) => {
      console.log('Artifact:', event);
    });

    session.onTerminal((event) => {
      console.log('Terminal:', event);
      client.destroy();
      resolve();
    });
  });

  session.onStream(async (streamRef) => {
    console.log(`Stream discovered: ${streamRef.descriptor.streamId} (${streamRef.descriptor.localDirection})`);

    const stream = streamRef.open();

    // The echo-stream agent declares the default stream as `format: events`
    // and writes one `{ text }` event per chunk. `events<T>()` flattens
    // batched event arrays into a single yield per event.
    let chunkCount = 0;
    for await (const ev of stream.events<{ text?: string } | string>()) {
      chunkCount++;
      const text =
        typeof ev === 'string'
          ? ev
          : typeof ev?.text === 'string'
            ? ev.text
            : JSON.stringify(ev);
      process.stdout.write(`[chunk ${chunkCount}] ${text}`);
    }

    console.log('\n--- Stream ended ---');
  });

  await done;
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
