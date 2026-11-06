import type {
  ExchangeContext,
  HandlerResult,
  RequestPart,
  SessionContext,
  StartTaskMessage,
} from '@blocks-network/sdk';

/**
 * Session echo handlers.
 *
 * A session is one conversation with one agent process. The consumer sends
 * many exchanges over it, and each exchange ends in exactly one outcome:
 * returning from `onExchange` completes it, throwing fails it.
 */

export function onSessionOpen(ctx: SessionContext): void {
  console.log(`[session-echo] session ${ctx.sessionId} opened`);
}

export function onExchange(_ctx: SessionContext, exchange: ExchangeContext): HandlerResult {
  // reportStatus publishes a progress event to the session's consumer
  exchange.reportStatus('Echoing...');
  return echo(exchange.requestParts);
}

export function onSessionClose(ctx: SessionContext, reason: string): void {
  console.log(`[session-echo] session ${ctx.sessionId} ended (${reason})`);
}

/** Task handler, so consumers that still send tasks get the same echo. */
export default async function handler(task: StartTaskMessage): Promise<HandlerResult> {
  return echo(task.requestParts ?? []);
}

function echo(parts: RequestPart[]): HandlerResult {
  const text = parts.find((part) => typeof part.text === 'string')?.text ?? '(no text)';
  return { artifacts: [{ data: `Echo: ${text}`, mimeType: 'text/plain' }] };
}
