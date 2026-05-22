/**
 * StreamClient - The developer-facing API for the Stream SDK.
 *
 * Handles PubNub client creation, token setup, UUID generation, channel
 * computation, direction routing, self-publish filtering, presence gating,
 * and inbound message consumption with multipart reassembly.
 *
 * StreamClient is the public API. StreamBundle is the internal engine.
 */

import PubNub from 'pubnub';
import { StreamBundle } from './stream-bundle.js';
import { validateStreamId } from './validate.js';
import { getEnv } from '../env.js';
import { log as baseLog } from '../runtime/logger.js';
import { base64ToBytes, concatBytes, utf8Decode, utf8Encode } from './bytes.js';
import type { StreamDescriptor } from './descriptor.js';
import type {
  StreamClientOptions,
  StreamClientFromDescriptorOptions,
  StreamDirection,
  StreamFormat,
  StreamAffinity,
  InboundMessage,
  StreamBundleConfig,
} from './types.js';

const log = (
  level: 'debug' | 'info' | 'warn' | 'error',
  message: string,
  meta?: Record<string, unknown>,
): void => baseLog('[StreamClient]', level, message, meta);

// Per-process UUID counter for the {agentName}-stream-{NNNN} convention
let uuidCounter = 0;

/** Reset the UUID counter (for testing). */
export function _resetUuidCounter(): void {
  uuidCounter = 0;
}

/**
 * Read a config value: constructor option overrides env var, which overrides the default.
 */
function resolveConfig<T>(
  optionValue: T | undefined,
  envVar: string,
  defaultValue: T,
  parse: (s: string) => T,
): T {
  if (optionValue !== undefined) return optionValue;
  const envVal = getEnv(envVar);
  if (envVal !== undefined && envVal !== '') return parse(envVal);
  return defaultValue;
}

function parseNumber(s: string): number {
  return Number(s);
}

function parseBoolean(s: string): boolean {
  const lower = s.toLowerCase();
  return lower === 'true' || lower === 'on' || lower === '1';
}

// Multipart safety limits (aligned with Python SDK)
const MULTIPART_TTL_MS = 30_000;      // 30 seconds
const MULTIPART_MAX_GROUPS = 64;       // max buffered incomplete groups

// Fatal category allowlist — PubNub status categories that should
// force-terminate the stream because the PAM grant is gone and won't
// come back. Non-fatal error categories (network, timeout, etc.) fire
// onError but leave the stream running so PubNub's retry machinery can
// recover. Intentionally small and explicit; expand only with plan
// justification.
export const FATAL_STREAM_ERROR_CATEGORIES: ReadonlySet<string> = new Set([
  'PNAccessDeniedCategory',  // PAM revocation / token denied
  'PNBadRequestCategory',    // auth config / malformed grant
]);

/**
 * Compatibility helper that detects a PubNub status error across the two
 * shapes the pinned `pubnub@10.2.x` JS SDK exposes on the subscribe
 * listener:
 *
 * - `Status` — has a required `error: boolean` and `statusCode: number`.
 * - `StatusEvent` — has an optional polymorphic
 *   `error?: string | StatusCategory | boolean` and no numeric status
 *   code.
 *
 * Priority:
 *   1. Truthy `status.error` (covers `true`, non-empty string, or
 *      category-name string surfaced by `StatusEvent`).
 *   2. Numeric `statusCode >= 400` (REST-style errors surfaced via
 *      `Status`).
 *   3. Fatal-category membership as a last resort when the payload is
 *      sparse (defensive; keeps PAM detection working even if neither of
 *      the above fields is populated by a future SDK minor release).
 *
 * Exported so classifier behavior can be unit-tested directly without
 * instantiating a full `StreamClient`.
 */
export function isStreamStatusError(status: {
  error?: unknown;
  statusCode?: unknown;
  category?: unknown;
} | null | undefined): boolean {
  if (!status) return false;
  if (status.error) return true;
  const statusCode = status.statusCode;
  if (typeof statusCode === 'number' && statusCode >= 400) return true;
  const category = typeof status.category === 'string' ? status.category : '';
  return category !== '' && FATAL_STREAM_ERROR_CATEGORIES.has(category);
}

