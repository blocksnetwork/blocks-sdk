/**
 * StreamBundle - Internal transport engine for the Stream SDK.
 *
 * Accumulates write() calls into buffered bundles and publishes them to
 * PubNub stream channels. Handles both wire formats (stream_data and
 * stream_events), multipart splitting for oversized payloads, binary
 * encoding, presence gating, and meta.sender on every publish.
 *
 * This class is internal to the Stream SDK package. The public API is
 * StreamClient, which owns a StreamBundle instance.
 *
 * Wire formats:
 *   stream_data:   { type, streamId, seq, ts, encoding, chunks }
 *   stream_events: { type, streamId, seq, ts, encoding: "utf8", events }
 *
 * Sequence numbering:
 *   stream_data starts at seq 0
 *   stream_events starts at seq 1
 */

import type PubNub from 'pubnub';
import type { StreamFormat, StreamBundleConfig } from './types.js';
import { utf8ByteLength, utf8Encode, bytesToBase64 } from './bytes.js';
import { CURRENT_PROTOCOL_VERSION } from '../runtime/protocol-version.js';
import { log as baseLog } from '../runtime/logger.js';

const log = (
  level: 'debug' | 'info' | 'warn' | 'error',
  message: string,
  meta?: Record<string, unknown>,
): void => baseLog('[StreamBundle]', level, message, meta);

// Reserved bytes for the per-part envelope in multipart messages.
const ENVELOPE_RESERVE = 512;

/**
 * Cap on concurrent in-flight publishes per multipart group.
 *
 * Rationale: a single 2s fMP4 video segment can split into 13–15
 * multipart parts at the 32 KB per-message limit. Publishing all of
 * them via `Promise.all` saturates the local DNS resolver, TLS
 * handshake pool, and PubNub edge connection limits under macOS +
 * VPN, surfacing as transient `PNNetworkIssuesCategory` /
 * `getaddrinfo ENOTFOUND` errors at ~segment 18. 4 in-flight is a
 * conservative pick that keeps wall-clock under ~1 s for 13 parts at
 * typical 50–150 ms RTT (4 rounds × RTT), comfortably inside a 1.8 s
 * real-time publish budget, while staying well under the saturation
 * point. Matches the Python SDK's inherently-serialized behavior in
 * spirit (Python currently publishes one at a time via blocking
 * `.sync()`; the right long-term pairing is a 4-wide semaphore on
 * both sides).
 */
const DEFAULT_MULTIPART_CONCURRENCY = 4;

/**
 * Run an array of async task thunks with at most `limit` in flight.
 * Workers drain a shared cursor cooperatively. Preserves per-task
 * completion order into `results[]` (though callers that only care
 * about side effects — like `publishMessage` — can ignore it).
 *
 * Keeps local state to the call; no external dependencies, no heap
 * retention after resolution.
 */
async function runWithConcurrency<T>(
  tasks: Array<() => Promise<T>>,
  limit: number,
): Promise<T[]> {
  const results: T[] = new Array(tasks.length);
  let cursor = 0;
  const workerCount = Math.min(limit, tasks.length);
  const workers = Array.from({ length: workerCount }, async () => {
    while (true) {
      const i = cursor++;
      if (i >= tasks.length) return;
      results[i] = await tasks[i]();
    }
  });
  await Promise.all(workers);
  return results;
}

export class StreamBundle {
  private readonly pubnub: PubNub;
  private readonly channel: string;
  private readonly streamId: string;
  private readonly format: StreamFormat;
  private readonly maxMessageSize: number;
  private readonly bundleSizeBytes: number;
  private readonly maxLatencyMs: number;
  private readonly uuid: string;

  // Presence gating state
  private readonly gated: boolean;
  private occupancy = 0;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private presenceListener: any = null;

  // Byte-format buffer
  private buffer: string[] = [];
  private bufferBytes = 0;
  private currentBatchHasBinary = false;

  // Event-format buffer
  private eventBuffer: unknown[] = [];
  private eventBufferSize = 0;

