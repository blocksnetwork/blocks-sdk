/**
 * @blocks-network/sdk/stream - Public API surface.
 *
 * Exports the StreamClient class, StreamDescriptor type, invertDirection
 * helper, validateStreamId validator, and all shared types.
 */

export { StreamClient } from './stream-client.js';
export type { StreamError } from './stream-client.js';
export { type StreamDescriptor, type StreamAffinity, invertDirection } from './descriptor.js';
export { validateStreamId } from './validate.js';
export type {
  StreamFormat,
  StreamDirection,
  StreamClientOptions,
  StreamClientFromDescriptorOptions,
  InboundMessage,
  StreamBundleConfig,
} from './types.js';
