import { createLogger } from '../logger.js';
import { SeqWindow } from './seq-window.js';

/** How long a `seq` gap may persist before recovery runs; the stream reorder default. */
export const SESSION_GAP_RECOVER_MS = 750;

const logger = createLogger('Session');

interface SenderQueue<M> {
  window: SeqWindow;
  held: Map<number, M>;
  /** Held seqs that history also returned, so a gap below one was missing from history too. */
  replayed: Set<number>;
  timer?: ReturnType<typeof setTimeout>;
}

export interface InOrderDeliveryOptions<M> {
  deliver(message: M): void;
  /** Fills gaps from history by feeding the missing messages back through `accept`. */
  recover(): Promise<void>;
}

/**
 * Delivers each sender's messages in `seq` order exactly once. A message
 * ahead of a gap is held; a gap that outlasts {@link SESSION_GAP_RECOVER_MS}
 * runs `recover()`, after which any `seq` still missing is skipped.
 */
export class InOrderDelivery<M> {
  private readonly queues = new Map<string, SenderQueue<M>>();
  private recovering?: Promise<void>;
  private disposed = false;

  constructor(private readonly options: InOrderDeliveryOptions<M>) {}

  /**
   * Returns false for a `(sender, seq)` already delivered or held, or once disposed. `replayed`
   * marks a message read from history rather than received live.
   */
  accept(sender: string, seq: number, message: M, replayed = false): boolean {
    if (this.disposed) return false;
    const queue = this.queueFor(sender);
    if (queue.window.has(seq)) return false;
    if (queue.held.has(seq)) {
      if (replayed) queue.replayed.add(seq);
      return false;
    }
    if (seq !== queue.window.next) {
      queue.held.set(seq, message);
      if (replayed) queue.replayed.add(seq);
      if (!queue.timer) this.armGapTimer(queue);
      return true;
    }
    this.deliverSeq(queue, seq, message);
    // A partial fill keeps the running timer: the remaining gap is as old as the first.
    this.drain(queue);
    return true;
  }

  /** Each sender's highest held `seq`: the gaps a recovery is asked to close. */
  heldCutoffs(): Map<string, number> {
    return this.cutoffsOf((queue) => queue.held.keys());
  }

  /** Each sender's highest held `seq` that history returned: the gaps a replay revealed. */
  replayedCutoffs(): Map<string, number> {
    return this.cutoffsOf((queue) => queue.replayed);
  }

  /**
   * Delivers every message held at or below each sender's cutoff, skipping any `seq` history did
   * not fill. Recovery calls it before it applies the backend's record, so a held artifact reaches
   * its exchange while that exchange is still unsettled.
   */
  skipThrough(cutoffs: Map<string, number>): void {
    for (const [sender, cutoff] of cutoffs) {
      const queue = this.queues.get(sender);
      if (!queue) continue;
      const heldBefore = queue.held.size;
      this.skipQueueThrough(queue, cutoff);
      // What is still held waits behind a newer gap, which gets its own window, not the old one's.
      if (queue.held.size > 0 && queue.held.size < heldBefore && !this.disposed) this.armGapTimer(queue);
    }
  }

  /** Every message held behind a gap, each sender's in `seq` order. */
  heldMessages(): M[] {
    return [...this.queues.values()].flatMap((queue) =>
      [...queue.held.keys()].sort((a, b) => a - b).map((seq) => queue.held.get(seq) as M),
    );
  }

  /** True when any held message matches. */
  holdsAny(matches: (message: M) => boolean): boolean {
    for (const queue of this.queues.values()) {
      for (const message of queue.held.values()) if (matches(message)) return true;
    }
    return false;
  }

  /** True while any sender has a message held behind a gap. */
  get holding(): boolean {
    for (const queue of this.queues.values()) if (queue.held.size > 0) return true;
    return false;
  }

  dispose(): void {
    this.disposed = true;
    for (const queue of this.queues.values()) clearTimeout(queue.timer);
    this.queues.clear();
  }

  private queueFor(sender: string): SenderQueue<M> {
    let queue = this.queues.get(sender);
    if (!queue) {
      queue = { window: new SeqWindow(), held: new Map(), replayed: new Set() };
      this.queues.set(sender, queue);
    }
    return queue;
  }

  private deliverSeq(queue: SenderQueue<M>, seq: number, message: M): void {
    queue.held.delete(seq);
    queue.replayed.delete(seq);
    queue.window.add(seq);
    this.options.deliver(message);
  }

  private drain(queue: SenderQueue<M>): void {
    let next = queue.held.get(queue.window.next);
    while (next !== undefined && !this.disposed) {
      this.deliverSeq(queue, queue.window.next, next);
      next = queue.held.get(queue.window.next);
    }
    if (queue.held.size === 0) {
      clearTimeout(queue.timer);
      queue.timer = undefined;
    }
  }

  private cutoffsOf(seqs: (queue: SenderQueue<M>) => Iterable<number>): Map<string, number> {
    const cutoffs = new Map<string, number>();
    for (const [sender, queue] of this.queues) {
      const held = [...seqs(queue)];
      if (held.length > 0) cutoffs.set(sender, Math.max(...held));
    }
    return cutoffs;
  }

  private armGapTimer(queue: SenderQueue<M>): void {
    clearTimeout(queue.timer);
    queue.timer = setTimeout(() => void this.recoverGap(queue), SESSION_GAP_RECOVER_MS);
  }

  private async recoverGap(queue: SenderQueue<M>): Promise<void> {
    queue.timer = undefined;
    const cutoff = Math.max(...queue.held.keys());
    try {
      await this.recoverAfterInFlight();
    } catch (err) {
      logger('warn', 'session recovery failed; skipping the gap', { error: String(err) });
    }
    if (this.disposed) return;
    this.skipQueueThrough(queue, cutoff);
    // A message that opened a fresh gap during recovery already armed its own timer.
    if (queue.held.size > 0 && !queue.timer && !this.disposed) this.armGapTimer(queue);
  }

  private skipQueueThrough(queue: SenderQueue<M>, cutoff: number): void {
    const stillHeld = [...queue.held.keys()].filter((seq) => seq <= cutoff).sort((a, b) => a - b);
    for (const seq of stillHeld) {
      if (this.disposed) return;
      if (seq > queue.window.next) {
        logger('warn', 'session seq missing after recovery; skipped', {
          from: queue.window.next,
          to: seq - 1,
        });
      }
      queue.window.skipTo(seq);
      this.deliverSeq(queue, seq, queue.held.get(seq) as M);
    }
    this.drain(queue);
  }

  /**
   * Runs a fresh `recover()` after any in-flight one: history fetched before
   * this gap was detected may not hold its missing messages.
   */
  private recoverAfterInFlight(): Promise<void> {
    const previous = this.recovering?.catch(() => {}) ?? Promise.resolve();
    const run: Promise<void> = previous
      .then(() => this.options.recover())
      .finally(() => {
        if (this.recovering === run) this.recovering = undefined;
      });
    this.recovering = run;
    return run;
  }
}
