/**
 * Consumer SDK Phase 2 tests -- AuthProvider transport refactor,
 * TaskClient.create() modes, 401 retry, connect() pagination,
 * token rotation, updateKeys, destroy.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { callRpc, type RpcClientConfig } from '../src/runtime/rpc-client.js';
import { StaticAuthProvider, type AuthProvider } from '../src/runtime/auth-provider.js';
import { requestUpload, type FileUploadAuth } from '../src/runtime/file-upload.js';
import { TaskClient } from '../src/runtime/task-client.js';
import type { TokenResult } from '../src/runtime/consumer-auth.js';

// ---------------------------------------------------------------------------
// Fetch mock
// ---------------------------------------------------------------------------

const originalFetch = globalThis.fetch;
let fetchSpy: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchSpy = vi.fn();
  globalThis.fetch = fetchSpy;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
});

// ---------------------------------------------------------------------------
// Mock PubNub for TaskClient tests
// ---------------------------------------------------------------------------

vi.mock('pubnub', () => {
  return {
    default: vi.fn().mockImplementation(() => ({
      addListener: vi.fn(),
      removeListener: vi.fn(),
      subscribe: vi.fn(),
      unsubscribe: vi.fn(),
      destroy: vi.fn(),
      setToken: vi.fn(),
      fetchMessages: vi.fn(),
      time: vi.fn().mockResolvedValue({ timetoken: '17000000000000000' }),
    })),
  };
});

// Mock CDM config
vi.mock('../src/runtime/cdm-config.js', () => ({
  fetchCdmConfig: vi.fn().mockResolvedValue({
    playground: { subscribeKey: 'sub-playground', publishKey: 'pub-playground' },
    network: { subscribeKey: 'sub-network', publishKey: 'pub-network' },
    api: { baseUrl: 'http://cdm-backend.example.com' },
  }),
  DEFAULT_CDM_URL: 'https://mock-cdm.example.com/config.json',
}));

// Mock env
vi.mock('../src/env.js', () => ({
  getEnv: vi.fn().mockReturnValue(undefined),
}));

// ==========================================================================
// RPC 401 reactive refresh
// ==========================================================================

describe('RPC 401 reactive refresh', () => {
  it('retries once on 401 when authProvider can refresh', async () => {
    let callCount = 0;
    const mockProvider: AuthProvider = {
      getAuthHeader: () => (callCount === 0 ? 'Bearer old-token' : 'Bearer new-token'),
      onAuthFailure: async () => {
        callCount++;
        return true;
      },
    };

    // First call: 401
    fetchSpy.mockResolvedValueOnce({
      ok: false,
      status: 401,
      json: async () => ({}),
    });
    // Retry: success
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ jsonrpc: '2.0', id: 'x', result: { data: 'ok' } }),
    });

    const config: RpcClientConfig = {
      subscribeKey: 'sub-test',
      authProvider: mockProvider,
      baseUrl: 'http://localhost:3001',
    };

    const result = await callRpc<{ data: string }>(config, 'Method', {});
    expect(result.data).toBe('ok');
    expect(fetchSpy).toHaveBeenCalledTimes(2);

    // Verify the retry used the new token
    const [, retryInit] = fetchSpy.mock.calls[1];
    expect(retryInit.headers['Authorization']).toBe('Bearer new-token');
  });

  it('propagates 401 when authProvider returns false from onAuthFailure', async () => {
    const mockProvider: AuthProvider = {
      getAuthHeader: () => 'Bearer expired-token',
      onAuthFailure: async () => false,
    };

    fetchSpy.mockResolvedValueOnce({
      ok: false,
      status: 401,
      json: async () => ({}),
    });

    const config: RpcClientConfig = {
      subscribeKey: 'sub-test',
      authProvider: mockProvider,
      baseUrl: 'http://localhost:3001',
    };

    await expect(callRpc(config, 'Method', {})).rejects.toThrow('HTTP 401');
    expect(fetchSpy).toHaveBeenCalledTimes(1);
  });

  it('propagates 401 with StaticAuthProvider (no refresh)', async () => {
    fetchSpy.mockResolvedValueOnce({
      ok: false,
      status: 401,
      json: async () => ({}),
    });

    const config: RpcClientConfig = {
      subscribeKey: 'sub-test',
      authProvider: new StaticAuthProvider('static-jwt'),
      baseUrl: 'http://localhost:3001',
    };

    await expect(callRpc(config, 'Method', {})).rejects.toThrow('HTTP 401');
  });

  it('does not interfere with agentAuth 401 handling', async () => {
    // When agentAuth is present, its authenticatedFetch handles 401 internally.
    // authProvider 401 retry should NOT run.
    const mockAgentAuth = {
      authenticatedFetch: vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ jsonrpc: '2.0', id: 'x', result: 'agent-ok' }),
      }),
    };

    const config: RpcClientConfig = {
      subscribeKey: 'sub-test',
      authProvider: new StaticAuthProvider('consumer-jwt'),
      baseUrl: 'http://localhost:3001',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      agentAuth: mockAgentAuth as any,
    };

    const result = await callRpc<string>(config, 'Method', {});
    expect(result).toBe('agent-ok');
    expect(mockAgentAuth.authenticatedFetch).toHaveBeenCalledTimes(1);
  });
});

// ==========================================================================
// File upload 401 retry
// ==========================================================================

describe('file-upload 401 retry', () => {
  it('retries backendFetch on 401 when authProvider refreshes', async () => {
    let headerCallCount = 0;
    const mockProvider: AuthProvider = {
      getAuthHeader: () => {
        headerCallCount++;
        return headerCallCount === 1 ? 'Bearer old' : 'Bearer new';
      },
      onAuthFailure: async () => true,
    };

    const auth: FileUploadAuth = {
      baseUrl: 'http://localhost:3001',
      authProvider: mockProvider,
    };

    // First call: 401
    fetchSpy.mockResolvedValueOnce({
      ok: false,
      status: 401,
      text: async () => 'Unauthorized',
    });
    // Retry: success
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        uploadSessionId: 's1',
        uploadId: 'u1',
        uploadUrl: 'https://s3.example.com',
        formFields: [],
      }),
    });

    const result = await requestUpload(auth, {
      role: 'consumer-input',
      agentName: 'test',
      fileName: 'f.txt',
      fileSize: 10,
      mimeType: 'text/plain',
      partId: 'p',
    });

    expect(result.uploadId).toBe('u1');
    expect(fetchSpy).toHaveBeenCalledTimes(2);
  });
});

// ==========================================================================
// TaskClient.create() modes
// ==========================================================================

describe('TaskClient.create() auth modes', () => {
  it('creates ConsumerAuth for apiKey mode', async () => {
    // Mock the consumer-token endpoint
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({
        accessToken: 'consumer-jwt-from-api-key',
        refreshToken: 'rt-1',
        expiresIn: 60,
        userId: 'user-from-key',
      }),
    });

    const client = await TaskClient.create({
      billingMode: 'free',
      apiKey: 'bk_test_key',
    });

    // Verify the consumer JWT is used for RPC
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ jsonrpc: '2.0', id: 'x', result: { task: { taskId: 't1' } } }),
    });

    await client.getTask('t1');
    const [, init] = fetchSpy.mock.calls[1];
    expect(init.headers['Authorization']).toBe('Bearer consumer-jwt-from-api-key');

    client.destroy();
  });

  it('creates ConsumerAuth for tokenEndpoint mode', async () => {
    // Mock the token endpoint
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => ({
        token: 'proxy-jwt',
        expiresIn: 120,
        userId: 'proxy-user',
      }),
    });

    const client = await TaskClient.create({
      billingMode: 'free',
      tokenEndpoint: 'http://my-proxy.com/token',
    });

    // Verify the proxy JWT is used
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ jsonrpc: '2.0', id: 'x', result: { task: { taskId: 't1' } } }),
    });

    await client.getTask('t1');
    const [, init] = fetchSpy.mock.calls[1];
    expect(init.headers['Authorization']).toBe('Bearer proxy-jwt');

    client.destroy();
  });

  it('creates ConsumerAuth for tokenProvider mode', async () => {
    const provider = vi.fn<() => Promise<TokenResult>>().mockResolvedValue({
      token: 'custom-jwt',
      expiresIn: 300,
    });

    const client = await TaskClient.create({
      billingMode: 'free',
      tokenProvider: provider,
    });

    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ jsonrpc: '2.0', id: 'x', result: { task: { taskId: 't1' } } }),
    });

    await client.getTask('t1');
    const [, init] = fetchSpy.mock.calls[0];
    expect(init.headers['Authorization']).toBe('Bearer custom-jwt');

    client.destroy();
  });

  // --------------------------------------------------------------------------
  // Mutual exclusion
  // --------------------------------------------------------------------------

  it('throws when multiple provider modes are specified', async () => {
    await expect(
      TaskClient.create({
        billingMode: 'free',
        apiKey: 'key',
        tokenEndpoint: 'http://proxy.com/token',
      }),
    ).rejects.toThrow('Only one token provider mode may be specified');
  });

  it('throws when all three provider modes are specified', async () => {
    await expect(
      TaskClient.create({
        billingMode: 'free',
        apiKey: 'key',
        tokenEndpoint: 'http://proxy.com/token',
        tokenProvider: async () => ({ token: 't', expiresIn: 60 }),
      }),
    ).rejects.toThrow('Only one token provider mode may be specified');
  });
});

// ==========================================================================
// Token rotation through RPC
// ==========================================================================

describe('token rotation', () => {
  it('subsequent RPC calls use the refreshed token after ConsumerAuth refresh', async () => {
    let callCount = 0;
    const provider = vi.fn<() => Promise<TokenResult>>().mockImplementation(async () => {
      callCount++;
      return { token: `jwt-v${callCount}`, expiresIn: 300 };
    });

    const client = await TaskClient.create({
      billingMode: 'free',
      tokenProvider: provider,
    });

    // First RPC call uses jwt-v1
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ jsonrpc: '2.0', id: 'x', result: { task: { taskId: 't1' } } }),
    });
    await client.getTask('t1');
    expect(fetchSpy.mock.calls[0][1].headers['Authorization']).toBe('Bearer jwt-v1');

    // Simulate a 401 -> reactive refresh -> retry
    fetchSpy
      .mockResolvedValueOnce({ ok: false, status: 401, json: async () => ({}) })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ jsonrpc: '2.0', id: 'x', result: { task: { taskId: 't2' } } }),
      });

    await client.getTask('t2');
    // After refresh, token should be jwt-v2
    expect(fetchSpy.mock.calls[2][1].headers['Authorization']).toBe('Bearer jwt-v2');

    client.destroy();
  });
});

// ==========================================================================
// updateKeys()
// ==========================================================================

describe('TaskClient.updateKeys()', () => {
  it('updates only keyset values', () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-old',
      authProvider: new StaticAuthProvider('token-old'),
    });

    client.updateKeys('sub-new', 'pub-new');
    expect((client as unknown as { _subscribeKey: string })._subscribeKey).toBe('sub-new');
  });

  it('preserves ConsumerAuth-managed auth state', async () => {
    const provider = vi.fn<() => Promise<TokenResult>>().mockResolvedValue({
      token: 'consumer-managed-jwt',
      expiresIn: 300,
    });

    const client = await TaskClient.create({
      billingMode: 'free',
      tokenProvider: provider,
    });

    client.updateKeys('sub-new', 'pub-new');

    // RPC should still use the ConsumerAuth token
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ jsonrpc: '2.0', id: 'x', result: { task: { taskId: 't1' } } }),
    });
    await client.getTask('t1');

    const [, init] = fetchSpy.mock.calls[0];
    expect(init.headers['Authorization']).toBe('Bearer consumer-managed-jwt');

    client.destroy();
  });
});

// ==========================================================================
// TaskClient.destroy()
// ==========================================================================

describe('TaskClient.destroy()', () => {
  it('stops ConsumerAuth refresh timer', async () => {
    vi.useFakeTimers();
    let callCount = 0;
    const provider = vi.fn<() => Promise<TokenResult>>().mockImplementation(async () => {
      callCount++;
      return { token: `jwt-${callCount}`, expiresIn: 100 };
    });

    const client = await TaskClient.create({
      billingMode: 'free',
      tokenProvider: provider,
    });

    // destroy() stops timer
    client.destroy();

    // Advance past refresh window
    await vi.advanceTimersByTimeAsync(200_000);

    // Provider should only have been called once (init)
    expect(provider).toHaveBeenCalledTimes(1);

    vi.useRealTimers();
  });
});

// ==========================================================================
// connect() auth validation
// ==========================================================================

describe('connect() auth validation', () => {
  it('throws when no auth provider is set', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-test',
      baseUrl: 'http://localhost:3001',
    });

    await expect(
      client.connect({ taskId: 'task-1' }),
    ).rejects.toThrow(
      'connect() requires an authenticated TaskClient',
    );
  });
});

// ==========================================================================
// connect() pagination
// ==========================================================================

describe('connect() pagination', () => {
  it('fetches multiple pages of history for terminal tasks', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-test',
      baseUrl: 'http://localhost:3001',
      authProvider: new StaticAuthProvider('jwt-test'),
    });

    // Mock getTask RPC
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          task: {
            taskId: 'task-paginated',
            agentName: 'test_agent',
            state: 'completed',
            owner: 'user-1',
          },
        },
      }),
    });

    // Mock task-read-token
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        pamToken: 'pam-token-123',
        channel: 'u.org1.task-paginated',
        ttlMinutes: 60,
      }),
    });

    // PubNub mock with paginated fetchMessages
    const PubNub = (await import('pubnub')).default;
    const mockPubNub = new PubNub({
      subscribeKey: 'sub-test',
      userId: 'test',
    });

    let fetchCallCount = 0;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (mockPubNub as any).fetchMessages = vi.fn().mockImplementation(
      (params: { channels: string[]; count?: number; start?: string }) => {
        fetchCallCount++;
        if (fetchCallCount === 1) {
          // First page: 100 messages
          const msgs = Array.from({ length: 100 }, (_, i) => ({
            message: { type: 'progress', taskId: 'task-paginated', index: i },
            timetoken: String(1000 + i),
          }));
          return Promise.resolve({ channels: { [params.channels[0]]: msgs } });
        }
        if (fetchCallCount === 2) {
          // Second page: 50 messages (partial -- signals end)
          const msgs = Array.from({ length: 50 }, (_, i) => ({
            message: { type: 'progress', taskId: 'task-paginated', index: 100 + i },
            timetoken: String(900 + i),
          }));
          return Promise.resolve({ channels: { [params.channels[0]]: msgs } });
        }
        return Promise.resolve({ channels: {} });
      },
    );

    // Override createPerSessionPubNub
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (client as any).createPerSessionPubNub = () => mockPubNub;

    const session = await client.connect({ taskId: 'task-paginated' });

    // Should have called fetchMessages at least 2 times for pagination
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect((mockPubNub as any).fetchMessages).toHaveBeenCalledTimes(2);

    // Verify second call uses oldest timetoken from first page as cursor
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const secondCall = (mockPubNub as any).fetchMessages.mock.calls[1][0];
    expect(secondCall.start).toBe('1000'); // oldest timetoken from page 1

    session.close();
  });

  it('handles empty history gracefully', async () => {
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-test',
      baseUrl: 'http://localhost:3001',
      authProvider: new StaticAuthProvider('jwt-test'),
    });

    // Mock getTask
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          task: {
            taskId: 'task-empty',
            agentName: 'test_agent',
            state: 'completed',
            owner: 'user-1',
          },
        },
      }),
    });

    // Mock task-read-token
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        pamToken: 'pam-token-123',
        channel: 'u.org1.task-empty',
        ttlMinutes: 60,
      }),
    });

    const PubNub = (await import('pubnub')).default;
    const mockPubNub = new PubNub({
      subscribeKey: 'sub-test',
      userId: 'test',
    });

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (mockPubNub as any).fetchMessages = vi.fn().mockResolvedValue({
      channels: {},
    });

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (client as any).createPerSessionPubNub = () => mockPubNub;

    const session = await client.connect({ taskId: 'task-empty' });

    // Should have called fetchMessages once (empty = stop)
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    expect((mockPubNub as any).fetchMessages).toHaveBeenCalledTimes(1);

    expect(session.listArtifacts()).toEqual([]);
    expect(session.listStreams()).toEqual([]);

    session.close();
  });
});

// ==========================================================================
// Task-read-token 401 retry
// ==========================================================================

describe('fetchConsumerReadToken 401 retry', () => {
  it('retries task-read-token on 401 with refreshed token', async () => {
    let tokenVersion = 1;
    const mockProvider: AuthProvider = {
      getAuthHeader: () => `Bearer jwt-v${tokenVersion}`,
      onAuthFailure: async () => {
        tokenVersion++;
        return true;
      },
    };

    // Build a TaskClient with our mock provider
    const client = new TaskClient({
      billingMode: 'free',
      subscribeKey: 'sub-test',
      baseUrl: 'http://localhost:3001',
    });
    // Inject the provider directly
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (client as any).config.authProvider = mockProvider;

    // Mock getTask
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        jsonrpc: '2.0',
        id: 'x',
        result: {
          task: { taskId: 'task-1', agentName: 'agent-1', state: 'completed', owner: 'user-1' },
        },
      }),
    });

    // Mock task-read-token: first call 401, second call success
    fetchSpy
      .mockResolvedValueOnce({ ok: false, status: 401, text: async () => 'Unauthorized' })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ pamToken: 'pam-1', channel: 'u.org.task-1', ttlMinutes: 60 }),
      });

    const PubNub = (await import('pubnub')).default;
    const mockPubNub = new PubNub({ subscribeKey: 'sub-test', userId: 'test' });
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (mockPubNub as any).fetchMessages = vi.fn().mockResolvedValue({ channels: {} });
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (client as any).createPerSessionPubNub = () => mockPubNub;

    const session = await client.connect({ taskId: 'task-1' });
    expect(session).toBeDefined();

    // Verify the retry used the refreshed token
    const thirdCallInit = fetchSpy.mock.calls[2][1];
    expect(thirdCallInit.headers['Authorization']).toBe('Bearer jwt-v2');

    session.close();
  });
});
