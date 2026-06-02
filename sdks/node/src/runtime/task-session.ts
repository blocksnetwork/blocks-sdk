/**
 * TaskSession - Consumer-side task session with eager subscription.
 *
 * Replaces SendMessageResult. Owns one task's channel subscription,
 * parsed task events, discovered streams, and cleanup. Auto-closes
 * on terminal event.
 */

import PubNub from 'pubnub';
import { buildPubNubLogConfig } from './pubnub-client.js';
import {
  StreamClient,
  invertDirection,
  type StreamDescriptor,
  type StreamClientFromDescriptorOptions,
} from '../stream/index.js';
import { StreamRef } from './stream-ref.js';
import { taskChannel } from './channel-manager.js';
import { callRpc, type RpcClientConfig } from './rpc-client.js';
import { downloadArtifact, type ArtifactRef, type DownloadedArtifact } from './artifacts.js';
import { log as baseLog } from './logger.js';
import { TerminalDeliveryTracker } from './terminal-delivery-tracker.js';

const log = (
  level: 'debug' | 'info' | 'warn' | 'error',
  message: string,
  meta?: Record<string, unknown>,
): void => baseLog('[TaskSession]', level, message, meta);

export type Unsubscribe = () => void;

// -- Terminal state set (shared with task-client.ts and stream-ref.ts) --
export const TERMINAL_STATES = new Set(['completed', 'failed', 'canceled']);

// -- Base event interface (for catch-all and legacy usage) --
export interface TaskEvent {
  type: string;
  taskId: string;
  [key: string]: unknown;
}

// -- Typed event interfaces (Fix 4: discriminated unions) --

export interface ProgressEvent {
  type: 'progress';
  taskId: string;
  message?: string;
  progress?: number;
  streamEvent?: string;
  [key: string]: unknown;
}

export interface ArtifactEvent {
  type: 'artifact';
  taskId: string;
  artifactRef: ArtifactRef;
  outputId?: string;
  [key: string]: unknown;
}

export interface TerminalEvent {
  type: 'terminal';
  taskId: string;
  state: 'completed' | 'failed' | 'canceled';
  reason?: string;
  error?: string;
  [key: string]: unknown;
}

/**
 * Backend-published acknowledgment that a cooperative cancel was committed.
 * Carries no actor identity (the obs.* channel records owner ID for ops/admin
 * audit). Fires zero or once per session: suppressed once a terminal has
 * been delivered (causality), and suppressed on duplicate wire emissions
 * of the event itself (e.g. PubNub cache replay before timetoken-dedup
 * catches it). See `schemas/SDK/task-events/cancel_requested.schema.json`.
 */
export interface CancelRequestedEvent {
  type: 'cancel_requested';
  taskId: string;
  ts: number;
  [key: string]: unknown;
}

export interface CallbackErrorContext {
  entryPoint: 'taskSession' | 'subscribeToTask';
  callbackType:
    | 'onProgress'
    | 'onArtifact'
    | 'onTerminal'
    | 'onCancelRequested'
    | 'onSystem'
    | 'onEvent'
    | 'onStream'
    | 'streamPredicate';
  event: TaskEvent | StreamRef;
}

/** Parsed stream entry from a stream_started event. */
interface StreamStartedEntry {
  channel: string;
  direction: string;
  format: string;
  affinity: string;
  token: string;
  tokenTtlMinutes: number;
  metadata?: Record<string, unknown>;
}

export class TaskSession {
  readonly taskId: string;
  readonly ownerId: string;
  readonly orgId: string;
  readonly readToken: string | null;
  readonly statusChannel: string;

  private readonly pubnub: PubNub | null;
  private readonly ownsSubscribeClient: boolean;
  private readonly sdkOptions: StreamClientFromDescriptorOptions;
  private readonly agentName: string;
  private readonly rpcConfig: RpcClientConfig | null;
  private readonly subscribeKey: string;
  private readonly publishKey: string;

  // Event callbacks (typed per Fix 4)
  private progressCallbacks: Array<(event: ProgressEvent) => void> = [];
  private artifactCallbacks: Array<(event: ArtifactEvent) => void> = [];
  private terminalCallbacks: Array<(event: TerminalEvent) => void> = [];
  private cancelRequestedCallbacks: Array<
    (event: CancelRequestedEvent) => void
  > = [];
  private eventCallbacks: Array<(event: TaskEvent) => void> = [];
  private streamCallbacks: Array<(stream: StreamRef) => void> = [];

  // BLOCKS-370 R7: shared first-terminal-wins tracker. Every public
  // terminal-delivery surface (handleEvent, onTerminal, waitForTerminal,
  // synthetic re-emit) routes through this so SDK consumers see at most
  // one terminal even when the wire delivers two (e.g. scanner Phase 6
  // force-canceled and the agent's own delayed terminal).
  private readonly terminalTracker = new TerminalDeliveryTracker();

