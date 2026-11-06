import type PubNub from 'pubnub';
import { createLogger } from '../logger.js';
import { isRateLimited, rateLimitBackoffMs, retryAfterOf } from '../rate-limit.js';
import { mapTransportCategory, type TransportStatusPayload } from '../transport-categories.js';
import {
  ProviderApiError,
  type ProviderSessionApi,
  type RouteGrant,
  type SessionManifest,
  ROUTE_LOST,
  SESSION_NOT_FOUND,
  TASK_ONLY_CODES,
  errorCodeOf,
  parseManifest,
} from './provider-api.js';
import { classifyInbound } from './counterpart.js';
import { CHECKPOINT_MAX_OPEN_SESSIONS, CheckpointLoop } from './provider-checkpoints.js';
import { isRefusalStatus } from './session-http.js';
import { ProviderSession, type SessionHandlers, MAX_PENDING_EXCHANGES } from './provider-session.js';
import { MAX_SESSION_READY_TIMEOUT_MS } from './session.js';
import { TOKEN_REFRESH_AT, TOKEN_RETRY_MAX_MS, TOKEN_RETRY_MIN_MS } from './token-schedule.js';

const logger = createLogger('SessionProvider');

const ROUTE_RETRY_MIN_MS = 1_000;
const ROUTE_RETRY_MAX_MS = 60_000;
const MANIFEST_CACHE_LIMIT = 256;
const LOOKUP_RETRY_DELAYS_MS = [250, 1_000, 3_000];
const ENDED_SESSION_LIMIT = 1_024;
const MAX_SESSIONS_RANGE = { min: 1, max: CHECKPOINT_MAX_OPEN_SESSIONS };
const OPENING_BUFFER_LIMIT = MAX_PENDING_EXCHANGES * 2;
// A route that cannot subscribe in this long is replaced rather than left unoffered.
const ROUTE_CONNECT_TIMEOUT_MS = 10_000;

export interface SessionProviderRuntimeOptions {
  api: ProviderSessionApi;
  /** The control-channel identity the backend targets manifests at. */
  instanceId: string;
  maxSessions: number;
  handlers: SessionHandlers;
  /** A route PubNub client for `userId`, with connection supervision always on. */
  createClient(userId: string): PubNub;
}

interface RouteMessageEvent {
  channel?: unknown;
  publisher?: unknown;
  message?: unknown;
}

interface SessionRouteDeps extends SessionProviderRuntimeOptions {
  takeManifest(sessionId: string): SessionManifest | undefined;
  onLost(): void;
  onConnected(): void;
  /** The route never subscribed; the runtime retries on its backoff rather than at once. */
  onConnectTimeout(): void;
}

function sessionOpenFor(message: unknown, sessionId: string): boolean {
  const body = message as { type?: unknown; sessionId?: unknown } | null;
  return body?.type === 'session_open' && body.sessionId === sessionId;
}

type RouteProbe = { type: 'route_probe'; nonce?: unknown };

/** A route probe is recognized by type wherever it arrives, and never reaches a session. */
function isRouteProbe(message: unknown): message is RouteProbe {
  return (message as { type?: unknown } | null)?.type === 'route_probe';
}

class SessionRoute {
  private readonly sessions = new Map<string, ProviderSession>();
  private readonly opening = new Map<string, Array<[string, unknown]>>();
  private readonly ended = new Set<string>();
  private readonly client: PubNub;
  private readonly checkpoints: CheckpointLoop;
  private readonly sessionPrefix: string;
  private readonly listener = {
    message: (event: RouteMessageEvent) => this.onMessage(event),
    status: (event: TransportStatusPayload) => this.onStatus(event),
  };
  private connectTimer: ReturnType<typeof setTimeout> | undefined;
  private heartbeating = false;
  private refreshingOnDenial = false;
  /** A denial refresh succeeded and the subscription has not reconnected since. */
  private refreshedSinceConnect = false;
  private tokenTimer: ReturnType<typeof setTimeout> | undefined;
  /** Session lookups waiting out a retry; `close()` cuts them short. */
  private readonly lookupWaits = new Set<() => void>();
  private tokenRetryMs = TOKEN_RETRY_MIN_MS;
  private closed = false;

  constructor(
    private readonly grant: RouteGrant,
    private readonly deps: SessionRouteDeps,
  ) {
    this.sessionPrefix = grant.routeChannel.replace(/\*$/, '');
    this.client = deps.createClient(grant.providerTransportUuid);
    this.client.setToken(grant.pamToken);
    this.checkpoints = new CheckpointLoop({
      routeId: grant.routeId,
      post: (batch) => deps.api.postCheckpoint(batch),
      openSessions: () => this.liveSessionIds(),
      onRouteLost: () => deps.onLost(),
    });
  }

