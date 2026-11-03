import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { AuthRequiredError } from '../src/runtime/rpc-client.js';
import { callRpc } from '../src/runtime/rpc-client.js';

describe('RPC AuthRequired error mapping', () => {
  const originalFetch = globalThis.fetch;
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  it('callRpc throws AuthRequiredError carrying integration, reason, connectUrl', async () => {
    fetchSpy.mockResolvedValueOnce({
      ok: true,
      status: 200,
      headers: new Headers(),
      json: async () => ({
        id: 'rpc-1',
        jsonrpc: '2.0',
        error: {
          code: -32000,
          message: 'Integration connection required',
          data: {
            code: 'AuthRequired',
            details: {
              integration: 'github',
              reason: 'not_connected',
              connectUrl: 'http://localhost:3001/connect/org-1/github?agent=test_agent',
            },
          },
        },
      }),
    });

    try {
      await callRpc(
        {
          subscribeKey: 'test-sub-key',
          baseUrl: 'http://localhost:3001',
        },
        'testMethod',
        {},
      );
      expect.unreachable('should have thrown');
    } catch (err) {
      expect(err).toBeInstanceOf(AuthRequiredError);
      const error = err as AuthRequiredError;
      expect(error.integration).toBe('github');
      expect(error.reason).toBe('not_connected');
      expect(error.connectUrl).toBe('http://localhost:3001/connect/org-1/github?agent=test_agent');
      expect(error.message).toContain('[RPC]');
      expect(error.message).toContain('Integration connection required');
      expect(error.code).toBe(-32000);
      expect(error.rpcMessage).toBe('Integration connection required');
      expect(error.data).toMatchObject({ code: 'AuthRequired' });
    }
  });
});