  // Sequence counter: stream_data starts at 0, stream_events starts at 1
  private seq: number;
  private closed = false;
  private flushTimer: ReturnType<typeof setTimeout> | null = null;
  private inflightFlushes: Promise<void>[] = [];

  /** Callback invoked when the stream ends. */
  public onEnd?: (() => Promise<void> | void);

  constructor(
    pubnub: PubNub,
    channel: string,
    streamId: string,
    format: StreamFormat,
    config: StreamBundleConfig,
    gated: boolean,
  ) {
    this.pubnub = pubnub;
    this.channel = channel;
    this.streamId = streamId;
    this.format = format;
    if (config.maxMessageSize <= ENVELOPE_RESERVE) {
      throw new Error(
        `maxMessageSize (${config.maxMessageSize}) must be greater than ENVELOPE_RESERVE (${ENVELOPE_RESERVE})`,
      );
    }
    this.maxMessageSize = config.maxMessageSize;
    this.bundleSizeBytes = config.bundleSizeBytes;
    this.maxLatencyMs = config.maxLatencyMs;
    this.uuid = config.uuid;
    this.gated = gated;

    // stream_data starts at seq 0, stream_events starts at seq 1
    this.seq = format === 'events' ? 1 : 0;

    // TODO(presence-gating): Temporarily disabled. The current
    // implementation silently discards writes when occupancy === 0,
    // which races against consumer subscription timing and causes
    // data loss on pipe tasks. Re-enable after fixing the race
    // (buffer writes until first subscriber, or seed occupancy
    // synchronously before returning the stream to the handler).
    // if (this.gated) {
    //   this.setupPresenceGating();
    // }
  }

  get isActive(): boolean {
    return !this.closed;
  }

  get consumerCount(): number {
    return this.gated ? this.occupancy : 0;
  }

  /**
   * Buffered write. Appends data to the internal buffer.
   * Flushing happens asynchronously on size or time threshold.
   */
  write(data: string | Uint8Array | unknown): void {
    if (this.closed) {
      throw new Error('Cannot write to a closed stream');
    }

    // TODO(presence-gating): Disabled — see constructor comment.
    // if (this.gated && this.occupancy === 0) {
    //   return;
    // }

    if (this.format === 'events') {
      this.writeEvent(data);
    } else {
      this.writeBytes(data as string | Uint8Array);
    }
  }

  /**
   * Publish a stream_end marker on the data channel.
   * Uses the next seq value after the final data flush.
   * Swallows publish errors silently (terminal fallback is the safety net).
   */
  async publishEndMarker(): Promise<void> {
    const message = {
      type: 'stream_end' as const,
      streamId: this.streamId,
      seq: this.seq++,
      ts: Date.now(),
      protocolVersion: CURRENT_PROTOCOL_VERSION,
    };
    await this.publishMessage(message).catch(() => {});
  }

  /**
   * Flush remaining data, clean up presence, invoke onEnd callback.
   */
  async end(): Promise<void> {
    if (this.closed) {
      return;
    }
    this.closed = true;
    this.clearTimer();

    // Await all in-flight flushes
    if (this.inflightFlushes.length > 0) {
      await Promise.all(this.inflightFlushes);
      this.inflightFlushes = [];
    }

    // Flush remaining buffered data
    if (this.format === 'events') {
      await this.flushEvents();
    } else {
      await this.flush();
    }

    // Clean up presence tracking
    this.teardownPresenceGating();

    await this.onEnd?.();
  }

  // -- Presence gating -------------------------------------------------------

  private setupPresenceGating(): void {
    const presChannel = this.channel + '-pnpres';
    this.presenceListener = {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      message: (event: any) => {
        if (event.channel === presChannel && typeof event.message?.occupancy === 'number') {
          this.occupancy = event.message.occupancy;
        }
      },
    };
    this.pubnub.addListener(this.presenceListener);
    this.pubnub.subscribe({ channels: [presChannel] });

    // Seed occupancy from hereNow
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    this.pubnub.hereNow({ channels: [this.channel] }).then((result: any) => {
      const channelData = result.channels?.[this.channel];
      if (channelData && typeof channelData.occupancy === 'number') {
        this.occupancy = channelData.occupancy;
      }
    }).catch(() => {
      // Presence events will correct
    });
  }