  // BLOCKS-370: cancel_requested fires zero-or-once per session.
  // Suppressed once a terminal has been delivered (causality) AND
  // suppressed on duplicate wire emissions of the event itself (e.g.
  // PubNub cache replay before timetoken-dedup catches it).
  private cancelRequestedDelivered = false;
  // BLOCKS-370: single-slot capture for sticky-replay. Mirrors
  // terminalTracker.peek() — a callback registered after the wire event
  // arrived gets the first event synthesized at registration time.
  private firstCancelRequested: CancelRequestedEvent | null = null;

  // Error callbacks (P1-3)
  private errorCallbacks: Array<(error: Error, context: CallbackErrorContext) => void> = [];

  // Stream tracking
  private streams = new Map<string, StreamRef>();

  // Artifact tracking (P1-2)
  private artifacts: ArtifactRef[] = [];

  // History event tracking for connect(). Live events are delivered through callbacks.
  private historyEvents: TaskEvent[] = [];
  private historyTimetokens = new Set<string>();

  // Waiters for stream discovery
  private streamWaiters: Array<{
    resolve: (ref: StreamRef) => void;
    reject: (err: Error) => void;
    streamId?: string;
    predicate?: (ref: StreamRef) => boolean;
  }> = [];

  // Waiters for terminal event (mirrors streamWaiters pattern)
  private terminalWaiters: Array<{
    resolve: (event: TerminalEvent) => void;
    reject: (err: Error) => void;
  }> = [];

  private closed = false;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private listener: any = null;

  // Auto-drain state
  private readonly autoDrain: boolean;
  private terminalReceived = false;
  private drainTimer: ReturnType<typeof setTimeout> | null = null;
  private readonly drainWindowMs: number;
  private openStreamClients = new Set<StreamClient>();

  // Dedup: bounded seen-timetoken set to suppress duplicate dispatch when
  // PubNub's cache replay overlaps live delivery (SDK_CONTRACT §10.4.1a).
  // Insertion order is preserved by JS Set; oldest entries are evicted
  // once size exceeds SEEN_TIMETOKENS_MAX.
  private readonly seenTimetokens: Set<string> = new Set();
  private static readonly SEEN_TIMETOKENS_MAX = 200;

  /** RPC response metadata from sendMessage(). */
  readonly idempotent?: boolean;
  readonly queued?: boolean;
  readonly pushConfigId?: string;

  /**
   * Current session state. Set from history at construction time (for
   * pre-closed idempotent hits and connect() sessions) and kept current
   * on live `terminal` events. Treat as a live observable rather than an
   * immutable snapshot: `StreamRef.open()` consults this value via its
   * `sessionState` hook to short-circuit on terminal sessions.
   */
  state?: string;

  /** Whether this is a skipSubscription session (terminal connect). */
  private readonly skipSubscriptionMode: boolean;

