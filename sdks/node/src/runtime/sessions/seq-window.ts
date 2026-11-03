/** Counterpart `seq` starts at 1; backend notices carry 0 and are not sequenced. */
export function isCounterpartSeq(seq: unknown): seq is number {
  return Number.isSafeInteger(seq) && (seq as number) >= 1;
}

type Range = [low: number, high: number];

/**
 * The set of one sender's `seq` values already processed, kept as the
 * highest one seen plus the sorted ranges below it never seen. Memory grows
 * per gap, not per message or per missing `seq`.
 */
export class SeqWindow {
  private max = 0;
  private readonly missing: Range[] = [];

  /** The lowest `seq` not yet processed. */
  get next(): number {
    return this.missing[0]?.[0] ?? this.max + 1;
  }

  get highest(): number {
    return this.max;
  }

  has(seq: number): boolean {
    return seq <= this.max && this.rangeIndex(seq) < 0;
  }

  add(seq: number): void {
    if (seq > this.max) {
      if (seq > this.max + 1) this.missing.push([this.max + 1, seq - 1]);
      this.max = seq;
      return;
    }
    const index = this.rangeIndex(seq);
    if (index < 0) return;
    const [low, high] = this.missing[index];
    const rest: Range[] = [];
    if (low < seq) rest.push([low, seq - 1]);
    if (seq < high) rest.push([seq + 1, high]);
    this.missing.splice(index, 1, ...rest);
  }

  /** Treats every `seq` below `seq` as processed. */
  skipTo(seq: number): void {
    while (this.missing.length > 0 && this.missing[0][1] < seq) this.missing.shift();
    if (this.missing.length > 0 && this.missing[0][0] < seq) this.missing[0][0] = seq;
    this.max = Math.max(this.max, seq - 1);
  }

  /** Binary search for the missing range holding `seq`, or -1. */
  private rangeIndex(seq: number): number {
    let lo = 0;
    let hi = this.missing.length - 1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      const [low, high] = this.missing[mid];
      if (seq < low) hi = mid - 1;
      else if (seq > high) lo = mid + 1;
      else return mid;
    }
    return -1;
  }
}
