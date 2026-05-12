import type { HandlerResult, StartTaskMessage, TaskContext } from '@blocks-network/sdk';

const DEFAULT_SYMBOLS = ['AAPL', 'MSFT', 'NVDA'];

export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const log = (msg: string) => console.log(`[stock-sim] ${msg}`);

  if (!ctx) {
    return {
      artifacts: [{ data: JSON.stringify({ error: 'TaskContext is required for streaming' }), mimeType: 'application/json' }],
    };
  }

  if (task.taskKind && task.taskKind !== 'pipe') {
    throw new Error('stock-sim only supports pipe tasks');
  }

  log(`Task ${task.taskId} received from ${task.ownerId}`);

  const symbols = parseSymbols(task.requestParts);
  const durationMinutes = normalizeDuration(task.duration);

  log(`Symbols: ${symbols.join(', ')}  Duration: ${durationMinutes}m`);

  // format: 'events' means the stream carries structured JSON events (vs 'bytes' for raw binary)
  const stream = await ctx.createStream({ format: 'events' });
  log(`Stream created: ${stream.channel}`);

  const lastQuotes: Record<string, QuoteEvent> = {};
  let updatesEmitted = 0;
  let tick = 0;

  ctx.reportStatus(
    `Streaming ${symbols.join(', ')} for ${durationMinutes} minute${durationMinutes === 1 ? '' : 's'}...`,
  );

  try {
    // cancelSignal is an AbortSignal for cooperative cancellation (CancelTask or duration expiry)
    while (!ctx.cancelSignal.aborted) {
      tick += 1;

      for (const symbol of symbols) {
        const quote = buildQuote(symbol, lastQuotes[symbol]?.price, tick);
        lastQuotes[symbol] = quote;
        updatesEmitted += 1;
        stream.write(quote);
      }

      if (tick % 10 === 0) {
        log(`Tick ${tick}: ${updatesEmitted} quotes sent`);
      }

      await sleepMs(1_000, ctx.cancelSignal);
    }
  } catch (err) {
    if (!isAbortError(err)) {
      throw err;
    }
  }

  log(`Ending stream (${updatesEmitted} quotes sent across ${tick} ticks)`);
  await stream.end();
  log('Stream ended');

  const completionReason = ctx.isExpired
    ? 'duration_expired'
    : ctx.isCancelled
      ? 'canceled'
      : 'stopped';
  log(`Task complete: ${completionReason}`);

  ctx.reportStatus(
    ctx.isExpired ? 'Streaming complete (duration expired)' : 'Streaming stopped',
  );

  return {
    artifacts: [{
      data: JSON.stringify(
        {
          symbols,
          requestedDurationMinutes: durationMinutes,
          updatesEmitted,
          completionReason,
          lastQuotes,
        },
        null,
        2,
      ),
      mimeType: 'application/json',
    }],
  };
}

interface QuoteEvent {
  type: 'quote';
  symbol: string;
  price: number;
  change: number;
  tick: number;
  at: string;
}

function parseSymbols(parts: unknown[] | undefined): string[] {
  const requestParts = parts ?? [];
  const candidates: string[] = [];

  for (const part of requestParts) {
    if (typeof part === 'string') {
      candidates.push(part);
      continue;
    }

    if (part !== null && typeof part === 'object' && !Array.isArray(part)) {
      const record = parsePartContent(part as Record<string, unknown>);
      if (typeof record.text === 'string') {
        candidates.push(record.text);
      }
      if (typeof record.symbols === 'string') {
        candidates.push(record.symbols);
      }
      if (Array.isArray(record.symbols)) {
        for (const symbol of record.symbols) {
          if (typeof symbol === 'string') {
            candidates.push(symbol);
          }
        }
      }
    }
  }

  const symbols = normalizeSymbols(candidates.join(','));
  return symbols.length > 0 ? symbols : DEFAULT_SYMBOLS;
}

function parsePartContent(part: Record<string, unknown>): Record<string, unknown> {
  if (typeof part.text === 'string') {
    try {
      const parsed = JSON.parse(part.text);
      if (parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // text is plain string (e.g. comma-separated symbols), not JSON -- fall through
    }
  }
  return part;
}

function normalizeSymbols(raw: string): string[] {
  return [...new Set(raw
    .split(',')
    .map((symbol) => symbol.trim().toUpperCase())
    .filter(Boolean))];
}

function normalizeDuration(value: number | undefined): number {
  if (typeof value === 'number' && Number.isFinite(value) && value > 0) {
    return Math.trunc(value);
  }
  return 1;
}

function buildQuote(symbol: string, previousPrice: number | undefined, tick: number): QuoteEvent {
  const startPrice = previousPrice ?? basePriceForSymbol(symbol);
  const nextPrice = Math.max(1, startPrice + randomDelta(startPrice));
  const rounded = roundPrice(nextPrice);
  const previousRounded = previousPrice ?? rounded;

  return {
    type: 'quote',
    symbol,
    price: rounded,
    change: roundPrice(rounded - previousRounded),
    tick,
    at: new Date().toISOString(),
  };
}

function basePriceForSymbol(symbol: string): number {
  let hash = 0;
  for (let i = 0; i < symbol.length; i += 1) {
    hash = ((hash << 5) - hash + symbol.charCodeAt(i)) | 0;
  }
  const normalized = Math.abs(hash % 250);
  return 50 + normalized;
}

function randomDelta(price: number): number {
  const maxMove = Math.max(0.5, price * 0.015);
  return (Math.random() * 2 - 1) * maxMove;
}

function roundPrice(value: number): number {
  return Math.round(value * 100) / 100;
}

function sleepMs(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);

    const onAbort = () => {
      clearTimeout(timer);
      signal.removeEventListener('abort', onAbort);
      reject(new Error('aborted'));
    };

    signal.addEventListener('abort', onAbort, { once: true });
  });
}

function isAbortError(err: unknown): boolean {
  return err instanceof Error && err.message === 'aborted';
}
