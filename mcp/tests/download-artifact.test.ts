import { describe, it, expect } from 'vitest';
import { downloadArtifact } from '../src/tools.js';
import { makeFakeDeps, makeFakeClient, makeFakeSession } from './helpers.js';

describe('download_artifact', () => {
  it('returns inline text content for a text artifact when savePath is omitted', async () => {
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
      task: { taskId: 't1', state: 'completed' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await downloadArtifact(
      { taskId: 't1', fileName: 'output.json' },
      deps,
    );

    expect(client.connectMock).toHaveBeenCalledWith({ taskId: 't1' });
    expect(res.isError).toBeUndefined();
    expect(res.content[0].text).toContain('[artifact: output.json]');
    expect(res.content[0].text).toContain('{"ok":true}');
    expect(session.closeMock).toHaveBeenCalledOnce();
  });

  it('returns base64 content for binary artifacts when savePath is omitted', async () => {
    const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47]);
    const session = makeFakeSession({
      artifacts: [{ fileName: 'image.png', mimeType: 'image/png', data: bytes }],
    });
    const client = makeFakeClient({
      task: { taskId: 't2', state: 'completed' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await downloadArtifact(
      { taskId: 't2', fileName: 'image.png' },
      deps,
    );

    expect(res.isError).toBeUndefined();
    expect(res.content[0].text).toContain('image/png');
    expect(res.content[0].text).toContain('base64');
    expect(res.content[0].text).toContain(Buffer.from(bytes).toString('base64'));
  });

  it('writes to disk when savePath is provided', async () => {
    const bytes = new TextEncoder().encode('hello');
    const session = makeFakeSession({
      artifacts: [{ fileName: 'note.txt', mimeType: 'text/plain', data: bytes }],
    });
    const client = makeFakeClient({
      task: { taskId: 't3', state: 'completed' },
      session,
    });
    const { deps, mocks } = makeFakeDeps({ client });

    const res = await downloadArtifact(
      { taskId: 't3', fileName: 'note.txt', savePath: 'out/note.txt' },
      deps,
    );

    expect(res.isError).toBeUndefined();
    expect(mocks.resolveSavePath).toHaveBeenCalledWith('out/note.txt');
    expect(mocks.writeFile).toHaveBeenCalledWith('out/note.txt', bytes);
    expect(res.content[0].text).toContain('Saved 5 bytes to out/note.txt');
  });

  it('returns an error when the requested artifact is not found', async () => {
    const session = makeFakeSession({
      artifacts: [
        {
          fileName: 'other.txt',
          mimeType: 'text/plain',
          data: new TextEncoder().encode('x'),
        },
      ],
    });
    const client = makeFakeClient({
      task: { taskId: 't4', state: 'completed' },
      session,
    });
    const { deps } = makeFakeDeps({ client });

    const res = await downloadArtifact(
      { taskId: 't4', fileName: 'missing.txt' },
      deps,
    );

    expect(res.isError).toBe(true);
    expect(res.content[0].text).toContain('"missing.txt" not found');
    expect(res.content[0].text).toContain('other.txt');
    expect(session.closeMock).toHaveBeenCalledOnce();
  });

  it('uses paid TaskClient for paid tasks', async () => {
    const session = makeFakeSession({
      artifacts: [
        {
          fileName: 'a.txt',
          mimeType: 'text/plain',
          data: new TextEncoder().encode('a'),
        },
      ],
    });
    const client = makeFakeClient({
      task: { taskId: 't5', state: 'completed', billingMode: 'paid' },
      session,
    });
    const { deps, mocks } = makeFakeDeps({ client });

    await downloadArtifact({ taskId: 't5', fileName: 'a.txt' }, deps);

    expect(mocks.getTaskClient).toHaveBeenCalledWith('free');
    expect(mocks.getTaskClient).toHaveBeenCalledWith('paid');
  });

  it('returns an error when connect fails', async () => {
    const client = makeFakeClient({
      task: { taskId: 't6', state: 'completed' },
    });
    client.connectMock.mockRejectedValueOnce(new Error('connect refused'));
    const { deps } = makeFakeDeps({ client });

    const res = await downloadArtifact(
      { taskId: 't6', fileName: 'x.txt' },
      deps,
    );

    expect(res.isError).toBe(true);
    expect(res.content[0].text).toContain('connect refused');
  });
});