/** True if this category is in the fatal allowlist. */
export function isFatalStreamCategory(category: string | undefined | null): boolean {
  return typeof category === 'string' && FATAL_STREAM_ERROR_CATEGORIES.has(category);
}

/**
 * Typed error payload fired to `StreamClient.onError(...)` subscribers.
 *
 * Fires for every PubNub status event classified as an error by
 * `isStreamStatusError`. Consumers branch on `fatal` for
 * must-terminate conditions (PAM revocation, bad grant) and on
 * `category` for finer-grained UX.
 */
export interface StreamError {
  /** PubNub status category (e.g., `PNAccessDeniedCategory`). */
  category: string;
  /** Raw PubNub error data (`errorData` or `error` payload if present). */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  error: any;
  /** The stream channel the error applies to. */
  channel: string;
  /** Unix ms timestamp when the error was observed. */
  timestamp: number;
  /** Whether the error triggered forced stream termination. */
  fatal: boolean;
}

// Multipart reassembly buffer entry
interface MultipartBuffer {
  total: number;
  parts: Map<number, string>;
  seq: number;
  ts: number;
  type: string;
  streamId: string | undefined;
  createdAt: number;
}

/** Validate multipart metadata fields from the wire. */
function isValidMultipartMeta(
  mp: Record<string, unknown>,
): mp is { id: string; part: number; total: number } {
  if (typeof mp.id !== 'string' || mp.id === '') return false;
  if (typeof mp.part !== 'number' || !Number.isInteger(mp.part) || mp.part < 1) return false;
  if (typeof mp.total !== 'number' || !Number.isInteger(mp.total) || mp.total < 2) return false;
  if (mp.part > mp.total) return false;
  return true;
}

export class StreamClient {
  private readonly pubnub: PubNub;
  private readonly bundle: StreamBundle | null;
  private readonly _channel: string;
  private readonly _uuid: string;
  private readonly direction: StreamDirection;
  private readonly format: StreamFormat;
  private readonly _affinity: StreamAffinity;
  private _isActive = true;
  private endCallbacks: Array<() => void> = [];
  private inboundDoneCallbacks: Array<() => void> = [];
  private inboundDoneFired = false;
  private errorCallbacks: Array<(err: StreamError) => void> = [];

  // Inbound state
  private inboundQueue: InboundMessage[] = [];
  private inboundResolve: ((value: IteratorResult<InboundMessage>) => void) | null = null;
  private inboundDone = false;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private messageListener: any = null;
  private multipartBuffers = new Map<string, MultipartBuffer>();

  // Reorder buffer state
  private nextExpectedSeq: number;
  private reorderBuffer = new Map<number, InboundMessage>();
  private reorderTimer: ReturnType<typeof setTimeout> | null = null;
  private endSeq: number | null = null;
  private readonly reorderTimeoutMs: number;

