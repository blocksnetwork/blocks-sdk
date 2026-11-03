// Local stand-in for the session API surface until the SDK exports it.
import type {
  AgentInstanceHandle,
  AgentInstanceOptions,
  HandlerResult,
  Message,
  RequestPart,
  SessionInput,
} from '../../../src/index.js';
import type { ArtifactRef } from '../../../src/runtime/artifacts.js';
import type { TaskClient } from '../../../src/runtime/task-client.js';

export type ExchangeState = 'completed' | 'failed' | 'canceled';
export type SessionState = 'open' | 'ready' | 'closing' | 'closed' | 'failed' | 'canceled';

export interface ExchangeOutcome {
  exchangeId: string;
  state: ExchangeState;
  error?: string;
  reason?: string;
  artifacts: ArtifactRef[];
}

export interface ExchangeProgress {
  message?: string;
  progress?: unknown;
}

export interface Exchange {
  readonly exchangeId: string;
  onProgress(cb: (event: ExchangeProgress) => void): void;
  onArtifact(cb: (ref: ArtifactRef) => void): void;
  waitForOutcome(timeoutMs?: number): Promise<ExchangeOutcome>;
  stream(): AsyncIterable<Message>;
}

export interface Session {
  readonly sessionId: string;
  readonly state: SessionState;
  send(input: SessionInput): Exchange;
  onStateChange(cb: (state: SessionState) => void): void;
  close(): Promise<void>;
}

export interface BlocksClient {
  call(agentName: string, input: SessionInput): Promise<ExchangeOutcome>;
  openSession(agentName: string): Promise<Session>;
  destroy(): void;
}

export interface SessionError extends Error {
  readonly code: string;
}

export type { ArtifactRef, HandlerResult, Message, SessionInput };

type ErrorClass = abstract new (...args: never[]) => SessionError;

export interface SessionContext {
  readonly sessionId: string;
  readonly agentName: string;
}

export interface ExchangeContext {
  readonly exchangeId: string;
  readonly requestParts: RequestPart[];
  reportStatus(message: string): Promise<void> | void;
}

export interface SessionProviderOptions {
  onSessionOpen?: (ctx: SessionContext) => Promise<void> | void;
  onExchange: (
    ctx: SessionContext,
    exchange: ExchangeContext,
  ) => Promise<HandlerResult> | HandlerResult;
  onSessionClose?: (ctx: SessionContext, reason: string) => Promise<void> | void;
}

export interface SessionSdk {
  BlocksClient: {
    create(options: NonNullable<Parameters<typeof TaskClient.create>[0]>): Promise<BlocksClient>;
  };
  SessionError: ErrorClass;
  SessionsDisabledError: ErrorClass;
  SessionPaidAgentError: ErrorClass;
  SessionStreamError: ErrorClass;
  startAgentInstance(
    options: AgentInstanceOptions & SessionProviderOptions,
  ): Promise<AgentInstanceHandle>;
}

const REQUIRED_EXPORTS = [
  'BlocksClient',
  'SessionError',
  'SessionsDisabledError',
  'SessionPaidAgentError',
  'SessionStreamError',
] as const;

export async function loadSessionSdk(): Promise<SessionSdk> {
  const mod = (await import('../../../src/index.js')) as unknown as Partial<SessionSdk>;
  const missing = REQUIRED_EXPORTS.filter((name) => mod[name] === undefined);
  if (missing.length > 0) {
    throw new Error(`SDK does not export the session API surface yet: ${missing.join(', ')}`);
  }
  return mod as SessionSdk;
}