  get routeId(): string {
    return this.grant.routeId;
  }

  /**
   * Heartbeats start once the route channel is subscribed: the backend offers a route only after
   * its first acknowledged batch, so no session_open can be published before this route listens.
   */
  start(): void {
    this.client.addListener(this.listener);
    this.client.subscribe({ channels: [this.grant.routeChannel] });
    this.connectTimer = setTimeout(() => {
      logger('warn', 'session route did not subscribe in time; retrying', {
        routeId: this.grant.routeId,
      });
      this.deps.onConnectTimeout();
    }, ROUTE_CONNECT_TIMEOUT_MS);
    this.connectTimer.unref?.();
    this.scheduleTokenRefresh(this.grant.ttlMinutes);
  }

  private onStatus(event: TransportStatusPayload): void {
    if (this.closed) return;
    const category = mapTransportCategory(event);
    if (category === 'access_denied') {
      void this.refreshOnDenial();
      return;
    }
    if (category === 'connected' || category === 'reconnected') this.refreshedSinceConnect = false;
    if (this.heartbeating || category !== 'connected') return;
    this.heartbeating = true;
    clearTimeout(this.connectTimer);
    this.deps.onConnected();
    this.checkpoints.start();
    // The first heartbeat goes out now rather than a tick later: it is what makes the route offered.
    void this.checkpoints.flush();
  }

  close(reason: string): void {
    if (this.closed) return;
    this.closed = true;
    this.checkpoints.stop();
    clearTimeout(this.tokenTimer);
    clearTimeout(this.connectTimer);
    for (const wake of this.lookupWaits) wake();
    for (const session of this.sessions.values()) void session.endLocally(reason);
    this.sessions.clear();
    this.opening.clear();
    try {
      this.client.removeListener(this.listener);
      this.client.unsubscribe({ channels: [this.grant.routeChannel] });
      this.client.destroy();
    } catch {
      /* the client may already be torn down */
    }
  }

  private onMessage(event: RouteMessageEvent): void {
    const { channel, publisher, message } = event;
    if (this.closed || typeof channel !== 'string' || typeof publisher !== 'string') return;
    if (isRouteProbe(message)) {
      if (publisher === this.grant.backendTransportUuid) this.checkpoints.echoProbe(message.nonce);
      return;
    }
    if (!channel.startsWith(this.sessionPrefix)) return;
    const sessionId = channel.slice(this.sessionPrefix.length);
    const session = this.sessions.get(sessionId);
    if (session) {
      session.receive(publisher, message);
      return;
    }
    if (this.ended.has(sessionId)) return;
    const buffered = this.opening.get(sessionId);
    if (buffered) {
      if (buffered.length < OPENING_BUFFER_LIMIT) buffered.push([publisher, message]);
      return;
    }
    if (sessionOpenFor(message, sessionId)) {
      void this.openSession(sessionId, channel, [[publisher, message]]);
    }
  }

  private async openSession(
    sessionId: string,
    channel: string,
    buffered: Array<[string, unknown]>,
  ): Promise<void> {
    this.opening.set(sessionId, buffered);
    try {
      const manifest = await this.resolveManifest(sessionId);
      if (!manifest || this.closed) return;
      const backendUuid = manifest.backendTransportUuid ?? this.grant.backendTransportUuid;
      const [opener, openMessage] = buffered[0];
      const parties = { sessionId, counterpartUuid: manifest.consumerTransportUuid, backendUuid };
      // An opener the session would drop must not take a seat it could never release.
      if (classifyInbound('provider', parties, opener, openMessage).kind !== 'sequenced') return;
      const session = new ProviderSession({
        manifest,
        channel,
        backendUuid,
        pubnub: this.client,
        handlers: this.deps.handlers,
        record: (event) => this.checkpoints.record(event),
        onEnded: () => this.markEnded(sessionId),
      });
      if (this.seatsTaken() >= this.deps.maxSessions) {
        void session.rejectAtCapacity();
        this.markEnded(sessionId);
        return;
      }
      this.sessions.set(sessionId, session);
      for (const [publisher, message] of buffered) session.receive(publisher, message);
    } finally {
      this.opening.delete(sessionId);
    }
  }

