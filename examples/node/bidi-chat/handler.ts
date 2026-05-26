import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';

interface ChatMessage {
  text?: string;
}

export default async function handler(
  task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx) {
    return { artifacts: [{ data: 'no context — nothing streamed', mimeType: 'text/plain' }] };
  }

  const greeting = extractGreeting(task.requestParts);

  ctx.reportStatus('Opening bidirectional stream...');
  const stream = await ctx.createStream({
    direction: 'bidirectional',
    format: 'events',
    bundleSizeBytes: 512,
    maxLatencyMs: 25,
  });

  const transcript: string[] = [];

  if (greeting) {
    const reply = `AGENT> ${greeting.toUpperCase()}`;
    stream.write({ text: reply });
    transcript.push(`> ${greeting}`);
    transcript.push(reply);
  }

  ctx.reportStatus('Awaiting consumer messages...');

  for await (const ev of stream.events<ChatMessage | string>()) {
    const inboundText =
      typeof ev === 'string' ? ev : typeof ev?.text === 'string' ? ev.text : JSON.stringify(ev);

    transcript.push(`> ${inboundText}`);

    const reply = `AGENT> ${inboundText.toUpperCase()}`;
    stream.write({ text: reply });
    transcript.push(reply);

    if (inboundText.trim().toLowerCase() === 'bye') {
      break;
    }
  }

  await stream.end();
  ctx.reportStatus('Stream ended');

  return {
    artifacts: [{ data: transcript.join('\n'), mimeType: 'text/plain' }],
  };
}

function extractGreeting(parts: unknown[] | undefined): string | undefined {
  if (!parts || parts.length === 0) return undefined;
  for (const part of parts) {
    if (isRecord(part) && typeof part.text === 'string') return part.text;
    if (typeof part === 'string') return part;
  }
  return undefined;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === 'object';
}
