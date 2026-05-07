/**
 * Integration tests for protocol versioning: registration payload and RPC headers.
 *
 * Covers:
 * - Registration payload includes sdkVersion, protocolVersions, preferredProtocolVersion, cliVersion
 * - RPC header emission (Blocks-Protocol-Version)
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { CURRENT_PROTOCOL_VERSION, PROTOCOL_VERSION_HEADER, SDK_VERSION, SUPPORTED_PROTOCOL_VERSIONS } from '../src/runtime/protocol-version.js';
import { StaticAuthProvider } from '../src/runtime/auth-provider.js';

// ============================================================================
// 1. Registration payload tests
// ============================================================================

describe('registration payload includes version fields', () => {
  let savedCliVersion: string | undefined;

  function mockAgentAuth() {
    return { init: vi.fn().mockResolvedValue({ pamToken: 'pam-test' }) };
  }

  beforeEach(() => {
    savedCliVersion = process.env.BLOCKS_CLI_VERSION;
    delete process.env.BLOCKS_CLI_VERSION;
  });

  afterEach(() => {
    if (savedCliVersion !== undefined) process.env.BLOCKS_CLI_VERSION = savedCliVersion;
    else delete process.env.BLOCKS_CLI_VERSION;
  });

  it('sends sdkVersion, protocolVersions, and preferredProtocolVersion', async () => {
    const { connectAgent } = await import('../src/runtime/agent-registry.js');
    const auth = mockAgentAuth();

    await connectAgent('test_agent', {
      instanceId: 'AG-test-123',
      baseUrl: 'http://test-api.example.com',
      agentAuth: auth as any,
    });

    expect(auth.init).toHaveBeenCalledTimes(1);
    const payload = auth.init.mock.calls[0][0];

    expect(payload.sdkVersion).toBe(SDK_VERSION);
    expect(payload.protocolVersions).toEqual([...SUPPORTED_PROTOCOL_VERSIONS]);
    expect(payload.preferredProtocolVersion).toBe(CURRENT_PROTOCOL_VERSION);
  });

  it('includes cliVersion when BLOCKS_CLI_VERSION env is set', async () => {
    process.env.BLOCKS_CLI_VERSION = '1.2.3';
    const { connectAgent } = await import('../src/runtime/agent-registry.js');
    const auth = mockAgentAuth();

    await connectAgent('test_agent', {
      instanceId: 'AG-test-123',
      baseUrl: 'http://test-api.example.com',
      agentAuth: auth as any,
    });

    const payload = auth.init.mock.calls[0][0];
    expect(payload.cliVersion).toBe('1.2.3');
  });

  it('omits cliVersion when BLOCKS_CLI_VERSION env is not set', async () => {
    delete process.env.BLOCKS_CLI_VERSION;
    const { connectAgent } = await import('../src/runtime/agent-registry.js');
    const auth = mockAgentAuth();

    await connectAgent('test_agent', {
      instanceId: 'AG-test-123',
      baseUrl: 'http://test-api.example.com',
      agentAuth: auth as any,
    });

    const payload = auth.init.mock.calls[0][0];
    expect(payload.cliVersion).toBeUndefined();
  });
});

// ============================================================================
// 2. RPC header tests
// ============================================================================

describe('RPC client sends Blocks-Protocol-Version header', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it('includes Blocks-Protocol-Version header on every RPC call', async () => {
    const { callRpc } = await import('../src/runtime/rpc-client.js');

    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ jsonrpc: '2.0', id: 'x', result: { ok: true } }),
    });

    await callRpc(
      { subscribeKey: 'sub-c-test', baseUrl: 'http://localhost:3001' },
      'TestMethod',
      {},
    );

    const [, init] = fetchSpy.mock.calls[0];
    expect(init.headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
  });

  it('sends header alongside Authorization when authProvider is present', async () => {
    const { callRpc } = await import('../src/runtime/rpc-client.js');

    fetchSpy.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ jsonrpc: '2.0', id: 'x', result: {} }),
    });

    await callRpc(
      { subscribeKey: 'sub-c-test', authProvider: new StaticAuthProvider('my-jwt'), baseUrl: 'http://localhost:3001' },
      'Method',
      {},
    );

    const [, init] = fetchSpy.mock.calls[0];
    expect(init.headers[PROTOCOL_VERSION_HEADER]).toBe(CURRENT_PROTOCOL_VERSION);
    expect(init.headers['Authorization']).toBe('Bearer my-jwt');
  });
});