  private teardownPresenceGating(): void {
    if (this.presenceListener) {
      this.pubnub.removeListener(this.presenceListener);
      this.presenceListener = null;
    }
    if (this.gated) {
      this.pubnub.unsubscribe({ channels: [this.channel + '-pnpres'] });
    }
  }

  // -- Byte-format helpers ---------------------------------------------------

  private writeBytes(data: string | Uint8Array): void {
    let chunk: string;
    if (typeof data === 'string') {
      chunk = data;
    } else if (data instanceof Uint8Array) {
      chunk = bytesToBase64(data);
      this.currentBatchHasBinary = true;
    } else {
      chunk = String(data);
    }

    const chunkBytes = utf8ByteLength(chunk);
    this.buffer.push(chunk);
    this.bufferBytes += chunkBytes;

    if (this.bufferBytes >= this.bundleSizeBytes) {
      this.trackFlush(this.flush());
    } else if (this.flushTimer === null) {
      this.flushTimer = setTimeout(() => {
        this.flushTimer = null;
        this.trackFlush(this.flush());
      }, this.maxLatencyMs);
    }
  }

  private async flush(): Promise<void> {
    if (this.buffer.length === 0) {
      return;
    }

    this.clearTimer();

    // Snapshot and reset buffer before any await
    const chunks = this.buffer;
    const hasBinary = this.currentBatchHasBinary;
    this.buffer = [];
    this.bufferBytes = 0;
    this.currentBatchHasBinary = false;

    const message = {
      type: 'stream_data' as const,
      streamId: this.streamId,
      seq: this.seq++,
      ts: Date.now(),
      encoding: hasBinary ? ('base64' as const) : ('utf8' as const),
      chunks,
      protocolVersion: CURRENT_PROTOCOL_VERSION,
    };

    const serialized = JSON.stringify(message);
    if (utf8ByteLength(serialized) > this.maxMessageSize) {
      await this.publishMultipart(message);
    } else {
      await this.publishMessage(message);
    }
  }

  // -- Event-format helpers --------------------------------------------------

  private writeEvent(data: unknown): void {
    if (typeof data === 'string') {
      throw new Error(
        'write() does not accept raw strings in format: "events". ' +
        'Pass an object (e.g., { text: "..." }) or use format: "bytes".',
      );
    }

    if (data instanceof Uint8Array) {
      this.eventBuffer.push({ $binary: bytesToBase64(data) });
      // Estimate size including the $binary wrapper
      this.eventBufferSize += Math.ceil(data.length * 4 / 3) + 20;
    } else {
      this.eventBuffer.push(data);
      this.eventBufferSize += utf8ByteLength(JSON.stringify(data));
    }

    if (this.eventBufferSize >= this.bundleSizeBytes) {
      this.trackFlush(this.flushEvents());
    } else if (this.flushTimer === null) {
      this.flushTimer = setTimeout(() => {
        this.flushTimer = null;
        this.trackFlush(this.flushEvents());
      }, this.maxLatencyMs);
    }
  }

  private async flushEvents(): Promise<void> {
    if (this.eventBuffer.length === 0) {
      return;
    }

    this.clearTimer();

    const events = this.eventBuffer;
    this.eventBuffer = [];
    this.eventBufferSize = 0;

    const message = {
      type: 'stream_events' as const,
      streamId: this.streamId,
      seq: this.seq++,
      ts: Date.now(),
      encoding: 'utf8' as const,
      events,
      protocolVersion: CURRENT_PROTOCOL_VERSION,
    };

    const serialized = JSON.stringify(message);
    if (utf8ByteLength(serialized) > this.maxMessageSize) {
      await this.publishMultipart(message);
    } else {
      await this.publishMessage(message);
    }
  }

