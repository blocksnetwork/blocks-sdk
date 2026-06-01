import { describe, it, expect } from 'vitest';
import { pauseTask, resumeTask, retryTask } from '../src/tools.js';
import { makeFakeDeps, makeFakeClient } from './helpers.js';

describe('pause_task', () => {
  it('forwards taskId to client.pauseTask and confirms', async () => {
    const client = makeFakeClient();
    const { deps } = makeFakeDeps({ client });

    const res = await pauseTask({ taskId: 't_1' }, deps);

    expect(client.pauseTaskMock).toHaveBeenCalledWith('t_1');
    expect(res.content[0].text).toBe('Task t_1 paused.');
  });

  it('propagates errors from pauseTask', async () => {
    const client = makeFakeClient();
    client.pauseTaskMock.mockRejectedValueOnce(new Error('already terminal'));
    const { deps } = makeFakeDeps({ client });

    await expect(pauseTask({ taskId: 't_err' }, deps)).rejects.toThrow('already terminal');
  });
});

describe('resume_task', () => {
  it('forwards taskId to client.resumeTask and confirms', async () => {
    const client = makeFakeClient();
    const { deps } = makeFakeDeps({ client });

    const res = await resumeTask({ taskId: 't_2' }, deps);

    expect(client.resumeTaskMock).toHaveBeenCalledWith('t_2');
    expect(res.content[0].text).toBe('Task t_2 resumed.');
  });

  it('propagates errors from resumeTask', async () => {
    const client = makeFakeClient();
    client.resumeTaskMock.mockRejectedValueOnce(new Error('not paused'));
    const { deps } = makeFakeDeps({ client });

    await expect(resumeTask({ taskId: 't_err' }, deps)).rejects.toThrow('not paused');
  });
});

describe('retry_task', () => {
  it('forwards taskId to client.retryTask and confirms', async () => {
    const client = makeFakeClient();
    const { deps } = makeFakeDeps({ client });

    const res = await retryTask({ taskId: 't_3' }, deps);

    expect(client.retryTaskMock).toHaveBeenCalledWith('t_3');
    expect(res.content[0].text).toBe('Task t_3 retry requested.');
  });

  it('propagates errors from retryTask', async () => {
    const client = makeFakeClient();
    client.retryTaskMock.mockRejectedValueOnce(new Error('not failed'));
    const { deps } = makeFakeDeps({ client });

    await expect(retryTask({ taskId: 't_err' }, deps)).rejects.toThrow('not failed');
  });
});

