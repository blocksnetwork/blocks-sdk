import { describe, it, expect } from 'vitest';
import { sendTask } from '../src/tools.js';
import { makeFakeDeps, makeFakeClient, makeFakeSession } from './helpers.js';

describe('send_task', () => {
  it('sends a text part with default partId="text" when agent has no declared inputs', async () => {
    const session = makeFakeSession({ taskId: 't1', terminal: { state: 'completed' } });
    const client = makeFakeClient({ session });
    const { deps, mocks } = makeFakeDeps({ client, agentEntry: null });

    const res = await sendTask(
      { agentName: 'alice', message: 'hello' },
      deps,
    );

    expect(client.sendMessageMock).toHaveBeenCalledOnce();
    const call = client.sendMessageMock.mock.calls[0][0];
    expect(call.agentName).toBe('alice');
    expect(call.requestParts).toHaveLength(1);
    expect(mocks.textPart).toHaveBeenCalledWith('hello', 'text');
    expect(res.isError).toBeUndefined();
    expect(res.content[0].text).toContain('Task t1 completed');
  });

  it('uses the agent card declared text input id', async () => {
    const client = makeFakeClient();
    const { deps, mocks } = makeFakeDeps({
      client,
      agentEntry: {
        agentName: 'bob',
        card: { io: { inputs: [{ id: 'prompt', contentType: 'text/plain' }] } },
      },
    });

    await sendTask({ agentName: 'bob', message: 'hi' }, deps);

    expect(mocks.textPart).toHaveBeenCalledWith('hi', 'prompt');
  });

  it('skips the default text part when caller supplies inputs override for the same id', async () => {
    const client = makeFakeClient();
    const { deps, mocks } = makeFakeDeps({
      client,
      agentEntry: {
        agentName: 'bob',
        card: { io: { inputs: [{ id: 'prompt', contentType: 'text/plain' }] } },
      },
    });

    await sendTask(
      { agentName: 'bob', message: 'ignored', inputs: { prompt: 'override' } },
      deps,
    );

    expect(mocks.textPart).toHaveBeenCalledTimes(1);
    expect(mocks.textPart).toHaveBeenCalledWith('override', 'prompt');
  });

  it('rejects file uploads larger than maxUploadBytes', async () => {
    const client = makeFakeClient();
    const { deps } = makeFakeDeps({
      client,
      maxUploadBytes: 1000,
      fileSize: 5000,
    });

    const res = await sendTask(
      { agentName: 'alice', message: 'hi', filePath: '/tmp/big.bin' },
      deps,
    );

    expect(res.isError).toBe(true);
    expect(res.content[0].text).toContain('File too large');
    expect(client.sendMessageMock).not.toHaveBeenCalled();
  });

  it('attaches a file part with the agent-declared file partId', async () => {
    const client = makeFakeClient();
    const { deps, mocks } = makeFakeDeps({
      client,
      agentEntry: {
        agentName: 'alice',
        card: {
          io: {
            inputs: [
              { id: 'prompt', contentType: 'text/plain' },
              { id: 'attachment', contentType: 'image/png' },
            ],
          },
        },
      },
      fileSize: 50,
    });

    await sendTask(
      { agentName: 'alice', message: 'hi', filePath: '/safe/img.png' },
      deps,
    );

    expect(mocks.validateFilePath).toHaveBeenCalledWith('/safe/img.png');
    expect(mocks.filePartFromPath).toHaveBeenCalledWith('/safe/img.png', {
      partId: 'attachment',
      contentType: 'image/png',
    });
  });

  it('forwards taskKind and duration to sendMessage', async () => {
    const client = makeFakeClient();
    const { deps } = makeFakeDeps({ client });

    await sendTask(
      { agentName: 'alice', message: 'hi', taskKind: 'pipe', duration: 30 },
      deps,
    );

    const call = client.sendMessageMock.mock.calls[0][0];
    expect(call.taskKind).toBe('pipe');
    expect(call.duration).toBe(30);
  });

  it('uses the billingMode from the agent registry entry', async () => {
    const { deps, mocks } = makeFakeDeps({
      agentEntry: { agentName: 'alice', billingMode: 'paid' },
    });

    await sendTask({ agentName: 'alice', message: 'hi' }, deps);

    expect(mocks.getTaskClient).toHaveBeenCalledWith('paid');
  });

  it('emits progress lines, includes text artifacts inline, and closes the session', async () => {
    const session = makeFakeSession({
      taskId: 't9',
      terminal: { state: 'completed' },
      progressEvents: [{ message: 'starting' }, { message: 'half-done' }],
      artifacts: [
        {
          fileName: 'out.txt',
          mimeType: 'text/plain',
          data: new TextEncoder().encode('hello world'),
        },
      ],
    });
    const client = makeFakeClient({ session });
    const { deps } = makeFakeDeps({ client });

    const res = await sendTask({ agentName: 'alice', message: 'hi' }, deps);

    const text = res.content[0].text;
    expect(text).toContain('Task t9 completed');
    expect(text).toContain('[progress] starting');
    expect(text).toContain('[progress] half-done');
    expect(text).toContain('[artifact: out.txt]');
    expect(text).toContain('hello world');
    expect(session.closeMock).toHaveBeenCalledOnce();
  });

  it('summarises binary artifacts by size rather than embedding their bytes', async () => {
    const session = makeFakeSession({
      terminal: { state: 'completed' },
      artifacts: [
        {
          fileName: 'image.png',
          mimeType: 'image/png',
          data: new Uint8Array(64),
        },
      ],
    });
    const client = makeFakeClient({ session });
    const { deps } = makeFakeDeps({ client });

    const res = await sendTask({ agentName: 'alice', message: 'hi' }, deps);

    expect(res.content[0].text).toContain('[artifact: image.png] (image/png, 64 bytes)');
  });

  it('reports artifact download failures without crashing', async () => {
    const session = makeFakeSession({
      terminal: { state: 'completed' },
      artifacts: [
        {
          fileName: 'broken.bin',
          mimeType: 'application/octet-stream',
          data: new Uint8Array(),
          downloadFails: true,
        },
      ],
    });
    const client = makeFakeClient({ session });
    const { deps } = makeFakeDeps({ client });

    const res = await sendTask({ agentName: 'alice', message: 'hi' }, deps);
    expect(res.content[0].text).toContain('[artifact: broken.bin] (download failed)');
  });

  it('returns isError=true when waitForTerminal rejects (timeout)', async () => {
    const session = makeFakeSession({
      taskId: 't_timeout',
      terminalRejects: new Error('timed out after 60000ms'),
    });
    const client = makeFakeClient({ session });
    const { deps } = makeFakeDeps({ client });

    const res = await sendTask({ agentName: 'alice', message: 'hi' }, deps);

    expect(res.isError).toBe(true);
    expect(res.content[0].text).toContain('Task t_timeout error: timed out after 60000ms');
    expect(session.closeMock).toHaveBeenCalledOnce();
  });

  it('appends an "Error:" line for failed terminals', async () => {
    const session = makeFakeSession({
      terminal: { state: 'failed', error: 'boom' },
    });
    const client = makeFakeClient({ session });
    const { deps } = makeFakeDeps({ client });

    const res = await sendTask({ agentName: 'alice', message: 'hi' }, deps);
    expect(res.content[0].text).toContain('Error: boom');
  });
});
