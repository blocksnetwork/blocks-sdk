import { isolateCallbacks } from './callbacks.js';
import { classifyInbound } from './counterpart.js';
import { InOrderDelivery } from './in-order-delivery.js';
import type { RecordedFileRef, SessionSnapshot, SnapshotExchange } from './session-http.js';
import type { StreamMode, StreamStarted } from './stream-types.js';
import {
  TERMINAL_SESSION_STATES,
  type ExchangeArtifact,
  type ExchangeOutcome,
  type ExchangeOutcomeState,
  type ExchangeProgress,
  type SessionMessage,
  type SessionParties,
  type SessionState,
} from './types.js';

type TerminalState = 'closed' | 'failed' | 'canceled';

const TERMINAL_MESSAGES: ReadonlySet<unknown> = new Set(['session_closed', 'session_failed']);

const FOREIGN_SUBSCRIBE = 'foreign_subscribe';
const UNKNOWN_STREAM_KIND = 'session_stream';

const STREAM_MODES: ReadonlySet<unknown> = new Set<StreamMode>(['access_manager', 'consent']);

const OUTCOME_STATES: ReadonlySet<string> = new Set<ExchangeOutcomeState>([
  'completed',
  'failed',
  'canceled',
]);

export interface ConsumerReceiverEvents {
  /** `error` accompanies a `failed` terminal state. */
  onStateChange?(state: SessionState, error?: string): void;
  onProgress?(progress: ExchangeProgress): void;
  onArtifact?(artifact: ExchangeArtifact): void;
  onOutcome?(outcome: ExchangeOutcome): void;
  /** A stream began for an exchange this session sent; at most once per stream. */
  onStreamStarted?(started: StreamStarted): void;
}

export interface ConsumerSessionReceiverOptions {
  parties: SessionParties;
  recover(): Promise<void>;
  events?: ConsumerReceiverEvents;
  /** The state the session is in when receiving starts. */
  state?: SessionState;
}

interface Recovery {
  deferred: SessionMessage[];
  cutoffs: Map<string, number>;
  record?: SessionSnapshot;
  /** The first local end asked for while the recovery is open or winding down. */
  localEnd?: { state: TerminalState; error?: string };
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined;
}

/** A `foreign_subscribe` stream's address, template and mode, or undefined when they are malformed. */
function foreignSubscribeFields(message: SessionMessage): Partial<StreamStarted> | undefined {
  const { channel, wildcard } = message;
  const address =
    typeof channel === 'string' && wildcard === undefined
      ? { channel }
      : typeof wildcard === 'string' && channel === undefined
        ? { wildcard }
        : undefined;
  if (address === undefined || !STREAM_MODES.has(message.mode)) return undefined;
  return {
    ...address,
    templateParam: optionalString(message.templateParam),
    templateValue: optionalString(message.templateValue),
    mode: message.mode as StreamMode,
    presence: false,
  };
}

function withoutUndefined<T extends object>(value: T): T {
  return Object.fromEntries(Object.entries(value).filter(([, v]) => v !== undefined)) as T;
}

/**
 * The artifacts already received, then any recorded one not among them. A recorded ref's keys may
 * come back in another order, so file refs match on `fileId`.
 */
function mergeArtifacts(received: unknown[], recorded: RecordedFileRef[]): unknown[] {
  const fileIds = new Set(received.map(fileIdOf));
  return [...received, ...recorded.filter((ref) => !fileIds.has(ref.fileId))];
}

function fileIdOf(ref: unknown): string | undefined {
  const fileId = (ref as { fileId?: unknown } | null)?.fileId;
  return typeof fileId === 'string' ? fileId : undefined;
}

/**
 * The consumer side of one session channel: counterpart filtering, in-order
 * delivery per sender, and exchange settlement. Each exchange settles once,
 * and the terminal state reaches `onStateChange` at most once.
 */
