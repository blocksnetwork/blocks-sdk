/**
 * Stream Processing Context
 *
 * Wraps a StreamClient from the Stream SDK and manages the onActivate
 * callback lifecycle. Each embedded stream ID maps to one processing
 * context. The context owns the StreamClient instance and provides the
 * stream object interface to onActivate callbacks.
 *
 * onActivate runs as an async task (microtask/promise) on the stream
 * processing context, not the handler thread. Unhandled errors in
 * onActivate trigger automatic failStream.
 *
 * `StreamObject.end()` is task-scoped: it delegates to the agent-instance
 * `releaseStream(streamId, taskId)` hook, which evicts the per-task handle
 * cache entry, releases the registry taskId, and — only when the last
 * ref-holder releases — tears down the underlying StreamClient. On shared
 * streams the teardown never publishes a `stream_end` marker (see
 * SDK_CONTRACT §8.4.1a shared-affinity carve-out).
 */

import type { StreamClient, InboundMessage, StreamError } from '../stream/index.js';
import { log as baseLog } from './logger.js';

const log = (
  level: 'debug' | 'info' | 'warn' | 'error',
  message: string,
  meta?: Record<string, unknown>,
): void => baseLog('[StreamContext]', level, message, meta);

/** Hook the agent-instance passes to the stream wrapper so `end()` can
 * route through the registry + handle-cache eviction path. Decouples
 * stream-context.ts from agent-instance.ts (no import cycle). */
export interface AgentInstanceHooks {
  releaseStream: (streamId: string, taskId: string) => Promise<void>;
}

/** The stream object passed to onActivate and returned from createStream.
 *
 * The read/error/uuid surface mirrors the underlying `StreamClient` so
 * handlers and consumers see the same API. `onInboundDone` is intentionally
 * NOT forwarded — it's classified internal-only by SDK_CONTRACT §8.3.8.
 * Handlers needing a "stream drained" signal should `await` the
 * for-await-of loop on `bytes()` / `events()` / `inbound`.
 */
export interface StreamObject {
  readonly streamId: string;
  readonly channel: string;
  readonly isActive: boolean;
  readonly external: boolean;
  /** Underlying StreamClient uuid for log correlation. */
  readonly uuid: string;
  write(data: string | Uint8Array | unknown): void;
  end(): Promise<void>;
  /** Low-level wire iterator. Prefer `bytes()` / `events()` for decoded reads. */
  readonly inbound: AsyncIterable<InboundMessage>;
  /** Decoded byte iterator. Recommended for `format: bytes` streams. */
  bytes(): AsyncIterable<Uint8Array>;
  /** Flattened event iterator. Recommended for `format: events` streams. */
  events<T = unknown>(): AsyncIterable<T>;
  /** Node `Readable` adapter for piping into files / subprocesses. */
  readable(): Promise<import('node:stream').Readable>;
  onEnd(cb: () => void): void;
  /** Subscribe to stream-level errors (PAM revocation, network failure, …). */
  onError(cb: (err: StreamError) => void): void;
  /** T7a token (only available on external streams). */
  readonly token?: string;
  /** Activate an external stream (only available on external streams). */
  activate?(opts?: { metadata?: Record<string, unknown> }): Promise<void>;
}

export type OnActivateCallback = (stream: StreamObject) => void | Promise<void>;
export type FailStreamCallback = (streamId: string, error: string) => Promise<void>;

/**
 * Create a StreamObject wrapper around a StreamClient.
 * This is the shape passed to onActivate and returned from createStream.
 *
 * `hooks.releaseStream` — when provided, `end()` delegates to it so the
 * agent-instance can run task-scoped cleanup (registry release,
 * handle-cache eviction, affinity-gated teardown). Omit for synthetic
 * test wrappers with no agent-instance owner — `end()` falls back to
 * calling `client.end()` directly.
 */
export function createStreamObject(
  streamId: string,
  client: StreamClient,
  taskId?: string,
  hooks?: AgentInstanceHooks,
): StreamObject {
  return {
    get streamId() { return streamId; },
    get channel() { return client.channel; },
    get isActive() { return client.isActive; },
    get external() { return false; },
    get uuid() { return client.uuid; },
    write(data: string | Uint8Array | unknown) { client.write(data); },
    async end(): Promise<void> {
      if (hooks && taskId) {
        await hooks.releaseStream(streamId, taskId);
        return;
      }
      // Fallback for standalone usage (tests, non-agent-instance callers).
      await client.end();
    },
    get inbound() { return client.inbound; },
    bytes() { return client.bytes(); },
    events<T = unknown>() { return client.events<T>(); },
    readable() { return client.readable(); },
    onEnd(cb: () => void) { client.onEnd(cb); },
    onError(cb: (err: StreamError) => void) { client.onError(cb); },
  };
}

/**
 * Create a StreamObject for an external stream (no StreamClient).
 * write() and inbound throw; token and activate are available.
 */
export function createExternalStreamObject(
  streamId: string,
  channel: string,
  t7aToken: string,
  activateFn: (opts?: { metadata?: Record<string, unknown> }) => Promise<void>,
): StreamObject {
  let ended = false;
  const endCallbacks: Array<() => void> = [];

  return {
    get streamId() { return streamId; },
    get channel() { return channel; },
    get isActive() { return !ended; },
    get external() { return true; },
    get uuid(): string {
      throw new Error('Cannot read uuid on an external stream');
    },
    write() { throw new Error('Cannot write to an external stream -- use Stream SDK directly'); },
    async end() {
      ended = true;
      for (const cb of endCallbacks) { cb(); }
      endCallbacks.length = 0;
    },
    get inbound(): AsyncIterable<InboundMessage> {
      throw new Error('Cannot read from an external stream');
    },
    bytes(): AsyncIterable<Uint8Array> {
      throw new Error('Cannot read from an external stream');
    },
    events<T = unknown>(): AsyncIterable<T> {
      throw new Error('Cannot read from an external stream');
    },
    readable(): Promise<import('node:stream').Readable> {
      throw new Error('Cannot read from an external stream');
    },
    onEnd(cb: () => void) { endCallbacks.push(cb); },
    onError(_cb: (err: StreamError) => void) {
      throw new Error('Cannot subscribe to errors on an external stream');
    },
    get token() { return t7aToken; },
    activate: activateFn,
  };
}

/**
 * Run the onActivate callback on the stream processing context.
 * Returns a promise that resolves when the callback completes.
 * If the callback throws, the error is caught and failStream is called.
 */
export function runOnActivate(
  streamId: string,
  streamObject: StreamObject,
  callback: OnActivateCallback,
  failStreamCb: FailStreamCallback,
): Promise<void> {
  return Promise.resolve()
    .then(() => callback(streamObject))
    .catch(async (err: unknown) => {
      const errMsg = (err instanceof Error) ? err.message : String(err);
      log('error', `onActivate error for stream "${streamId}"`, {
        event: 'stream_context_on_activate_error',
        streamId,
        error: errMsg,
      });
      try {
        await failStreamCb(streamId, 'stream_crashed');
      } catch (failErr) {
        log('error', `failStream error for "${streamId}"`, {
          event: 'stream_context_fail_stream_error',
          streamId,
          error: failErr instanceof Error ? failErr.message : String(failErr),
        });
      }
    });
}
