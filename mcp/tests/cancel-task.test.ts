import { describe, it, expect } from 'vitest';
import { cancelTask } from '../src/tools.js';
import { makeFakeDeps, makeFakeClient } from './helpers.js';

describe('cancel_task', () => {
  it('calls client.cancelTask with the supplied id and confirms', async () => {
    const client = makeFakeClient();
    const { deps } = makeFakeDeps({ client });

    const res = await cancelTask({ taskId: 'task_xyz' }, deps);

    expect(client.cancelTaskMock).toHaveBeenCalledWith('task_xyz');
    expect(res.content[0].text).toBe('Task task_xyz cancelled.');
    expect(res.isError).toBeUndefined();
  });

  it('propagates errors from cancelTask', async () => {
    const client = makeFakeClient();
    client.cancelTaskMock.mockRejectedValueOnce(new Error('not allowed'));
    const { deps } = makeFakeDeps({ client });

    await expect(cancelTask({ taskId: 'task_zzz' }, deps)).rejects.toThrow('not allowed');
  });
});