  // -- Publish helpers -------------------------------------------------------

  private trackFlush(p: Promise<void>): void {
    this.inflightFlushes.push(p);
    p.then(() => {
      const idx = this.inflightFlushes.indexOf(p);
      if (idx >= 0) this.inflightFlushes.splice(idx, 1);
    });
  }

  private async publishMessage(message: Record<string, unknown>): Promise<void> {
    const MAX_ATTEMPTS = 3;
    let lastErr: unknown = null;
    for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
      try {
        await this.pubnub.publish({
          channel: this.channel,
          // Stream messages are valid JSON objects; cast satisfies PubNub's Payload type.
          message: message as Record<string, unknown> & { [key: string]: PubNub.Payload | null },
          meta: { sender: this.uuid, protocolVersion: CURRENT_PROTOCOL_VERSION },
          storeInHistory: false,
          // Multipart stream frames can be tens of KB each. GET-based
          // publishes put the payload in the URL path, which saturates
          // connection pools and TLS buffers once ~15 concurrent large
          // URLs fly per segment (observed with video/fMP4 streams).
          // POST moves the payload into the request body and the SDK
          // gzips it. Parity with agent-instance.ts:252 + :436 which
          // already use POST for control-plane publishes.
          sendByPost: true,
        });
        return;
      } catch (e) {
        lastErr = e;
        if (attempt < MAX_ATTEMPTS) {
          // Exponential backoff between attempts: 100ms, 200ms. Absorbs
          // transient DNS / network blips seen with macOS + local DNS
          // proxies (e.g., VPN clients binding 127.0.0.1:<port>) where
          // node-fetch's agent pool hits intermittent ENOTFOUND on
          // otherwise-resolvable PubNub edge hostnames. Total wall
          // clock on full failure: ~300ms + 3×RTT.
          const delay = 100 * Math.pow(2, attempt - 1);
          await new Promise<void>((r) => setTimeout(r, delay));
        }
      }
    }
    log('error', `failed to publish stream message after ${MAX_ATTEMPTS} attempts`, {
      event: 'stream_bundle_publish_failed_after_retries',
      maxAttempts: MAX_ATTEMPTS,
      error: lastErr instanceof Error ? lastErr.message : String(lastErr),
    });
  }

  /**
   * Split an oversized message into multiple base64-encoded parts.
   * Splitting is byte-safe: converts to Uint8Array, splits on byte
   * boundaries, then base64-encodes each part.
   *
   * Publishes run with bounded concurrency (see
   * `DEFAULT_MULTIPART_CONCURRENCY`). Part order on the wire is
   * irrelevant — the consumer reassembles by `multipart.part` index.
   */
  private async publishMultipart(message: Record<string, unknown>): Promise<void> {
    const serialized = JSON.stringify(message);
    const bytes = utf8Encode(serialized);
    const partSize = Math.floor((this.maxMessageSize - ENVELOPE_RESERVE) * 3 / 4);
    const totalParts = Math.ceil(bytes.length / partSize);
    const multipartId = `mp-${Date.now()}-${(message.seq as number)}`;

    const tasks: Array<() => Promise<void>> = [];
    for (let i = 0; i < totalParts; i++) {
      const partBytes = bytes.subarray(i * partSize, (i + 1) * partSize);
      const payload = {
        type: message.type as string,
        streamId: this.streamId,
        seq: message.seq as number,
        ts: message.ts as number,
        multipart: { id: multipartId, part: i + 1, total: totalParts },
        data: bytesToBase64(partBytes),
        protocolVersion: CURRENT_PROTOCOL_VERSION,
      };
      tasks.push(() => this.publishMessage(payload));
    }
    await runWithConcurrency(tasks, DEFAULT_MULTIPART_CONCURRENCY);
  }

  private clearTimer(): void {
    if (this.flushTimer !== null) {
      clearTimeout(this.flushTimer);
      this.flushTimer = null;
    }
  }
}
