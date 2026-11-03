import { preflightAuthOrThrow, type AuthProvider } from '../auth-provider.js';
import { createLogger } from '../logger.js';
import { CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER } from '../protocol-version.js';
import { isRateLimited, retryAfterFrom } from '../rate-limit.js';
import { stripProtectedHeaders } from '../rpc-client.js';
import { captureAffinity, injectAffinity } from '../write-affinity.js';
import { withinDeadline } from './deadlines.js';
import { SessionError, sessionErrorFromResponse } from './session-errors.js';
import type { StreamCredentials } from './stream-types.js';
import type { SessionState, StartedExchange } from './types.js';

const logger = createLogger('Session');

export interface SessionHttpConfig {
  baseUrl: string;
  authProvider?: AuthProvider;
  /** Caller request headers, merged under the SDK's own. */
  headers?: Record<string, string>;
}

/** What `POST /api/v1/sessions` returns: the session, its channel, and the consumer's credential. */
export interface OpenedSession {
  sessionId: string;
  sessionChannel: string;
  pamToken: string;
  ttlMinutes: number;
  consumerTransportUuid: string;
  providerTransportUuid: string;
  backendTransportUuid: string;
  /** Set when the backend answered a repeated `idempotencyKey` with the session it already opened. */
  idempotent?: boolean;
  /** The replayed session's state, present with `idempotent`. */
  state?: SessionState;
  /** True when the agent is a Connector served by the backend; missing means false. */
  managed?: boolean;
}

const OPENED_SESSION_STRINGS = [
  'sessionId',
  'sessionChannel',
  'pamToken',
  'consumerTransportUuid',
  'providerTransportUuid',
  'backendTransportUuid',
] as const;

/** What `POST /api/v1/sessions/{sessionId}/token` returns. */
export interface SessionToken {
  pamToken: string;
  ttlMinutes: number;
}

export type SessionBeacon = 'opened' | 'closed' | 'alive';

export interface ManagedAnswer {
  status: number;
  exchangeId?: string;
  state?: string;
  stream?: StreamCredentials;
}

const FINAL_EXCHANGE_STATES: ReadonlySet<unknown> = new Set(['completed', 'failed', 'canceled']);

/** A `200` that reports a final state; the result itself arrives on the channel only. */
export function answersFinal(answer: ManagedAnswer): boolean {
  return answer.status === 200 && FINAL_EXCHANGE_STATES.has(answer.state);
}

export const SNAPSHOT_TIMEOUT_MS = 10_000;

export type RecordedFileRef = { fileId: string } & Record<string, unknown>;

/** One settled exchange as the backend recorded it; `artifactRefs` holds file-type refs only. */
export interface SnapshotExchange {
  exchangeId: string;
  state: string;
  error: string | null;
  artifactRefs: RecordedFileRef[];
}

/** What `GET /api/v1/sessions/{sessionId}` returns: the backend's record of the session. */
export interface SessionSnapshot {
  sessionId: string;
  state: string;
  reason: string | null;
  error: string | null;
  exchanges: SnapshotExchange[];
}

const SNAPSHOT_STATES: ReadonlySet<unknown> = new Set<SessionState>([
  'open',
  'ready',
  'closing',
  'closed',
  'failed',
  'canceled',
]);
const SNAPSHOT_OUTCOMES: ReadonlySet<unknown> = new Set(['completed', 'failed', 'canceled']);

function isRecordedFileRef(ref: unknown): ref is RecordedFileRef {
  return typeof (ref as { fileId?: unknown } | null)?.fileId === 'string';
}

function parseSessionSnapshot(body: unknown): SessionSnapshot {
  const value = (body ?? {}) as Record<string, unknown>;
  const exchanges = value.exchanges;
  if (
    typeof value.sessionId !== 'string' ||
    !SNAPSHOT_STATES.has(value.state) ||
    !Array.isArray(exchanges) ||
    !exchanges.every(
      (x) =>
        typeof x === 'object' &&
        x !== null &&
        typeof (x as SnapshotExchange).exchangeId === 'string' &&
        SNAPSHOT_OUTCOMES.has((x as SnapshotExchange).state) &&
        Array.isArray((x as SnapshotExchange).artifactRefs) &&
        (x as SnapshotExchange).artifactRefs.every(isRecordedFileRef),
    )
  ) {
    throw new Error('GET /api/v1/sessions/{sessionId} returned an invalid snapshot');
  }
  const text = (v: unknown) => (typeof v === 'string' ? v : null);
  return {
    sessionId: value.sessionId,
    state: value.state as string,
    reason: text(value.reason),
    error: text(value.error),
    exchanges: (exchanges as SnapshotExchange[]).map((x) => ({
      exchangeId: x.exchangeId,
      state: x.state,
      error: text(x.error),
      artifactRefs: x.artifactRefs,
    })),
  };
}

