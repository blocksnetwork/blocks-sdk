import { describe, it, expect } from 'vitest';
import { listTasks } from '../src/tools.js';
import { makeFakeDeps, makeFakeClient } from './helpers.js';

describe('list_tasks', () => {
  it('renders one row per task with header showing totalCount', async () => {
    const client = makeFakeClient({
      listResult: {
        tasks: [
          { taskId: 'a', agentName: 'alice', state: 'running', createdTime: '2026-05-18T00:00:00Z' },
          { taskId: 'b', agentName: 'bob', state: 'completed', createdTime: '2026-05-18T00:01:00Z' },
        ],
        totalCount: 17,
      },
    });
    const { deps } = makeFakeDeps({ client });

    const res = await listTasks({}, deps);
    expect(res.content[0].text.split('\n')).toEqual([
      'Tasks (17 total):',
      'a | alice | running | 2026-05-18T00:00:00Z',
      'b | bob | completed | 2026-05-18T00:01:00Z',
    ]);
  });

  it('forwards filters to the client', async () => {
    const client = makeFakeClient();
    const { deps } = makeFakeDeps({ client });

    await listTasks({ agentName: 'alice', state: 'failed', limit: 5 }, deps);

    expect(client.listTasksMock).toHaveBeenCalledWith({
      agentName: 'alice',
      state: 'failed',
      limit: 5,
    });
  });

  it('substitutes "?" for missing agentName/state', async () => {
    const client = makeFakeClient({
      listResult: { tasks: [{ taskId: 'x' }] },
    });
    const { deps } = makeFakeDeps({ client });

    const res = await listTasks({}, deps);
    expect(res.content[0].text).toContain('x | ? | ? |');
  });

  it('falls back to tasks.length when totalCount is omitted', async () => {
    const client = makeFakeClient({
      listResult: { tasks: [{ taskId: 'x' }, { taskId: 'y' }] },
    });
    const { deps } = makeFakeDeps({ client });

    const res = await listTasks({}, deps);
    expect(res.content[0].text).toMatch(/^Tasks \(2 total\):/);
  });
});
