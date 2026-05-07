/**
 * Stream ID validation.
 *
 * Stream IDs appear in the channel name: stream.{agentName}.{streamId}
 * Dots are disallowed because they are PubNub channel hierarchy separators.
 */

import { utf8ByteLength } from './bytes.js';

const STREAM_ID_REGEX = /^[a-zA-Z0-9\-_]+$/;
const MAX_STREAM_ID_BYTES = 92;

/**
 * Validate a stream ID. Throws if the ID is invalid.
 *
 * Rules:
 * - Cannot be empty
 * - Cannot exceed 92 bytes
 * - Only [a-zA-Z0-9-_] allowed (no dots)
 */
export function validateStreamId(id: string): void {
  if (id.length === 0) {
    throw new Error('Stream ID cannot be empty');
  }
  if (utf8ByteLength(id) > MAX_STREAM_ID_BYTES) {
    throw new Error('Stream ID exceeds 92 byte limit');
  }
  if (!STREAM_ID_REGEX.test(id)) {
    throw new Error(
      'Stream ID contains invalid characters. Allowed: a-z, A-Z, 0-9, -, _',
    );
  }
}
