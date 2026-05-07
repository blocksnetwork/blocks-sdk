/**
 * StreamDescriptor type and invertDirection helper.
 *
 * StreamDescriptor is the plain data shape that bridges the Agent SDK
 * (control plane) and the Stream SDK (data plane). It carries all the
 * information needed to open a stream connection.
 *
 * invertDirection computes localDirection from agentDirection as
 * delivered in stream_started events.
 */

import type { StreamDirection, StreamFormat } from './types.js';

/** Stream affinity as declared on the agent card. */
export type StreamAffinity = 'dedicated' | 'shared';

/**
 * Plain data shape carrying all information needed to open a stream connection.
 *
 * - agentDirection: what arrived on the wire in stream_started
 * - localDirection: what the local client actually does (computed via invertDirection)
 * - affinity: 'dedicated' (per-task channel) or 'shared' (cross-task
 *   broadcast). Required: the stream_started wire event carries it, and
 *   producer-side construction has it in scope from the card declaration.
 *   Consumer-side cleanup rules consult this field to decide whether to
 *   publish a stream_end marker on a shared channel (they must not).
 */
export interface StreamDescriptor {
  taskId: string;
  streamId: string;
  agentName: string;
  channel: string;
  token: string;
  agentDirection: StreamDirection;
  localDirection: StreamDirection;
  format: StreamFormat;
  affinity: StreamAffinity;
  metadata?: Record<string, unknown>;
  /** The declared stream key from the agent card's streams block. */
  declaredStream?: string;
}

/**
 * Compute the local direction from the agent-facing direction delivered
 * in stream_started.
 *
 * - agent outbound -> consumer inbound (consumer reads)
 * - agent inbound -> consumer outbound (consumer writes)
 * - agent bidirectional -> consumer bidirectional
 */
export function invertDirection(agentDirection: StreamDirection): StreamDirection {
  switch (agentDirection) {
    case 'outbound':
      return 'inbound';
    case 'inbound':
      return 'outbound';
    case 'bidirectional':
      return 'bidirectional';
    default:
      throw new Error(`Unknown direction: ${agentDirection as string}`);
  }
}
