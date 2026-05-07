/**
 * Task artifacts tests -- covers plural HandlerResult.artifacts,
 * downloadInputArtifact, publishArtifact, and the consumer file
 * upload flow in sendMessage.
 */
import { describe, it, expect, vi, beforeAll, afterAll } from 'vitest';
import { startAgentInstance, type TaskContext, type HandlerResult, type StartTaskMessage } from '../src/runtime/agent-instance.js';
import { makeTestCard } from './helpers/test-card.js';

// Mock global fetch so connectAgent resolves quickly in tests
const originalFetch = globalThis.fetch;
beforeAll(() => {
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({}),
  }) as unknown as typeof fetch;
});
afterAll(() => {
  globalThis.fetch = originalFetch;
});

// Shared publish mock used by all per-task PubNub instances
let sharedPublish = vi.fn(async () => ({ timetoken: Date.now().toString() }));

// Mock createPubNubClient so per-task clients use shared publish mock
vi.mock('../src/runtime/pubnub-client.js', () => ({
  createPubNubClient: vi.fn(() => ({
    publish: (...args: unknown[]) => sharedPublish(...args),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    unsubscribeAll: vi.fn(),
    setFilterExpression: vi.fn(),
    setToken: vi.fn(),
    setState: vi.fn(async () => ({})),
    destroy: vi.fn(),
    hereNow: vi.fn(async () => ({ channels: {} })),
    downloadFile: vi.fn(async () => ({
      data: Buffer.from('downloaded-file-content'),
    })),
  })),
}));

const createFakePubNub = () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  // Reset shared publish mock per test
  sharedPublish = vi.fn(async () => ({ timetoken: Date.now().toString() }));
  const pubnub = {
    publish: sharedPublish,
    addMessageAction: vi.fn().mockResolvedValue({}),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    addListener: (l: any) => listeners.push(l),
    removeListener: vi.fn(),
    subscribe: vi.fn(),
    unsubscribe: vi.fn(),
    setFilterExpression: vi.fn(),
    setState: vi.fn().mockResolvedValue({}),
    setToken: vi.fn(),
    destroy: vi.fn(),
    downloadFile: vi.fn(async () => ({
      data: Buffer.from('downloaded-file-content'),
    })),
    _configuration: { keySet: { publishKey: 'pub-mock', subscribeKey: 'sub-mock' } },
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any;
  return { pubnub, listeners };
};

describe('task artifacts: plural HandlerResult.artifacts', () => {
  it('publishes multiple artifacts in array order before terminal', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn(async (): Promise<HandlerResult> => {
      return {
        artifacts: [
          { data: 'report content', mimeType: 'text/plain', fileName: 'report.txt', outputId: 'report' },
          { data: Buffer.from('chart data'), mimeType: 'image/png', fileName: 'chart.png', outputId: 'chart' },
        ],
      };
    });

    await startAgentInstance({ pubnub, agentName: 'multi_artifact', card: makeTestCard(), handler });
    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'multi-art-1', ownerId: 'user1' },
    });

    // Wait for terminal
    await vi.waitFor(() => {
      const hasTerminal = sharedPublish.mock.calls.some(
        (call: unknown[]) => (call[0] as { message?: { type?: string } })?.message?.type === 'terminal',
      );
      expect(hasTerminal).toBe(true);
    });

    // Collect all published messages in order
    const messages = sharedPublish.mock.calls.map(
      (call: unknown[]) => (call[0] as { message: Record<string, unknown> }).message,
    );

    // Find artifact events
    const artifacts = messages.filter((m) => m.type === 'artifact');
    expect(artifacts).toHaveLength(2);
    expect(artifacts[0].outputId).toBe('report');
    expect(artifacts[1].outputId).toBe('chart');

    // Verify artifacts come before terminal
    const terminalIdx = messages.findIndex((m) => m.type === 'terminal');
    const lastArtifactIdx = messages.lastIndexOf(artifacts[1]);
    expect(lastArtifactIdx).toBeLessThan(terminalIdx);
  });

  it('returns empty artifacts array without publishing artifact events', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn(async (): Promise<HandlerResult> => {
      return { artifacts: [] };
    });

    await startAgentInstance({ pubnub, agentName: 'no_artifact', card: makeTestCard(), handler });
    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'no-art-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const hasTerminal = sharedPublish.mock.calls.some(
        (call: unknown[]) => (call[0] as { message?: { type?: string } })?.message?.type === 'terminal',
      );
      expect(hasTerminal).toBe(true);
    });

    const artifacts = sharedPublish.mock.calls.filter(
      (call: unknown[]) => (call[0] as { message?: { type?: string } })?.message?.type === 'artifact',
    );
    expect(artifacts).toHaveLength(0);
  });

  it('returns undefined artifacts (handler returns empty result)', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn(async (): Promise<HandlerResult> => {
      return {};
    });

    await startAgentInstance({ pubnub, agentName: 'undef_artifact', card: makeTestCard(), handler });
    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'undef-art-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const hasTerminal = sharedPublish.mock.calls.some(
        (call: unknown[]) => (call[0] as { message?: { type?: string } })?.message?.type === 'terminal',
      );
      expect(hasTerminal).toBe(true);
    });

    const artifacts = sharedPublish.mock.calls.filter(
      (call: unknown[]) => (call[0] as { message?: { type?: string } })?.message?.type === 'artifact',
    );
    expect(artifacts).toHaveLength(0);
  });

  it('publishes artifact with string data converted to Buffer', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn(async (): Promise<HandlerResult> => {
      return {
        artifacts: [{ data: 'string artifact content', mimeType: 'text/plain' }],
      };
    });

    await startAgentInstance({ pubnub, agentName: 'str_artifact', card: makeTestCard(), handler });
    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'str-art-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const hasArtifact = sharedPublish.mock.calls.some(
        (call: unknown[]) => (call[0] as { message?: { type?: string } })?.message?.type === 'artifact',
      );
      expect(hasArtifact).toBe(true);
    });

    const artifactCall = sharedPublish.mock.calls.find(
      (call: unknown[]) => (call[0] as { message?: { type?: string } })?.message?.type === 'artifact',
    );
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const msg = (artifactCall as any)[0].message;
    expect(msg.artifactRef.kind).toBe('inline');
    expect(msg.artifactRef.data).toBe(Buffer.from('string artifact content', 'utf-8').toString('base64'));
  });
});

