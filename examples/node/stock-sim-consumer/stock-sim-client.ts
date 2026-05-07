import { createInterface } from 'node:readline/promises';
import { stdin as input, stdout as output } from 'node:process';
import type { TaskClient } from '@blocks-network/sdk';

const DEFAULT_SYMBOLS = 'AAPL,MSFT,NVDA';
const DEFAULT_DURATION_MINUTES = 1;
const PROVIDERS: Record<string, string> = { node: 'stock-sim', python: 'stock-sim-python' };
const DEFAULT_PROVIDER = 'node';

export interface StockQuote {
  type: 'quote';
  symbol: string;
  price: number;
  change: number;
  tick: number;
  at: string;
}

export interface StockRequest {
  symbolsInput: string;
  symbols: string[];
  durationMinutes: number;
  provider: string;
}

export interface StockStreamResult {
  providerTaskId: string;
  symbols: string[];
  durationMinutes: number;
  quotesReceived: number;
  lastQuotes: Record<string, StockQuote>;
}

export function parseStockRequest(parts: unknown[] | undefined): Partial<StockRequest> {
  const requestParts = parts ?? [];
  const result: Partial<StockRequest> = {};

  for (const part of requestParts) {
    if (typeof part === 'string') {
      result.symbolsInput = part;
      continue;
    }

    if (part !== null && typeof part === 'object' && !Array.isArray(part)) {
      let record = part as Record<string, unknown>;
      // The task:send script wraps --message as {kind: "input_text", text: "..."},
      // so try to JSON-parse the text field to extract structured fields.
      if (typeof record.text === 'string') {
        try {
          const parsed = JSON.parse(record.text);
          if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
            record = { ...record, ...parsed };
          }
        } catch {
          result.symbolsInput = record.text as string;
        }
      }
      if (typeof record.symbols === 'string') {
        result.symbolsInput = record.symbols;
      } else if (Array.isArray(record.symbols)) {
        result.symbolsInput = record.symbols.filter((value): value is string => typeof value === 'string').join(',');
      } else if (typeof record.text === 'string' && !result.symbolsInput) {
        result.symbolsInput = record.text;
      }
      if (typeof record.durationMinutes === 'number') {
        result.durationMinutes = normalizeDuration(record.durationMinutes);
      }
      if (typeof record.duration === 'number') {
        result.durationMinutes = normalizeDuration(record.duration);
      }
      if (typeof record.provider === 'string' && record.provider in PROVIDERS) {
        result.provider = record.provider;
      }
    }
  }

  return result;
}

export function finalizeStockRequest(initial: Partial<StockRequest> = {}): StockRequest {
  const symbolsInput = (initial.symbolsInput ?? DEFAULT_SYMBOLS).trim() || DEFAULT_SYMBOLS;
  const symbols = normalizeSymbols(symbolsInput);

  return {
    symbolsInput: symbols.join(','),
    symbols,
    durationMinutes: initial.durationMinutes ?? DEFAULT_DURATION_MINUTES,
    provider: initial.provider ?? DEFAULT_PROVIDER,
  };
}

export async function promptForStockRequest(
  initial: Partial<StockRequest> = {},
): Promise<StockRequest> {
  const defaults = finalizeStockRequest(initial);
  const rl = createInterface({ input, output });

  try {
    const symbolsInput =
      (await rl.question(`Symbols (comma-separated) [${defaults.symbolsInput}]: `)).trim() ||
      defaults.symbolsInput;
    const durationRaw =
      (await rl.question(`Duration in minutes [${defaults.durationMinutes}]: `)).trim();
    const providerRaw =
      (await rl.question(`Provider (node/python) [${defaults.provider}]: `)).trim().toLowerCase();

    return finalizeStockRequest({
      symbolsInput,
      durationMinutes:
        durationRaw.length > 0 ? normalizeDuration(Number(durationRaw)) : defaults.durationMinutes,
      provider: providerRaw in PROVIDERS ? providerRaw : defaults.provider,
    });
  } finally {
    rl.close();
  }
}

export async function runStockSimTask(options: {
  taskClient: TaskClient;
  ownerId: string;
  request: StockRequest;
  log?: (line: string) => void;
}): Promise<StockStreamResult> {
  const { taskClient, ownerId, request, log } = options;
  const agentName = PROVIDERS[request.provider] ?? PROVIDERS[DEFAULT_PROVIDER];
  const session = await taskClient.sendMessage({
    agentName,
    ownerId,
    taskKind: 'pipe',
    duration: request.durationMinutes,
    requestParts: [{ partId: 'symbols', text: request.symbolsInput }],
  });

  log?.(
    `Submitted pipe task ${session.taskId} for ${request.symbols.join(', ')} ` +
    `(${request.durationMinutes} minute${request.durationMinutes === 1 ? '' : 's'})`,
  );

  const lastQuotes: Record<string, StockQuote> = {};
  let quotesReceived = 0;

  const streamRef = await session.waitForStream();
  log?.(`Opened stream ${streamRef.descriptor.streamId}`);

  const stream = streamRef.open();

  // stock-sim declares `format: events` and writes one quote (or a batch)
  // per write. `events<T>()` flattens batched event arrays into a single
  // yield per event, so the consumer does not need to handle batching.
  for await (const ev of stream.events<unknown>()) {
    for (const quote of normalizeQuotes(ev)) {
      lastQuotes[quote.symbol] = quote;
      quotesReceived += 1;
      log?.(
        `[${quote.at}] ${quote.symbol} $${quote.price.toFixed(2)} ` +
        `(${formatChange(quote.change)})`,
      );
    }
  }

  session.close();

  return {
    providerTaskId: session.taskId,
    symbols: request.symbols,
    durationMinutes: request.durationMinutes,
    quotesReceived,
    lastQuotes,
  };
}

function normalizeSymbols(raw: string): string[] {
  const symbols = [...new Set(raw
    .split(',')
    .map((symbol) => symbol.trim().toUpperCase())
    .filter(Boolean))];
  return symbols.length > 0 ? symbols : DEFAULT_SYMBOLS.split(',');
}

function normalizeDuration(value: number): number {
  if (!Number.isFinite(value) || value <= 0) {
    return DEFAULT_DURATION_MINUTES;
  }
  return Math.max(1, Math.trunc(value));
}

function normalizeQuotes(data: unknown): StockQuote[] {
  if (Array.isArray(data)) {
    return data.flatMap((entry) => normalizeQuotes(entry));
  }

  if (isQuote(data)) {
    return [data];
  }

  return [];
}

function isQuote(value: unknown): value is StockQuote {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return false;
  }
  const quote = value as Record<string, unknown>;
  return quote.type === 'quote'
    && typeof quote.symbol === 'string'
    && typeof quote.price === 'number'
    && typeof quote.change === 'number'
    && typeof quote.tick === 'number'
    && typeof quote.at === 'string';
}

function formatChange(change: number): string {
  const sign = change >= 0 ? '+' : '';
  return `${sign}${change.toFixed(2)}`;
}
