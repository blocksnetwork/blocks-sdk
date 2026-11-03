import { createLogger } from '../logger.js';
import { isolateCallbacks } from './callbacks.js';
import { classifyInbound } from './counterpart.js';
import { SeqWindow } from './seq-window.js';
import {
  MAX_EXCHANGE_ID_LENGTH,
  TERMINAL_SESSION_STATES,
  type SessionMessage,
  type SessionParties,
  type SessionState,
  type StartedExchange,
} from './types.js';

const logger = createLogger('Session');

export interface SeqGap {
  expected: number;
  received: number;
}

export interface ProviderReceiverEvents {
  /** `session_open` accepted; the inline exchange, if any, follows through `onExchange`. */
  onSessionOpen?(): void;
  onExchange?(exchange: StartedExchange): void;
  onExchangeCancel?(exchangeId: string): void;
  /**
   * The consumer asked to close: cancel the in-flight exchange, publish
   * `session_closed`, then call `terminate('closed')`.
   */
  onCloseRequested?(): void;
  /** A backend notice ended the session: cancel the in-flight exchange and publish nothing. */
  onBackendNotice?(state: 'closed' | 'failed', detail: string | undefined): void;
  onRouteProbe?(nonce: string): void;
  onGap?(gap: SeqGap): void;
}

export interface ProviderSessionReceiverOptions {
  parties: SessionParties;
  events?: ProviderReceiverEvents;
  /** The state the session is in when receiving starts. */
  state?: SessionState;
}

function isExchangeId(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value.length <= MAX_EXCHANGE_ID_LENGTH;
}

function startedExchange(value: unknown): StartedExchange | undefined {
  if (typeof value !== 'object' || value === null) return undefined;
  const { exchangeId, requestParts } = value as Record<string, unknown>;
  if (!isExchangeId(exchangeId) || !Array.isArray(requestParts)) return undefined;
  return { exchangeId, requestParts };
}

/**
 * The provider side of one session channel: counterpart filtering and
 * `(publisher, seq)` dedup. A gap is reported and processing continues;
 * the provider never holds messages back.
 */
export class ProviderSessionReceiver {
  private current: SessionState;
  private readonly window = new SeqWindow();
  private readonly events: ProviderReceiverEvents;

  constructor(private readonly options: ProviderSessionReceiverOptions) {
    this.current = options.state ?? 'open';
    this.events = isolateCallbacks(options.events ?? {});
  }

  get state(): SessionState {
    return this.current;
  }

  get isTerminal(): boolean {
    return TERMINAL_SESSION_STATES.has(this.current);
  }

  /** Returns true when the message passed counterpart filtering and dedup. */
  receive(publisher: string, message: unknown): boolean {
    const inbound = classifyInbound('provider', this.options.parties, publisher, message);
    const body = message as SessionMessage;
    if (inbound.kind === 'drop') return false;
    if (inbound.kind === 'notice') {
      this.onNotice(body);
      return true;
    }
    if (this.isTerminal || this.window.has(inbound.seq)) return false;
    this.trackSeq(inbound.seq);
    this.dispatch(body);
    return true;
  }

  /** Ends the session once the provider has published its terminal message. */
  terminate(state: 'closed' | 'failed'): void {
    if (!this.isTerminal) this.current = state;
  }

  private trackSeq(seq: number): void {
    const expected = this.window.highest + 1;
    this.window.add(seq);
    if (seq <= expected) return;
    logger('warn', 'session seq gap; continuing', { expected, received: seq });
    this.events.onGap?.({ expected, received: seq });
  }

  private onNotice(message: SessionMessage): void {
    if (message.type === 'route_probe') {
      if (typeof message.nonce === 'string') this.events.onRouteProbe?.(message.nonce);
      return;
    }
    if (this.isTerminal) return;
    const state = message.type === 'session_failed' ? 'failed' : 'closed';
    this.current = state;
    const detail = state === 'failed' ? message.error : message.reason;
    this.events.onBackendNotice?.(state, typeof detail === 'string' ? detail : undefined);
  }

  private dispatch(message: SessionMessage): void {
    switch (message.type) {
      case 'session_open':
        return this.onSessionOpen(message);
      case 'exchange':
        return this.startExchange(message);
      case 'exchange_cancel':
        if (isExchangeId(message.exchangeId)) {
          this.events.onExchangeCancel?.(message.exchangeId);
        }
        return;
      case 'session_close':
        if (this.current !== 'open' && this.current !== 'ready') return;
        this.current = 'closing';
        this.events.onCloseRequested?.();
        return;
    }
  }

  private onSessionOpen(message: SessionMessage): void {
    if (this.current !== 'open') return;
    this.current = 'ready';
    this.events.onSessionOpen?.();
    if (message.exchange !== undefined) this.startExchange(message.exchange);
  }

  private startExchange(value: unknown): void {
    if (this.current !== 'ready') return;
    const exchange = startedExchange(value);
    if (exchange) this.events.onExchange?.(exchange);
  }
}
