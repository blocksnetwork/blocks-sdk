/** Base class of every session error; `code` is the backend or channel error code. */
export class SessionError extends Error {
  constructor(
    readonly code: string,
    message: string = code,
  ) {
    super(message);
    this.name = new.target.name;
  }
}

/** Sessions are not enabled for this deployment, the caller's org, or the agent's org. */
export class SessionsDisabledError extends SessionError {
  constructor(message = 'Sessions are not enabled') {
    super('SESSIONS_DISABLED', message);
  }
}

/** Sessions serve free agents only. */
export class SessionPaidAgentError extends SessionError {
  constructor(message = 'Sessions do not support paid agents') {
    super('SESSION_PAID_AGENT_UNSUPPORTED', message);
  }
}

/** The agent has no live, healthy route with capacity. */
export class SessionNoRouteError extends SessionError {
  constructor(message = 'The agent has no route available for a session') {
    super('SESSION_NO_ROUTE', message);
  }
}

/** Unknown session, or one the caller is not a party to. */
export class SessionNotFoundError extends SessionError {
  constructor(message = 'Session not found') {
    super('SESSION_NOT_FOUND', message);
  }
}

/** `send()` would exceed the session's pending-exchange bound. */
export class SessionBackpressureError extends SessionError {
  constructor(limit: number) {
    super('session_backpressure', `A session holds at most ${limit} pending exchanges`);
  }
}

/** The session failed; `code` is the failure reason, such as `route_lost`. */
export class SessionFailedError extends SessionError {
  constructor(code: string) {
    super(code, `Session failed: ${code}`);
  }
}

/**
 * An exchange's `stream()` cannot deliver: the exchange ended `failed` or `canceled` without a
 * stream (`code` is the outcome's `reason`, else `error`, else the state), the stream's kind is
 * unsupported (`stream_kind_unsupported`) or its credentials never arrived
 * (`stream_credentials_unavailable`).
 *
 * @experimental Part of the sessions preview; see {@link Session}.
 */
export class SessionStreamError extends SessionError {
  constructor(
    code: string,
    readonly state: 'failed' | 'canceled',
  ) {
    super(code, `Stream exchange ${state}: ${code}`);
  }
}

export const MANAGED_ERROR_CODES = {
  notManaged: 'SESSION_NOT_MANAGED',
  busy: 'SESSION_BUSY',
  notReady: 'SESSION_NOT_READY',
  exchangeNotFound: 'EXCHANGE_NOT_FOUND',
  streamNotFound: 'STREAM_NOT_FOUND',
  managedUnsupported: 'SESSION_MANAGED_UNSUPPORTED',
  exposureRevoked: 'EXPOSURE_REVOKED',
  linkedKeysetDisabled: 'LINKED_KEYSET_DISABLED',
} as const;

export const SESSION_ACCESS_REVOKED = 'session_access_revoked';

/** The code of the error a `SESSION_ALREADY_OPEN` refusal becomes, as an idempotent replay's. */
export const SESSION_ALREADY_OPEN = 'session_already_open';

const HTTP_ERRORS = new Map<string, (message?: string) => SessionError>([
  ['SESSIONS_DISABLED', (m) => new SessionsDisabledError(m)],
  ['SESSION_PAID_AGENT_UNSUPPORTED', (m) => new SessionPaidAgentError(m)],
  ['SESSION_NO_ROUTE', (m) => new SessionNoRouteError(m)],
  ['SESSION_NOT_FOUND', (m) => new SessionNotFoundError(m)],
  ['SESSION_ALREADY_OPEN', (m) => new SessionError(SESSION_ALREADY_OPEN, m)],
  ...Object.values(MANAGED_ERROR_CODES).map(
    (code): [string, (message?: string) => SessionError] => [
      code,
      (m) => new SessionError(code, m),
    ],
  ),
]);

/** Maps a session endpoint's `{ code, error }` refusal to its typed error, or null when the code is not a session code. */
export function sessionErrorFromResponse(code: unknown, message: unknown): SessionError | null {
  const build = typeof code === 'string' ? HTTP_ERRORS.get(code) : undefined;
  return build ? build(typeof message === 'string' ? message : undefined) : null;
}
