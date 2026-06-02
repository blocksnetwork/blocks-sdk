import type { TerminalEvent } from './task-session.js';

/**
 * Layer-1 dedup for terminal events.
 *
 * Cooperative cancel and the scanner's Phase-6 safety net can both produce
 * a terminal for the same task. The DB layer (`taskFanout` PubNub Function)
 * already dedupes at the row-status level, but consumers subscribed to the
 * task channel still see two wire-level terminals. This tracker enforces
 * exactly-once delivery to every public terminal callback surface
 * (`TaskSession.onTerminal`, `waitForTerminal`, `subscribeToTask`,
 * synthetic re-emit) so SDK consumers never have to defend against the
 * duplicate themselves.
 *
 * The tracker is single-shot: the *first* terminal observed wins; any
 * later terminal — different state, different reason, different source —
 * is silently dropped. The user's intent (cancel) wins over a stale agent
 * terminal because the scanner publishes after the cancel-budget has
 * elapsed.
 */
export class TerminalDeliveryTracker {
  private delivered = false;
  private firstEvent: TerminalEvent | null = null;

  /**
   * Deliver the event through `onDeliver` if no terminal has been
   * delivered yet. Returns `true` when the event was the first and was
   * delivered; `false` when a terminal had already been delivered and the
   * event was dropped. The `delivered` flag is set *before* the callback
   * runs so the callback itself sees `isDelivered === true` (re-entrant
   * safety: prevents recursive delivery via callback side-effects).
   */
  tryDeliver(
    evt: TerminalEvent,
    onDeliver: (e: TerminalEvent) => void,
  ): boolean {
    if (this.delivered) return false;
    this.delivered = true;
    this.firstEvent = evt;
    onDeliver(evt);
    return true;
  }

  /**
   * Returns the first delivered terminal, or `null` if none has been
   * delivered yet. Used by `onTerminal` and `waitForTerminal` to
   * synchronously hand a previously-delivered terminal to a freshly
   * registered observer.
   */
  peek(): TerminalEvent | null {
    return this.firstEvent;
  }

  get isDelivered(): boolean {
    return this.delivered;
  }
}
