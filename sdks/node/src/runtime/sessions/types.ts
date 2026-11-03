export type SessionRole = 'consumer' | 'provider';

export type SessionState = 'open' | 'ready' | 'closing' | 'closed' | 'failed' | 'canceled';

export const TERMINAL_SESSION_STATES: ReadonlySet<SessionState> = new Set([
  'closed',
  'failed',
  'canceled',
]);

export type ExchangeOutcomeState = 'completed' | 'failed' | 'canceled';

export type SessionMessageType =
  | 'session_open'
  | 'session_ready'
  | 'exchange'
  | 'exchange_cancel'
  | 'exchange_progress'
  | 'exchange_artifact'
  | 'exchange_outcome'
  | 'stream_started'
  | 'session_close'
  | 'session_closed'
  | 'session_failed'
  | 'route_probe';

/** A session-channel message body as received; fields beyond `type` are read per type. */
export type SessionMessage = Record<string, unknown> & { type: string; seq?: unknown };

/** The session a receiver serves and the transport UUIDs it accepts messages from. */
export interface SessionParties {
  sessionId: string;
  counterpartUuid: string;
  backendUuid: string;
}

/** Backend notices carry this `seq`; they are not sequenced. */
export const BACKEND_NOTICE_SEQ = 0;

export const MAX_EXCHANGE_ID_LENGTH = 64;

export interface ExchangeProgress {
  exchangeId: string;
  message?: string;
  progress?: unknown;
}

export interface ExchangeArtifact {
  exchangeId: string;
  artifactRef: unknown;
  outputId?: string;
}

export interface ExchangeOutcome {
  exchangeId: string;
  state: ExchangeOutcomeState;
  error?: string;
  reason?: string;
  artifacts: unknown[];
}

export interface StartedExchange {
  exchangeId: string;
  requestParts: unknown[];
}