  constructor(opts: {
    taskId: string;
    ownerId: string;
    orgId?: string;
    readToken: string | null;
    statusChannel?: string;
    agentName: string;
    pubnub: PubNub | null;
    ownsSubscribeClient?: boolean;
    sdkOptions: StreamClientFromDescriptorOptions;
    rpcConfig?: RpcClientConfig;
    idempotent?: boolean;
    queued?: boolean;
    pushConfigId?: string;
    autoDrain?: boolean;
    /**
     * Duration in milliseconds the session waits for already-open streams
     * to finish draining naturally after a terminal event. Defaults to
     * 30000 ms (30 seconds). Ignored when `autoDrain` is false.
     *
     * Only applies to streams that were opened while the task was still
     * active. Unopened streams on a terminal session throw
     * `StreamUnavailableError` per the merged t7c baseline.
     */
    drainWindowMs?: number;
    /**
     * When true, the session starts already closed. Used for terminal
     * idempotent hits where the task is already in a terminal state and
     * no PubNub subscription is needed.
     */
    preClosed?: boolean;
    /** The terminal state of the task (e.g., "completed", "failed", "canceled"). */
    state?: string;
    /** Skip PubNub subscription but keep client alive for downloadArtifact().
     *  Used by connect() for terminal tasks. */
    skipSubscription?: boolean;
    /** Pre-populated stream refs from history (connect()). */
    preloadedStreams?: Map<string, StreamRef>;
    /** Pre-populated artifact refs from history (connect()). */
    preloadedArtifacts?: ArtifactRef[];
    /** Pre-populated task events from connect() history. */
    preloadedEvents?: TaskEvent[];
    /** External subscription managed by connect() for active tasks. */
    externalSubscription?: {
      listener: unknown;
      channel: string;
      onReady: (dispatch: (event: TaskEvent, timetoken?: string) => void) => void;
    };
  }) {
    this.taskId = opts.taskId;
    this.ownerId = opts.ownerId;
    this.orgId = opts.orgId ?? opts.ownerId;
    this.readToken = opts.readToken;
    this.agentName = opts.agentName;
    this.pubnub = opts.pubnub;
    this.ownsSubscribeClient = opts.ownsSubscribeClient ?? false;
    this.sdkOptions = { ...opts.sdkOptions, consumerUserId: opts.ownerId };
    this.rpcConfig = opts.rpcConfig ?? null;
    this.statusChannel = opts.statusChannel ?? taskChannel(opts.taskId, this.orgId);
    this.idempotent = opts.idempotent;
    this.queued = opts.queued;
    this.pushConfigId = opts.pushConfigId;
    this.autoDrain = opts.autoDrain ?? true;
    this.drainWindowMs = opts.drainWindowMs ?? 30000;
    this.state = opts.state;
    this.skipSubscriptionMode = opts.skipSubscription ?? false;
    this.subscribeKey = opts.sdkOptions.subscribeKey;
    this.publishKey = opts.sdkOptions.publishKey ?? '';

    // Pre-populate streams and artifacts from connect() history.
    // Re-wrap preloaded StreamRefs with onOpen hooks so auto-drain
    // tracks clients opened from preloaded streams the same way it
    // tracks clients opened from live-discovered streams.
    if (opts.preloadedStreams) {
      for (const [id, ref] of opts.preloadedStreams) {
        const hooked = new StreamRef(ref.descriptor, this.sdkOptions, {
          onOpen: (client) => this.trackStreamClient(client),
          sessionState: () => this.state,
        });
        this.streams.set(id, hooked);
      }
    }
    if (opts.preloadedArtifacts) {
      this.artifacts.push(...opts.preloadedArtifacts);
    }
    if (opts.preloadedEvents) {
      this.historyEvents.push(...opts.preloadedEvents);
    }

    if (opts.preClosed) {
      // Pre-closed session: skip PubNub subscription, start closed.
      // Used for terminal idempotent hits where the task already finished.
      // No PubNub client is allocated for pre-closed sessions (pubnub is null).
      this.closed = true;
    } else if (opts.skipSubscription) {
      // Terminal connect() mode: hold client for downloadArtifact(),
      // skip subscribe, stay open for close() cleanup.
    } else if (opts.externalSubscription) {
      // Active connect() mode: subscription is already active externally.
      // Store listener reference for cleanup in close().
      this.listener = opts.externalSubscription.listener;
      // Hand back dispatch reference via onReady callback. connect() will
      // forward both the parsed message and the raw PubNub timetoken so
      // the session dedup layer (handleEvent) can drop replay duplicates.
      opts.externalSubscription.onReady(this.handleEvent.bind(this));
    } else {
      this.setupSubscription();
    }
  }

  private setupSubscription(): void {
    // Only called for non-preClosed sessions where pubnub is always provided.
    const pn = this.pubnub!;
    const channel = this.statusChannel;

    this.listener = {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      message: (event: any) => {
        if (event.channel !== channel) return;
        const msg = event.message as TaskEvent | undefined;
        if (!msg || typeof msg !== 'object' || !msg.type) return;
        const tt = event.timetoken != null ? String(event.timetoken) : undefined;
        this.handleEvent(msg, tt);
      },
    };

    pn.addListener(this.listener);
    // timetoken: 1000 asks PubNub to replay everything still in the
    // channel's in-memory cache (per SDK_CONTRACT §10.4.1a). Using 0
    // would mean "initial subscribe, no catch-up" and leaves the
    // publish-before-subscribe race unfixed.
    pn.subscribe({ channels: [channel], timetoken: 1000 });
  }

  private routeCallbackError(
    err: unknown,
    callbackType: CallbackErrorContext['callbackType'],
    event: TaskEvent | StreamRef,
  ): void {
    const error = err instanceof Error ? err : new Error(String(err));
    if (this.errorCallbacks.length > 0) {
      for (const ecb of this.errorCallbacks) {
        try {
          ecb(error, { entryPoint: 'taskSession', callbackType, event });
        } catch {
          // Prevent infinite loop if error handler itself throws
        }
      }
    } else {
      log('warn', `callback error in ${callbackType}`, {
        event: 'task_session_callback_error',
        callbackType,
        error: error.message,
      });
    }
  }

