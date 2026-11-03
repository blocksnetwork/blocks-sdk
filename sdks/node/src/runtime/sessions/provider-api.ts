import { AgentAuthHttpError } from '../agent-auth.js';
import type { JsonValue } from '../pubnub-types.js';
import { retryAfterFrom } from '../rate-limit.js';
import type { CheckpointBatch } from './provider-checkpoints.js';
import { isRefusalStatus } from './session-http.js';

export const SESSIONS_DISABLED = 'SESSIONS_DISABLED';
export const SESSION_PAID_AGENT_UNSUPPORTED = 'SESSION_PAID_AGENT_UNSUPPORTED';
export const SESSION_NOT_FOUND = 'SESSION_NOT_FOUND';
export const ROUTE_LOST = 'ROUTE_LOST';

export const TASK_ONLY_CODES: ReadonlySet<string> = new Set([
  SESSIONS_DISABLED,
  SESSION_PAID_AGENT_UNSUPPORTED,
]);

export class ProviderApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string | undefined,
    message: string,
    /** The response's `Retry-After`, when it had one. */
    readonly retryAfterMs?: number,
  ) {
    super(message);
    this.name = 'ProviderApiError';
  }
}

export const errorCodeOf = (err: unknown): string | undefined =>
  err instanceof ProviderApiError ? err.code : undefined;

/**
 * The agent credential is refused: its own refresh was turned down, or the call still answered
 * 401 after that refresh. A refresh that fails in transit is not a refusal.
 */
export function isCredentialRefusal(err: unknown): boolean {
  if (err instanceof AgentAuthHttpError) return isRefusalStatus(err.status);
  return err instanceof ProviderApiError && err.status === 401;
}

export interface RouteGrant {
  routeId: string;
  routeChannel: string;
  pamToken: string;
  ttlMinutes: number;
  providerTransportUuid: string;
  /** Backend notices are accepted from this UUID only, so a route without one is refused. */
  backendTransportUuid: string;
}

export interface RouteToken {
  pamToken: string;
  ttlMinutes: number;
}

export interface SessionManifest {
  sessionId: string;
  routeId: string;
  consumerTransportUuid: string;
  backendTransportUuid?: string;
  agentName: string;
  orgId: string | null;
  ownerId: string;
  callerClaims: Record<string, JsonValue>;
}

export type AuthenticatedFetch = (url: string, init?: RequestInit) => Promise<Response>;

type Fields = Record<string, unknown>;

const isObject = (value: unknown): value is Fields =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const isNonEmptyString = (value: unknown): value is string =>
  typeof value === 'string' && value.length > 0;

function requireStrings(body: Fields, keys: readonly string[], what: string): void {
  const missing = keys.filter((key) => !isNonEmptyString(body[key]));
  if (missing.length > 0) throw new Error(`${what} is missing ${missing.join(', ')}`);
}

const optionalString = (value: unknown): string | undefined =>
  isNonEmptyString(value) ? value : undefined;

function parseRouteGrant(body: unknown): RouteGrant {
  if (!isObject(body)) throw new Error('route response is not an object');
  requireStrings(
    body,
    ['routeId', 'routeChannel', 'pamToken', 'providerTransportUuid', 'backendTransportUuid'],
    'route',
  );
  return {
    routeId: body.routeId as string,
    routeChannel: body.routeChannel as string,
    pamToken: body.pamToken as string,
    ttlMinutes: parseTtl(body.ttlMinutes),
    providerTransportUuid: body.providerTransportUuid as string,
    backendTransportUuid: body.backendTransportUuid as string,
  };
}

function parseTtl(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) {
    throw new Error('route token ttlMinutes must be a positive number');
  }
  return value;
}

function parseRouteToken(body: unknown): RouteToken {
  if (!isObject(body)) throw new Error('route token response is not an object');
  requireStrings(body, ['pamToken'], 'route token');
  return { pamToken: body.pamToken as string, ttlMinutes: parseTtl(body.ttlMinutes) };
}

export function parseManifest(value: unknown): SessionManifest | undefined {
  if (!isObject(value)) return undefined;
  const ids = ['sessionId', 'routeId', 'consumerTransportUuid', 'agentName', 'ownerId'];
  if (!ids.every((key) => isNonEmptyString(value[key]))) return undefined;
  if (value.orgId !== null && !isNonEmptyString(value.orgId)) return undefined;
  return {
    sessionId: value.sessionId as string,
    routeId: value.routeId as string,
    consumerTransportUuid: value.consumerTransportUuid as string,
    backendTransportUuid: optionalString(value.backendTransportUuid),
    agentName: value.agentName as string,
    orgId: value.orgId as string | null,
    ownerId: value.ownerId as string,
    callerClaims: isObject(value.callerClaims)
      ? (value.callerClaims as Record<string, JsonValue>)
      : {},
  };
}

/** The provider's session endpoints, called with the agent JWT. */
export class ProviderSessionApi {
  private readonly baseUrl: string;

  constructor(
    baseUrl: string,
    private readonly fetchFn: AuthenticatedFetch,
  ) {
    this.baseUrl = baseUrl.replace(/\/+$/, '');
  }

  async registerRoute(instanceId: string, maxSessions: number): Promise<RouteGrant> {
    const body = { instanceId, maxSessions };
    return parseRouteGrant(await this.request('POST', '/api/v1/provider/routes', body));
  }

  async refreshRouteToken(routeId: string): Promise<RouteToken> {
    const path = `/api/v1/provider/routes/${encodeURIComponent(routeId)}/token`;
    return parseRouteToken(await this.request('POST', path, {}));
  }

  async getSession(sessionId: string): Promise<SessionManifest> {
    const path = `/api/v1/provider/sessions/${encodeURIComponent(sessionId)}`;
    const manifest = parseManifest(await this.request('GET', path));
    if (!manifest) throw new Error('session lookup returned an incomplete manifest');
    return manifest;
  }

  /** Resolves only on the backend's acknowledgement of this batch; anything else is a retry. */
  async postCheckpoint(batch: CheckpointBatch): Promise<void> {
    const ack = await this.request('POST', '/api/v1/provider/checkpoints', batch);
    if (
      !isObject(ack) ||
      ack.routeId !== batch.routeId ||
      ack.idempotencyKey !== batch.idempotencyKey
    ) {
      throw new Error('checkpoint response is not the acknowledgement of this batch');
    }
  }

  private async request(method: string, path: string, body?: unknown): Promise<unknown> {
    const response = await this.fetchFn(`${this.baseUrl}${path}`, {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const parsed: unknown = await response.json().catch(() => undefined);
    if (!response.ok) {
      const fields = isObject(parsed) ? parsed : {};
      const code = optionalString(fields.code);
      const message = optionalString(fields.error) ?? `HTTP ${response.status}`;
      throw new ProviderApiError(
        response.status,
        code,
        `${method} ${path} failed: ${message}`,
        retryAfterFrom(response.headers),
      );
    }
    return parsed;
  }
}