describe('task artifacts: downloadInputArtifact', () => {
  it('decodes inline artifactRef from a request part', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let downloadedData: Buffer | undefined;

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      const parts = ctx!.requestParts;
      expect(parts).toHaveLength(1);
      downloadedData = await ctx!.downloadInputArtifact(parts[0]);
      return {};
    });

    await startAgentInstance({ pubnub, agentName: 'download_test', card: makeTestCard(), handler });
    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'dl-inline-1',
        ownerId: 'user1',
        requestParts: [
          {
            partId: 'doc',
            artifactRef: {
              kind: 'inline',
              mimeType: 'text/plain',
              size: 11,
              data: Buffer.from('hello world').toString('base64'),
              fileName: 'hello.txt',
            },
          },
        ],
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    await vi.waitFor(() => expect(downloadedData).toBeDefined());
    expect(downloadedData!.toString()).toBe('hello world');
  });

  it('throws when request part has no artifactRef', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let caughtError: Error | undefined;

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      try {
        await ctx!.downloadInputArtifact(ctx!.requestParts[0]);
      } catch (e) {
        caughtError = e as Error;
      }
      return {};
    });

    await startAgentInstance({ pubnub, agentName: 'download_err', card: makeTestCard(), handler });
    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'dl-err-1',
        ownerId: 'user1',
        requestParts: [{ partId: 'text', text: 'just text' }],
      },
    });

    await vi.waitFor(() => expect(caughtError).toBeDefined());
    expect(caughtError!.message).toContain('no artifactRef');
  });
});

describe('task artifacts: mid-execution publishArtifact', () => {
  it('publishes inline artifact mid-execution via ctx.publishArtifact', async () => {
    const { pubnub, listeners } = createFakePubNub();

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      await ctx!.publishArtifact('mid-exec data', {
        mimeType: 'text/plain',
        fileName: 'mid.txt',
        outputId: 'mid_output',
      });
      return {};
    });

    await startAgentInstance({ pubnub, agentName: 'mid_pub', card: makeTestCard(), handler });
    const listener = listeners[0];
    listener.message({
      message: { type: 'StartTask', taskId: 'mid-pub-1', ownerId: 'user1' },
    });

    await vi.waitFor(() => {
      const hasTerminal = sharedPublish.mock.calls.some(
        (call: unknown[]) => (call[0] as { message?: { type?: string } })?.message?.type === 'terminal',
      );
      expect(hasTerminal).toBe(true);
    });

    const messages = sharedPublish.mock.calls.map(
      (call: unknown[]) => (call[0] as { message: Record<string, unknown> }).message,
    );

    const artifacts = messages.filter((m) => m.type === 'artifact');
    expect(artifacts).toHaveLength(1);
    expect(artifacts[0].outputId).toBe('mid_output');

    // Mid-execution artifact should be before terminal
    const terminalIdx = messages.findIndex((m) => m.type === 'terminal');
    const artifactIdx = messages.indexOf(artifacts[0]);
    expect(artifactIdx).toBeLessThan(terminalIdx);
  });
});

describe('task artifacts: artifactRef on RequestPart', () => {
  it('passes artifactRef through to the handler via requestParts', async () => {
    const { pubnub, listeners } = createFakePubNub();
    let receivedRef: unknown;

    const handler = vi.fn(async (_task: StartTaskMessage, ctx?: TaskContext): Promise<HandlerResult> => {
      receivedRef = ctx!.requestParts[0].artifactRef;
      return {};
    });

    await startAgentInstance({ pubnub, agentName: 'ref_passthrough', card: makeTestCard(), handler });
    const listener = listeners[0];
    listener.message({
      message: {
        type: 'StartTask',
        taskId: 'ref-1',
        ownerId: 'user1',
        requestParts: [
          {
            partId: 'file_input',
            artifactRef: {
              kind: 'file',
              channel: 'u.org1.task-ref-1',
              mimeType: 'application/pdf',
              size: 50000,
              fileId: 'pn-file-abc',
              fileName: 'doc.pdf',
            },
          },
        ],
      },
    });

    await vi.waitFor(() => expect(handler).toHaveBeenCalled());
    expect(receivedRef).toBeDefined();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const ref = receivedRef as any;
    expect(ref.kind).toBe('file');
    expect(ref.channel).toBe('u.org1.task-ref-1');
    expect(ref.fileId).toBe('pn-file-abc');
    expect(ref.fileName).toBe('doc.pdf');
  });
});