function parseOpenedSession(body: unknown): OpenedSession {
  const value = (body ?? {}) as Record<string, unknown>;
  const invalid: string[] = OPENED_SESSION_STRINGS.filter(
    (field) => typeof value[field] !== 'string' || value[field] === '',
  );
  if (typeof value.ttlMinutes !== 'number') invalid.push('ttlMinutes');
  if (value.idempotent !== undefined && typeof value.idempotent !== 'boolean') {
    invalid.push('idempotent');
  }
  if (value.managed !== undefined && typeof value.managed !== 'boolean') invalid.push('managed');
  // A replay's state decides whether it may be joined, so an unknown one is refused, not guessed.
  if (value.idempotent === true && !SNAPSHOT_STATES.has(value.state)) invalid.push('state');
  if (invalid.length > 0) {
    throw new Error(`POST /api/v1/sessions returned an invalid session: ${invalid.join(', ')}`);
  }
  return value as unknown as OpenedSession;
}

function parseSessionToken(body: unknown): SessionToken {
  const value = (body ?? {}) as Record<string, unknown>;
  const { pamToken, ttlMinutes } = value;
  if (typeof pamToken !== 'string' || pamToken === '') {
    throw new Error('session token response has no pamToken');
  }
  if (typeof ttlMinutes !== 'number' || !Number.isFinite(ttlMinutes) || ttlMinutes <= 0) {
    throw new Error('session token ttlMinutes must be a positive number');
  }
  return { pamToken, ttlMinutes };
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value !== '';
}

function parseStreamCredentials(body: unknown): StreamCredentials {
  const value = (body ?? {}) as Record<string, unknown>;
  const { streamId, subscribeKey, origin, channel, wildcard, mode } = value;
  const invalid: string[] = [];
  if (!nonEmptyString(streamId)) invalid.push('streamId');
  if (!nonEmptyString(subscribeKey)) invalid.push('subscribeKey');
  if (origin !== undefined && origin !== null && !nonEmptyString(origin)) invalid.push('origin');
  if (nonEmptyString(channel) === nonEmptyString(wildcard)) invalid.push('channel or wildcard');
  if (mode !== 'access_manager' && mode !== 'consent') invalid.push('mode');
  if (mode === 'access_manager') {
    if (!nonEmptyString(value.token)) invalid.push('token');
    if (typeof value.ttlMinutes !== 'number' || !(value.ttlMinutes > 0)) invalid.push('ttlMinutes');
    if (!nonEmptyString(value.expiresAt)) invalid.push('expiresAt');
  }
  if (invalid.length > 0) {
    throw new Error(`stream credentials are invalid: ${invalid.join(', ')}`);
  }
  return { ...value, origin: origin ?? null } as unknown as StreamCredentials;
}

function parseManagedAnswer(status: number, body: unknown): ManagedAnswer {
  const value = (typeof body === 'object' && body !== null ? body : {}) as Record<string, unknown>;
  const answer: ManagedAnswer = { status };
  if (typeof value.exchangeId === 'string') answer.exchangeId = value.exchangeId;
  if (typeof value.state === 'string') answer.state = value.state;
  if (value.stream !== undefined) {
    // A malformed object is dropped, not retried: the stream token endpoint serves the same ones.
    try {
      answer.stream = parseStreamCredentials(value.stream);
    } catch (err) {
      logger('warn', 'exchange answer carries invalid stream credentials', { error: String(err) });
    }
  }
  return answer;
}

/** A session endpoint answered with an HTTP error that carries no session code. */
export class SessionHttpError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** The response's `Retry-After`, when it had one. */
    readonly retryAfterMs?: number,
  ) {
    super(message);
    this.name = 'SessionHttpError';
  }
}

const MANAGED_CAPABILITY = 'managed';

const TRANSIENT_STATUSES: ReadonlySet<number> = new Set([408, 429]);

/** A 4xx other than 408 or 429: the backend turned the call down, and retrying will not help. */
export function isRefusalStatus(status: number): boolean {
  return status >= 400 && status < 500 && !TRANSIENT_STATUSES.has(status);
}

/** True when the backend answered and turned the call down, as opposed to failing in transit. */
export function isRefusal(err: unknown): boolean {
  if (err instanceof SessionError) return true;
  if (!(err instanceof SessionHttpError)) return false;
  return isRefusalStatus(err.status);
}

function parseRefusal(text: string): { code?: unknown; error?: unknown } | undefined {
  try {
    const parsed: unknown = JSON.parse(text);
    return typeof parsed === 'object' && parsed !== null ? parsed : undefined;
  } catch {
    return undefined;
  }
}

/** The consumer's session endpoints on the Blocks backend. */
export class SessionHttp {
  constructor(private readonly config: SessionHttpConfig) {}

  async openSession(
    agentName: string,
    idempotencyKey?: string,
    signal?: AbortSignal,
  ): Promise<OpenedSession> {
    const body = await this.request(
      '/api/v1/sessions',
      {
        agentName,
        capabilities: [MANAGED_CAPABILITY],
        ...(idempotencyKey !== undefined ? { idempotencyKey } : {}),
      },
      signal,
    );
    return parseOpenedSession(body);
  }

