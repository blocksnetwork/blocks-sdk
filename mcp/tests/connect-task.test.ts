import { describe, it, expect } from 'vitest';
import { connectTask } from '../src/tools.js';
import {
  asyncIterFrom,
  makeFakeClient,
  makeFakeDeps,
  makeFakeSession,
} from './helpers.js';
import type { StreamRef } from '../src/tools.js';

function makeStreamRef(opts: {
  streamId: string;
  declaredStream?: string;
  format: 'events' | 'bytes';
  localDirection: 'inbound' | 'outbound' | 'bidirectional';
  events?: unknown[];
  bytes?: Uint8Array[];
}): StreamRef {
  return {
    descriptor: {
      streamId: opts.streamId,
      declaredStream: opts.declaredStream,
      format: opts.format,
      localDirection: opts.localDirection,
    },
    open: () => ({
      events: () => asyncIterFrom(opts.events ?? []),
      bytes: () => asyncIterFrom(opts.bytes ?? []),
    }),
  };
}

describe('connect_task', () => {
  it('drains inbound event streams and includes them in the output', async () => {
    const stream = makeStreamRef({
      streamId: 's1',
      declaredStream: 'thoughts',
      format: 'events',
      localDirection: 'inbound',
      events: [{ kind: 'tick', n: 1 }, { kind: 'tick', n: 2 }],
    });
    const session = makeFakeSession({
      taskId: 't1',
      streams: [stream],
      terminal: { state: 'completed' },
    });
    const client = makeFakeClient({
      task: { taskId: 't1', state: 'running' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await connectTask({ taskId: 't1' }, deps);

    expect(res.content[0].text).toContain('Task t1 completed');
    expect(res.content[0].text).toContain('[stream: thoughts]');
    expect(res.content[0].text).toContain('{"kind":"tick","n":1}');
    expect(res.content[0].text).toContain('{"kind":"tick","n":2}');
    expect(session.asyncCloseMock).toHaveBeenCalledOnce();
  });

  it('drains inbound byte streams and decodes UTF-8', async () => {
    const stream = makeStreamRef({
      streamId: 's2',
      format: 'bytes',
      localDirection: 'inbound',
      bytes: [
        new TextEncoder().encode('hello '),
        new TextEncoder().encode('world'),
      ],
    });
    const session = makeFakeSession({
      streams: [stream],
      terminal: { state: 'completed' },
    });
    const client = makeFakeClient({
      task: { taskId: 't2', state: 'running' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await connectTask({ taskId: 't2' }, deps);
    expect(res.content[0].text).toContain('[stream: s2]\nhello world');
  });

  it('skips outbound-only streams', async () => {
    const stream = makeStreamRef({
      streamId: 's3',
      format: 'events',
      localDirection: 'outbound',
      events: [{ ignored: true }],
    });
    const session = makeFakeSession({
      streams: [stream],
      terminal: { state: 'completed' },
    });
    const client = makeFakeClient({
      task: { taskId: 't3', state: 'running' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await connectTask({ taskId: 't3' }, deps);
    expect(res.content[0].text).not.toContain('[stream:');
  });

  it('drains streams that arrive after connect via onStream callback', async () => {
    const session = makeFakeSession({
      streams: [],
      lateStreams: [
        makeStreamRef({
          streamId: 'late',
          format: 'events',
          localDirection: 'bidirectional',
          events: [{ msg: 'late-arrival' }],
        }),
      ],
      terminal: { state: 'completed' },
    });
    const client = makeFakeClient({
      task: { taskId: 't4', state: 'running' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await connectTask({ taskId: 't4' }, deps);
    expect(res.content[0].text).toContain('[stream: late]');
    expect(res.content[0].text).toContain('"late-arrival"');
  });

  it('returns isError=true and partial stream output when waitForTerminal rejects', async () => {
    const stream = makeStreamRef({
      streamId: 's5',
      format: 'events',
      localDirection: 'inbound',
      events: [{ tick: 1 }],
    });
    const session = makeFakeSession({
      streams: [stream],
      terminalRejects: new Error('timeout'),
    });
    const client = makeFakeClient({
      task: { taskId: 't5', state: 'running' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await connectTask({ taskId: 't5' }, deps);

    expect(res.isError).toBe(true);
    expect(res.content[0].text).toContain('Task t5 timed out (timeout)');
    expect(session.asyncCloseMock).toHaveBeenCalledOnce();
  });

  it('routes paid tasks through the paid TaskClient', async () => {
    const client = makeFakeClient({
      task: { taskId: 't6', state: 'running', billingMode: 'paid' },
    });
    const { deps, mocks } = makeFakeDeps({ client });

    await connectTask({ taskId: 't6' }, deps);

    expect(mocks.getTaskClient).toHaveBeenCalledWith('free');
    expect(mocks.getTaskClient).toHaveBeenCalledWith('paid');
  });

  it('appends "Error:" line for failed terminals', async () => {
    const session = makeFakeSession({
      terminal: { state: 'failed', reason: 'boom' },
    });
    const client = makeFakeClient({
      task: { taskId: 't7', state: 'running' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await connectTask({ taskId: 't7' }, deps);
    expect(res.content[0].text).toContain('Error: boom');
  });
});