  constructor(options: StreamClientOptions) {
    const agentName = options.agentName;
    if (!agentName) {
      throw new Error('agentName is required (pass in options)');
    }

    validateStreamId(options.streamId);

    this.format = options.format ?? 'bytes';
    if (this.format !== 'bytes' && this.format !== 'events') {
      throw new Error(`Invalid stream format: "${this.format}". Must be "bytes" or "events".`);
    }
    this.direction = options.direction ?? 'outbound';
    // Affinity gates stream_end publishing. Default 'dedicated' preserves
    // existing behavior for callers that don't yet pass affinity. Matches
    // Python's warn-on-invalid-fallback path for JS callers that bypass
    // the TypeScript union check (descriptor-parsed affinity is already
    // enum-validated upstream; this guards hand-constructed StreamClient
    // instances). `'dedicated'` is the safer fallback — wrongly
    // suppressing the end-marker is more surprising than wrongly
    // publishing it.
    const rawAffinity = options.affinity;
    if (rawAffinity === undefined || rawAffinity === null) {
      this._affinity = 'dedicated';
    } else if (rawAffinity === 'dedicated' || rawAffinity === 'shared') {
      this._affinity = rawAffinity;
    } else {
      log('warn', 'invalid affinity in stream options — falling back to dedicated', {
        event: 'stream_client_invalid_affinity_fallback',
        streamId: options.streamId,
        receivedAffinity: rawAffinity,
        fallbackAffinity: 'dedicated',
      });
      this._affinity = 'dedicated';
    }

    // Reorder buffer: seq starts at 0 for bytes, 1 for events (matching stream-bundle.ts:87)
    this.nextExpectedSeq = this.format === 'events' ? 1 : 0;
    this.reorderTimeoutMs = options.reorderTimeoutMs ?? 750;

    // Generate UUID: {agentName}-stream-{NNNN}
    uuidCounter++;
    this._uuid = `${agentName}-stream-${String(uuidCounter).padStart(4, '0')}`;

    // Use explicit channel or compute from agentName + streamId
    this._channel = options.channel ?? `stream.${agentName}.${options.streamId}`;

    // Resolve configuration with env var fallbacks
    const maxMessageSize = resolveConfig(options.maxMessageSize, 'STREAM_MAX_MESSAGE_SIZE', 16384, parseNumber);
    const bundleSizeBytes = resolveConfig(options.bundleSizeBytes, 'STREAM_BUNDLE_SIZE', 4096, parseNumber);
    const maxLatencyMs = resolveConfig(options.maxLatencyMs, 'STREAM_MAX_LATENCY_MS', 250, parseNumber);
    const gating = resolveConfig(options.gating, 'STREAM_GATING', true, parseBoolean);

    // Create PubNub client instance
    this.pubnub = new PubNub({
      subscribeKey: options.subscribeKey,
      publishKey: options.publishKey,
      userId: this._uuid,
      enableEventEngine: true,
    });
    this.pubnub.setToken(options.token);

    // Self-publish filter for bidirectional streams
    if (this.direction === 'bidirectional') {
      this.pubnub.setFilterExpression(`meta.sender != '${this._uuid}'`);
    }

    // Create StreamBundle for outbound/bidirectional
    const canWrite = this.direction === 'outbound' || this.direction === 'bidirectional';
    if (canWrite) {
      const bundleConfig: StreamBundleConfig = {
        maxMessageSize,
        bundleSizeBytes,
        maxLatencyMs,
        uuid: this._uuid,
      };
      this.bundle = new StreamBundle(
        this.pubnub,
        this._channel,
        options.streamId,
        this.format,
        bundleConfig,
        gating,
      );
    } else {
      this.bundle = null;
    }

    // Subscribe for inbound/bidirectional
    const canRead = this.direction === 'inbound' || this.direction === 'bidirectional';
    if (canRead) {
      this.setupInbound();
    }
  }

  // -- Properties ------------------------------------------------------------

  get isActive(): boolean {
    return this._isActive;
  }

  get channel(): string {
    return this._channel;
  }

  get uuid(): string {
    return this._uuid;
  }

  // -- Write / End -----------------------------------------------------------

  /**
   * Write data to the stream.
   * Throws if the stream is ended or direction is inbound-only.
   */
  write(data: string | Uint8Array | unknown): void {
    if (!this._isActive) {
      throw new Error('Cannot write to an ended stream');
    }
    if (!this.bundle) {
      throw new Error('Cannot write to an inbound-only stream');
    }
    this.bundle.write(data);
  }

