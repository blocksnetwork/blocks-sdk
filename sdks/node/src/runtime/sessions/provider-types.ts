import type { HandlerResult, RequestPart } from '../agent-instance.js';
import type { JsonValue } from '../pubnub-types.js';

/** The session an `onSessionOpen` / `onExchange` / `onSessionClose` call belongs to. */
export interface SessionContext {
  readonly sessionId: string;
  readonly agentName: string;
  /** `null` when the caller has no org. */
  readonly orgId: string | null;
  readonly ownerId: string;
  readonly callerClaims: Record<string, JsonValue>;
  /** Closes the session from the provider side (`reason: "provider_closed"`). */
  close(): Promise<void>;
}

export interface PublishArtifactOptions {
  mimeType?: string;
  fileName?: string;
  outputId?: string;
}

/** One exchange inside a session. */
export interface ExchangeContext {
  readonly exchangeId: string;
  readonly requestParts: RequestPart[];
  /** Publishes `exchange_progress`, at most once a second; a pending status goes out before the outcome. */
  reportStatus(message: string): void;
  publishArtifact(data: Buffer | string, options?: PublishArtifactOptions): Promise<void>;
  downloadInputArtifact(part: RequestPart): Promise<Buffer>;
  readonly cancelSignal: AbortSignal;
  readonly isCancelled: boolean;
}

/**
 * @experimental Sessions are a preview: refused with `SESSIONS_DISABLED` until enabled for
 * your org, and may change before general availability.
 */
export interface SessionProviderOptions {
  /** Runs once before `session_ready` is published. A throw fails the session. */
  onSessionOpen?: (ctx: SessionContext) => Promise<void> | void;
  /** Runs once per exchange, one at a time per session. Setting it opens a session route. */
  onExchange?: (
    ctx: SessionContext,
    exchange: ExchangeContext,
  ) => Promise<HandlerResult> | HandlerResult;
  /** Runs once when a session whose `onSessionOpen` returned ends. Errors are logged. */
  onSessionClose?: (ctx: SessionContext, reason: string) => Promise<void> | void;
  /** Sessions this instance serves at once. Defaults to `concurrency`. */
  maxSessions?: number;
}