  async refreshToken(sessionId: string): Promise<SessionToken> {
    const body = await this.request(`/api/v1/sessions/${encodeURIComponent(sessionId)}/token`, {});
    return parseSessionToken(body);
  }

  /** `exchange` rides only an `opened` beacon of a managed session. */
  async sendBeacon(
    sessionId: string,
    beacon: SessionBeacon,
    exchange?: StartedExchange,
    signal?: AbortSignal,
  ): Promise<ManagedAnswer> {
    const { status, body } = await this.exchange(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/beacons`,
      { beacon, ...(exchange ? { exchange } : {}) },
      signal,
    );
    return parseManagedAnswer(status, body);
  }

  async postExchange(
    sessionId: string,
    exchange: StartedExchange,
    signal?: AbortSignal,
  ): Promise<ManagedAnswer> {
    const { status, body } = await this.exchange(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/exchanges`,
      exchange,
      signal,
    );
    return parseManagedAnswer(status, body);
  }

  async cancelExchange(
    sessionId: string,
    exchangeId: string,
    signal?: AbortSignal,
  ): Promise<ManagedAnswer> {
    const { status, body } = await this.exchange(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/exchanges/${encodeURIComponent(exchangeId)}/cancel`,
      {},
      signal,
    );
    return parseManagedAnswer(status, body);
  }

  async fetchStreamToken(
    sessionId: string,
    streamId: string,
    signal?: AbortSignal,
  ): Promise<StreamCredentials> {
    const body = await this.request(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/streams/${encodeURIComponent(streamId)}/token`,
      {},
      signal,
    );
    return parseStreamCredentials(body);
  }

  /**
   * Gives up after `timeoutMs`, auth preparation included, so a stalled read cannot hold a
   * recovery open.
   */
  async getSession(
    sessionId: string,
    timeoutMs = SNAPSHOT_TIMEOUT_MS,
  ): Promise<SessionSnapshot> {
    const controller = new AbortController();
    const body = await withinDeadline(
      this.request(
        `/api/v1/sessions/${encodeURIComponent(sessionId)}`,
        undefined,
        controller.signal,
        'GET',
      ),
      timeoutMs,
      'session snapshot',
      (err) => controller.abort(err),
    );
    const snapshot = parseSessionSnapshot(body);
    if (snapshot.sessionId !== sessionId) {
      throw new Error(
        `GET /api/v1/sessions/{sessionId} returned the snapshot of another session (${snapshot.sessionId})`,
      );
    }
    return snapshot;
  }

  private async request(
    path: string,
    body: unknown,
    signal?: AbortSignal,
    method: 'GET' | 'POST' = 'POST',
  ): Promise<unknown> {
    return (await this.exchange(path, body, signal, method)).body;
  }

  private async exchange(
    path: string,
    body: unknown,
    signal?: AbortSignal,
    method: 'GET' | 'POST' = 'POST',
  ): Promise<{ status: number; body: unknown }> {
    const provider = this.config.authProvider;
    await preflightAuthOrThrow(provider);
    await provider?.ensureReady?.();

    const url = `${this.config.baseUrl.replace(/\/+$/, '')}${path}`;
    const send = async () => {
      const headers: Record<string, string> = { ...this.config.headers };
      stripProtectedHeaders(headers);
      if (method === 'POST') headers['Content-Type'] = 'application/json';
      headers[PROTOCOL_VERSION_HEADER] = CURRENT_PROTOCOL_VERSION;
      const auth = provider?.getAuthHeader();
      if (auth) headers['Authorization'] = auth;
      injectAffinity(headers);
      // Bearer transport: omit cookies, or a same-origin browser attaches the
      // dashboard session cookie, the backend authenticates that instead and
      // CSRF then refuses the call. See the file-upload helper for the same rule.
      const response = await fetch(url, {
        method,
        credentials: 'omit',
        headers,
        ...(method === 'POST' ? { body: JSON.stringify(body) } : {}),
        signal,
      });
      captureAffinity(response.headers);
      return response;
    };

    let response = await send();
    if (response.status === 401 && provider) {
      if (await provider.onAuthFailure()) {
        response = await send();
      } else {
        // A credential refresh held back by a rate limit is a wait, not a refusal of this call.
        const authError = provider.getLastAuthError?.();
        if (isRateLimited(authError)) throw authError;
      }
    }
    if (response.ok) {
      return {
        status: response.status,
        body: response.status === 204 ? undefined : await response.json().catch(() => undefined),
      };
    }
    const text = await response.text().catch(() => '');
    const refusal = parseRefusal(text);
    throw (
      sessionErrorFromResponse(refusal?.code, refusal?.error) ??
      new SessionHttpError(
        response.status,
        `${method} ${path} failed: HTTP ${response.status}${text ? ` ${text}` : ''}`,
        retryAfterFrom(response.headers),
      )
    );
  }
}
