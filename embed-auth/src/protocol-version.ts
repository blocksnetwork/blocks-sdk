/**
 * Widget-local protocol-version constants.
 *
 * The Consumer SDK's `runtime/protocol-version.ts` is NOT in the SDK
 * package's public `exports` map, so the widget keeps its own copy and a
 * parity test (`test/protocol-version.parity.test.ts`) asserts byte-
 * identity with the SDK source — any SDK bump trips CI before the widget
 * can drift.
 */

export const CURRENT_PROTOCOL_VERSION = '2026-05-01';
export const PROTOCOL_VERSION_HEADER = 'Blocks-Protocol-Version';
