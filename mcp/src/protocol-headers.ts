/**
 * Shared protocol-version header constants for direct-HTTP backend
 * callers (`registry-list`, `agent-status`, `billing`).
 *
 * MUST stay in sync with
 * blocks-sdk/sdks/node/src/runtime/protocol-version.ts (the canonical
 * source). Until the SDK exports these from its public surface, both
 * files must update atomically when the protocol version bumps.
 */

export const PROTOCOL_VERSION_HEADER = 'Blocks-Protocol-Version';
export const CURRENT_PROTOCOL_VERSION = '2026-05-01';
