import { describe, it, expect } from 'vitest';
import { getTask } from '../src/tools.js';
import { makeFakeDeps, makeFakeClient, makeFakeSession } from './helpers.js';

describe('get_task', () => {
  it('returns the task JSON for a still-running task without connecting', async () => {
    const client = makeFakeClient({
      task: { taskId: 't1', state: 'running', agentName: 'alice' },
    });
    const { deps } = makeFakeDeps({ client });

    const res = await getTask({ taskId: 't1' }, deps);

    expect(client.connectMock).not.toHaveBeenCalled();
    const parsed = JSON.parse(res.content[0].text);
    expect(parsed).toMatchObject({ taskId: 't1', state: 'running' });
  });

  it('connects on terminal state and appends artifact content', async () => {
    const session = makeFakeSession({
      artifacts: [
        {
          fileName: 'output.json',
          mimeType: 'application/json',
          data: new TextEncoder().encode('{"ok":true}'),
        },
      ],
    });
    const client = makeFakeClient({
      task: { taskId: 't2', state: 'completed' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await getTask({ taskId: 't2' }, deps);

    expect(client.connectMock).toHaveBeenCalledWith({ taskId: 't2' });
    expect(res.content[0].text).toContain('[artifact: output.json]');
    expect(res.content[0].text).toContain('{"ok":true}');
    expect(session.closeMock).toHaveBeenCalledOnce();
  });

  it('uses paid TaskClient for paid tasks', async () => {
    const client = makeFakeClient({
      task: { taskId: 't3', state: 'completed', billingMode: 'paid' },
    });
    const { deps, mocks } = makeFakeDeps({ client });

    await getTask({ taskId: 't3' }, deps);

    expect(mocks.getTaskClient).toHaveBeenCalledWith('free');
    expect(mocks.getTaskClient).toHaveBeenCalledWith('paid');
  });

  it('falls back to task JSON only when connect fails', async () => {
    const client = makeFakeClient({
      task: { taskId: 't4', state: 'failed' },
    });
    client.connectMock.mockRejectedValueOnce(new Error('connect refused'));
    const { deps } = makeFakeDeps({ client });

    const res = await getTask({ taskId: 't4' }, deps);
    expect(res.isError).toBeUndefined();
    expect(res.content[0].text).toContain('"taskId": "t4"');
  });

  it('connects for canceled tasks (also a terminal state)', async () => {
    const client = makeFakeClient({
      task: { taskId: 't5', state: 'canceled' },
    });
    const { deps } = makeFakeDeps({ client });

    await getTask({ taskId: 't5' }, deps);
    expect(client.connectMock).toHaveBeenCalled();
  });
});
