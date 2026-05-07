import 'dotenv/config';
import { TaskClient, decodeInlineArtifact } from '@blocks-network/sdk';
import type { StreamRef, ArtifactEvent, TerminalEvent } from '@blocks-network/sdk';
import {
  produceBytes,
  produceEvents,
  consumeBytes,
  consumeEvents,
  sleep,
  type BytesReport,
  type EventsReport,
} from '../symmetry_shared/helpers.js';
import {
  BYTES_VARIANTS,
  EVENTS_VARIANTS,
} from '../symmetry_shared/payloads.js';

const apiKey = process.env.BLOCKS_API_KEY;
if (!apiKey) {
  console.error("BLOCKS_API_KEY not set. Put it in .env or export it.");
  process.exit(1);
}

const AGENT_NAME = process.env.AGENT_NAME ?? 'symmetry_provider';
const DURATION_MIN = Number.parseFloat(process.env.DURATION_MIN ?? '1');
const PUBLISH_GRACE_MS = 2000;
const DEADLINE_BUFFER_MS = 2000;

interface SymmetryReport {
  provider_sent_bytes: BytesReport;
  provider_sent_events: EventsReport;
  provider_received_bytes: BytesReport;
  provider_received_events: EventsReport;
}

async function main(): Promise<void> {
  const client = await TaskClient.create({ billingMode: 'free', apiKey });

  console.log(`Sending pipe task to ${AGENT_NAME} (duration=${DURATION_MIN} min)`);
  const session = await client.sendMessage({
    agentName: AGENT_NAME,
    taskKind: 'pipe',
    duration: DURATION_MIN,
    requestParts: [{ partId: 'request', data: {} }],
  });
  console.log(`Task created: ${session.taskId}`);

  const taskStartMs = Date.now();
  const deadlineMs = taskStartMs + DURATION_MIN * 60_000 - DEADLINE_BUFFER_MS;

  // Local hash reports computed on the consumer side.
  const consumerSent: { bytes?: BytesReport; events?: EventsReport } = {};
  const consumerReceived: { bytes?: BytesReport; events?: EventsReport } = {};

  // Track per-stream tasks so we can wait on them before adjudicating.
  const streamPromises: Array<Promise<void>> = [];
  let providerReportRaw: string | null = null;

  session.onArtifact(async (event: ArtifactEvent) => {
    const ref = event.artifactRef;
    try {
      if (ref.kind === 'inline' && ref.data) {
        providerReportRaw = new TextDecoder().decode(decodeInlineArtifact(ref));
      } else {
        const dl = await session.downloadArtifact(ref);
        providerReportRaw = new TextDecoder().decode(dl.data);
      }
      console.log(`[artifact] received provider report (${providerReportRaw.length}B)`);
    } catch (err) {
      console.error('[artifact] decode failed:', err);
    }
  });

  session.onStream(async (ref: StreamRef) => {
    const name = ref.descriptor.declaredStream;
    const dir = ref.descriptor.localDirection;
    const fmt = ref.descriptor.format;
    console.log(`[stream] ${name} (${dir}/${fmt})`);

    if (name === 'p_to_c_bytes') {
      const stream = ref.open();
      streamPromises.push(
        (async () => {
          const r = await consumeBytes(stream);
          consumerReceived.bytes = r;
          console.log(
            `[p_to_c_bytes] got ${r.totalBytes}B / ${r.chunkCount} chunks / hash=${r.hash.slice(0, 16)}…`,
          );
        })(),
      );
      return;
    }

    if (name === 'p_to_c_events') {
      const stream = ref.open();
      streamPromises.push(
        (async () => {
          const r = await consumeEvents(stream);
          consumerReceived.events = r;
          console.log(
            `[p_to_c_events] got ${r.eventCount} events / hash=${r.hash.slice(0, 16)}…`,
          );
        })(),
      );
      return;
    }

    if (name === 'c_to_p_bytes') {
      const stream = ref.open();
      streamPromises.push(
        (async () => {
          await sleep(PUBLISH_GRACE_MS);
          if (Date.now() > deadlineMs) {
            console.warn(`[c_to_p_bytes] deadline reached before publishing; ending without writes`);
            await stream.end();
            return;
          }
          const r = await produceBytes(stream, BYTES_VARIANTS);
          consumerSent.bytes = r;
          console.log(
            `[c_to_p_bytes] sent ${r.totalBytes}B / ${r.chunkCount} writes / hash=${r.hash.slice(0, 16)}…`,
          );
        })(),
      );
      return;
    }

    if (name === 'c_to_p_events') {
      const stream = ref.open();
      streamPromises.push(
        (async () => {
          await sleep(PUBLISH_GRACE_MS);
          if (Date.now() > deadlineMs) {
            console.warn(`[c_to_p_events] deadline reached before publishing; ending without writes`);
            await stream.end();
            return;
          }
          const r = await produceEvents(stream, EVENTS_VARIANTS);
          consumerSent.events = r;
          console.log(
            `[c_to_p_events] sent ${r.eventCount} events / hash=${r.hash.slice(0, 16)}…`,
          );
        })(),
      );
      return;
    }

    console.warn(`[stream] unexpected stream '${name}'`);
  });

  const exitCode = await new Promise<number>((resolve) => {
    session.onTerminal(async (event: TerminalEvent) => {
      console.log(`\n[terminal] ${event.state}`);
      try {
        await Promise.all(streamPromises);
      } catch (err) {
        console.error('[stream] wrap-up error:', err);
      }

      if (!providerReportRaw) {
        console.error('FAIL: no provider report received');
        return resolve(1);
      }

      let providerReport: SymmetryReport;
      try {
        providerReport = JSON.parse(providerReportRaw);
      } catch (err) {
        console.error('FAIL: provider report not valid JSON:', err);
        return resolve(1);
      }

      const checks: Array<{ name: string; expected: string | undefined; actual: string | undefined }> = [
        { name: 'P->C bytes ', expected: providerReport.provider_sent_bytes.hash, actual: consumerReceived.bytes?.hash },
        { name: 'P->C events', expected: providerReport.provider_sent_events.hash, actual: consumerReceived.events?.hash },
        { name: 'C->P bytes ', expected: consumerSent.bytes?.hash, actual: providerReport.provider_received_bytes.hash },
        { name: 'C->P events', expected: consumerSent.events?.hash, actual: providerReport.provider_received_events.hash },
      ];

      console.log('\nHash comparison (expected = sender, actual = receiver):');
      let pass = true;
      for (const c of checks) {
        const ok = c.expected !== undefined && c.actual !== undefined && c.expected === c.actual;
        const exp = c.expected ? c.expected.slice(0, 16) + '…' : 'MISSING';
        const act = c.actual ? c.actual.slice(0, 16) + '…' : 'MISSING';
        const mark = ok ? '✓' : '✗';
        console.log(`  ${mark} ${c.name}  expected=${exp}  actual=${act}`);
        if (!ok) pass = false;
      }
      console.log(pass ? '\nSYMMETRY TEST PASSED' : '\nSYMMETRY TEST FAILED');
      resolve(pass ? 0 : 1);
    });
  });

  session.close();
  client.destroy();
  process.exit(exitCode);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
