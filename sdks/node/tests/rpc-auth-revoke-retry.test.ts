/**
 * Regression: an embedded-auth refresh-token rotation race surfaces as a
 * JSON-RPC application error with `data.code === 'EMBEDDED_JWT_REVOKED'` (the
 * transport `code` is a generic JSON-RPC code, NOT 401). callRpc must treat
 * that like a 401 — refresh via the auth provider and retry once — so the
 * stale-token race self-heals instead of surfacing to the caller.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';

import { callRpc, RpcError } from '../src/runtime/rpc-client.js';

const CONFIG = { subscribeKey: 'sub-test', baseUrl: 'https://blocks.test' };

function rpcError(dataCode: string) {
  return JSON.stringify({
    jsonrpc: '2.0',
    id: 'x',
    error: {
      code: -32000,
      message: 'Embedded refresh token revoked or expired',
      data: { code: dataCode },
    },
  });
}

function rpcOk(result: unknown) {
  return JSON.stringify({ jsonrpc: '2.0', id: 'x', result });
}

function jsonResp(body: string) {
  return new Response(body, {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

afterEach(() => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).fetch = undefined;
  vi.restoreAllMocks();
});

describe('callRpc reactive refresh on embedded-auth revoke', () => {
  it('EMBEDDED_JWT_REVOKED → refresh + retry once → succeeds', async () => {
    const onAuthFailure = vi.fn().mockResolvedValue(true);
    const provider = {
      getAuthHeader: () => 'Bearer jwt-1',
      onAuthFailure,
    };

    let call = 0;
    const fetchMock = vi.fn(async () => {
      call += 1;
      return call === 1
        ? jsonResp(rpcError('EMBEDDED_JWT_REVOKED')) // raced the rotation
        : jsonResp(rpcOk({ ok: true })); // retry with rotated token
    });
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (globalThis as any).fetch = fetchMock;

    const result = await callRpc<{ ok: boolean }>(
      { ...CONFIG, authProvider: provider },
      'submitTask',
      {},
    );

    expect(result).toEqual({ ok: true });
    expect(onAuthFailure).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it.each([
    'EMBEDDED_JWT_LOGOUT',
    'EMBEDDED_JWT_KILLED',
    'EMBEDDED_JWT_SCOPE_DRIFT',
    'AGENT_OUT_OF_SCOPE',
  ])(
    '%s → refresh + retry once (liveness revoke recovers like _REVOKED)',
    async (dataCode) => {
      // These embedded-JWT liveness codes are raised in auth middleware
      // before the task runs and are recoverable by a refresh: LOGOUT/
      // REVOKED 401 the refresh (clearing the dead session immediately),
      // while KILLED/SCOPE_DRIFT re-mint a narrowed token the retry can
      // use. Without them in the retry set the dead session lingered
      // until JWT expiry (~60s).
      const onAuthFailure = vi.fn().mockResolvedValue(true);
      const provider = {
        getAuthHeader: () => 'Bearer jwt-1',
        onAuthFailure,
      };
      let call = 0;
      const fetchMock = vi.fn(async () => {
        call += 1;
        return call === 1
          ? jsonResp(rpcError(dataCode))
          : jsonResp(rpcOk({ ok: true }));
      });
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (globalThis as any).fetch = fetchMock;

      const result = await callRpc<{ ok: boolean }>(
        { ...CONFIG, authProvider: provider },
        'submitTask',
        {},
      );

      expect(result).toEqual({ ok: true });
      expect(onAuthFailure).toHaveBeenCalledTimes(1);
      expect(fetchMock).toHaveBeenCalledTimes(2);
    },
  );

  it('non-auth RPC error → no refresh, error propagates', async () => {
    const onAuthFailure = vi.fn().mockResolvedValue(true);
    const provider = {
      getAuthHeader: () => 'Bearer jwt-1',
      onAuthFailure,
    };

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (globalThis as any).fetch = vi.fn(async () =>
      jsonResp(rpcError('SOME_OTHER_ERROR')),
    );

    await expect(
      callRpc({ ...CONFIG, authProvider: provider }, 'submitTask', {}),
    ).rejects.toBeInstanceOf(RpcError);
    expect(onAuthFailure).not.toHaveBeenCalled();
  });

  it('revoke but refresh fails (onAuthFailure → false) → error propagates, no extra retry', async () => {
    const onAuthFailure = vi.fn().mockResolvedValue(false);
    const provider = {
      getAuthHeader: () => 'Bearer jwt-1',
      onAuthFailure,
    };

    const fetchMock = vi.fn(async () =>
      jsonResp(rpcError('EMBEDDED_JWT_REVOKED')),
    );
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (globalThis as any).fetch = fetchMock;

    await expect(
      callRpc({ ...CONFIG, authProvider: provider }, 'submitTask', {}),
    ).rejects.toBeInstanceOf(RpcError);
    expect(onAuthFailure).toHaveBeenCalledTimes(1);
  });
});