  private handleEvent(event: TaskEvent, timetoken?: string): void {
    if (this.closed) return;

    // Dedup: cache replay + live delivery can surface the same message twice.
    // Drop repeats by PubNub timetoken before any dispatch. Bounded to the
    // last SEEN_TIMETOKENS_MAX entries to cap memory. Events without a
    // timetoken (e.g. synthetic test fixtures, pre-existing call sites that
    // don't thread it) bypass dedup and dispatch unchanged.
    if (timetoken) {
      if (this.seenTimetokens.has(timetoken)) {
        return;
      }
      this.seenTimetokens.add(timetoken);
      if (this.seenTimetokens.size > TaskSession.SEEN_TIMETOKENS_MAX) {
        const oldest = this.seenTimetokens.values().next().value;
        if (oldest !== undefined) {
          this.seenTimetokens.delete(oldest);
        }
      }
    }

    // Catch-all
    for (const cb of this.eventCallbacks) {
      try { cb(event); } catch (err) {
        this.routeCallbackError(err, 'onEvent', event);
      }
    }

    // Typed dispatch
    switch (event.type) {
      case 'progress':
        for (const cb of this.progressCallbacks) {
          try { cb(event as ProgressEvent); } catch (err) {
            this.routeCallbackError(err, 'onProgress', event);
          }
        }
        // Check for stream_started
        if (event.streamEvent === 'stream_started' && event.streams) {
          this.handleStreamStarted(event);
        }
        break;
      case 'artifact':
        // Accumulate artifact refs from live events
        this.accumulateArtifact(event);
        for (const cb of this.artifactCallbacks) {
          try { cb(event as ArtifactEvent); } catch (err) {
            this.routeCallbackError(err, 'onArtifact', event);
          }
        }
        break;
      case 'cancel_requested':
        // BLOCKS-370: backend acknowledgment of a cooperative cancel.
        // Two suppression gates:
        //   1. terminal already delivered — task is over from the
        //      consumer's perspective; a late cancel_requested would
        //      invert causality.
        //   2. cancel_requested already delivered — duplicate wire
        //      emissions (e.g. PubNub cache replay before timetoken
        //      dedup catches it) must not double-fire.
        if (this.terminalTracker.isDelivered) break;
        if (this.cancelRequestedDelivered) break;
        this.cancelRequestedDelivered = true;
        this.firstCancelRequested = event as CancelRequestedEvent;
        for (const cb of this.cancelRequestedCallbacks) {
          try {
            cb(event as CancelRequestedEvent);
          } catch (err) {
            this.routeCallbackError(err, 'onCancelRequested', event);
          }
        }
        break;
      case 'terminal':
        // BLOCKS-370 R7: route through the tracker so a duplicate wire
        // terminal (e.g. scanner Phase-6 force-cancel + agent's delayed
        // terminal) is silently dropped before any callback fires.
        this.terminalTracker.tryDeliver(event as TerminalEvent, (e) => {
          // Update session state FIRST so callbacks (and any ref.open() calls
          // they make) see the terminal state, not the stale 'running' value.
          this.state = e.state;
          for (const cb of this.terminalCallbacks) {
            try { cb(e); } catch (err) {
              this.routeCallbackError(err, 'onTerminal', e);
            }
          }
          // Resolve all pending terminal waiters
          for (const waiter of this.terminalWaiters) {
            waiter.resolve(e);
          }
          this.terminalWaiters = [];
          if (this.autoDrain) {
            this.startAutoDrain();
          } else {
            this.close();
          }
        });
        break;
    }
  }

  /** Register a stream client for auto-drain tracking. */
  private trackStreamClient(client: StreamClient): void {
    this.openStreamClients.add(client);
    client.onInboundDone(() => {
      this.openStreamClients.delete(client);
      if (client.isActive) {
        client.end().catch(() => {});
      }
      if (this.terminalReceived && this.openStreamClients.size === 0) {
        if (this.drainTimer) {
          clearTimeout(this.drainTimer);
          this.drainTimer = null;
        }
        this.close();
      }
    });
  }

  private accumulateArtifact(event: TaskEvent): void {
    // Extract artifact ref from the event if present
    const artifactRef = event.artifactRef as ArtifactRef | undefined;
    if (artifactRef && typeof artifactRef === 'object' && artifactRef.kind) {
      this.artifacts.push(artifactRef);
    }
  }

