import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  withRetry,
  rpcEndpoint,
  callRpc,
  RpcError,
  BillingModeMismatchError,
  type RpcClientConfig,
} from '../src/runtime/rpc-client.js';
import { StaticAuthProvider } from '../src/runtime/auth-provider.js';

describe('rpc-client', () => {
  // ==========================================================================
  // rpcEndpoint
  // ==========================================================================

  describe('rpcEndpoint', () => {
    it('falls back to PubNub Functions gateway when baseUrl is not provided', () => {
      expect(rpcEndpoint('sub-c-abc123')).toBe(
        'https://ps.pndsn.com/v1/blocks/sub-key/sub-c-abc123/rpc',
      );
    });

    it('builds the correct URL from a root baseUrl', () => {
      expect(rpcEndpoint('sub-c-abc123', 'http://localhost:3001')).toBe(
        'http://localhost:3001/api/v1/rpc',
      );
    });

    it('uses baseUrl when provided', () => {
      expect(rpcEndpoint('sub-c-abc123', 'http://localhost:8080')).toBe(
        'http://localhost:8080/api/v1/rpc',
      );
    });

    it('strips trailing slash from baseUrl', () => {
      expect(rpcEndpoint('sub-c-abc123', 'http://localhost:8080/')).toBe(
        'http://localhost:8080/api/v1/rpc',
      );
    });
  });

  // ==========================================================================
  // RpcError
  // ==========================================================================

  describe('RpcError', () => {
    it('stores code, rpcMessage, and data', () => {
      const err = new RpcError('Something broke', -32000, { code: 'InvalidArgument' });
      expect(err).toBeInstanceOf(Error);
      expect(err.name).toBe('RpcError');
      expect(err.message).toBe('[RPC] Something broke');
      expect(err.rpcMessage).toBe('Something broke');
      expect(err.code).toBe(-32000);
      expect(err.data).toEqual({ code: 'InvalidArgument' });
    });

    it('works without code and data', () => {
      const err = new RpcError('Oops');
      expect(err.code).toBeUndefined();
      expect(err.data).toBeUndefined();
      expect(err.rpcMessage).toBe('Oops');
    });
  });

  // ==========================================================================
  // withRetry
  // ==========================================================================

  describe('withRetry', () => {
    it('returns the value on first success', async () => {
      const fn = vi.fn().mockResolvedValueOnce('ok');
      const result = await withRetry(fn);
      expect(result).toBe('ok');
      expect(fn).toHaveBeenCalledTimes(1);
    });

    it('retries on transient ENOTFOUND errors', async () => {
      const fn = vi
        .fn()
        .mockRejectedValueOnce(new Error('fetch failed: ENOTFOUND'))
        .mockResolvedValueOnce('recovered');

      const result = await withRetry(fn, 3, 1);
      expect(result).toBe('recovered');
      expect(fn).toHaveBeenCalledTimes(2);
    });

    it('retries on ETIMEDOUT errors', async () => {
      const fn = vi
        .fn()
        .mockRejectedValueOnce(new Error('ETIMEDOUT'))
        .mockResolvedValueOnce('ok');

      const result = await withRetry(fn, 3, 1);
      expect(result).toBe('ok');
      expect(fn).toHaveBeenCalledTimes(2);
    });

    it('does not retry on non-transient errors', async () => {
      const fn = vi.fn().mockRejectedValueOnce(new Error('Invalid argument'));
      await expect(withRetry(fn, 3, 1)).rejects.toThrow('Invalid argument');
      expect(fn).toHaveBeenCalledTimes(1);
    });

    it('throws after max retries on persistent transient errors', async () => {
      const fn = vi.fn().mockRejectedValue(new Error('ECONNRESET'));
      await expect(withRetry(fn, 3, 1)).rejects.toThrow('ECONNRESET');
      expect(fn).toHaveBeenCalledTimes(3);
    });
  });

  // ==========================================================================
  // callRpc
  // ==========================================================================

  describe('callRpc', () => {
    let fetchSpy: ReturnType<typeof vi.fn>;
    const originalFetch = globalThis.fetch;
    const config: RpcClientConfig = { subscribeKey: 'sub-c-test-key', baseUrl: 'http://localhost:3001' };

    beforeEach(() => {
      fetchSpy = vi.fn();
      globalThis.fetch = fetchSpy;
    });

    afterEach(() => {
      globalThis.fetch = originalFetch;
    });

    it('falls back to PubNub Functions gateway when baseUrl is missing from config', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({ jsonrpc: '2.0', id: 'x', result: { ok: true } }),
      });
      const noBaseUrlConfig: RpcClientConfig = { subscribeKey: 'sub-c-test-key' };
      await callRpc(noBaseUrlConfig, 'Method', {});
      const [url] = fetchSpy.mock.calls[0];
      expect(url).toBe('https://ps.pndsn.com/v1/blocks/sub-key/sub-c-test-key/rpc');
    });

    it('sends correct JSON-RPC 2.0 envelope', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({ jsonrpc: '2.0', id: 'x', result: { foo: 'bar' } }),
      });

      const result = await callRpc<{ foo: string }>(config, 'MyMethod', { key: 'value' });

      expect(result).toEqual({ foo: 'bar' });
      expect(fetchSpy).toHaveBeenCalledTimes(1);

      const [url, init] = fetchSpy.mock.calls[0];
      expect(url).toBe('http://localhost:3001/api/v1/rpc');
      expect(init.method).toBe('POST');
      expect(init.headers['Content-Type']).toBe('application/json');

      const body = JSON.parse(init.body);
      expect(body.jsonrpc).toBe('2.0');
      expect(body.method).toBe('MyMethod');
      expect(body.params).toEqual({ key: 'value' });
      expect(body.id).toMatch(/^rpc-/);
    });

    it('includes Authorization header when authProvider is provided', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({ jsonrpc: '2.0', id: 'x', result: {} }),
      });

      const configWithAuth: RpcClientConfig = {
        subscribeKey: 'sub-c-test-key',
        authProvider: new StaticAuthProvider('my-jwt-token'),
        baseUrl: 'http://localhost:3001',
      };
      await callRpc(configWithAuth, 'Method', {});

      const [, init] = fetchSpy.mock.calls[0];
      expect(init.headers['Authorization']).toBe('Bearer my-jwt-token');
    });

    it('does not include Authorization header when authProvider is absent', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({ jsonrpc: '2.0', id: 'x', result: {} }),
      });

      await callRpc(config, 'Method', {});

      const [, init] = fetchSpy.mock.calls[0];
      expect(init.headers['Authorization']).toBeUndefined();
    });

    it('throws RpcError on HTTP error status', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 500,
      });

      try {
        await callRpc(config, 'Method', {});
        expect.unreachable('should have thrown');
      } catch (e) {
        expect(e).toBeInstanceOf(RpcError);
        expect((e as RpcError).code).toBe(500);
        expect((e as RpcError).rpcMessage).toBe('HTTP 500');
      }
    });

    it('throws RpcError with data.message on JSON-RPC error', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          jsonrpc: '2.0',
          id: 'x',
          error: {
            code: -32000,
            message: 'A2A Error',
            data: { code: 'InvalidArgument', message: 'agentName is required' },
          },
        }),
      });

      await expect(callRpc(config, 'Method', {})).rejects.toThrow('agentName is required');
    });

    it('falls back to error.message when data.message is absent', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          jsonrpc: '2.0',
          id: 'x',
          error: { code: -32601, message: 'Method not found' },
        }),
      });

      await expect(callRpc(config, 'Method', {})).rejects.toThrow('Method not found');
    });

    it('retries on transient network errors', async () => {
      fetchSpy
        .mockRejectedValueOnce(new Error('ENOTFOUND'))
        .mockResolvedValueOnce({
          ok: true,
          json: async () => ({ jsonrpc: '2.0', id: 'x', result: 'ok' }),
        });

      const result = await callRpc<string>(config, 'Method', {});
      expect(result).toBe('ok');
      expect(fetchSpy).toHaveBeenCalledTimes(2);
    });

    it('uses custom baseUrl from config', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({ jsonrpc: '2.0', id: 'x', result: { ok: true } }),
      });

      const customConfig: RpcClientConfig = {
        subscribeKey: 'sub-c-test-key',
        baseUrl: 'http://localhost:8080',
      };
      await callRpc(customConfig, 'Method', {});

      const [url] = fetchSpy.mock.calls[0];
      expect(url).toBe('http://localhost:8080/api/v1/rpc');
    });

    // ========================================================================
    // BillingModeMismatch — typed error mapping
    // ========================================================================

    /**
     * Backend wire shape (Phase 1 `bmc-data` IMPL_REPORT):
     *   {
     *     "jsonrpc": "2.0",
     *     "error": {
     *       "code": -32000,
     *       "message": "Billing mode mismatch: ...",
     *       "data": {
     *         "code": "BillingModeMismatch",
     *         "details": { "expected": "free" | "paid", "got": "free" | "paid" }
     *       }
     *     }
     *   }
     */
    const billingMismatchEnvelope = (expected: 'free' | 'paid', got: 'free' | 'paid') => ({
      jsonrpc: '2.0',
      id: 'x',
      error: {
        code: -32000,
        message: `Billing mode mismatch: caller declared '${got}', agent is '${expected}'`,
        data: {
          code: 'BillingModeMismatch',
          details: { expected, got },
        },
      },
    });

    it('maps RPC code=BillingModeMismatch into a typed BillingModeMismatchError', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => billingMismatchEnvelope('paid', 'free'),
      });

      try {
        await callRpc(config, 'SendMessage', {});
        expect.unreachable('should have thrown');
      } catch (e) {
        expect(e).toBeInstanceOf(BillingModeMismatchError);
        expect(e).toBeInstanceOf(RpcError); // parity-with-Python: extends RpcError
        const err = e as BillingModeMismatchError;
        expect(err.expected).toBe('paid');
        expect(err.got).toBe('free');
        // Generic RpcError fields are still populated.
        expect(err.code).toBe(-32000);
        expect(err.rpcMessage).toBe(
          "Billing mode mismatch: caller declared 'free', agent is 'paid'",
        );
        // The raw structured `data` envelope is also reachable for debugging.
        expect(err.data).toEqual({
          code: 'BillingModeMismatch',
          details: { expected: 'paid', got: 'free' },
        });
      }
    });

    it('also maps the inverse direction (caller=paid, agent=free) into a typed error', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => billingMismatchEnvelope('free', 'paid'),
      });

      try {
        await callRpc(config, 'SendMessage', {});
        expect.unreachable('should have thrown');
      } catch (e) {
        expect(e).toBeInstanceOf(BillingModeMismatchError);
        const err = e as BillingModeMismatchError;
        expect(err.expected).toBe('free');
        expect(err.got).toBe('paid');
      }
    });

    it('does NOT auto-retry on BillingModeMismatch — exactly one fetch call', async () => {
      // BMC §6: SDK MUST NOT auto-retry or auto-correct the caller's
      // billing mode. Surface the typed error so the caller fixes their
      // code.
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => billingMismatchEnvelope('paid', 'free'),
      });

      await expect(callRpc(config, 'SendMessage', {})).rejects.toBeInstanceOf(
        BillingModeMismatchError,
      );
      expect(fetchSpy).toHaveBeenCalledTimes(1);
    });

    it('non-BillingModeMismatch JSON-RPC errors still surface as plain RpcError (not the subclass)', async () => {
      // Guard rail: only the structured BillingModeMismatch wire shape
      // upgrades to BillingModeMismatchError. Other RPC errors must NOT
      // accidentally land in this subclass.
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          jsonrpc: '2.0',
          id: 'x',
          error: {
            code: -32000,
            message: 'A2A Error',
            data: { code: 'InvalidArgument', message: 'agentName is required' },
          },
        }),
      });

      try {
        await callRpc(config, 'SendMessage', {});
        expect.unreachable('should have thrown');
      } catch (e) {
        expect(e).toBeInstanceOf(RpcError);
        expect(e).not.toBeInstanceOf(BillingModeMismatchError);
      }
    });

    it('malformed BillingModeMismatch payload (missing details) falls back to plain RpcError', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          jsonrpc: '2.0',
          id: 'x',
          error: {
            code: -32000,
            message: 'Billing mode mismatch',
            data: { code: 'BillingModeMismatch' /* details intentionally absent */ },
          },
        }),
      });

      try {
        await callRpc(config, 'SendMessage', {});
        expect.unreachable('should have thrown');
      } catch (e) {
        expect(e).toBeInstanceOf(RpcError);
        expect(e).not.toBeInstanceOf(BillingModeMismatchError);
      }
    });

    it('malformed BillingModeMismatch payload (invalid expected/got values) falls back to plain RpcError', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          jsonrpc: '2.0',
          id: 'x',
          error: {
            code: -32000,
            message: 'Billing mode mismatch',
            data: {
              code: 'BillingModeMismatch',
              details: { expected: 'something_else', got: 'paid' },
            },
          },
        }),
      });

      try {
        await callRpc(config, 'SendMessage', {});
        expect.unreachable('should have thrown');
      } catch (e) {
        expect(e).toBeInstanceOf(RpcError);
        expect(e).not.toBeInstanceOf(BillingModeMismatchError);
      }
    });
  });

  // ==========================================================================
  // BillingModeMismatchError — direct constructor + wire-shape parity
  // ==========================================================================

  describe('BillingModeMismatchError', () => {
    it('extends RpcError so existing instanceof RpcError checks still match', () => {
      const err = new BillingModeMismatchError('msg', 'paid', 'free', -32000, {
        code: 'BillingModeMismatch',
        details: { expected: 'paid', got: 'free' },
      });
      expect(err).toBeInstanceOf(RpcError);
      expect(err).toBeInstanceOf(Error);
      expect(err.name).toBe('BillingModeMismatchError');
    });

    it('exposes expected/got + structured data passed through', () => {
      const data = {
        code: 'BillingModeMismatch',
        details: { expected: 'free', got: 'paid' },
      };
      const err = new BillingModeMismatchError('msg', 'free', 'paid', -32000, data);
      expect(err.expected).toBe('free');
      expect(err.got).toBe('paid');
      expect(err.code).toBe(-32000);
      expect(err.data).toBe(data);
    });
  });
});
