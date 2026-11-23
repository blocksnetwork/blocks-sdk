import type { JsonObject } from './pubnub-types.js';
import type { TerminalEvent } from './task-session.js';

/** The code of a refusal that asks the task's user to connect or approve an integration, on RPC errors and terminals. */
export const AUTH_REQUIRED_CODE = 'AuthRequired';

const DEFAULT_MESSAGE = 'Integration connection required';

// A relayed refusal is copied into each caller's own terminal, so a called agent must not be able to size it.
const MAX_FIELD_LENGTH = 128;
const MAX_URL_LENGTH = 2048;
const MAX_MESSAGE_LENGTH = 500;
/** `http(s)://host…` with no whitespace: the same rule as the Python SDK, whatever each URL parser would repair. */
const HTTP_URL = /^https?:\/\/[^\s/]+\S*$/i;

/** A called task failed needing its user's approval. The fields are that agent's words: check `connectUrl`. */
export class RelayedAuthRequiredError extends Error {
  integration: string;
  reason: string;
  connectUrl: string;

  constructor(message: string, integration: string, reason: string, connectUrl: string) {
    super(message);
    this.name = 'RelayedAuthRequiredError';
    this.integration = integration;
    this.reason = reason;
    this.connectUrl = connectUrl;
  }
}

const RELAYED_ERROR_NAMES = new Set([
  'AuthRequiredError',
  'ConnectionRequiredError',
  'RelayedAuthRequiredError',
]);

interface AuthRequiredDetails {
  integration: string;
  reason: string;
  connectUrl: string;
}

function isHttpUrl(value: string): boolean {
  if (value.length > MAX_URL_LENGTH || !HTTP_URL.test(value)) return false;
  try {
    new URL(value);
    return true;
  } catch {
    return false;
  }
}

const isShortText = (value: unknown): value is string =>
  typeof value === 'string' && value.length > 0 && value.length <= MAX_FIELD_LENGTH;

/** The three fields as short strings, with an http(s) link: these travel through agents, so nothing else is passed on. */
function detailsOf(value: unknown): AuthRequiredDetails | null {
  if (!value || typeof value !== 'object') return null;
  const { integration, reason, connectUrl } = value as Record<string, unknown>;
  return isShortText(integration) &&
    isShortText(reason) &&
    typeof connectUrl === 'string' &&
    isHttpUrl(connectUrl)
    ? { integration, reason, connectUrl }
    : null;
}

const messageOf = (text: unknown): string =>
  typeof text === 'string' && text ? text.slice(0, MAX_MESSAGE_LENGTH) : DEFAULT_MESSAGE;

/** Terminal fields for a handler error asking for the user's approval, else null; matched by name, so another SDK copy's error relays. */
export function authRequiredTerminalFields(
  err: unknown,
): { error: string; errorCode: string; errorDetails: JsonObject } | null {
  const details = detailsOf(err);
  if (!details || !RELAYED_ERROR_NAMES.has((err as Error).name)) return null;
  const { rpcMessage, message } = err as { rpcMessage?: unknown; message?: unknown };
  return {
    error: messageOf(typeof rpcMessage === 'string' ? rpcMessage : message),
    errorCode: AUTH_REQUIRED_CODE,
    errorDetails: { ...details },
  };
}

/** The relayed refusal a failed terminal carries when the task's user must connect or approve, else null. */
export function authRequiredFromTerminal(event: TerminalEvent): RelayedAuthRequiredError | null {
  if (event.state !== 'failed' || event.errorCode !== AUTH_REQUIRED_CODE) return null;
  const details = detailsOf(event.errorDetails);
  if (!details) return null;
  return new RelayedAuthRequiredError(
    messageOf(event.error),
    details.integration,
    details.reason,
    details.connectUrl,
  );
}