  /**
   * A session that has started ending frees its seat and leaves `openSessions`, though it stays
   * routed until it finishes. Listing it would have the backend re-send its notice.
   */
  private liveSessionIds(): string[] {
    const ids: string[] = [];
    for (const [id, session] of this.sessions) if (!session.ending) ids.push(id);
    return ids;
  }

  private seatsTaken(): number {
    return this.liveSessionIds().length;
  }

  private markEnded(sessionId: string): void {
    this.sessions.delete(sessionId);
    this.ended.add(sessionId);
    if (this.ended.size > ENDED_SESSION_LIMIT) {
      this.ended.delete(this.ended.values().next().value as string);
    }
  }

  private async resolveManifest(sessionId: string): Promise<SessionManifest | undefined> {
    const manifest = this.deps.takeManifest(sessionId) ?? (await this.lookUpSession(sessionId));
    if (!manifest) return undefined;
    const matches = manifest.sessionId === sessionId && manifest.routeId === this.grant.routeId;
    return matches ? manifest : undefined;
  }

  /**
   * Retries a failed lookup with backoff; only SESSION_NOT_FOUND is final at once. A rate-limited
   * attempt waits out its `Retry-After` and is not counted, unless the wait would outlast the
   * longest a consumer waits for session_ready.
   */
  private async lookUpSession(sessionId: string): Promise<SessionManifest | undefined> {
    const consumerGivesUpAt = Date.now() + MAX_SESSION_READY_TIMEOUT_MS;
    let retries = 0;
    let rateLimitedAttempts = 0;
    for (;;) {
      if (this.closed) return undefined;
      let delayMs: number | undefined;
      try {
        return await this.deps.api.getSession(sessionId);
      } catch (err) {
        if (errorCodeOf(err) === SESSION_NOT_FOUND) return undefined;
        const retryAfterMs = retryAfterOf(err);
        if (isRateLimited(err)) {
          const waitMs = rateLimitBackoffMs(rateLimitedAttempts++, retryAfterMs);
          if (Date.now() + waitMs < consumerGivesUpAt) delayMs = waitMs;
        } else {
          delayMs = LOOKUP_RETRY_DELAYS_MS[retries++];
        }
        const next = delayMs === undefined ? 'dropping session_open' : 'retrying';
        logger('warn', `session lookup failed; ${next}`, {
          sessionId,
          retryInMs: delayMs,
          error: String(err),
        });
      }
      if (delayMs === undefined) return undefined;
      await this.lookupWait(delayMs);
    }
  }

  private lookupWait(delayMs: number): Promise<void> {
    return new Promise((resolve) => {
      const wake = (): void => {
        clearTimeout(timer);
        this.lookupWaits.delete(wake);
        resolve();
      };
      const timer = setTimeout(wake, delayMs);
      timer.unref?.();
      this.lookupWaits.add(wake);
    });
  }

  private scheduleTokenRefresh(ttlMinutes: number): void {
    this.setTokenTimer(ttlMinutes * 60_000 * TOKEN_REFRESH_AT);
  }

  private setTokenTimer(delayMs: number): void {
    clearTimeout(this.tokenTimer);
    this.tokenTimer = setTimeout(() => void this.refreshToken(), delayMs);
    this.tokenTimer.unref?.();
  }

  /**
   * One refresh per connection. A route that cannot refresh, or is denied again before it
   * reconnects, cannot listen, though its heartbeats would keep it offered, so it is replaced.
   */
  private async refreshOnDenial(): Promise<void> {
    if (this.refreshingOnDenial) return;
    if (this.refreshedSinceConnect) {
      logger('warn', 'route token denied again after a refresh; replacing the route', {
        routeId: this.grant.routeId,
      });
      this.deps.onLost();
      return;
    }
    this.refreshingOnDenial = true;
    try {
      const token = await this.deps.api.refreshRouteToken(this.grant.routeId);
      if (this.closed) return;
      this.client.setToken(token.pamToken);
      this.scheduleTokenRefresh(token.ttlMinutes);
      this.refreshedSinceConnect = true;
      this.client.reconnect();
    } catch (err) {
      if (this.closed) return;
      logger('warn', 'route token denied and not refreshed; replacing the route', {
        routeId: this.grant.routeId,
        error: String(err),
      });
      this.deps.onLost();
    } finally {
      this.refreshingOnDenial = false;
    }
  }