export class ConsumerSessionReceiver {
  private current: SessionState;
  private ending = false;
  /** A local end waits for an open recovery to wind down, but no new work may start meanwhile. */
  private endRequested = false;
  /** Unsettled exchanges, in the order they were tracked, with artifacts received so far. */
  private readonly unsettled = new Map<string, unknown[]>();
  /** Recorded outcomes waiting for their exchange's messages still held behind a live gap. */
  private readonly recordedLater = new Map<string, SnapshotExchange>();
  private readonly startedStreams = new Set<string>();
  private readonly delivery: InOrderDelivery<SessionMessage>;
  private readonly events: ConsumerReceiverEvents;
  private recovery?: Recovery;
  /** Set while finishRecovery runs, so a callback it fires cannot re-enter it. */
  private finishing?: Recovery;

  constructor(private readonly options: ConsumerSessionReceiverOptions) {
    this.current = options.state ?? 'open';
    this.events = isolateCallbacks(options.events ?? {});
    this.delivery = new InOrderDelivery({
      deliver: (message) => this.dispatch(message),
      recover: options.recover,
    });
  }

  get state(): SessionState {
    return this.current;
  }

  /** True while a message waits behind a `seq` gap; history must replay from before it. */
  get hasGap(): boolean {
    return this.delivery.holding;
  }

  /** True from the moment the session starts ending, before its state turns terminal. */
  get isEnding(): boolean {
    return this.ending || this.isTerminal || this.endRequested;
  }

  get isTerminal(): boolean {
    return TERMINAL_SESSION_STATES.has(this.current);
  }

  trackExchange(exchangeId: string): void {
    if (this.isTerminal) return;
    this.unsettled.set(exchangeId, []);
  }

  /** Stops tracking an exchange settled outside the channel, such as one whose publish failed. */
  forgetExchange(exchangeId: string): void {
    this.unsettled.delete(exchangeId);
  }

  /**
   * Returns true when the message passed counterpart filtering and dedup, even if held.
   * `replayed` marks a message read from history rather than received live.
   */
  receive(publisher: string, message: unknown, replayed = false): boolean {
    const inbound = classifyInbound('consumer', this.options.parties, publisher, message);
    const body = message as SessionMessage;
    switch (inbound.kind) {
      case 'drop':
        return false;
      case 'notice':
        this.dispatch(body);
        return true;
      case 'sequenced':
        return !this.isTerminal && this.delivery.accept(publisher, inbound.seq, body, replayed);
    }
  }

  /** Enters `closing` once the consumer has asked to close; a later `session_ready` no longer applies. */
  beginClosing(): void {
    if (!this.isTerminal && this.current !== 'closing') this.changeState('closing');
  }

  /**
   * Ends the session, settling unsettled exchanges locally. A no-op once terminal. An open
   * recovery ends first, so a terminal message it deferred, or a terminal state it recorded,
   * wins over this local one; among local ends the first wins, including over one a callback
   * starts while the recovery winds down.
   */
  terminate(state: TerminalState, error?: string): void {
    const recovery = this.recovery ?? this.finishing;
    if (!recovery) return this.end(state, error);
    recovery.localEnd ??= { state, error };
    this.endRequested = true;
    this.finishRecovery();
  }

  private end(state: TerminalState, error?: string): void {
    // The first end wins, even over one a callback starts while this one hands artifacts over.
    if (this.ending || this.isTerminal) return;
    this.ending = true;
    // A received artifact stays with its exchange. A held outcome is dropped: the provider's
    // messages before it never arrived, so it cannot be reported as the exchange's result.
    const heldArtifacts = this.delivery.heldMessages().filter((m) => m.type === 'exchange_artifact');
    this.delivery.dispose();
    for (const message of heldArtifacts) this.onArtifact(message);
    this.current = state;
    for (const [exchangeId, artifacts] of [...this.unsettled]) {
      const recorded = this.recordedLater.get(exchangeId);
      if (recorded) {
        this.settleFromRecord(recorded, artifacts);
        continue;
      }
      this.settle(
        state === 'failed'
          ? { exchangeId, state: 'failed', error, artifacts }
          : { exchangeId, state: 'canceled', artifacts },
      );
    }
    this.events.onStateChange?.(state, error);
  }

