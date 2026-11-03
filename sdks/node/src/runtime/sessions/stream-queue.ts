import type { Message, StreamSubscription } from './stream-types.js';

export const STREAM_BUFFER_LIMIT = 1000;

/**
 * The stream's undelivered messages. When the consumer lags behind, the oldest is dropped;
 * those still buffered are delivered before the iteration completes or throws.
 */
export class StreamQueue {
  private readonly buffer: Message[] = [];
  private readonly waiting: Array<() => void> = [];
  private iterated = false;
  private closed = false;
  private error?: Error;

  constructor(private readonly onReturn: () => Promise<void>) {}

  push(message: Message): void {
    if (this.closed) return;
    if (this.buffer.length >= STREAM_BUFFER_LIMIT) this.buffer.shift();
    this.buffer.push(message);
    this.wake();
  }

  close(error?: Error): void {
    if (this.closed) return;
    this.closed = true;
    this.error = error;
    this.wake();
  }

  /** A subscription has one consumer, so a second iteration is refused. */
  iterator(): AsyncIterator<Message> {
    if (this.iterated) throw new TypeError('A stream subscription can be iterated only once');
    this.iterated = true;
    return {
      next: () => this.next(),
      return: async () => {
        await this.onReturn();
        return { done: true, value: undefined };
      },
    };
  }

  private wake(): void {
    for (const resolve of this.waiting.splice(0)) resolve();
  }

  private async next(): Promise<IteratorResult<Message>> {
    for (;;) {
      const value = this.buffer.shift();
      if (value !== undefined) return { done: false, value };
      if (this.closed) {
        const error = this.error;
        this.error = undefined;
        if (error) throw error;
        return { done: true, value: undefined };
      }
      await new Promise<void>((resolve) => {
        this.waiting.push(resolve);
      });
    }
  }
}

export function streamSubscription(
  streamId: string,
  queue: StreamQueue,
  stop: () => Promise<void>,
): StreamSubscription {
  return { streamId, stop, [Symbol.asyncIterator]: () => queue.iterator() };
}
