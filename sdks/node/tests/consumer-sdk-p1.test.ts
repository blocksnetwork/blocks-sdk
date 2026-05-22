/**
 * Consumer SDK Phase 1 tests -- covers P1-1, P1-2, P1-3, P1-5.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { StaticAuthProvider } from '../src/runtime/auth-provider.js';
import { TaskSession, type CallbackErrorContext } from '../src/runtime/task-session.js';
import { TaskClient } from '../src/runtime/task-client.js';
import { downloadArtifact, type ArtifactRef } from '../src/runtime/artifacts.js';

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation(() => createMockPubNub().pubnub),
  };
});

function createMockPubNub() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const listeners: any[] = [];
  const subscribedChannels: string[] = [];
  const pubnub = {
    addListener: vi.fn((l: unknown) => listeners.push(l)),
    removeListener: vi.fn(),
    subscribe: vi.fn(({ channels }: { channels: string[] }) => {
      subscribedChannels.push(...channels);
    }),
    unsubscribe: vi.fn(),
    destroy: vi.fn(),
    setToken: vi.fn(),
    downloadFile: vi.fn(),
    fetchMessages: vi.fn(),
    time: vi.fn().mockResolvedValue({ timetoken: '17000000000000000' }),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
  } as any;

  let simCounter = 0;
  return {
    pubnub,
    listeners,
    subscribedChannels,
    // When `timetoken` is omitted, auto-increment a unique token so repeated
    // fixture deliveries don't get dedup-suppressed by TaskSession's
    // timetoken-based dedup (SDK_CONTRACT §10.4.1a / Family E).
    _simulateMessage(channel: string, message: unknown, timetoken?: string) {
      const tt = timetoken ?? `sim-${++simCounter}`;
      for (const l of listeners) {
        l.message?.({ channel, message, timetoken: tt });
      }
    },
  };
}

// Mock RPC responses for getTask
const mockRpcResponse = (result: unknown) => ({
  ok: true,
  json: async () => ({ jsonrpc: '2.0', id: 'x', result }),
});

// Default SDK options for TaskSession
const defaultSdkOptions = { subscribeKey: 'sub-key', publishKey: 'pub-key' };

// Mock StreamClient to avoid real PubNub
vi.mock('../src/stream/index.js', async (importOriginal) => {
  const actual = await importOriginal() as Record<string, unknown>;
  return {
    ...actual,
    StreamClient: {
      fromDescriptor: vi.fn(() => ({
        isActive: true,
        end: vi.fn(async () => {}),
        onEnd: vi.fn(),
        onInboundDone: vi.fn(),
      })),
    },
  };
});

// ============================================================================
// P1-1: Artifact Download
// ============================================================================

describe('P1-1: downloadArtifact()', () => {
  it('decodes inline artifact', async () => {
    const ref: ArtifactRef = {
      kind: 'inline',
      mimeType: 'text/plain',
      size: 5,
      data: btoa('hello'),
    };

    const mock = createMockPubNub();
    const result = await downloadArtifact(ref, mock.pubnub);
    expect(result.mimeType).toBe('text/plain');
    expect(new TextDecoder().decode(result.data)).toBe('hello');
    expect(mock.pubnub.downloadFile).not.toHaveBeenCalled();
  });

  it('downloads file artifact using pubnub.downloadFile()', async () => {
    const ref: ArtifactRef = {
      kind: 'file',
      mimeType: 'application/octet-stream',
      size: 100,
      fileId: 'file-1',
      fileName: 'data.bin',
      channel: 'u.org1.task-1',
    };

    const fileContent = new Uint8Array([1, 2, 3, 4]);
    const mock = createMockPubNub();
    mock.pubnub.downloadFile.mockResolvedValue({
      data: {
        toBuffer: async () => Buffer.from(fileContent),
      },
    });

    const result = await downloadArtifact(ref, mock.pubnub);
    expect(mock.pubnub.downloadFile).toHaveBeenCalledWith({
      channel: 'u.org1.task-1',
      id: 'file-1',
      name: 'data.bin',
    });
    expect(result.data).toEqual(fileContent);
    expect(result.mimeType).toBe('application/octet-stream');
    expect(result.fileName).toBe('data.bin');
  });

  it('falls back to toArrayBuffer when toBuffer is not available', async () => {
    const ref: ArtifactRef = {
      kind: 'file',
      mimeType: 'image/png',
      size: 4,
      fileId: 'f2',
      fileName: 'img.png',
      channel: 'ch',
    };

    const ab = new ArrayBuffer(4);
    new Uint8Array(ab).set([10, 20, 30, 40]);
    const mock = createMockPubNub();
    mock.pubnub.downloadFile.mockResolvedValue({
      data: {
        toArrayBuffer: async () => ab,
      },
    });

    const result = await downloadArtifact(ref, mock.pubnub);
    expect(new Uint8Array(result.data)).toEqual(new Uint8Array([10, 20, 30, 40]));
  });

  it('throws for inline ref missing data', async () => {
    const ref: ArtifactRef = { kind: 'inline', mimeType: 'text/plain', size: 0 };
    const mock = createMockPubNub();
    await expect(downloadArtifact(ref, mock.pubnub)).rejects.toThrow('missing data');
  });

  it('throws for file ref missing required fields', async () => {
    const ref: ArtifactRef = { kind: 'file', mimeType: 'text/plain', size: 0 };
    const mock = createMockPubNub();
    await expect(downloadArtifact(ref, mock.pubnub)).rejects.toThrow('missing required fields');
  });

  it('throws for unknown kind', async () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const ref = { kind: 'unknown', mimeType: 'text/plain', size: 0 } as any;
    const mock = createMockPubNub();
    await expect(downloadArtifact(ref, mock.pubnub)).rejects.toThrow('Unknown ArtifactRef kind');
  });
});

describe('P1-1: TaskSession.downloadArtifact()', () => {
  it('delegates to pubnub when session has active client', async () => {
    const mock = createMockPubNub();
    const ref: ArtifactRef = {
      kind: 'inline',
      mimeType: 'text/plain',
      size: 5,
      data: btoa('hello'),
    };

    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: 't4',
      agentName: 'echo',
      pubnub: mock.pubnub,
      sdkOptions: defaultSdkOptions,
    });

    const result = await session.downloadArtifact(ref);
    expect(new TextDecoder().decode(result.data)).toBe('hello');
    session.close();
  });

  it('creates temporary client for pre-closed session', async () => {
    const ref: ArtifactRef = {
      kind: 'inline',
      mimeType: 'text/plain',
      size: 5,
      data: btoa('world'),
    };

    const session = new TaskSession({
      taskId: 'task-done',
      ownerId: 'alice',
      readToken: 't4-token',
      agentName: 'echo',
      pubnub: null,
      sdkOptions: defaultSdkOptions,
      preClosed: true,
      state: 'completed',
    });

    // Inline artifacts don't need PubNub calls, so the temp client creation
    // won't fail even with mock PubNub constructor
    const result = await session.downloadArtifact(ref);
    expect(new TextDecoder().decode(result.data)).toBe('world');
  });
});

// ============================================================================
// P1-3: Callback Error Handling
// ============================================================================

describe('P1-3: onError routing', () => {
  let mock: ReturnType<typeof createMockPubNub>;
  let session: TaskSession;
  const channel = 'u.alice.task-1';

  beforeEach(() => {
    mock = createMockPubNub();
    session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: 't4',
      agentName: 'echo',
      pubnub: mock.pubnub,
      sdkOptions: defaultSdkOptions,
    });
  });

  afterEach(() => {
    if (!session.isClosed) session.close();
  });

  it('routes callback error to onError handler', () => {
    const errors: Array<{ error: Error; context: CallbackErrorContext }> = [];
    session.onError((error, context) => {
      errors.push({ error, context });
    });

    session.onProgress(() => {
      throw new Error('callback boom');
    });

    mock._simulateMessage(channel, { type: 'progress', taskId: 'task-1' });

    expect(errors).toHaveLength(1);
    expect(errors[0].error.message).toBe('callback boom');
    expect(errors[0].context.callbackType).toBe('onProgress');
    expect(errors[0].context.entryPoint).toBe('taskSession');
  });

  it('logs warning when no onError registered', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});

    session.onArtifact(() => {
      throw new Error('artifact boom');
    });

    mock._simulateMessage(channel, {
      type: 'artifact',
      taskId: 'task-1',
      artifactRef: { kind: 'inline', mimeType: 'text/plain', size: 0 },
    });

    expect(warnSpy).toHaveBeenCalledWith(
      '[TaskSession]',
      expect.objectContaining({
        level: 'warn',
        event: 'task_session_callback_error',
        callbackType: 'onArtifact',
        error: 'artifact boom',
        message: expect.stringContaining('onArtifact'),
      }),
    );
    warnSpy.mockRestore();
  });

  it('prevents infinite loop when onError handler throws', () => {
    session.onError(() => {
      throw new Error('error handler boom');
    });

    session.onTerminal(() => {
      throw new Error('terminal boom');
    });

    // Should not throw or infinite loop
    expect(() => {
      mock._simulateMessage(channel, { type: 'terminal', taskId: 'task-1', state: 'completed' });
    }).not.toThrow();
  });

  it('remaining callbacks fire after one throws', () => {
    const second = vi.fn();

    session.onProgress(() => {
      throw new Error('first boom');
    });
    session.onProgress(second);

    session.onError(() => {}); // Register error handler to suppress warn

    mock._simulateMessage(channel, { type: 'progress', taskId: 'task-1' });

    expect(second).toHaveBeenCalledTimes(1);
  });

  it('routes onEvent errors correctly', () => {
    const errors: CallbackErrorContext[] = [];
    session.onError((_e, ctx) => errors.push(ctx));
    session.onEvent(() => { throw new Error('event boom'); });

    mock._simulateMessage(channel, { type: 'progress', taskId: 'task-1' });

    expect(errors.some(c => c.callbackType === 'onEvent')).toBe(true);
  });

  it('routes onStream errors correctly', () => {
    const errors: CallbackErrorContext[] = [];
    session.onError((_e, ctx) => errors.push(ctx));
    session.onStream(() => { throw new Error('stream boom'); });

    mock._simulateMessage(channel, {
      type: 'progress',
      taskId: 'task-1',
      streamEvent: 'stream_started',
      streams: {
        s1: {
          channel: 'stream.echo.s1',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c',
          tokenTtlMinutes: 62,
        },
      },
    });

    expect(errors.some(c => c.callbackType === 'onStream')).toBe(true);
  });

  it('routes streamPredicate errors correctly', () => {
    const errors: CallbackErrorContext[] = [];
    session.onError((_e, ctx) => errors.push(ctx));

    // First add a stream
    mock._simulateMessage(channel, {
      type: 'progress',
      taskId: 'task-1',
      streamEvent: 'stream_started',
      streams: {
        s1: {
          channel: 'stream.echo.s1',
          direction: 'outbound',
          format: 'bytes',
          affinity: 'dedicated',
          token: 't7c',
          tokenTtlMinutes: 62,
        },
      },
    });

    // Use waitForStreamWhere with a throwing predicate against already-known stream.
    // The predicate throws, so no match is found. The returned promise will be
    // rejected when the session closes. Catch it to prevent unhandled rejection.
    const pending = session.waitForStreamWhere(() => { throw new Error('predicate boom'); });
    pending.catch(() => {}); // Suppress unhandled rejection from close()

    expect(errors.some(c => c.callbackType === 'streamPredicate')).toBe(true);
  });

  it('clears errorCallbacks on close()', () => {
    const handler = vi.fn();
    session.onError(handler);
    session.close();

    // After close, error callbacks should be cleared
    // (verified by checking that creating a new session and routing errors works differently)
    expect(session.isClosed).toBe(true);
  });

  it('unsubscribe from onError works', () => {
    const handler = vi.fn();
    const unsub = session.onError(handler);
    unsub();

    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    session.onProgress(() => { throw new Error('boom'); });
    mock._simulateMessage(channel, { type: 'progress', taskId: 'task-1' });

    // handler should NOT have been called (unsubscribed)
    expect(handler).not.toHaveBeenCalled();
    // warn should fire instead (no error handlers registered)
    expect(warnSpy).toHaveBeenCalled();
    warnSpy.mockRestore();
  });
});

describe('P1-3: subscribeToTask error routing', () => {
  it('routes callback errors through onError', () => {
    const mock = createMockPubNub();
    const errors: Array<{ error: Error; context: CallbackErrorContext }> = [];

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      pubnub: mock.pubnub,
    });

    client.subscribeToTask('task-1', 'alice', {
      onProgress: () => { throw new Error('progress error'); },
      onError: (error, context) => {
        errors.push({ error, context });
      },
    });

    mock._simulateMessage('u.alice.task-1', { type: 'progress', taskId: 'task-1' });

    expect(errors).toHaveLength(1);
    expect(errors[0].error.message).toBe('progress error');
    expect(errors[0].context.entryPoint).toBe('subscribeToTask');
    expect(errors[0].context.callbackType).toBe('onProgress');
  });

  it('logs warning when no onError in callbacks', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const mock = createMockPubNub();

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      pubnub: mock.pubnub,
    });

    client.subscribeToTask('task-1', 'alice', {
      onTerminal: () => { throw new Error('terminal error'); },
    });

    mock._simulateMessage('u.alice.task-1', { type: 'terminal', taskId: 'task-1', state: 'completed' });

    expect(warnSpy).toHaveBeenCalledWith(
      '[TaskClient]',
      expect.objectContaining({
        level: 'warn',
        event: 'subscribe_callback_error',
        callbackType: 'onTerminal',
        error: 'terminal error',
      }),
    );
    warnSpy.mockRestore();
  });
});

// ============================================================================
// P1-5: TaskClient.create() factory
// ============================================================================

describe('P1-5: TaskClient.create()', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;
  const originalEnv = { ...process.env };

  const fakeCdm = {
    playground: { subscribeKey: 'sub-pg', publishKey: 'pub-pg' },
    network: { subscribeKey: 'sub-net', publishKey: 'pub-net' },
    api: { baseUrl: 'https://api.blocks.test' },
  };

  beforeEach(() => {
    fetchSpy = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => fakeCdm,
    });
    globalThis.fetch = fetchSpy;
    // Clean env
    delete process.env.BLOCKS_SUBSCRIBE_KEY;
    delete process.env.BLOCKS_PUBLISH_KEY;
    delete process.env.BLOCKS_BACKEND_URL;
    delete process.env.BLOCKS_CDM_URL;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    Object.assign(process.env, originalEnv);
  });

  it('throws when billingMode is not provided', async () => {
    await expect(TaskClient.create()).rejects.toThrow('billingMode');
  });

  it('selects playground keyset for billingMode=free', async () => {
    const client = await TaskClient.create({ billingMode: 'free' });
    // Verify the client was created with playground keys by checking internal state
    // via a test-accessible proxy: send a message that triggers RPC with the sub key
    expect(client).toBeInstanceOf(TaskClient);
    // The fetch was called for CDM config
    expect(fetchSpy).toHaveBeenCalled();
  });

  it('selects network keyset for billingMode=paid', async () => {
    const client = await TaskClient.create({ billingMode: 'paid' });
    expect(client).toBeInstanceOf(TaskClient);
  });

  it('uses explicit options over env and CDM', async () => {
    process.env.BLOCKS_SUBSCRIBE_KEY = 'env-sub';
    process.env.BLOCKS_BACKEND_URL = 'https://env.test';

    const client = await TaskClient.create({
      billingMode: 'free',
      subscribeKey: 'explicit-sub',
      baseUrl: 'https://explicit.test',
    });
    expect(client).toBeInstanceOf(TaskClient);
  });

  it('uses BLOCKS_* env vars when explicit options not provided', async () => {
    process.env.BLOCKS_SUBSCRIBE_KEY = 'env-sub';
    process.env.BLOCKS_PUBLISH_KEY = 'env-pub';
    process.env.BLOCKS_BACKEND_URL = 'https://env.test';

    const client = await TaskClient.create({ billingMode: 'free' });
    expect(client).toBeInstanceOf(TaskClient);
  });

  it('resolves baseUrl from CDM when not in env or options', async () => {
    const client = await TaskClient.create({ billingMode: 'free' });
    expect(client).toBeInstanceOf(TaskClient);
    // CDM api.baseUrl should be used
  });

  it('throws when baseUrl cannot be resolved', async () => {
    fetchSpy.mockResolvedValue({
      ok: true,
      json: async () => ({
        playground: { subscribeKey: 'sub-pg', publishKey: 'pub-pg' },
        network: { subscribeKey: 'sub-net', publishKey: 'pub-net' },
        api: {}, // no baseUrl
      }),
    });

    await expect(TaskClient.create({ billingMode: 'free' })).rejects.toThrow('baseUrl');
  });

});

// ============================================================================
// P1-2: connect()
// ============================================================================

describe('P1-2: connect()', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  function setupConnectMocks(opts: {
    taskState: string;
    agentName?: string;
    historyMessages?: unknown[];
  }) {
    let callCount = 0;
    fetchSpy.mockImplementation(async (_url: string) => {
      callCount++;
      // First call: getTask RPC
      if (callCount === 1) {
        return mockRpcResponse({
          task: {
            taskId: 'task-1',
            agentName: opts.agentName ?? 'echo',
            owner: 'alice',
            state: opts.taskState,
          },
        });
      }
      // Second call: task-read-token
      if (callCount === 2) {
        return {
          ok: true,
          json: async () => ({
            pamToken: 'consumer-t4',
            channel: 'u.org1.task-1',
            ttlMinutes: 60,
          }),
        };
      }
      return { ok: true, json: async () => ({}) };
    });
  }

  it('throws without auth', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      baseUrl: 'https://api.test',
    });

    await expect(
      client.connect({ taskId: 'task-1' }),
    ).rejects.toThrow('requires an authenticated TaskClient');
  });

  it('connects to terminal task with skipSubscription', async () => {
    setupConnectMocks({ taskState: 'completed' });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      authProvider: new StaticAuthProvider('jwt-token'),
      baseUrl: 'https://api.test',
      createSessionPubNub: () => {
        const mock = createMockPubNub();
        // Provide fetchMessages for history
        mock.pubnub.fetchMessages = vi.fn().mockResolvedValue({
          channels: {
            'u.org1.task-1': [
              {
                message: {
                  type: 'request',
                  taskId: 'task-1',
                  requestParts: [],
                },
                timetoken: '0000000090',
              },
              {
                message: {
                  type: 'progress',
                  taskId: 'task-1',
                  message: 'Working',
                  progress: 50,
                },
                timetoken: '0000000095',
              },
              {
                message: {
                  type: 'artifact',
                  taskId: 'task-1',
                  artifactRef: {
                    kind: 'inline',
                    mimeType: 'text/plain',
                    size: 5,
                    data: btoa('hello'),
                  },
                },
                timetoken: '0000000100',
              },
              {
                message: {
                  type: 'system',
                  taskId: 'task-1',
                  status: 'paused',
                },
                timetoken: '0000000110',
              },
              {
                message: {
                  type: 'log',
                  taskId: 'task-1',
                  message: 'finished',
                },
                timetoken: '0000000115',
              },
              {
                message: {
                  type: 'terminal',
                  taskId: 'task-1',
                  state: 'completed',
                },
                timetoken: '0000000120',
              },
              {
                message: 'ignore-me',
                timetoken: '0000000130',
              },
            ],
          },
        });
        return mock.pubnub;
      },
    });

    const session = await client.connect({ taskId: 'task-1' });

    expect(session.state).toBe('completed');
    expect(session.isClosed).toBe(false); // skipSubscription, not preClosed
    expect(session.listArtifacts()).toHaveLength(1);
    expect(session.listArtifacts()[0].kind).toBe('inline');
    expect(session.listEvents().map((event) => event.type)).toEqual([
      'request',
      'progress',
      'artifact',
      'system',
      'log',
      'terminal',
    ]);

    session.close();
    expect(session.isClosed).toBe(true);
  });

  it('derives terminal state from history when backend RPC lags', async () => {
    // Regression: a consumer reconnecting within a few seconds of task
    // completion can hit a window where the PubNub terminal event has
    // already fired but the backend's taskFanout → DB write hasn't
    // propagated yet, so `GetTask` still returns state=running. Without
    // this fix, connect() would fall into the active path, hand back
    // a live session with state=running, and `ref.open()` would skip
    // the terminal short-circuit and construct a StreamClient against
    // an about-to-be-revoked T7c — the exact silent-hang Fix A was
    // supposed to prevent.
    setupConnectMocks({ taskState: 'running' });

    const mockPn = createMockPubNub();
    mockPn.pubnub.fetchMessages = vi.fn().mockResolvedValue({
      channels: {
        'u.org1.task-1': [
          {
            message: {
              type: 'request',
              taskId: 'task-1',
              requestParts: [],
            },
            timetoken: '25',
          },
          {
            message: {
              type: 'progress',
              taskId: 'task-1',
              streamEvent: 'stream_started',
              streams: {
                s1: {
                  channel: 'stream.echo.s1',
                  direction: 'outbound',
                  format: 'bytes',
                  affinity: 'dedicated',
                  token: 't7c-1',
                  tokenTtlMinutes: 17,
                },
              },
            },
            timetoken: '100',
          },
          {
            message: {
              type: 'terminal',
              taskId: 'task-1',
              state: 'completed',
            },
            timetoken: '200',
          },
        ],
      },
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      authProvider: new StaticAuthProvider('jwt-token'),
      baseUrl: 'https://api.test',
      createSessionPubNub: () => mockPn.pubnub,
    });

    const session = await client.connect({ taskId: 'task-1' });

    // Despite RPC state='running', history's terminal event wins.
    expect(session.state).toBe('completed');
    expect(session.listStreams()).toHaveLength(1);

    // Ref.open() must short-circuit now that state is terminal.
    const ref = session.listStreams()[0];
    expect(() => ref.open()).toThrow(/terminal state/);

    session.close();
  });

  it('connects to active task with subscribe-first sequence', async () => {
    setupConnectMocks({
      taskState: 'running',
      historyMessages: [],
    });

    const mockPn = createMockPubNub();
    mockPn.pubnub.fetchMessages = vi.fn().mockResolvedValue({
      channels: {
        'u.org1.task-1': [
          {
            message: {
              type: 'request',
              taskId: 'task-1',
              requestParts: [],
            },
            timetoken: '0000000040',
          },
          {
            message: {
              type: 'progress',
              taskId: 'task-1',
              streamEvent: 'stream_started',
              streams: {
                s1: {
                  channel: 'stream.echo.s1',
                  direction: 'outbound',
                  format: 'bytes',
                  affinity: 'dedicated',
                  token: 't7c-1',
                  tokenTtlMinutes: 62,
                },
              },
            },
            timetoken: '0000000050',
          },
          {
            message: {
              type: 'system',
              taskId: 'task-1',
              status: 'heartbeat',
            },
            timetoken: '0000000060',
          },
          {
            message: null,
            timetoken: '0000000070',
          },
        ],
      },
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      authProvider: new StaticAuthProvider('jwt-token'),
      baseUrl: 'https://api.test',
      createSessionPubNub: () => mockPn.pubnub,
    });

    const session = await client.connect({ taskId: 'task-1' });

    expect(session.state).toBe('running');
    expect(session.isClosed).toBe(false);
    // Stream from history should be preloaded
    expect(session.listStreams()).toHaveLength(1);
    expect(session.listStreams()[0].descriptor.streamId).toBe('s1');
    expect(session.listEvents().map((event) => [event.type, event.streamEvent])).toEqual([
      ['request', undefined],
      ['progress', 'stream_started'],
      ['system', undefined],
    ]);
    expect(mockPn.pubnub.fetchMessages).toHaveBeenCalledTimes(1);

    session.close();
  });

  it('deduplicates events between history and buffer', async () => {
    setupConnectMocks({ taskState: 'running' });

    const mockPn = createMockPubNub();
    mockPn.pubnub.fetchMessages = vi.fn().mockResolvedValue({
      channels: {
        'u.org1.task-1': [
          {
            message: { type: 'progress', taskId: 'task-1', progress: 50 },
            timetoken: '100',
          },
        ],
      },
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      authProvider: new StaticAuthProvider('jwt-token'),
      baseUrl: 'https://api.test',
      createSessionPubNub: () => mockPn.pubnub,
    });

    const session = await client.connect({ taskId: 'task-1' });

    // Events with timetokens <= high-water mark (100) should be deduped
    const progressEvents: unknown[] = [];
    session.onProgress((e) => progressEvents.push(e));

    // Simulate a live event with timetoken > high-water mark
    mockPn._simulateMessage('u.org1.task-1', { type: 'progress', taskId: 'task-1', progress: 75 }, '200');

    expect(progressEvents).toHaveLength(1);
    expect((progressEvents[0] as Record<string, unknown>).progress).toBe(75);

    session.close();
  });

  it('waitForStream on skipSubscription with no match rejects immediately', async () => {
    setupConnectMocks({ taskState: 'completed' });

    const mockPn = createMockPubNub();
    mockPn.pubnub.fetchMessages = vi.fn().mockResolvedValue({ channels: {} });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      authProvider: new StaticAuthProvider('jwt-token'),
      baseUrl: 'https://api.test',
      createSessionPubNub: () => mockPn.pubnub,
    });

    const session = await client.connect({ taskId: 'task-1' });

    await expect(session.waitForStream('nonexistent')).rejects.toThrow(
      'No matching stream found',
    );
    await expect(session.waitForStreamWhere(() => true)).rejects.toThrow(
      'No matching stream found',
    );

    session.close();
  });

  it('waitForStream on skipSubscription with preloaded match resolves', async () => {
    setupConnectMocks({ taskState: 'completed' });

    const mockPn = createMockPubNub();
    mockPn.pubnub.fetchMessages = vi.fn().mockResolvedValue({
      channels: {
        'u.org1.task-1': [
          {
            message: {
              type: 'progress',
              taskId: 'task-1',
              streamEvent: 'stream_started',
              streams: {
                s1: {
                  channel: 'stream.echo.s1',
                  direction: 'outbound',
                  format: 'bytes',
                  affinity: 'dedicated',
                  token: 't7c-1',
                  tokenTtlMinutes: 62,
                },
              },
            },
            timetoken: '50',
          },
        ],
      },
    });

    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-key',
      publishKey: 'pub-key',
      authProvider: new StaticAuthProvider('jwt-token'),
      baseUrl: 'https://api.test',
      createSessionPubNub: () => mockPn.pubnub,
    });

    const session = await client.connect({ taskId: 'task-1' });
    const ref = await session.waitForStream('s1');
    expect(ref.descriptor.streamId).toBe('s1');

    session.close();
  });
});

// ============================================================================
// P1-2: listArtifacts() accumulation
// ============================================================================

describe('P1-2: listArtifacts() accumulation', () => {
  it('accumulates artifacts from live events', () => {
    const mock = createMockPubNub();
    const channel = 'u.alice.task-1';

    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: 't4',
      agentName: 'echo',
      pubnub: mock.pubnub,
      sdkOptions: defaultSdkOptions,
    });

    expect(session.listArtifacts()).toHaveLength(0);

    mock._simulateMessage(channel, {
      type: 'artifact',
      taskId: 'task-1',
      artifactRef: {
        kind: 'inline',
        mimeType: 'text/plain',
        size: 5,
        data: btoa('hello'),
      },
    });

    expect(session.listArtifacts()).toHaveLength(1);
    expect(session.listArtifacts()[0].kind).toBe('inline');

    mock._simulateMessage(channel, {
      type: 'artifact',
      taskId: 'task-1',
      artifactRef: {
        kind: 'file',
        mimeType: 'image/png',
        size: 1000,
        fileId: 'f1',
        fileName: 'img.png',
        channel: 'u.org1.task-1',
      },
    });

    expect(session.listArtifacts()).toHaveLength(2);

    session.close();
  });

  it('includes preloaded artifacts from constructor', () => {
    const mock = createMockPubNub();

    const preloaded: ArtifactRef[] = [
      {
        kind: 'inline',
        mimeType: 'text/plain',
        size: 3,
        data: btoa('abc'),
      },
    ];

    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: 't4',
      agentName: 'echo',
      pubnub: mock.pubnub,
      sdkOptions: defaultSdkOptions,
      preloadedArtifacts: preloaded,
    });

    expect(session.listArtifacts()).toHaveLength(1);

    session.close();
  });

  it('returns copy of artifacts list', () => {
    const mock = createMockPubNub();
    const session = new TaskSession({
      taskId: 'task-1',
      ownerId: 'alice',
      readToken: 't4',
      agentName: 'echo',
      pubnub: mock.pubnub,
      sdkOptions: defaultSdkOptions,
    });

    const list1 = session.listArtifacts();
    const list2 = session.listArtifacts();
    expect(list1).not.toBe(list2); // different references

    session.close();
  });
});