  /**
   * Starts a recovery. Until {@link finishRecovery}, a terminal message (`session_closed` or
   * `session_failed`), live, replayed or delivered from behind a gap, is deferred rather than
   * applied.
   */
  beginRecovery(): void {
    if (this.recovery) throw new Error('a session recovery is already open');
    this.recovery = { deferred: [], cutoffs: this.delivery.heldCutoffs() };
  }

  /**
   * Adds the gaps the replay revealed to those open when recovery began; recovery closes both. A
   * gap a live message opened since, with nothing above it from history, keeps its own timer, as
   * its missing message may still be in flight.
   */
  historyReplayed(): void {
    const cutoffs = this.recovery?.cutoffs;
    if (!cutoffs) return;
    for (const [sender, seq] of this.delivery.replayedCutoffs()) {
      cutoffs.set(sender, Math.max(seq, cutoffs.get(sender) ?? 0));
    }
  }

  /**
   * Ends a recovery in one order, so no step cuts another short: skip the gaps history did not
   * fill, settle the outcomes the backend recorded, apply the deferred terminal messages, then
   * follow the recorded state. Without a snapshot only the skip and the deferred messages apply.
   * A session about to end skips every gap, as no live message will fill one in time.
   */
  finishRecovery(snapshot?: SessionSnapshot): void {
    const recovery = this.recovery;
    if (!recovery || this.finishing) return;
    this.finishing = recovery;
    try {
      this.finish(recovery, snapshot);
    } finally {
      this.finishing = undefined;
    }
  }

  private finish(recovery: Recovery, snapshot?: SessionSnapshot): void {
    // An outcome released from behind a gap takes the recorded file refs too: the artifact message
    // that carried one may be the message history lost.
    recovery.record = snapshot;
    const ending =
      recovery.deferred.length > 0 ||
      TERMINAL_SESSION_STATES.has(snapshot?.state as SessionState);
    try {
      this.delivery.skipThrough(ending ? this.delivery.heldCutoffs() : recovery.cutoffs);
    } finally {
      this.recovery = undefined;
    }
    if (snapshot) this.applyRecordedOutcomes(snapshot);
    for (const message of recovery.deferred) this.dispatch(message);
    if (snapshot) this.applyRecordedState(snapshot);
    if (recovery.localEnd) this.end(recovery.localEnd.state, recovery.localEnd.error);
  }

  /**
   * An exchange with a message still held behind a gap left open for live delivery settles once
   * that message is released, so a held artifact is not dropped by an exchange already settled.
   */
  private applyRecordedOutcomes(snapshot: SessionSnapshot): void {
    if (this.isTerminal) return;
    const held = this.heldExchangeIds();
    for (const recorded of snapshot.exchanges) {
      const received = this.unsettled.get(recorded.exchangeId);
      if (received === undefined || !OUTCOME_STATES.has(recorded.state)) continue;
      if (held.has(recorded.exchangeId)) this.recordedLater.set(recorded.exchangeId, recorded);
      else this.settleFromRecord(recorded, received);
    }
  }

  private heldExchangeIds(): Set<string | undefined> {
    return new Set(this.delivery.heldMessages().map((m) => optionalString(m.exchangeId)));
  }

  /** Settles an exchange whose recorded outcome waited, once nothing of it is held any longer. */
  private settleRecordedIfReleased(exchangeId: string | undefined): void {
    const recorded = exchangeId === undefined ? undefined : this.recordedLater.get(exchangeId);
    if (!recorded || this.delivery.holdsAny((m) => m.exchangeId === exchangeId)) return;
    const received = this.unsettled.get(recorded.exchangeId);
    if (received !== undefined) this.settleFromRecord(recorded, received);
  }

  private settleFromRecord(recorded: SnapshotExchange, received: unknown[]): void {
    this.settle(
      withoutUndefined({
        exchangeId: recorded.exchangeId,
        state: recorded.state as ExchangeOutcomeState,
        error: recorded.error ?? undefined,
        artifacts: mergeArtifacts(received, recorded.artifactRefs),
      }),
    );
  }

