import type { AuthProvider } from '../auth-provider.js';
import { ConsumerAuth, type TokenEndpointConfig, type TokenResult } from '../consumer-auth.js';
import { resolveConsumerEndpoints, type BillingMode } from '../consumer-endpoints.js';
import { createLogger } from '../logger.js';
import { createPubNubClient } from '../pubnub-client.js';
import {
  ClientSession,
  readyDeadline,
  readyTimeoutError,
  SESSION_CLOSE_TIMEOUT_MS,
  toRequestParts,
  type ReadyDeadline,
  type ExchangeOutcome,
  type Session,
  type SessionInput,
} from './session.js';
import { SESSION_ALREADY_OPEN, SessionError } from './session-errors.js';
import { succeedsWithin } from './deadlines.js';
import { sendManagedClosedBeacon } from './managed-retry.js';
import { type OpenedSession, SessionHttp } from './session-http.js';

const logger = createLogger('BlocksClient');
const CREATE = 'BlocksClient.create()';

export interface BlocksClientOptions {
  billingMode: BillingMode;
  cdmUrl?: string;
  subscribeKey?: string;
  publishKey?: string;
  baseUrl?: string;
  /** Exactly one credential: `apiKey`, `tokenEndpoint`, `tokenProvider` or `authProvider`. */
  apiKey?: string;
  tokenEndpoint?: TokenEndpointConfig;
  tokenProvider?: () => Promise<TokenResult>;
  authProvider?: AuthProvider;
  onAuthError?: (error: Error) => void;
  /** Extra headers sent on every backend call, merged under the SDK's own. */
  rpcHeaders?: Record<string, string>;
}

export interface OpenSessionOptions {
  /**
   * Makes the open safe to retry: the backend answers a repeat under the same key with the session
   * it already opened. A key names one open. Retry under it from the same client, which holds the
   * key until the earlier attempt has torn down. From another client, reuse it only once the
   * earlier attempt's process is gone: a rejected call may still be tearing down, and a repeat
   * that joins its session can lose it to that teardown. A repeat that names a session already
   * past opening, or one this client still opens or holds, is refused with `session_already_open`.
   * At most 256 characters.
   */
  idempotencyKey?: string;
  /** How long to wait for the agent to report the session ready. Default 30 s, at most 2 minutes. */
  readyTimeoutMs?: number;
}

export interface CallOptions extends OpenSessionOptions {
  /** How long to wait for the exchange outcome once the session is ready. */
  timeoutMs?: number;
}

interface BlocksClientConfig {
  subscribeKey: string;
  publishKey: string;
  http: SessionHttp;
  ownedAuth?: ConsumerAuth;
}

const isReplay = (opened: OpenedSession) => opened.idempotent === true;

const destroyedError = () => new SessionError('session_canceled', 'The BlocksClient was destroyed');

/** How an open under an idempotency key reports the session it holds the key for. */
interface KeyHold {
  onCreated(): void;
  onReleased(): void;
  /** The open request was abandoned; `settled` resolves once its late answer has been handled. */
  onAbandoned(settled: Promise<unknown>): void;
}

/**
 * The consumer client for sessions: open a session with an agent, or make one call.
 *
 * @experimental Sessions are a preview: refused with `SESSIONS_DISABLED` until enabled for
 * your org, and may change before general availability.
 */
export class BlocksClient {
  private readonly sessions = new Set<ClientSession>();
  private destroyed = false;
  /** Open requests still in flight, aborted by destroy(). */
  private readonly pendingOpens = new Set<AbortController>();
  /**
   * Keys whose session this client opened and has not released. A key names one open, and until
   * the provider's checkpoint lands the backend may still report its session `open`, so a second
   * open under it would join the same session as another holder.
   */
  private readonly keysHeld = new Set<string>();

  private constructor(private readonly config: BlocksClientConfig) {}

  static async create(options: BlocksClientOptions): Promise<BlocksClient> {
    const credentials = [
      options.apiKey,
      options.tokenEndpoint,
      options.tokenProvider,
      options.authProvider,
    ].filter(Boolean).length;
    if (credentials !== 1) {
      throw new Error(
        `${CREATE} requires exactly one of apiKey, tokenEndpoint, tokenProvider or authProvider`,
      );
    }
    const { subscribeKey, publishKey, baseUrl } = await resolveConsumerEndpoints(options, CREATE);
    if (!publishKey) {
      throw new Error(`${CREATE} could not resolve publishKey from options, env, or CDM`);
    }

    let ownedAuth: ConsumerAuth | undefined;
    if (!options.authProvider) {
      ownedAuth = new ConsumerAuth({
        apiKey: options.apiKey,
        tokenEndpoint: options.tokenEndpoint,
        tokenProvider: options.tokenProvider,
        baseUrl,
        onAuthError: options.onAuthError,
      });
      await ownedAuth.init();
    }
    const http = new SessionHttp({
      baseUrl,
      authProvider: options.authProvider ?? ownedAuth,
      headers: options.rpcHeaders,
    });
    return new BlocksClient({ subscribeKey, publishKey, http, ownedAuth });
  }

  /** Resolves once the session is ready, or rejects with the refusal or terminal error. */
  async openSession(agentName: string, options: OpenSessionOptions = {}): Promise<Session> {
    const { session } = await this.open(agentName, options);
    return session;
  }

