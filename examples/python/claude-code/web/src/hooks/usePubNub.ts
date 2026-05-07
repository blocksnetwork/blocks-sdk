import PubNub from 'pubnub';
import { getConfig } from '../config.ts';

/**
 * Singleton PubNub client and owner ID management.
 *
 * The PubNub client is initialized once on first access and reused
 * across the application. The ownerId is a stable per-session identifier
 * generated on first access using crypto.randomUUID().
 */

let _pubnub: PubNub | null = null;
let _ownerId: string | null = null;

/**
 * Get or generate a stable per-session owner ID.
 * Format: "cc-web-{8-char-uuid-prefix}"
 */
export function getOwnerId(): string {
  if (!_ownerId) {
    _ownerId = `cc-web-${crypto.randomUUID().slice(0, 8)}`;
  }
  return _ownerId;
}

/**
 * Get or create the singleton PubNub client instance.
 * Requires config to be loaded first (via loadConfig()).
 */
export function getPubNub(): PubNub {
  if (!_pubnub) {
    const config = getConfig();
    _pubnub = new PubNub({
      subscribeKey: config.subscribeKey,
      userId: getOwnerId(),
      enableEventEngine: true,
      restore: true,
    });
  }
  return _pubnub;
}