  private applyRecordedState(snapshot: SessionSnapshot): void {
    if (this.isTerminal) return;
    switch (snapshot.state) {
      case 'ready':
        if (this.current === 'open') this.changeState('ready');
        return;
      case 'closed':
      case 'canceled':
        return this.end(snapshot.state);
      case 'failed':
        return this.end('failed', snapshot.error ?? undefined);
    }
  }

  private dispatch(message: SessionMessage): void {
    if (this.isTerminal) return;
    if (this.recovery && TERMINAL_MESSAGES.has(message.type)) {
      this.recovery.deferred.push(message);
      return;
    }
    this.apply(message);
    // Any released message of an exchange, even one ignored here, may end its recorded outcome's wait.
    this.settleRecordedIfReleased(optionalString(message.exchangeId));
  }

  private apply(message: SessionMessage): void {
    switch (message.type) {
      case 'session_ready':
        if (this.current === 'open') this.changeState('ready');
        return;
      case 'exchange_progress':
        return this.onProgress(message);
      case 'exchange_artifact':
        return this.onArtifact(message);
      case 'exchange_outcome':
        return this.onOutcome(message);
      case 'stream_started':
        return this.onStreamStarted(message);
      case 'session_closed':
        return this.end('closed');
      case 'session_failed':
        return this.end('failed', optionalString(message.error));
    }
  }

  private changeState(state: SessionState): void {
    this.current = state;
    this.events.onStateChange?.(state);
  }

  private unsettledId(message: SessionMessage): string | undefined {
    const id = optionalString(message.exchangeId);
    return id !== undefined && this.unsettled.has(id) ? id : undefined;
  }

  private onProgress(message: SessionMessage): void {
    const exchangeId = this.unsettledId(message);
    if (exchangeId === undefined) return;
    this.events.onProgress?.(
      withoutUndefined({
        exchangeId,
        message: optionalString(message.message),
        progress: message.progress,
      }),
    );
  }

  private onArtifact(message: SessionMessage): void {
    const exchangeId = this.unsettledId(message);
    if (exchangeId === undefined || message.artifactRef === undefined) return;
    this.unsettled.get(exchangeId)?.push(message.artifactRef);
    this.events.onArtifact?.(
      withoutUndefined({
        exchangeId,
        artifactRef: message.artifactRef,
        outputId: optionalString(message.outputId),
      }),
    );
  }

  private onStreamStarted(message: SessionMessage): void {
    const exchangeId = optionalString(message.exchangeId);
    const streamId = optionalString(message.streamId);
    // A session stream of an ordinary agent describes itself without a `kind`.
    const kind = optionalString(message.kind) ?? UNKNOWN_STREAM_KIND;
    if (
      exchangeId === undefined ||
      streamId === undefined ||
      !this.unsettled.has(exchangeId) ||
      this.startedStreams.has(streamId)
    ) {
      return;
    }
    // Only the kind the SDK consumes has fields to check; any other is the session's to refuse.
    const fields = kind === FOREIGN_SUBSCRIBE ? foreignSubscribeFields(message) : {};
    if (!fields) return;
    this.startedStreams.add(streamId);
    this.events.onStreamStarted?.(withoutUndefined({ exchangeId, streamId, kind, ...fields }));
  }

  private onOutcome(message: SessionMessage): void {
    const exchangeId = this.unsettledId(message);
    if (exchangeId === undefined || !OUTCOME_STATES.has(message.state as string)) return;
    const received = this.unsettled.get(exchangeId) ?? [];
    const recorded =
      this.recovery?.record?.exchanges.find((x) => x.exchangeId === exchangeId) ??
      this.recordedLater.get(exchangeId);
    this.settle({
      exchangeId,
      state: message.state as ExchangeOutcomeState,
      error: optionalString(message.error),
      reason: optionalString(message.reason),
      artifacts: recorded ? mergeArtifacts(received, recorded.artifactRefs) : received,
    });
  }

  private settle(outcome: ExchangeOutcome): void {
    this.unsettled.delete(outcome.exchangeId);
    this.recordedLater.delete(outcome.exchangeId);
    this.events.onOutcome?.(withoutUndefined(outcome));
  }
}