  private async refreshToken(): Promise<void> {
    try {
      const token = await this.deps.api.refreshRouteToken(this.grant.routeId);
      if (this.closed) return;
      this.client.setToken(token.pamToken);
      this.tokenRetryMs = TOKEN_RETRY_MIN_MS;
      this.scheduleTokenRefresh(token.ttlMinutes);
    } catch (err) {
      if (this.closed) return;
      // A refused refresh is not retried: this route's credential is done, so replace it.
      if (
        errorCodeOf(err) === ROUTE_LOST ||
        (err instanceof ProviderApiError && isRefusalStatus(err.status))
      ) {
        this.deps.onLost();
        return;
      }
      logger('warn', 'route token refresh failed; retrying', {
        routeId: this.grant.routeId,
        error: String(err),
      });
      this.setTokenTimer(Math.max(this.tokenRetryMs, retryAfterOf(err) ?? 0));
      this.tokenRetryMs = Math.min(this.tokenRetryMs * 2, TOKEN_RETRY_MAX_MS);
    }
  }
}

export class SessionProviderRuntime {
  private route: SessionRoute | undefined;
  private readonly manifests = new Map<string, SessionManifest>();
  private retryTimer: ReturnType<typeof setTimeout> | undefined;
  private retryDelayMs = ROUTE_RETRY_MIN_MS;
  private stopped = false;

  constructor(private readonly options: SessionProviderRuntimeOptions) {}

  /** Resolves once the first route is open, or the instance is known to be task-only. */
  start(): Promise<void> {
    return this.openRoute();
  }

  acceptManifest(raw: unknown): void {
    const manifest = parseManifest(raw);
    if (!manifest) return;
    this.manifests.delete(manifest.sessionId);
    this.manifests.set(manifest.sessionId, manifest);
    if (this.manifests.size > MANIFEST_CACHE_LIMIT) {
      const oldest = this.manifests.keys().next().value as string;
      this.manifests.delete(oldest);
    }
  }

  stop(): void {
    this.stopped = true;
    clearTimeout(this.retryTimer);
    this.route?.close('route_lost');
    this.route = undefined;
  }

  private async openRoute(): Promise<void> {
    if (this.stopped) return;
    let grant: RouteGrant;
    try {
      grant = await this.options.api.registerRoute(this.options.instanceId, this.maxSessions);
    } catch (err) {
      const code = errorCodeOf(err);
      if (code !== undefined && TASK_ONLY_CODES.has(code)) {
        logger('info', `sessions are not available for this agent (${code}); serving tasks only`, {
          code,
        });
        return;
      }
      const retryInMs = Math.max(this.retryDelayMs, retryAfterOf(err) ?? 0);
      logger('warn', 'session route registration failed; retrying', {
        retryInMs,
        error: String(err),
      });
      this.scheduleRetry(retryInMs);
      return;
    }
    if (this.stopped) return;
    // The backoff resets only once a route is subscribed, so a subscribe outage cannot turn into a
    // registration every connect timeout.
    const route: SessionRoute = new SessionRoute(grant, {
      ...this.options,
      maxSessions: this.maxSessions,
      takeManifest: (sessionId) => this.takeManifest(sessionId),
      onLost: () => this.replaceRoute(route),
      onConnected: () => {
        this.retryDelayMs = ROUTE_RETRY_MIN_MS;
      },
      onConnectTimeout: () => this.retryRoute(route),
    });
    this.route = route;
    route.start();
    logger('info', 'session route open', { routeId: grant.routeId });
  }

  private replaceRoute(lost: SessionRoute): void {
    if (this.route !== lost) return;
    logger('warn', 'session route lost; opening a new one', { routeId: lost.routeId });
    lost.close('route_lost');
    this.route = undefined;
    void this.openRoute();
  }

  private retryRoute(unconnected: SessionRoute): void {
    if (this.route !== unconnected) return;
    unconnected.close('route_lost');
    this.route = undefined;
    this.scheduleRetry();
  }

  private scheduleRetry(delayMs = this.retryDelayMs): void {
    this.retryTimer = setTimeout(() => void this.openRoute(), delayMs);
    this.retryTimer.unref?.();
    this.retryDelayMs = Math.min(this.retryDelayMs * 2, ROUTE_RETRY_MAX_MS);
  }

  private get maxSessions(): number {
    const requested = Number.isFinite(this.options.maxSessions)
      ? Math.floor(this.options.maxSessions)
      : MAX_SESSIONS_RANGE.min;
    return Math.min(Math.max(requested, MAX_SESSIONS_RANGE.min), MAX_SESSIONS_RANGE.max);
  }

  private takeManifest(sessionId: string): SessionManifest | undefined {
    const manifest = this.manifests.get(sessionId);
    this.manifests.delete(sessionId);
    return manifest;
  }
}
