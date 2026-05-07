/**
 * Protocol versioning constants and helpers for the Blocks Node SDK.
 *
 * Centralizes the protocol version contract so no other file needs to
 * hardcode version strings or header names.
 */

/** Current protocol version used by this SDK build. */
export const CURRENT_PROTOCOL_VERSION = '2026-05-01';

/** All protocol versions this SDK can speak. */
export const SUPPORTED_PROTOCOL_VERSIONS: readonly string[] = [CURRENT_PROTOCOL_VERSION];

/** Protocol versions that are deprecated but still functional. */
export const DEPRECATED_PROTOCOL_VERSIONS: readonly string[] = [];

/** HTTP header name for protocol version negotiation. */
export const PROTOCOL_VERSION_HEADER = 'Blocks-Protocol-Version';

/** SDK package version (kept in sync with package.json manually). */
export const SDK_VERSION = '0.1.27';

/** Check whether a given protocol version is supported by this SDK. */
export function isProtocolVersionSupported(version: string): boolean {
  return SUPPORTED_PROTOCOL_VERSIONS.includes(version);
}
