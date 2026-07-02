/**
 * advanced-stream consumer — submits a pipe task, then reads all three
 * declared streams concurrently, branching on each stream's declared name
 * and wire format.
 *
 * Usage:
 *   npx tsx advanced-stream-consumer.ts
 *   npx tsx advanced-stream-consumer.ts 10        # request 10 ticks
 *
 * Authentication:
 *   BLOCKS_API_KEY   API key (bk_...) — from `blocks login --write-env`.
 *   BLOCKS_CDM_URL   CDM config URL (optional — falls back to default CDN).
 */

import 'dotenv/config';
import { TaskClient, fetchCdmConfig, getAgent, type StreamRef } from '@blocks-network/sdk';

const AGENT_NAME = 'advanced_stream';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error('BLOCKS_API_KEY not set. Run `blocks login --write-env` first.');
  process.exit(1);
}

async function main() {
  const ticks = Number.parseInt(process.argv[2] ?? '', 10);
  const requestParts = Number.isFinite(ticks)
    ? [{ partId: 'params', text: JSON.stringify({ ticks }) }]
    : [];

  // Resolve baseUrl from BLOCKS_* env or CDM so getAgent() knows where to look.
  const cdmUrl = process.env.BLOCKS_CDM_URL;
  const cdm = await fetchCdmConfig(cdmUrl);
  const baseUrl = process.env.BLOCKS_BACKEND_URL ?? cdm.api.baseUrl;

  // Read the agent's registered billingMode so we pick the matching keyset.
  // free → playground, paid → network. A mismatch puts PubNub messages on
  // different keys and the consumer silently hangs waiting on streams.
  // Pass apiKey so a privately-registered (not-yet-published) agent resolves —
  // the registry returns 404 for private agents on an unauthenticated lookup.
  const entry = await getAgent(AGENT_NAME, { baseUrl, apiKey });
  if (!entry) {
    console.error(`Agent "${AGENT_NAME}" not found in the registry at ${baseUrl}.`);
    process.exit(1);
  }
  const billingMode = entry.billingMode ?? 'free';
  console.log(`Registry says ${AGENT_NAME} is billingMode=${billingMode}; using ${billingMode === 'paid' ? 'network' : 'playground'} keyset.`);

  const client = await TaskClient.create({ billingMode, apiKey, cdmUrl, baseUrl });

  console.log(`Submitting pipe task to ${AGENT_NAME}${requestParts.length ? ` (ticks=${ticks})` : ''}...`);
  const session = await client.sendMessage({
    agentName: AGENT_NAME,
    taskKind: 'pipe',
    // Pipe tasks require a duration (max lifetime in minutes). The agent
    // ends its streams after `ticks`, well before this cap.
    duration: 1,
    requestParts,
  });
  console.log(`Task created: ${session.taskId}`);
  console.log('---');

  // Read every declared stream concurrently. Each is handled by name and
  // format, so adding a stream to the card only means adding a branch.
  //
  // Readers are fire-and-forget: a dedicated stream's `for await` ends
  // naturally when the agent calls stream.end() (which emits a stream_end
  // marker). A *shared*-affinity stream emits no per-task stream_end, so its
  // reader only unwinds when we close the session below — that is expected,
  // and is exactly why we don't `await` the readers before terminal.
  const seen = new Set<string>();
  session.onStream((streamRef) => {
    const { declaredStream, format, affinity, streamId } = streamRef.descriptor;
    const name = declaredStream ?? streamId;
    if (seen.has(name)) return;
    seen.add(name);
    console.log(`Stream discovered: ${name} (format=${format}, affinity=${affinity})`);
    void readStream(name, format, streamRef);
  });

  const terminal = await session.waitForTerminal(60_000);
  console.log(`--- Task ${terminal.state} ---`);

  // Closing the session unsubscribes and unwinds any still-open readers
  // (notably the shared broadcast stream, which has no per-task stream_end).
  await session.asyncClose();
  client.destroy();
  console.log('--- Done ---');
}

async function readStream(
  name: string,
  format: 'events' | 'bytes',
  streamRef: StreamRef,
): Promise<void> {
  const stream = streamRef.open();
  stream.onError((err) => console.error(`[${name}] stream error:`, err));

  try {
    if (format === 'bytes') {
      const decoder = new TextDecoder();
      for await (const chunk of stream.bytes()) {
        process.stdout.write(`[${name}] ${decoder.decode(chunk)}`);
      }
    } else {
      for await (const ev of stream.events<Record<string, unknown>>()) {
        console.log(`[${name}]`, JSON.stringify(ev));
      }
    }
    console.log(`[${name}] ended`);
  } catch {
    // Stream closed (e.g. session teardown for the shared broadcast stream).
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