  private handleStreamStarted(event: TaskEvent): void {
    const streamsMap = event.streams as Record<string, StreamStartedEntry> | undefined;
    if (!streamsMap || typeof streamsMap !== 'object') return;

    // declaredStream is a top-level field on the stream_started event (Fix 12)
    const declaredStreamKey = event.declaredStream as string | undefined;

    for (const [streamId, entry] of Object.entries(streamsMap)) {
      if (!entry || typeof entry !== 'object') continue;
      if (this.streams.has(streamId)) continue;

      const agentDirection = entry.direction as 'outbound' | 'inbound' | 'bidirectional';
      const localDirection = invertDirection(agentDirection);
      const format = entry.format as 'bytes' | 'events';

      if (!format || (format !== 'bytes' && format !== 'events')) continue;

      const affinity = entry.affinity as 'dedicated' | 'shared';
      if (affinity !== 'dedicated' && affinity !== 'shared') {
        // affinity became schema-required in 4.7.0. Silent drop would
        // leave a consumer missing a stream with no log. Warn loudly
        // so a malformed live event is diagnosable.
        log('warn', `live stream_started: dropping stream "${streamId}" for task "${this.taskId}" — invalid or missing affinity`, {
          event: 'task_session_live_invalid_affinity',
          streamId,
          taskId: this.taskId,
          receivedAffinity: entry.affinity,
        });
        continue;
      }

      const descriptor: StreamDescriptor = {
        taskId: this.taskId,
        streamId,
        agentName: this.agentName,
        channel: entry.channel,
        token: entry.token,
        agentDirection,
        localDirection,
        format,
        affinity,
        metadata: entry.metadata,
        declaredStream: declaredStreamKey,
      };

      const ref = new StreamRef(descriptor, this.sdkOptions, {
        onOpen: (client) => this.trackStreamClient(client),
        sessionState: () => this.state,
      });
      this.streams.set(streamId, ref);

      // Notify stream callbacks
      for (const cb of this.streamCallbacks) {
        try { cb(ref); } catch (err) {
          this.routeCallbackError(err, 'onStream', ref);
        }
      }

      // Resolve matching waiters
      this.resolveWaiters(ref);
    }
  }

  private startAutoDrain(): void {
    if (this.closed) return;
    this.terminalReceived = true;

    if (this.openStreamClients.size === 0) {
      this.close();
      return;
    }

    this.drainTimer = setTimeout(() => {
      this.drainTimer = null;
      for (const client of this.openStreamClients) {
        if (client.isActive) {
          client.end().catch(() => {});
        }
      }
      this.close();
    }, this.drainWindowMs);
  }

  /**
   * Find a stream by runtime stream ID or declared stream key (Fix 12).
   * Checks the map key first (runtime ID), then falls back to scanning
   * descriptors for a matching declaredStream.
   */
  private findStreamByIdOrDeclared(id: string): StreamRef | undefined {
    const direct = this.streams.get(id);
    if (direct) return direct;
    for (const ref of this.streams.values()) {
      if (ref.descriptor.declaredStream === id) return ref;
    }
    return undefined;
  }

  private resolveWaiters(ref: StreamRef): void {
    const remaining: typeof this.streamWaiters = [];

    for (const waiter of this.streamWaiters) {
      let matched = false;

      if (waiter.streamId !== undefined) {
        matched = ref.descriptor.declaredStream === waiter.streamId
               || ref.descriptor.streamId === waiter.streamId;
      } else if (waiter.predicate) {
        try { matched = waiter.predicate(ref); } catch (err) {
          this.routeCallbackError(err, 'streamPredicate', ref);
        }
      } else {
        // No streamId and no predicate: match any single stream
        matched = true;
      }

      if (matched) {
        waiter.resolve(ref);
      } else {
        remaining.push(waiter);
      }
    }

    this.streamWaiters = remaining;
  }

  // -- Public event subscription API --

  onProgress(cb: (event: ProgressEvent) => void): Unsubscribe {
    this.progressCallbacks.push(cb);
    return () => {
      this.progressCallbacks = this.progressCallbacks.filter(c => c !== cb);
    };
  }

  onArtifact(cb: (event: ArtifactEvent) => void): Unsubscribe {
    this.artifactCallbacks.push(cb);
    for (const ref of [...this.artifacts]) {
      const event: ArtifactEvent = { type: 'artifact', taskId: this.taskId, artifactRef: ref };
      try { cb(event); } catch (err) {
        this.routeCallbackError(err, 'onArtifact', event);
      }
    }
    return () => {
      this.artifactCallbacks = this.artifactCallbacks.filter(c => c !== cb);
    };
  }

  onTerminal(cb: (event: TerminalEvent) => void): Unsubscribe {
    this.terminalCallbacks.push(cb);
    // BLOCKS-370 R7: hand the registering callback the first-delivered
    // terminal if one exists. peek() is the source of truth — it covers
    // both the wire-delivered case (tryDeliver in handleEvent) and the
    // synthetic case below.
    const existing = this.terminalTracker.peek();
    if (existing) {
      try { cb(existing); } catch (err) {
        this.routeCallbackError(err, 'onTerminal', existing);
      }
    } else if (this.state && TERMINAL_STATES.has(this.state)) {
      // Session was constructed already-terminal (e.g. resumed from a
      // completed task) but no wire event has arrived. Synthesize one and
      // pump it through the tracker so any future wire-level duplicate is
      // suppressed.
      const synth: TerminalEvent = {
        type: 'terminal',
        taskId: this.taskId,
        state: this.state as 'completed' | 'failed' | 'canceled',
      };
      this.terminalTracker.tryDeliver(synth, (e) => {
        try { cb(e); } catch (err) {
          this.routeCallbackError(err, 'onTerminal', e);
        }
      });
    }
    return () => {
      this.terminalCallbacks = this.terminalCallbacks.filter(c => c !== cb);
    };
  }

