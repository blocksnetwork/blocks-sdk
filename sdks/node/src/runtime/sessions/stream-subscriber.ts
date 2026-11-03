import { createSubscribeOnlyPubNubClient } from '../pubnub-client.js';
import {
  DEGRADED_TRANSPORT_CATEGORIES,
  mapTransportCategory,
  type TransportStatusPayload,
} from '../transport-categories.js';
import { ConsumerTokenRotation } from './consumer-token.js';
import { isRefusal } from './session-http.js';
import { toStreamMessage, type StreamEvent, type StreamEventKind } from './stream-message.js';
import { StreamQueue, streamSubscription } from './stream-queue.js';
import type { ForeignStream, ForeignStreamOptions, StreamCredentials } from './stream-types.js';

export function startForeignStream(options: ForeignStreamOptions): ForeignStream {
  return new ForeignStreamSubscriber(options);
}

class ForeignStreamSubscriber implements ForeignStream {
  readonly subscription;
  private readonly pubnub;
  private readonly queue: StreamQueue;
  private readonly rotation?: ConsumerTokenRotation;
  private readonly listener;
  private isConnected = false;
  private tornDown = false;
  private ended = false;

  constructor(private readonly options: ForeignStreamOptions) {
    const { credentials } = options;
    this.queue = new StreamQueue(() => this.stop());
    this.subscription = streamSubscription(credentials.streamId, this.queue, () => this.stop());
    this.pubnub = createSubscribeOnlyPubNubClient({
      subscribeKey: credentials.subscribeKey,
      origin: credentials.origin,
      userId: crypto.randomUUID(),
    });
    this.listener = {
      message: (event: StreamEvent) => this.deliver(event, 'message'),
      file: (event: StreamEvent) => this.deliver(event, 'file'),
      status: (status: TransportStatusPayload) => this.onStatus(status),
    };
    this.pubnub.addListener(this.listener);
    if (credentials.mode === 'access_manager') {
      this.rotation = new ConsumerTokenRotation({
        sessionId: options.sessionId,
        refresh: async () => {
          try {
            const next = await options.refresh();
            if (next.mode !== 'access_manager') {
              throw new Error('The stream token refresh returned no token');
            }
            return { pamToken: next.token, ttlMinutes: next.ttlMinutes };
          } catch (err) {
            // A scheduled rotation gives up on a refusal without reporting it; the stream must end.
            if (isRefusal(err)) queueMicrotask(() => this.revoke());
            throw err;
          }
        },
        setToken: (token) => this.pubnub.setToken(token),
        resume: () => this.pubnub.reconnect(),
        onRevoked: () => this.revoke(),
      });
      this.pubnub.setToken(credentials.token);
      this.rotation.start(credentials.ttlMinutes);
    }
    this.pubnub.subscribe({ channels: [addressChannel(credentials)] });
  }

  get connected(): boolean {
    return this.isConnected;
  }

  async end(error?: Error): Promise<void> {
    this.ended = true;
    this.tearDown();
    this.queue.close(error);
  }

  private async stop(): Promise<void> {
    this.tearDown();
    this.queue.close();
    this.notifyEnded('stopped');
  }

  private revoke(): void {
    this.tearDown();
    this.notifyEnded('access_revoked');
  }

  private notifyEnded(reason: 'stopped' | 'access_revoked'): void {
    if (this.ended) return;
    this.ended = true;
    this.options.onEnded(reason);
  }

  private tearDown(): void {
    if (this.tornDown) return;
    this.tornDown = true;
    this.rotation?.stop();
    this.pubnub.removeListener(this.listener);
    this.pubnub.unsubscribeAll();
    this.pubnub.destroy();
  }

  private deliver(event: StreamEvent, kind: StreamEventKind): void {
    const { credentials, templateParam, templateValue } = this.options;
    const message = toStreamMessage(event, kind, credentials, { templateParam, templateValue });
    if (message) this.queue.push(message);
  }

  private onStatus(status: TransportStatusPayload): void {
    const category = mapTransportCategory(status);
    if (category === 'connected' || category === 'reconnected') {
      this.rotation?.onConnected();
      this.setConnected(true);
    } else if (category === 'access_denied') {
      this.setConnected(false);
      if (this.rotation) this.rotation.onDenied();
      else this.revoke();
    } else if (DEGRADED_TRANSPORT_CATEGORIES.has(category)) {
      this.setConnected(false);
    }
  }

  private setConnected(connected: boolean): void {
    if (this.isConnected === connected) return;
    this.isConnected = connected;
    this.options.onConnectionChange(connected);
  }
}

function addressChannel(credentials: StreamCredentials): string {
  return credentials.wildcard ?? credentials.channel;
}
