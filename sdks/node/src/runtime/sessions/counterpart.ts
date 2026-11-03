import { isCounterpartSeq } from './seq-window.js';
import {
  BACKEND_NOTICE_SEQ,
  type SessionMessage,
  type SessionMessageType,
  type SessionParties,
  type SessionRole,
} from './types.js';

type Accepted = '*' | ReadonlySet<SessionMessageType>;

interface AcceptRule {
  counterpart: Accepted;
  backend: Accepted;
}

const ACCEPTS: Record<SessionRole, AcceptRule> = {
  consumer: {
    counterpart: '*',
    backend: new Set(['session_closed', 'session_failed']),
  },
  provider: {
    counterpart: new Set(['session_open', 'exchange', 'exchange_cancel', 'session_close']),
    backend: new Set(['session_closed', 'session_failed', 'route_probe']),
  },
};

function allows(accepted: Accepted, type: string): boolean {
  return accepted === '*' || accepted.has(type as SessionMessageType);
}

/**
 * Whether `role` may act on a message of `type` published by `publisher`.
 * Runs before any dedup, callback or state transition; anything else is ignored.
 */
export function acceptsFromCounterpart(
  role: SessionRole,
  parties: SessionParties,
  publisher: string,
  type: string,
): boolean {
  const rule = ACCEPTS[role];
  if (publisher === parties.counterpartUuid) return allows(rule.counterpart, type);
  if (publisher === parties.backendUuid) return allows(rule.backend, type);
  return false;
}

/**
 * How a receiver handles one inbound message: drop it, act on it now (backend
 * notices, which carry no sender sequence), or sequence it by `seq`.
 */
export type Inbound = { kind: 'drop' } | { kind: 'notice' } | { kind: 'sequenced'; seq: number };

export function classifyInbound(
  role: SessionRole,
  parties: SessionParties,
  publisher: string,
  message: unknown,
): Inbound {
  if (!isSessionMessage(message)) return { kind: 'drop' };
  if (!acceptsFromCounterpart(role, parties, publisher, message.type)) return { kind: 'drop' };
  // A route probe is not a session message: its envelope has no sessionId or seq.
  // Only the provider takes one, and only from the backend, whatever envelope it carries.
  if (message.type === 'route_probe') return { kind: role === 'provider' ? 'notice' : 'drop' };
  if (message.sessionId !== parties.sessionId) return { kind: 'drop' };
  if (publisher === parties.backendUuid) {
    return message.seq === BACKEND_NOTICE_SEQ ? { kind: 'notice' } : { kind: 'drop' };
  }
  return isCounterpartSeq(message.seq) ? { kind: 'sequenced', seq: message.seq } : { kind: 'drop' };
}

function isSessionMessage(message: unknown): message is SessionMessage {
  return (
    typeof message === 'object' &&
    message !== null &&
    typeof (message as { type?: unknown }).type === 'string'
  );
}
