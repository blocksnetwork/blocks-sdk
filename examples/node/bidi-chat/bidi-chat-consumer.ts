/**
 * bidi-chat consumer (Node).
 *
 * Submits a request to the bidi-chat agent, opens the discovered
 * bidirectional stream, sends a few lines, reads the agent's
 * uppercased echoes, then ends the conversation with "bye".
 *
 * This script exercises the consumer-side stream UUID fix from
 * https://github.com/pubnub/blocksnetwork/pull/835: when consumer
 * and provider share the same agent name on their first stream,
 * the consumer-side `StreamClient` must derive its publisher UUID
 * from the consumer's user id, not the provider's agent name, so
 * the self-echo filter correctly distinguishes both sides.
 *
 * Usage:
 *   BLOCKS_BACKEND_URL=http://localhost:3031 \
 *   BLOCKS_API_KEY=bk_... \
 *     npx tsx bidi-chat-consumer.ts [line1] [line2] ...
 */

import 'dotenv/config';
import { TaskClient, fetchCdmConfig, getAgent } from '@blocks-network/sdk';

const AGENT_NAME = 'bidi_chat_node';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error('BLOCKS_API_KEY not set. Run `blocks login --write-env` first.');
  process.exit(1);
}

const cdmUrl = process.env.BLOCKS_CDM_URL;
const baseUrl = process.env.BLOCKS_BACKEND_URL ?? (await fetchCdmConfig(cdmUrl)).api.baseUrl;

const entry = await getAgent(AGENT_NAME, { baseUrl });
if (!entry) {
  console.error(`Agent "${AGENT_NAME}" not found at ${baseUrl}.`);
  process.exit(1);
}
const billingMode = entry.billingMode ?? 'free';
console.log(
  `Registry says ${AGENT_NAME} is billingMode=${billingMode}; using ${
    billingMode === 'paid' ? 'network' : 'playground'
  } keyset.`,
);

const client = await TaskClient.create({
  billingMode,
  apiKey,
  cdmUrl,
  baseUrl,
});

async function main() {
  const lines = process.argv.slice(2);
  const messagesToSend = lines.length > 0 ? lines : ['ping', 'hello there', 'how are you'];

  console.log(`Sending pipe task to ${AGENT_NAME}...`);
  const session = await client.sendMessage({
    agentName: AGENT_NAME,
    taskKind: 'pipe',
    duration: 1,
    requestParts: [{ partId: 'greeting', text: 'opening greeting' }],
  });
  console.log(`Task created: ${session.taskId}`);

  const streamRefPromise = new Promise<Awaited<ReturnType<typeof session.waitForStream>>>(
    (resolve, reject) => {
      const timeout = setTimeout(
        () => reject(new Error('Timed out waiting for stream after 30s')),
        30_000,
      );
      session.onStream((ref) => {
        clearTimeout(timeout);
        resolve(ref);
      });
    },
  );

  const streamRef = await streamRefPromise;
  console.log(
    `Stream discovered: ${streamRef.descriptor.streamId} (${streamRef.descriptor.localDirection})`,
  );

  const stream = streamRef.open();

  // Track uuid + channel so we can prove the self-echo filter is in
  // effect (i.e. consumer reads agent messages and not its own).
  console.log(`Consumer stream uuid: ${stream.uuid}`);
  console.log(`Stream channel:        ${stream.channel}`);

  const received: string[] = [];

  // Read loop. Per SDK_CONTRACT §8.4 bidirectional streams do NOT
  // publish a stream_end marker, so we must break out ourselves once
  // we've seen the agent's reply to our final "bye" line. Calling
  // `stream.end()` on the consumer side signals our inbound iterator
  // to complete (see stream-client.ts: inboundDone resolution).
  const readDone = (async () => {
    for await (const ev of stream.events<{ text?: string } | string>()) {
      const text =
        typeof ev === 'string' ? ev : typeof ev?.text === 'string' ? ev.text : JSON.stringify(ev);
      received.push(text);
      console.log(`[from agent] ${text}`);
      if (/\bBYE\b/.test(text)) {
        await stream.end();
        break;
      }
    }
    console.log('--- inbound iterator closed ---');
  })();

  // Write loop: send each line, wait briefly so logs interleave readably.
  for (const line of messagesToSend) {
    console.log(`[to agent ] ${line}`);
    stream.write({ text: line });
    await sleep(300);
  }

  console.log(`[to agent ] bye`);
  stream.write({ text: 'bye' });

  await readDone;

  // Pipe tasks do not auto-terminal when the handler returns; the
  // session only enters a terminal state when the duration window
  // expires, so we don't block on `onTerminal()` here. We do wait
  // briefly to surface any artifact the handler published.
  await Promise.race([
    new Promise<void>((resolve) => {
      session.onArtifact((event) => {
        const data = (event as { data?: unknown }).data;
        console.log('Artifact:', typeof data === 'string' ? data : JSON.stringify(event));
        resolve();
      });
    }),
    sleep(2000),
  ]);

  console.log(`\nReceived ${received.length} message(s) from agent.`);
  if (received.length === 0) {
    console.error(
      'FAIL: bidirectional read returned 0 messages — likely consumer/provider UUID collision.',
    );
    client.destroy();
    process.exit(2);
  }

  client.destroy();
  console.log('Done.');
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