  /** Opens a session, sends `input` with the open, waits for its outcome, and closes the session. */
  async call(
    agentName: string,
    input: SessionInput,
    options: CallOptions = {},
  ): Promise<ExchangeOutcome> {
    const { session, exchange } = await this.open(agentName, options, input);
    try {
      // Present whenever a first input is given.
      return await exchange!.waitForOutcome(options.timeoutMs);
    } finally {
      await session.close();
    }
  }

  /** Closes every session, including any still opening, without waiting; stops credential refresh. */
  destroy(): void {
    this.destroyed = true;
    for (const request of this.pendingOpens) request.abort();
    for (const session of this.sessions) {
      session.close().catch((err: unknown) => {
        logger('warn', 'session close on destroy failed', { error: String(err) });
      });
    }
    this.config.ownedAuth?.destroy();
  }

  private async open(agentName: string, options: OpenSessionOptions, firstInput?: SessionInput) {
    if (this.destroyed) throw destroyedError();
    const key = options.idempotencyKey;
    if (key === undefined) return this.openOnce(agentName, options, firstInput);
    if (this.keysHeld.has(key)) {
      throw new SessionError(
        SESSION_ALREADY_OPEN,
        `idempotencyKey ${key} already names a session on this client; open again under a new key`,
      );
    }
    this.keysHeld.add(key);
    let created = false;
    let abandoned: Promise<unknown> | undefined;
    const free = () => this.keysHeld.delete(key);
    try {
      return await this.openOnce(agentName, options, firstInput, {
        onCreated: () => {
          created = true;
        },
        onReleased: free,
        onAbandoned: (settled) => {
          abandoned = settled;
        },
      });
    } catch (err) {
      // A session created before the failure is still tearing down, and its release frees the
      // key; an abandoned request may yet open one, so the key waits for it to settle.
      if (!created) void (abandoned ?? Promise.resolve()).then(free);
      throw err;
    }
  }

  private async openOnce(
    agentName: string,
    options: OpenSessionOptions,
    firstInput?: SessionInput,
    held?: KeyHold,
  ) {
    const firstParts = firstInput === undefined ? undefined : toRequestParts(firstInput);
    if (this.destroyed) throw destroyedError();
    const { http, subscribeKey, publishKey } = this.config;
    const ready = readyDeadline(options.readyTimeoutMs);
    const opened = await this.requestOpen(agentName, options.idempotencyKey, ready, held);
    if (isReplay(opened) && opened.state !== 'open') {
      if (this.destroyed) throw destroyedError();
      // Another client holds this session past its open, under the same transport identity and
      // its own seq, so joining it would collide; and closing it would end it for that holder.
      throw new SessionError(
        SESSION_ALREADY_OPEN,
        `idempotencyKey ${options.idempotencyKey} names a session that is already ${opened.state}; open again under a new key`,
      );
    }
    if (this.destroyed) {
      // destroy() ran during the open request: the backend holds a session nobody will use.
      this.sendClosedBeacon(opened).catch((err: unknown) => {
        logger('debug', 'session beacon failed', { beacon: 'closed', error: String(err) });
      });
      throw destroyedError();
    }
    const pubnub = createPubNubClient({
      subscribeKey,
      publishKey,
      userId: opened.consumerTransportUuid,
    });
    return ClientSession.open(
      opened,
      { pubnub, http },
      { firstParts, ready },
      {
        onCreated: (session) => {
          this.sessions.add(session);
          held?.onCreated();
        },
        onReleased: (session) => {
          this.sessions.delete(session);
          held?.onReleased();
        },
      },
    );
  }

  private sendClosedBeacon(opened: OpenedSession): Promise<unknown> {
    const { http } = this.config;
    if (!opened.managed) return http.sendBeacon(opened.sessionId, 'closed');
    // Bounded like a close: a stalled attempt must not hold the idempotency key.
    const attempt = new AbortController();
    return succeedsWithin(
      sendManagedClosedBeacon(http, opened.sessionId, SESSION_CLOSE_TIMEOUT_MS, attempt.signal),
      SESSION_CLOSE_TIMEOUT_MS,
      'abandoned managed closed beacon',
    ).finally(() => attempt.abort());
  }

  /** `POST /api/v1/sessions`, abandoned when the ready deadline passes or the client is destroyed. */
  private async requestOpen(
    agentName: string,
    idempotencyKey: string | undefined,
    ready: ReadyDeadline,
    held?: KeyHold,
  ) {
    const request = new AbortController();
    const timer = setTimeout(() => request.abort(), Math.max(0, ready.at - Date.now()));
    this.pendingOpens.add(request);
    const { http } = this.config;
    const opening = http.openSession(agentName, idempotencyKey, request.signal);
    // The signal reaches fetch only; auth preflight or a token refresh before it can still hang.
    const abandoned = new Promise<never>((_, reject) =>
      request.signal.addEventListener('abort', () => reject(request.signal.reason)),
    );
    try {
      return await Promise.race([opening, abandoned]);
    } catch (err) {
      if (request.signal.aborted) {
        // An open that completes after being abandoned still leaves a session on the backend. A
        // replay names a session someone else may hold, even one still `open` while its holder
        // waits for the agent, so it is left alone.
        const late = opening
          .then((opened) => (isReplay(opened) ? undefined : this.sendClosedBeacon(opened)))
          .catch(() => undefined);
        held?.onAbandoned(late);
      }
      if (!request.signal.aborted) throw err;
      throw this.destroyed ? destroyedError() : readyTimeoutError(ready);
    } finally {
      clearTimeout(timer);
      this.pendingOpens.delete(request);
    }
  }
}
