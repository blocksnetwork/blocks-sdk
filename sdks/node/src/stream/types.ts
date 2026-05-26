/**
 * Shared types for the Stream SDK.
 *
 * These types define the public API surface for stream configuration,
 * direction modes, and inbound message shape.
 */

/** Wire format for stream data: 'bytes' for chunked text/binary, 'events' for structured objects. */
export type StreamFormat = 'bytes' | 'events';

/** Direction of stream data flow relative to the local client. */
export type StreamDirection = 'outbound' | 'inbound' | 'bidirectional';

/** Stream affinity — re-exported from descriptor.ts for call-site convenience. */
export type StreamAffinity = 'dedicated' | 'shared';

/** Configuration for StreamBundle (internal transport engine). */
export interface StreamBundleConfig {
  /** Maximum serialized message size before multipart splitting. Default: 16384. */
  maxMessageSize: number;
  /** Flush buffer when accumulated byte size reaches this limit. Default: 4096. */
  bundleSizeBytes: number;
  /** Flush buffer after this many ms since first unflushed write. Default: 250. */
  maxLatencyMs: number;
  /** Publisher UUID for meta.sender on every publish. */
  uuid: string;
}

/** Options for constructing a StreamClient directly. */
export interface StreamClientOptions {
  subscribeKey: string;
  publishKey: string;
  /** Per-stream token (T7a from setup handshake). */
  token: string;
  /** Agent name. Required. */
  agentName: string;
  streamId: string;
  /** Explicit channel name. Default: computed as stream.{agentName}.{streamId}. */
  channel?: string;
  /** Wire format. Default: 'bytes'. */
  format?: StreamFormat;
  /** Direction of data flow. Default: 'outbound'. */
  direction?: StreamDirection;
  /** Max serialized message size before multipart splitting. Default: 16384 (or STREAM_MAX_MESSAGE_SIZE). */
  maxMessageSize?: number;
  /** Flush buffer at this byte size. Default: 4096 (or STREAM_BUNDLE_SIZE). */
  bundleSizeBytes?: number;
  /** Flush buffer after this many ms. Default: 250 (or STREAM_MAX_LATENCY_MS). */
  maxLatencyMs?: number;
  /** Presence gating. Default: true (or STREAM_GATING). */
  gating?: boolean;
  /** Reorder buffer timeout in ms. Default: 750. Set to 0 to disable. */
  reorderTimeoutMs?: number;
  /**
   * Stream affinity — gates the `stream_end` publish on shared channels.
   * Shared streams never publish the end marker from either producer or
   * consumer-writer paths (SDK_CONTRACT §8.4.1a shared-affinity carve-out).
   * Default: 'dedicated'.
   */
  affinity?: StreamAffinity;
}

/** Options for StreamClient.fromDescriptor(). Keys not in the descriptor. */
export interface StreamClientFromDescriptorOptions {
  subscribeKey: string;
  publishKey: string;
  maxMessageSize?: number;
  bundleSizeBytes?: number;
  maxLatencyMs?: number;
  gating?: boolean;
  /** Reorder buffer timeout in ms. Default: 750. Set to 0 to disable. */
  reorderTimeoutMs?: number;
  /**
   * Consumer's user ID. When set, used as the publisher-identity prefix in
   * the StreamClient UUID so that consumer-side publishes carry an identity
   * derived from the consumer rather than the provider's agentName. Required
   * for correct self-echo filtering on bidirectional streams where the
   * consumer and provider share the same agentName. Omitting it falls back
   * to descriptor.agentName (legacy behavior — safe only for unidirectional
   * streams).
   */
  consumerUserId?: string;
}

/** Normalized inbound message yielded by the inbound async iterator. */
export interface InboundMessage {
  data: unknown;
  seq: number;
  ts: number;
  format: 'bytes' | 'events' | 'raw';
  encoding: string;
}