  /**
   * BLOCKS-370: subscribe to backend-published `cancel_requested` events.
   * Fires zero or once per session — suppressed once a terminal has been
   * delivered (causality) and suppressed on duplicate wire emissions of
   * the event itself. Carries `{ taskId, ts }`. No actor identity (the
   * obs.* channel records ownerId for ops/admin audit).
   */
  onCancelRequested(cb: (event: CancelRequestedEvent) => void): Unsubscribe {
    this.cancelRequestedCallbacks.push(cb);
    // BLOCKS-370: sticky-replay. A consumer that registers after the wire
    // event arrived (e.g. after `client.connect()` resolves) gets the first
    // cancel_requested synthesized here — UNLESS a terminal has since been
    // delivered, in which case the task is over and replaying would invert
    // causality. Same idempotency + causality contract as onTerminal's
    // terminalTracker path.
    if (this.firstCancelRequested && !this.terminalTracker.isDelivered) {
      try {
        cb(this.firstCancelRequested);
      } catch (err) {
        this.routeCallbackError(
          err,
          'onCancelRequested',
          this.firstCancelRequested,
        );
      }
    }
    return () => {
      this.cancelRequestedCallbacks = this.cancelRequestedCallbacks.filter(
        (c) => c !== cb,
      );
    };
  }

  onEvent(cb: (event: TaskEvent) => void): Unsubscribe {
    this.eventCallbacks.push(cb);
    return () => {
      this.eventCallbacks = this.eventCallbacks.filter(c => c !== cb);
    };
  }

  /**
   * Register an error handler for callback exceptions.
   * If registered, callback errors are routed here instead of the SDK logger.
   * Returns an unsubscribe function.
   */
  onError(cb: (error: Error, context: CallbackErrorContext) => void): Unsubscribe {
    this.errorCallbacks.push(cb);
    return () => {
      this.errorCallbacks = this.errorCallbacks.filter(c => c !== cb);
    };
  }

  // -- Artifact API --

  /** Return all artifact refs seen so far (from history and live events). */
  listArtifacts(): ArtifactRef[] {
    return [...this.artifacts];
  }

  /** Return all history events from connect() in arrival order. */
  listEvents(): TaskEvent[] {
    return [...this.historyEvents];
  }

  /** @internal Called by connect() to append buffer-drain events to the history snapshot. */
  _appendHistoryEvent(event: TaskEvent, timetoken?: string): void {
    if (timetoken) {
      if (this.historyTimetokens.has(timetoken)) return;
      this.historyTimetokens.add(timetoken);
    }
    this.historyEvents.push(event);
  }

  /**
   * Download an artifact from an ArtifactRef.
   * Delegates to the standalone downloadArtifact() helper.
   * If the session has no active PubNub client (e.g., pre-closed or skipSubscription
   * with destroyed client), lazily creates a temporary client for the download.
   */
  async downloadArtifact(ref: ArtifactRef): Promise<DownloadedArtifact> {
    if (this.pubnub) {
      return downloadArtifact(ref, this.pubnub);
    }

    // Lazily create a temporary PubNub client for the download
    const sessionId = `blocks-dl-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`;
    const tempPubnub = new PubNub({
      subscribeKey: this.subscribeKey,
      publishKey: this.publishKey || undefined,
      userId: sessionId,
      ...buildPubNubLogConfig(),
    });
    if (this.readToken) {
      tempPubnub.setToken(this.readToken);
    }

    try {
      return await downloadArtifact(ref, tempPubnub);
    } finally {
      tempPubnub.destroy();
    }
  }

  // -- Stream discovery API --

  onStream(cb: (stream: StreamRef) => void): Unsubscribe {
    this.streamCallbacks.push(cb);
    // Fire for already-known streams
    for (const ref of this.streams.values()) {
      try { cb(ref); } catch (err) {
        this.routeCallbackError(err, 'onStream', ref);
      }
    }
    return () => {
      this.streamCallbacks = this.streamCallbacks.filter(c => c !== cb);
    };
  }

  listStreams(): StreamRef[] {
    return [...this.streams.values()];
  }

