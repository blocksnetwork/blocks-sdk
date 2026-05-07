/**
 * Type definitions for PubNub SDK compatibility.
 *
 * The PubNub SDK has a strict `Payload` type that doesn't accept `unknown`.
 * This file provides type-safe interfaces and helpers that work with both:
 * - Node.js PubNub SDK (pubnub package)
 * - PubNub Functions runtime (built-in pubnub module)
 */

import type PubNub from 'pubnub';
import type { Payload } from 'pubnub';

/**
 * Cast any JSON-serializable object to PubNub's Payload type.
 * Use this when publishing messages that don't strictly satisfy the Payload type.
 */
export const asPayload = (obj: Record<string, unknown>): Payload => {
  // The object is JSON-serializable, so it's effectively a valid Payload.
  // TypeScript can't verify this at compile time due to the strict Payload type.
  return obj as unknown as Payload;
};

/**
 * Cast to Payload or undefined for meta fields.
 */
export const asPayloadMeta = (obj: Record<string, unknown>): Payload | undefined => {
  return obj as unknown as Payload;
};

/**
 * JSON-serializable value that's compatible with PubNub's Payload type.
 * Use this instead of `unknown` for message properties.
 */
export type JsonValue = string | number | boolean | null | JsonObject | JsonArray;
export interface JsonObject {
  [key: string]: JsonValue;
}
export type JsonArray = JsonValue[];

/**
 * Convert an object to a PubNub-compatible Payload.
 * This is a type assertion helper that ensures type safety at the boundary.
 */
export const toPayload = <T extends JsonObject>(obj: T): Payload => {
  return obj as unknown as Payload;
};

/**
 * Safely cast a message from PubNub to a typed object.
 */
export const fromPayload = <T>(payload: unknown): T => {
  return payload as T;
};

/**
 * Partial PubNub client interface for publish operations.
 * Compatible with both full SDK and Functions runtime.
 */
export interface PubNubPublisher {
  publish(params: {
    channel: string;
    message: Payload;
    storeInHistory?: boolean;
    sendByPost?: boolean;
    meta?: Payload;
  }): Promise<{ timetoken: string | number }>;
}

/**
 * Partial PubNub client interface for message actions.
 */
export interface PubNubMessageActions {
  addMessageAction?(params: {
    channel: string;
    messageTimetoken: string;
    action: { type: string; value: string };
  }): Promise<unknown>;

  getMessageActions?(params: {
    channel: string;
    start?: string;
    limit?: number;
  }): Promise<{ data?: Array<{ type: string; value: string; actionTimetoken?: string }> }>;
}

/**
 * Message entry from fetchMessages response.
 */
export interface FetchedMessage {
  message: unknown;
  timetoken: string;
  meta?: unknown;
}

/**
 * Partial PubNub client interface for fetching message history.
 * Uses a permissive signature to be compatible with the actual SDK's overloaded methods.
 */
export interface PubNubFetcher {
  fetchMessages?(params: {
    channels: string[];
    count?: number;
    start?: string;
    end?: string;
  }): Promise<{
    channels?: Record<string, FetchedMessage[]>;
  }>;
}

/**
 * Type helper to safely pass PubNub to functions expecting partial interfaces.
 * The SDK uses overloaded methods that don't directly match our simpler types.
 */
export const asPubNubFetcher = (
  pubnub: unknown,
): PubNubFetcher | undefined => {
  if (!pubnub || typeof pubnub !== 'object') return undefined;
  const p = pubnub as Record<string, unknown>;
  if (typeof p.fetchMessages === 'function') {
    return pubnub as PubNubFetcher;
  }
  return undefined;
};

/**
 * Combined partial PubNub interface for task index operations.
 */
export type PartialPubNubForIndex = PubNubPublisher &
  Partial<PubNubMessageActions> &
  Partial<PubNubFetcher>;

/**
 * Full PubNub client with all optional methods.
 */
export type FullPubNub = PubNub;

/**
 * Check if a PubNub client supports message actions.
 */
export const supportsMessageActions = (
  pubnub: PartialPubNubForIndex,
): pubnub is PartialPubNubForIndex & Required<PubNubMessageActions> => {
  return typeof pubnub.addMessageAction === 'function';
};

/**
 * Check if a PubNub client supports fetchMessages.
 */
export const supportsFetchMessages = (
  pubnub: PartialPubNubForIndex,
): pubnub is PartialPubNubForIndex & Required<PubNubFetcher> => {
  return typeof pubnub.fetchMessages === 'function';
};
