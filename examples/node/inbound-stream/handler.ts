import { createHash } from 'node:crypto';
import type { HandlerResult, StartTaskMessage, TaskContext } from '@blocks-network/sdk';

export default async function handler(
  _task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx) throw new Error('TaskContext is required');

  // Inbound streams are created by the agent; the consumer finds them and writes.
  const [bytesStream, eventsStream] = await Promise.all([
    ctx.createStream({ declaredStream: 'upload_bytes', direction: 'inbound', format: 'bytes' }),
    ctx.createStream({ declaredStream: 'upload_events', direction: 'inbound', format: 'events' }),
  ]);
  ctx.reportStatus('Waiting for the consumer to write...');

  // Each iterator ends when the consumer calls end() on its side.
  const [bytes, events] = await Promise.all([
    readBytes(bytesStream.bytes()),
    readEvents(eventsStream.events<{ text: string }>()),
  ]);

  const report = { bytes, events };
  return {
    artifacts: [{ data: JSON.stringify(report), mimeType: 'application/json', outputId: 'report' }],
  };
}

async function readBytes(chunks: AsyncIterable<Uint8Array>) {
  const hash = createHash('sha256');
  let totalBytes = 0;
  for await (const chunk of chunks) {
    hash.update(chunk);
    totalBytes += chunk.byteLength;
  }
  return { totalBytes, sha256: hash.digest('hex') };
}

async function readEvents<T>(events: AsyncIterable<T>): Promise<T[]> {
  const received: T[] = [];
  for await (const event of events) received.push(event);
  return received;
}
