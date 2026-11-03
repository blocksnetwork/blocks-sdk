export type StreamMode = 'access_manager' | 'consent';

export type StreamAddress =
  { channel: string; wildcard?: undefined } | { wildcard: string; channel?: undefined };

export type StreamCredentials = StreamAddress & {
  streamId: string;
  subscribeKey: string;
  /** `null`: the PubNub default origin. */
  origin: string | null;
} & (
    | {
        mode: 'access_manager';
        token: string;
        ttlMinutes: number;
        expiresAt: string;
      }
    | { mode: 'consent' }
  );

/** The address and mode fields are present only for a `foreign_subscribe` stream, the one kind consumed. */
export interface StreamStarted {
  exchangeId: string;
  streamId: string;
  kind: string;
  channel?: string;
  wildcard?: string;
  templateParam?: string;
  templateValue?: string;
  mode?: StreamMode;
  presence?: false;
}

/**
 * One message from a session stream, in the shape of one PubNub message.
 *
 * @experimental Part of the sessions preview; see {@link Exchange.stream}.
 */
export interface Message {
  /**
   * The Channel Template value, read from the channel on an all-values stream; absent under raw
   * access, a wildcard pattern included.
   */
  templateValue?: string;
  channel: string;
  timetoken: string;
  publisher?: string;
  message: unknown;
  meta?: unknown;
  /** -1 for a message, 4 for a file. */
  messageType: number;
  customMessageType?: string;
}

/** The messages of one stream client, buffered from its start; iterating it once ends the client. */
export interface StreamSubscription extends AsyncIterable<Message> {
  readonly streamId: string;
  /** Stops delivery; the session stays open. Idempotent. */
  stop(): Promise<void>;
}

export type StreamEndReason = 'stopped' | 'access_revoked';

export interface ForeignStreamOptions {
  sessionId: string;
  credentials: StreamCredentials;
  /** From `stream_started`; set for a Channel Template stream, so an all-values stream carries values. */
  templateParam?: string;
  templateValue?: string;
  /** The stream token endpoint. Its refusals are final, other failures transient. */
  refresh(): Promise<StreamCredentials>;
  onConnectionChange(connected: boolean): void;
  /** The caller stopped the stream, or its token could not be renewed. Called at most once. */
  onEnded(reason: StreamEndReason): void;
}

export interface ForeignStream {
  readonly subscription: StreamSubscription;
  readonly connected: boolean;
  end(error?: Error): Promise<void>;
}
