/**
 * StreamRef - Consumer-side bridge between task events and Stream SDK.
 *
 * StreamRef wraps a StreamDescriptor and provides open() to create a
 * StreamClient via fromDescriptor(). open() is idempotent while the
 * stream client is active and must not create duplicate clients.
 *
 * When the owning `TaskSession` is in a terminal state at the time
 * `open()` is called, the SDK short-circuits with `StreamUnavailableError`
 * instead of constructing a dead client against an already-revoked PAM
 * token. Stream data is live-only; on reconnect after termination, only
 * the descriptor metadata and task artifacts remain accessible.
 */

import {
  StreamClient,
  type StreamDescriptor,
  type StreamClientFromDescriptorOptions,
} from '../stream/index.js';
import { TERMINAL_STATES } from './task-session.js';

export { type StreamDescriptor } from '../stream/index.js';

/**
 * Thrown by `StreamRef.open()` when the owning session's task is in a
 * terminal state. The error's `terminalState` field lets consumers branch
 * programmatically; the message points at accessible alternatives
 * (`ref.descriptor`, `session.listArtifacts()`, `session.state`).
 */
export class StreamUnavailableError extends Error {
  readonly taskId: string;
  readonly streamId: string;
  readonly declaredStream?: string;
  readonly terminalState: string;

  constructor(
    message: string,
    fields: {
      taskId: string;
      streamId: string;
      declaredStream?: string;
      terminalState: string;
    },
  ) {
    super(message);
    this.name = 'StreamUnavailableError';
    this.taskId = fields.taskId;
    this.streamId = fields.streamId;
    this.declaredStream = fields.declaredStream;
    this.terminalState = fields.terminalState;
  }
}

export class StreamRef {
  readonly descriptor: StreamDescriptor;
  private _client: StreamClient | null = null;
  private _clientEnded = false;
  private readonly _sdkOptions: StreamClientFromDescriptorOptions;
  private readonly _onOpen?: (client: StreamClient) => void;
  private readonly _sessionState?: () => string | undefined;

  constructor(
    descriptor: StreamDescriptor,
    sdkOptions: StreamClientFromDescriptorOptions,
    hooks?: {
      onOpen?: (client: StreamClient) => void;
      sessionState?: () => string | undefined;
    },
  ) {
    this.descriptor = descriptor;
    this._sdkOptions = sdkOptions;
    this._onOpen = hooks?.onOpen;
    this._sessionState = hooks?.sessionState;
  }

  /**
   * Open a StreamClient from this ref's descriptor.
   *
   * Resolution order (first match wins):
   *
   *   1. If a StreamClient was previously opened and is still active,
   *      return that same client (idempotency). This also applies while
   *      the session is draining after a terminal event — a consumer
   *      that holds a live client MUST continue to receive it.
   *   2. If the previously opened client has already ended, throw a
   *      generic Error ("already been ended"). The terminal short-
   *      circuit does NOT fire in this path, because "already ended"
   *      is the more specific signal for "no new client here".
   *   3. If the owning session's task is in a terminal state
   *      (`completed`, `failed`, `canceled`) AND no client has ever
   *      been constructed for this ref, throw `StreamUnavailableError`.
   *      Stream data is live-only; use `ref.descriptor`,
   *      `session.listArtifacts()`, or `session.state` to inspect a
   *      terminal session.
   *   4. Otherwise, construct a new StreamClient from the descriptor.
   *
   * Uses descriptor.format for the wire format. No caller-supplied format
   * override is accepted.
   */
  open(options?: { reorderTimeoutMs?: number }): StreamClient {
    // Idempotency branches run BEFORE the terminal short-circuit: the
    // short-circuit exists to prevent *constructing* a new client against
    // a revoked T7c token, not to invalidate an already-live client. A
    // consumer that opened a stream while the task was running must be
    // able to re-call open() during the drain window (or with
    // autoDrain: false) and receive the same live client, per the
    // SDK_CONTRACT idempotency rule.
    if (this._client && this._client.isActive) {
      return this._client;
    }
    if (this._clientEnded) {
      throw new Error(
        `StreamRef for "${this.descriptor.streamId}" has already been ended ` +
        `and cannot be reopened`,
      );
    }

    const state = this._sessionState?.();
    if (state && TERMINAL_STATES.has(state)) {
      const streamName = this.descriptor.declaredStream ?? this.descriptor.streamId;
      throw new StreamUnavailableError(
        `Cannot open stream "${streamName}" on task "${this.descriptor.taskId}": ` +
        `the task is in terminal state "${state}" and stream data is ` +
        `live-only (not persisted after a task ends). The stream's metadata ` +
        `remains available on \`ref.descriptor\`; task artifacts are available ` +
        `via \`session.listArtifacts()\`; the final task state is in \`session.state\`.`,
        {
          taskId: this.descriptor.taskId,
          streamId: this.descriptor.streamId,
          declaredStream: this.descriptor.declaredStream,
          terminalState: state,
        },
      );
    }

    const sdkOpts = options?.reorderTimeoutMs !== undefined
      ? { ...this._sdkOptions, reorderTimeoutMs: options.reorderTimeoutMs }
      : this._sdkOptions;
    const client = StreamClient.fromDescriptor(this.descriptor, sdkOpts);

    client.onEnd(() => {
      this._clientEnded = true;
      this._client = null;
    });

    this._client = client;
    this._onOpen?.(client);
    return client;
  }

  /** Whether this ref's stream client is currently active. */
  get isOpen(): boolean {
    return this._client !== null && this._client.isActive;
  }
}
