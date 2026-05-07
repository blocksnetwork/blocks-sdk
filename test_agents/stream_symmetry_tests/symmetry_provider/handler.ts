import type {
  StartTaskMessage,
  TaskContext,
  HandlerResult,
} from '@blocks-network/sdk';
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

const PUBLISH_GRACE_MS = 2000;

interface SymmetryReport {
  provider_sent_bytes: BytesReport;
  provider_sent_events: EventsReport;
  provider_received_bytes: BytesReport;
  provider_received_events: EventsReport;
}

export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const tag = `[handler ${task.taskId.slice(0, 8)}]`;
  console.error(`${tag} start kind=${task.taskKind} duration=${task.duration ?? '?'}min`);
  if (!ctx) {
    return { artifacts: [{ data: '{}', mimeType: 'application/json' }] };
  }

  console.error(`${tag} opening 4 streams`);
  const [pToCBytes, pToCEvents, cToPBytes, cToPEvents] = await Promise.all([
    ctx.createStream({
      declaredStream: 'p_to_c_bytes',
      direction: 'outbound',
      format: 'bytes',
    }),
    ctx.createStream({
      declaredStream: 'p_to_c_events',
      direction: 'outbound',
      format: 'events',
    }),
    ctx.createStream({
      declaredStream: 'c_to_p_bytes',
      direction: 'inbound',
      format: 'bytes',
    }),
    ctx.createStream({
      declaredStream: 'c_to_p_events',
      direction: 'inbound',
      format: 'events',
    }),
  ]);
  console.error(`${tag} all streams open`);

  // P->C side: wait the publish-grace window so the consumer has subscribed,
  // then run the same shared helpers the consumer uses.
  const producePToC = (async () => {
    await sleep(PUBLISH_GRACE_MS);
    console.error(`${tag} producing P->C bytes (${BYTES_VARIANTS.length} payloads)`);
    const bytes = await produceBytes(pToCBytes, BYTES_VARIANTS);
    console.error(
      `${tag} P->C bytes done: ${bytes.totalBytes}B / ${bytes.chunkCount} writes / hash=${bytes.hash.slice(0, 16)}…`,
    );
    console.error(`${tag} producing P->C events (${EVENTS_VARIANTS.length} events)`);
    const events = await produceEvents(pToCEvents, EVENTS_VARIANTS);
    console.error(
      `${tag} P->C events done: ${events.eventCount} events / hash=${events.hash.slice(0, 16)}…`,
    );
    return { bytes, events };
  })();

  // C->P side: just iterate. Consumer-side stream_end markers terminate
  // these iterators, so this branch finishes when the consumer calls .end().
  const consumeCToP = (async () => {
    console.error(`${tag} consuming C->P bytes…`);
    const bytes = await consumeBytes(cToPBytes);
    console.error(
      `${tag} C->P bytes done: ${bytes.totalBytes}B / ${bytes.chunkCount} chunks / hash=${bytes.hash.slice(0, 16)}…`,
    );
    console.error(`${tag} consuming C->P events…`);
    const events = await consumeEvents(cToPEvents);
    console.error(
      `${tag} C->P events done: ${events.eventCount} events / hash=${events.hash.slice(0, 16)}…`,
    );
    return { bytes, events };
  })();

  const [produced, consumed] = await Promise.all([producePToC, consumeCToP]);

  const report: SymmetryReport = {
    provider_sent_bytes: produced.bytes,
    provider_sent_events: produced.events,
    provider_received_bytes: consumed.bytes,
    provider_received_events: consumed.events,
  };
  console.error(`${tag} report: ${JSON.stringify(report)}`);

  return {
    artifacts: [
      {
        data: JSON.stringify(report),
        mimeType: 'application/json',
        outputId: 'report',
      },
    ],
  };
}
