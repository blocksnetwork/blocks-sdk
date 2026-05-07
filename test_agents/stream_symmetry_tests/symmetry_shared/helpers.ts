import { createHash } from 'node:crypto';

/**
 * Shared producer / consumer helpers used by BOTH the symmetry_provider
 * agent (handler-side StreamObject) and the symmetry_consumer driver
 * (consumer-side StreamClient via streamRef.open()).
 *
 * Symmetry-by-construction: both sides call the SAME functions below.
 * If StreamObject and StreamClient ever diverge in shape, this file
 * fails to type-check on one of the sides and the test breaks loudly.
 *
 * Structural typing (no `@blocks-network/sdk` import) keeps this file
 * dependency-free so it can sit in a sibling directory and be imported
 * via plain relative path from both projects without npm-link gymnastics.
 */

export interface BytesProducer {
  write(data: Uint8Array): void;
  end(): Promise<void> | void;
}

export interface BytesConsumer {
  bytes(): AsyncIterable<Uint8Array>;
}

export interface EventsProducer {
  write(data: unknown): void;
  end(): Promise<void> | void;
}

export interface EventsConsumer {
  events<T = unknown>(): AsyncIterable<T>;
}

export interface BytesReport {
  hash: string;
  totalBytes: number;
  chunkCount: number;
}

export interface EventsReport {
  hash: string;
  eventCount: number;
}

/** Canonical JSON serialization for stable cross-side hashing. JSON.stringify
 * doesn't guarantee key order, so two identical events could hash differently.
 * This walks the value and emits sorted-key objects so both sides agree. */
function canonicalJSON(value: unknown): string {
  if (value === null || typeof value !== 'object') {
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) {
    return `[${value.map(canonicalJSON).join(',')}]`;
  }
  const obj = value as Record<string, unknown>;
  const keys = Object.keys(obj).sort();
  return `{${keys.map((k) => `${JSON.stringify(k)}:${canonicalJSON(obj[k])}`).join(',')}}`;
}

export async function produceBytes(
  stream: BytesProducer,
  payloads: readonly Uint8Array[],
): Promise<BytesReport> {
  const h = createHash('sha256');
  let totalBytes = 0;
  for (const p of payloads) {
    stream.write(p);
    h.update(p);
    totalBytes += p.byteLength;
  }
  await Promise.resolve(stream.end());
  return { hash: h.digest('hex'), totalBytes, chunkCount: payloads.length };
}

export async function consumeBytes(stream: BytesConsumer): Promise<BytesReport> {
  const h = createHash('sha256');
  let totalBytes = 0;
  let chunkCount = 0;
  for await (const chunk of stream.bytes()) {
    h.update(chunk);
    totalBytes += chunk.byteLength;
    chunkCount += 1;
  }
  return { hash: h.digest('hex'), totalBytes, chunkCount };
}

export async function produceEvents(
  stream: EventsProducer,
  events: readonly unknown[],
): Promise<EventsReport> {
  const h = createHash('sha256');
  for (const ev of events) {
    stream.write(ev);
    h.update(canonicalJSON(ev));
  }
  await Promise.resolve(stream.end());
  return { hash: h.digest('hex'), eventCount: events.length };
}

export async function consumeEvents(stream: EventsConsumer): Promise<EventsReport> {
  const h = createHash('sha256');
  let eventCount = 0;
  for await (const ev of stream.events()) {
    h.update(canonicalJSON(ev));
    eventCount += 1;
  }
  return { hash: h.digest('hex'), eventCount };
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