  /**
   * Open every readable stream known to this session synchronously.
   *
   * Returns an array of `StreamClient`s in insertion order (matching
   * `listStreams()`). Outbound-only streams are skipped; streams that
   * fail to open (already ended, terminal session, etc.) are skipped
   * silently. Calling twice returns the same client objects for
   * already-opened streams via `StreamRef.open()` idempotence.
   *
   * This is an active-session convenience. Under the merged t7c
   * baseline, `StreamRef.open()` throws `StreamUnavailableError` for
   * never-opened streams on a terminal session — call this method
   * while the task is still running if the goal is to observe every
   * stream.
   */
  openAllStreams(options?: { reorderTimeoutMs?: number }): StreamClient[] {
    const clients: StreamClient[] = [];
    for (const ref of this.streams.values()) {
      const dir = ref.descriptor.localDirection;
      if (dir !== 'inbound' && dir !== 'bidirectional') continue;
      try {
        clients.push(ref.open(options));
      } catch {
        // Terminal session, already-ended ref, or other open failure:
        // skip silently. Callers can inspect `listStreams()` to see
        // which refs exist and branch on ref.isOpen / session.state
        // for richer diagnostics.
      }
    }
    return clients;
  }

  waitForStream(streamId?: string): Promise<StreamRef> {
    if (this.closed) {
      return Promise.reject(new Error('TaskSession is closed'));
    }

    // skipSubscription mode: no live events will arrive
    if (this.skipSubscriptionMode) {
      if (streamId !== undefined) {
        const existing = this.findStreamByIdOrDeclared(streamId);
        if (existing) return Promise.resolve(existing);
      } else if (this.streams.size === 1) {
        return Promise.resolve(this.streams.values().next().value!);
      } else if (this.streams.size > 1) {
        return Promise.reject(
          new Error(
            `Multiple streams exist (${this.streams.size}). ` +
            `Use waitForStream(streamId) or waitForStreamWhere(predicate) to select one.`,
          ),
        );
      }
      return Promise.reject(
        new Error(
          'No matching stream found. This is a terminal task session with no live subscription ' +
          '-- no future stream announcements will arrive.',
        ),
      );
    }

    // Check already-known streams
    if (streamId !== undefined) {
      const existing = this.findStreamByIdOrDeclared(streamId);
      if (existing) return Promise.resolve(existing);
    } else {
      // No streamId: must have exactly one stream
      if (this.streams.size === 1) {
        return Promise.resolve(this.streams.values().next().value!);
      }
      if (this.streams.size > 1) {
        return Promise.reject(
          new Error(
            `Multiple streams exist (${this.streams.size}). ` +
            `Use waitForStream(streamId) or waitForStreamWhere(predicate) to select one.`,
          ),
        );
      }
    }

    return new Promise<StreamRef>((resolve, reject) => {
      this.streamWaiters.push({ resolve, reject, streamId });
    });
  }

  waitForStreamWhere(
    predicate: (stream: StreamRef) => boolean,
  ): Promise<StreamRef> {
    if (this.closed) {
      return Promise.reject(new Error('TaskSession is closed'));
    }

    // Check already-known streams
    for (const ref of this.streams.values()) {
      try {
        if (predicate(ref)) return Promise.resolve(ref);
      } catch (err) {
        this.routeCallbackError(err, 'streamPredicate', ref);
      }
    }

    // skipSubscription mode: no live events will arrive
    if (this.skipSubscriptionMode) {
      return Promise.reject(
        new Error(
          'No matching stream found. This is a terminal task session with no live subscription ' +
          '-- no future stream announcements will arrive.',
        ),
      );
    }

    return new Promise<StreamRef>((resolve, reject) => {
      this.streamWaiters.push({ resolve, reject, predicate });
    });
  }

  // -- waitForTerminal (Fix 2) --

  /**
   * Wait for the task to reach a terminal state. Returns the terminal event.
   *
   * Resolves immediately if the session is already in a terminal state
   * (pre-closed idempotent hit or terminal connect()).
   *
   * @param timeoutMs Optional timeout in milliseconds. Rejects with Error on timeout.
   */
  async waitForTerminal(timeoutMs?: number): Promise<TerminalEvent> {
    // BLOCKS-370 R7: peek() is the source of truth for "has a terminal
    // been delivered through this session?" — covers both wire-arrived
    // and synthetic terminals.
    const existing = this.terminalTracker.peek();
    if (existing) return existing;

    // Pre-tracker safety net: a session constructed already-terminal that
    // has not yet been observed by any callback or waiter. Synthesize and
    // record so any subsequent wire-level duplicate is suppressed.
    if (this.state && TERMINAL_STATES.has(this.state)) {
      const synth: TerminalEvent = {
        type: 'terminal',
        taskId: this.taskId,
        state: this.state as 'completed' | 'failed' | 'canceled',
      };
      this.terminalTracker.tryDeliver(synth, () => {});
      return synth;
    }

    if (this.closed) {
      throw new Error('TaskSession closed');
    }

    return new Promise<TerminalEvent>((resolve, reject) => {
      const waiter = { resolve, reject };
      this.terminalWaiters.push(waiter);

      if (timeoutMs !== undefined) {
        const timer = setTimeout(() => {
          // Remove this waiter from the array
          const idx = this.terminalWaiters.indexOf(waiter);
          if (idx !== -1) this.terminalWaiters.splice(idx, 1);
          reject(new Error(`waitForTerminal timed out after ${timeoutMs}ms`));
        }, timeoutMs);

        // Clear timeout if waiter resolves/rejects before timer fires.
        // Wrap resolve/reject to clear the timer on settlement.
        const origResolve = waiter.resolve;
        const origReject = waiter.reject;
        waiter.resolve = (event) => { clearTimeout(timer); origResolve(event); };
        waiter.reject = (err) => { clearTimeout(timer); origReject(err); };
      }
    });
  }

