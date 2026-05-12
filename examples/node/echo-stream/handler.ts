import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

/**
 * Echo-stream handler.
 *
 * Streams input back chunk-by-chunk (line-by-line when multiline,
 * otherwise word-by-word), then returns the full text artifact.
 *
 * Uses the unified createStream() API which performs a setup handshake
 * with the streamSetup Function to obtain a T7a stream token.
 *
 * This handler is outbound-only — it `write()`s and does not read
 * inbound chunks — so it does not exercise the recommended
 * `bytes()` / `events<T>()` decoded read iterators. See
 * `echo-stream-consumer.ts` for the recommended consumer-side read
 * pattern.
 */
export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  const fullText = extractInputText(task.requestParts);

  if (ctx) {
    ctx.reportStatus('Streaming echo output...');
    // createStream negotiates a dedicated channel for streaming via the streamSetup handshake
    const stream = await ctx.createStream({
      bundleSizeBytes: 2048,
      maxLatencyMs: 50,
    });
    const chunks = chunkText(fullText);

    // writes are batched and published according to bundleSizeBytes and maxLatencyMs
    for (const chunk of chunks) {
      stream.write({ text: chunk });
    }

    // end() publishes a stream_end marker so consumers know the stream is complete
    await stream.end();
    ctx.reportStatus('Streaming complete');
  }

  return {
    artifacts: [{ data: fullText, mimeType: 'text/plain' }],
  };
}

function extractInputText(parts: unknown[] | undefined): string {
  const requestParts = parts ?? [];
  if (requestParts.length === 0) {
    return 'Hello from echo-stream';
  }

  const pieces: string[] = [];
  for (const part of requestParts) {
    if (typeof part === 'string') {
      pieces.push(part);
      continue;
    }
    if (isRecord(part) && typeof part.text === 'string') {
      pieces.push(part.text);
      continue;
    }
    pieces.push(JSON.stringify(part));
  }

  return pieces.join('\n');
}

function chunkText(text: string): string[] {
  if (text.length === 0) return [''];

  if (text.includes('\n')) {
    const lineChunks = text.match(/[^\n]*\n|[^\n]+$/g);
    return lineChunks ?? [text];
  }

  const wordChunks = text.match(/\S+\s*/g);
  return wordChunks ?? [text];
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === 'object';
}