  /**
   * Flush remaining data, unsubscribe, and destroy the PubNub client.
   */
  async end(): Promise<void> {
    if (!this._isActive) {
      return;
    }
    this._isActive = false;

    // Best-effort flush: on fatal PAM revocation (the exact case that
    // force-ends the stream via handleStatusEvent), these publishes will
    // throw because the underlying token is dead. Swallow the failure so
    // the teardown below (iterator signal, listener removal, destroy)
    // still runs — otherwise consumer iterators hang waiting for the
    // stream_end marker that can never be published.
    if (this.bundle) {
      try { await this.bundle.end(); }
      catch (err) {
        log('warn', 'bundle.end() failed during end() — continuing teardown', {
          event: 'stream_client_bundle_end_failed',
          error: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // Publish end marker if this side is the writer on a unidirectional
    // stream. Shared-affinity channels are broadcast by design; per-task
    // cleanup is refcount-internal, not a "broadcast is over" signal, so
    // we NEVER publish the marker on shared streams — producer OR
    // consumer-writer. See SDK_CONTRACT §8.4.1a shared-affinity carve-out.
    if (
      this.direction !== 'bidirectional' &&
      this.bundle &&
      this._affinity !== 'shared'
    ) {
      try { await this.bundle.publishEndMarker(); }
      catch (err) {
        log('warn', 'publishEndMarker() failed during end() — continuing teardown', {
          event: 'stream_client_publish_end_marker_failed',
          error: err instanceof Error ? err.message : String(err),
        });
      }
    }

    // Signal inbound iterator completion
    this.inboundDone = true;
    if (this.inboundResolve) {
      this.inboundResolve({ value: undefined as unknown as InboundMessage, done: true });
      this.inboundResolve = null;
    }
    this.fireInboundDone();

    // Clear multipart buffers
    this.multipartBuffers.clear();

    // Clear reorder state
    this.reorderBuffer.clear();
    if (this.reorderTimer) {
      clearTimeout(this.reorderTimer);
      this.reorderTimer = null;
    }

    // Clean up PubNub
    if (this.messageListener) {
      this.pubnub.removeListener(this.messageListener);
      this.messageListener = null;
    }
    this.pubnub.unsubscribeAll();
    this.pubnub.destroy();

    // Invoke end callbacks
    for (const cb of this.endCallbacks) {
      cb();
    }
    this.endCallbacks = [];
  }

  /**
   * Register a callback to be invoked when end() completes.
   */
  onEnd(callback: () => void): void {
    this.endCallbacks.push(callback);
  }

  /**
   * Register a callback to fire when the inbound iterator completes for any
   * reason: stream_end marker, explicit end(), or error. Internal only --
   * used by TaskSession for auto-drain tracking.
   *
   * If the inbound side has already completed, the callback fires immediately.
   */
  onInboundDone(cb: () => void): void {
    if (this.inboundDoneFired) {
      cb();
      return;
    }
    this.inboundDoneCallbacks.push(cb);
  }

  private fireInboundDone(): void {
    if (this.inboundDoneFired) return;
    this.inboundDoneFired = true;
    for (const cb of this.inboundDoneCallbacks) {
      try { cb(); } catch { /* ignore */ }
    }
    this.inboundDoneCallbacks = [];
  }

  /**
   * Register a callback to fire when the stream encounters a PubNub
   * status error — PAM revocation, network failure, grant mismatch, etc.
   *
   * The callback receives a {@link StreamError} with `fatal: true` when
   * the error caused forced stream termination (and `onInboundDone` will
   * fire shortly after so consumer iterators exit cleanly). Non-fatal
   * errors fire with `fatal: false` and leave the stream running so
   * PubNub's retry machinery can recover.
   *
   * Consumer exceptions from the callback are swallowed and logged; they
   * do not interrupt stream teardown.
   */
  onError(callback: (err: StreamError) => void): void {
    this.errorCallbacks.push(callback);
  }

  private fireError(err: StreamError): void {
    for (const cb of [...this.errorCallbacks]) {
      try {
        cb(err);
      } catch (e) {
        log('error', 'onError callback raised', {
          event: 'stream_client_on_error_callback_raised',
          error: e instanceof Error ? e.message : String(e),
        });
      }
    }
  }

  // -- Inbound iterator ------------------------------------------------------

  /**
   * Async iterable of inbound messages. Throws if direction is outbound-only.
   * Handles multipart reassembly transparently.
   */
  get inbound(): AsyncIterable<InboundMessage> {
    if (this.direction === 'outbound') {
      throw new Error('Cannot read from an outbound-only stream');
    }

    // eslint-disable-next-line @typescript-eslint/no-this-alias
    const self = this;
    return {
      [Symbol.asyncIterator](): AsyncIterator<InboundMessage> {
        return {
          next(): Promise<IteratorResult<InboundMessage>> {
            // If there are queued messages, return one immediately
            if (self.inboundQueue.length > 0) {
              return Promise.resolve({ value: self.inboundQueue.shift()!, done: false });
            }
            // If done, signal completion
            if (self.inboundDone) {
              return Promise.resolve({ value: undefined as unknown as InboundMessage, done: true });
            }
            // Wait for next message
            return new Promise(resolve => {
              self.inboundResolve = resolve;
            });
          },
        };
      },
    };
  }

  // -- Inbound setup ---------------------------------------------------------

  private setupInbound(): void {
    this.messageListener = {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      message: (event: any) => {
        if (event.channel !== this._channel) return;
        this.handleInboundMessage(event.message);
      },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      status: (status: any) => {
        this.handleStatusEvent(status);
      },
    };
    this.pubnub.addListener(this.messageListener);
    // timetoken: 1000 asks PubNub to replay everything still in the
    // channel's in-memory cache (per SDK_CONTRACT §10.4.1a). On data-plane
    // stream channels this is a short-term mitigation for the
    // publish-before-subscribe race; the reorder buffer's seq-based
    // dedup handles duplicate delivery from replay overlap. The durable
    // data-plane fix is stream presence gating (issue #496).
    this.pubnub.subscribe({ channels: [this._channel], timetoken: 1000 });
  }

  /**
   * Dispatch a PubNub status event to `onError` subscribers, then
   * force-terminate the stream if the category is in the fatal
   * allowlist. Non-error / benign status events are ignored.
   *
   * All work is wrapped in try/catch so a listener exception can't
   * destabilize PubNub's internal event loop.
   */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private handleStatusEvent(status: any): void {
    try {
      if (!isStreamStatusError(status)) return;
      const category: string = typeof status?.category === 'string' ? status.category : '';
      const fatal = isFatalStreamCategory(category);
      const rawError = status?.errorData ?? status?.error ?? null;
      const err: StreamError = {
        category,
        error: rawError,
        channel: this._channel,
        timestamp: Date.now(),
        fatal,
      };
      log('warn', `status error: category=${category} fatal=${fatal} channel=${this._channel}`, {
        event: 'stream_client_status_error',
        category,
        fatal,
        channel: this._channel,
        rawError,
      });
      this.fireError(err);
      if (fatal && this._isActive) {
        // Fire-and-forget: end() is async but we don't want to block
        // the PubNub listener thread, and we must not throw. The consumer
        // iterator exits via onInboundDone once end() completes.
        this.end().catch((e) => {
          log('error', 'forced end() after fatal error raised', {
            event: 'stream_client_forced_end_after_fatal_raised',
            error: e instanceof Error ? e.message : String(e),
          });
        });
      }
    } catch (e) {
      log('error', 'status handler raised', {
        event: 'stream_client_status_handler_raised',
        error: e instanceof Error ? e.message : String(e),
      });
    }
  }

  private handleInboundMessage(msg: unknown): void {
    if (!msg || typeof msg !== 'object') return;
    const message = msg as Record<string, unknown>;

    // Check for stream_end marker
    if (message.type === 'stream_end') {
      // Disable mode: complete immediately (buffer is always empty)
      if (this.reorderTimeoutMs <= 0) {
        this.inboundDone = true;
        if (this.inboundResolve) {
          this.inboundResolve({ value: undefined as unknown as InboundMessage, done: true });
          this.inboundResolve = null;
        }
        this.fireInboundDone();
        return;
      }

      // Malformed stream_end: seq must be an integer per schema
      if (typeof message.seq !== 'number' || !Number.isInteger(message.seq)) {
        log('warn', 'stream_end missing numeric seq field — ignoring in reorder mode', {
          event: 'stream_client_stream_end_missing_seq',
        });
        return;
      }

      this.endSeq = message.seq as number;

      // If all data messages up to endSeq already emitted, complete now
      if (this.nextExpectedSeq >= this.endSeq) {
        this.checkEndReached();
      } else {
        // Gap exists between nextExpectedSeq and endSeq. Start a gap
        // timer even if reorderBuffer is empty -- handles the tail-gap
        // case where final data messages are permanently lost.
        this.startReorderTimer();
      }
      return;
    }

    // Check for multipart
    if (message.multipart && typeof message.multipart === 'object') {
      const mp = message.multipart as { id: string; part: number; total: number };
      this.handleMultipartPart(message, mp);
      return;
    }

    // Normal message -- normalize to InboundMessage
    const inbound = this.normalizeMessage(message);
    if (inbound) {
      this.enqueueReordered(inbound);
    }
  }

  private handleMultipartPart(
    message: Record<string, unknown>,
    mp: { id: string; part: number; total: number },
  ): void {
    // Validate multipart metadata
    if (!isValidMultipartMeta(mp as Record<string, unknown>)) return;

    // Validate data is a string (base64 payload)
    if (typeof message.data !== 'string') return;

    // Evict stale/overflowing groups before processing
    this.evictStaleGroups();

    const msgSeq = message.seq as number;
    const msgType = message.type as string;
    const msgStreamId = message.streamId as string | undefined;
    const payload = message.data as string;

    let entry = this.multipartBuffers.get(mp.id);
    if (entry) {
      // Consistency check: total, seq, type, and streamId must match
      if (
        entry.total !== mp.total ||
        entry.seq !== msgSeq ||
        entry.type !== msgType ||
        entry.streamId !== msgStreamId
      ) {
        this.dropMultipartGroup(mp.id);
        return;
      }

      // Duplicate detection
      const existing = entry.parts.get(mp.part);
      if (existing !== undefined) {
        if (existing === payload) {
          // Idempotent duplicate -- ignore
          return;
        }
        // Conflicting duplicate -- drop the whole group
        this.dropMultipartGroup(mp.id);
        return;
      }
    } else {
      entry = {
        total: mp.total,
        parts: new Map(),
        seq: msgSeq,
        ts: message.ts as number,
        type: msgType,
        streamId: msgStreamId,
        createdAt: Date.now(),
      };
      this.multipartBuffers.set(mp.id, entry);
    }

    entry.parts.set(mp.part, payload);

    // Check if all parts arrived
    if (entry.parts.size === entry.total) {
      this.dropMultipartGroup(mp.id);

      // Verify all keys 1..total are present (do not trust size alone)
      for (let i = 1; i <= entry.total; i++) {
        if (!entry.parts.has(i)) return;
      }

      // Reassemble with full defensive wrapping
      try {
        const sortedParts: Uint8Array[] = [];
        for (let i = 1; i <= entry.total; i++) {
          sortedParts.push(base64ToBytes(entry.parts.get(i)!));
        }
        const concatenated = concatBytes(sortedParts);
        const reassembled = JSON.parse(utf8Decode(concatenated)) as Record<string, unknown>;
        const inbound = this.normalizeMessage(reassembled);
        if (inbound) {
          this.enqueueReordered(inbound);
        }
      } catch {
        // Failed to decode/parse reassembled message -- drop silently
      }
    }
  }

  /** Remove a multipart group from the buffer. */
  private dropMultipartGroup(id: string): void {
    this.multipartBuffers.delete(id);
  }

  /** Evict groups older than TTL or when buffer exceeds capacity. */
  private evictStaleGroups(): void {
    const now = Date.now();

    // Evict groups older than TTL
    for (const [id, entry] of this.multipartBuffers) {
      if (now - entry.createdAt > MULTIPART_TTL_MS) {
        this.multipartBuffers.delete(id);
      }
    }

    // If still over capacity, evict oldest groups first
    if (this.multipartBuffers.size > MULTIPART_MAX_GROUPS) {
      const sorted = [...this.multipartBuffers.entries()]
        .sort((a, b) => a[1].createdAt - b[1].createdAt);
      const toEvict = sorted.length - MULTIPART_MAX_GROUPS;
      for (let i = 0; i < toEvict; i++) {
        this.multipartBuffers.delete(sorted[i][0]);
      }
    }
  }

  private normalizeMessage(message: Record<string, unknown>): InboundMessage | null {
    const type = message.type as string;

    if (type === 'stream_data') {
      if (typeof message.seq !== 'number' || !Number.isInteger(message.seq)) return null;
      const chunks = message.chunks;
      if (!Array.isArray(chunks) || chunks.length === 0 || !chunks.every(c => typeof c === 'string')) return null;
      const encoding = message.encoding as string | undefined;
      if (encoding !== undefined && encoding !== 'utf8' && encoding !== 'base64') return null;
      return {
        data: chunks,
        seq: message.seq as number,
        ts: message.ts as number,
        format: 'bytes',
        encoding: encoding ?? 'utf8',
      };
    }

    if (type === 'stream_events') {
      if (typeof message.seq !== 'number' || !Number.isInteger(message.seq)) return null;
      const events = message.events;
      if (!Array.isArray(events) || events.length === 0) return null;
      return {
        data: events,
        seq: message.seq as number,
        ts: message.ts as number,
        format: 'events',
        encoding: 'utf8',
      };
    }

    // Unknown message type -- pass through as raw only if seq is a valid integer
    if (typeof message.seq !== 'number' || !Number.isInteger(message.seq)) return null;
    return {
      data: message,
      seq: message.seq as number,
      ts: (message.ts as number) ?? Date.now(),
      format: 'raw',
      encoding: 'utf8',
    };
  }

  private enqueueInbound(msg: InboundMessage): void {
    if (this.inboundResolve) {
      const resolve = this.inboundResolve;
      this.inboundResolve = null;
      resolve({ value: msg, done: false });
    } else {
      this.inboundQueue.push(msg);
    }
  }

  // -- Reorder buffer -------------------------------------------------------

  private enqueueReordered(msg: InboundMessage): void {
    // Disable mode: bypass buffer entirely
    if (this.reorderTimeoutMs <= 0) {
      this.enqueueInbound(msg);
      return;
    }

    const seq = msg.seq;

    // Reject data past the stream boundary
    if (this.endSeq !== null && seq >= this.endSeq) return;

    // Duplicate detection: by seq number alone (first arrival wins).
    if (seq < this.nextExpectedSeq) return;  // already yielded
    if (this.reorderBuffer.has(seq)) return; // already buffered

    if (seq === this.nextExpectedSeq) {
      // In order: yield immediately and flush consecutive
      this.enqueueInbound(msg);
      this.nextExpectedSeq++;
      this.flushConsecutive();
      this.cancelAndRestartTimerIfNeeded();
      this.checkEndReached();
    } else {
      // Out of order: buffer and start timeout (does NOT reset if
      // already running -- timeout measures from first gap, bounding
      // how long any single missing seq can block the stream)
      this.reorderBuffer.set(seq, msg);
      this.startReorderTimer();
    }
  }

  private flushConsecutive(): void {
    while (this.reorderBuffer.has(this.nextExpectedSeq)) {
      const buffered = this.reorderBuffer.get(this.nextExpectedSeq)!;
      this.reorderBuffer.delete(this.nextExpectedSeq);
      this.enqueueInbound(buffered);
      this.nextExpectedSeq++;
    }
  }

  private startReorderTimer(): void {
    if (this.reorderTimer) return; // already running
    this.reorderTimer = setTimeout(() => {
      this.reorderTimer = null;
      if (this.reorderBuffer.size > 0) {
        // Skip missing seq(s), advance to next buffered message
        let minBuffered = Infinity;
        for (const k of this.reorderBuffer.keys()) { if (k < minBuffered) minBuffered = k; }
        this.nextExpectedSeq = minBuffered;
        this.flushConsecutive();
        this.cancelAndRestartTimerIfNeeded();
        this.checkEndReached();
      } else if (this.endSeq !== null && this.nextExpectedSeq < this.endSeq) {
        // Tail-gap: stream_end was received but final data messages
        // are permanently lost (buffer is empty, nothing to flush).
        // Skip to endSeq and complete the stream.
        this.nextExpectedSeq = this.endSeq;
        this.checkEndReached();
      }
    }, this.reorderTimeoutMs);
  }

  /**
   * Called after flushing consecutive messages. If the gap that started
   * the current timer has been resolved but a new gap exists, cancel
   * the old timer and start a fresh one for the new gap. If no gaps
   * remain, just cancel.
   */
  private cancelAndRestartTimerIfNeeded(): void {
    if (this.reorderTimer) {
      clearTimeout(this.reorderTimer);
      this.reorderTimer = null;
    }
    if (this.reorderBuffer.size > 0) {
      this.startReorderTimer();
    }
  }

  private checkEndReached(): void {
    if (this.endSeq !== null && this.nextExpectedSeq >= this.endSeq) {
      // Invariant: buffer should be empty at this point. All seqs < endSeq
      // were emitted or timed out, and no valid data message has seq >= endSeq.
      if (this.reorderBuffer.size > 0) {
        log('warn', `reorder buffer not empty at stream end (${this.reorderBuffer.size} unexpected messages with seq >= ${this.endSeq})`, {
          event: 'stream_client_reorder_buffer_not_empty_at_end',
          unexpectedCount: this.reorderBuffer.size,
          endSeq: this.endSeq,
        });
        this.reorderBuffer.clear();
      }
      if (this.reorderTimer) {
        clearTimeout(this.reorderTimer);
        this.reorderTimer = null;
      }
      this.inboundDone = true;
      if (this.inboundResolve) {
        this.inboundResolve({ value: undefined as unknown as InboundMessage, done: true });
        this.inboundResolve = null;
      }
      this.fireInboundDone();
    }
  }

  // -- Convenience iterators ------------------------------------------------

  /**
   * Decoded byte iterator. Each yield is a Uint8Array of decoded data.
   * Handles base64 and utf-8 encoding transparently. Browser-safe: uses
   * TextEncoder / atob via ./bytes.js helpers, no Buffer dependency.
   */
  async *bytes(): AsyncIterable<Uint8Array> {
    for await (const msg of this.inbound) {
      const chunks = Array.isArray(msg.data) ? msg.data : [msg.data];
      for (const chunk of chunks) {
        yield msg.encoding === 'base64'
          ? base64ToBytes(chunk as string)
          : utf8Encode(chunk as string);
      }
    }
  }

  /**
   * Flattened event iterator. Each yield is a single event object
   * unwrapped from batched event arrays.
   */
  async *events<T = unknown>(): AsyncIterable<T> {
    for await (const msg of this.inbound) {
      const items = Array.isArray(msg.data) ? msg.data : [msg.data];
      for (const item of items) {
        yield item as T;
      }
    }
  }

  /**
   * Node Readable adapter for pipe() integration.
   * Creates ONE persistent bytes() iterator and pumps from it across
   * read() calls. This ensures backpressure/resume continues the same
   * stream rather than restarting.
   *
   * Uses dynamic import of node:stream to avoid breaking browser bundles.
   */
  async readable(): Promise<import('node:stream').Readable> {
    const { Readable } = await import('node:stream');
    const iter = this.bytes()[Symbol.asyncIterator]();
    return new Readable({
      async read() {
        try {
          const { value, done } = await iter.next();
          if (done) {
            this.push(null);
          } else {
            this.push(value);
          }
        } catch {
          this.push(null);
        }
      },
    });
  }

  // -- Static factory --------------------------------------------------------

  /**
   * Create a StreamClient from a StreamDescriptor.
   *
   * This is the primary public entry point for descriptor-based construction.
   * Used internally by StreamRef.open() and directly by advanced callers.
   *
   * Consumer gating policy: when localDirection includes writing and gating
   * is not explicitly set, defaults to gating: false.
   */
  static fromDescriptor(
    descriptor: StreamDescriptor,
    options: StreamClientFromDescriptorOptions,
  ): StreamClient {
    // Consumer gating policy: default gating to false when writing
    const localDir = descriptor.localDirection;
    const canWrite = localDir === 'outbound' || localDir === 'bidirectional';
    const gating = options.gating !== undefined
      ? options.gating
      : canWrite
        ? false
        : true;

    return new StreamClient({
      subscribeKey: options.subscribeKey,
      publishKey: options.publishKey,
      token: descriptor.token,
      agentName: descriptor.agentName,
      streamId: descriptor.streamId,
      channel: descriptor.channel,
      direction: descriptor.localDirection,
      format: descriptor.format,
      affinity: descriptor.affinity,
      maxMessageSize: options.maxMessageSize,
      bundleSizeBytes: options.bundleSizeBytes,
      maxLatencyMs: options.maxLatencyMs,
      gating,
      reorderTimeoutMs: options.reorderTimeoutMs,
    });
  }
}