  // -- saveArtifacts (Fix 5) --

  /**
   * Download all accumulated artifacts and save them to a directory.
   * Creates the directory if it does not exist.
   * Returns the list of file paths written.
   */
  async saveArtifacts(dir: string): Promise<string[]> {
    const { mkdirSync, writeFileSync } = await import('node:fs');
    const { join, resolve, basename } = await import('node:path');
    const resolvedDir = resolve(dir);
    mkdirSync(resolvedDir, { recursive: true });
    const paths: string[] = [];
    const artifacts = this.listArtifacts();
    for (let i = 0; i < artifacts.length; i++) {
      const downloaded = await this.downloadArtifact(artifacts[i]);
      const rawName = downloaded.fileName ?? `artifact-${i}`;
      // Sanitize: strip path separators to prevent directory traversal
      const safeName = basename(rawName) || `artifact-${i}`;
      const filePath = join(resolvedDir, safeName);
      // Verify the resolved path stays within the target directory
      const resolvedPath = resolve(filePath);
      if (!resolvedPath.startsWith(resolvedDir + '/') && resolvedPath !== resolvedDir) {
        throw new Error(`Artifact filename "${rawName}" resolves outside target directory`);
      }
      writeFileSync(resolvedPath, downloaded.data);
      paths.push(resolvedPath);
    }
    return paths;
  }

  // -- Task lifecycle --

  async cancel(): Promise<void> {
    if (!this.rpcConfig) {
      throw new Error('TaskSession was not created with RPC config; use TaskClient.cancelTask() directly');
    }
    await callRpc<void>(this.rpcConfig, 'CancelTask', { taskId: this.taskId });
  }

  async terminate(): Promise<void> {
    if (!this.rpcConfig) {
      throw new Error('TaskSession was not created with RPC config; use TaskClient.terminateTask() directly');
    }
    await callRpc<void>(this.rpcConfig, 'TerminateTask', { taskId: this.taskId });
  }

  // -- Cleanup --

  close(): void {
    if (this.closed) return;
    this.closed = true;

    // Cancel pending drain timer
    if (this.drainTimer) {
      clearTimeout(this.drainTimer);
      this.drainTimer = null;
    }

    // End all open stream clients (fire-and-forget in sync close;
    // use asyncClose() for awaited cleanup)
    for (const client of this.openStreamClients) {
      if (client.isActive) {
        client.end().catch(() => {});
      }
    }
    this.openStreamClients.clear();

    // Reject all pending waiters
    const waitError = new Error('TaskSession closed');
    for (const waiter of this.streamWaiters) {
      waiter.reject(waitError);
    }
    this.streamWaiters = [];
    for (const waiter of this.terminalWaiters) {
      waiter.reject(waitError);
    }
    this.terminalWaiters = [];

    // Unsubscribe from task channel (pubnub is null for pre-closed sessions)
    if (this.pubnub) {
      if (this.listener) {
        this.pubnub.removeListener(this.listener);
        this.listener = null;
      }
      this.pubnub.unsubscribe({ channels: [this.statusChannel] });

      // Destroy session-owned PubNub client
      if (this.ownsSubscribeClient) {
        this.pubnub.destroy();
      }
    }

    // Clear callbacks
    this.progressCallbacks = [];
    this.artifactCallbacks = [];
    this.terminalCallbacks = [];
    this.cancelRequestedCallbacks = [];
    this.eventCallbacks = [];
    this.streamCallbacks = [];
    this.errorCallbacks = [];
  }

  /**
   * Async close that awaits all StreamClient.end() calls before
   * performing sync cleanup. Preferred over close() when streams
   * may have buffered data to flush.
   */
  async asyncClose(): Promise<void> {
    if (this.closed) return;
    // End all open stream clients and await flush
    const endPromises = [...this.openStreamClients]
      .filter(c => c.isActive)
      .map(c => c.end().catch(() => {}));
    await Promise.all(endPromises);
    this.openStreamClients.clear();
    this.close(); // sync remainder: unsubscribe, reject waiters, etc.
  }

  async [Symbol.asyncDispose](): Promise<void> {
    await this.asyncClose();
  }

  /** Whether the session has been closed. */
  get isClosed(): boolean {
    return this.closed;
  }
}
