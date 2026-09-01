/**
 * Tests for ConsumerAuth -- all 3 modes, proactive refresh, reactive refresh,
 * concurrent 401, retry with backoff, permanent failure, destroy.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { ConsumerAuth, AuthRefreshFailedError, type TokenResult } from '../src/runtime/consumer-auth.js';

describe('ConsumerAuth', () => {
  const originalFetch = globalThis.fetch;
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
    vi.useFakeTimers();
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.useRealTimers();
  });

  // ==========================================================================
  // Mode 1: API key
  // ==========================================================================

  describe('Mode 1: API key', () => {
    it('calls consumer-token endpoint on init()', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          accessToken: 'jwt-123',
          refreshToken: 'rt-456',
          expiresIn: 60,
          userId: 'user-1',
        }),
      });

      const auth = new ConsumerAuth({
        apiKey: 'bk_test_key',
        baseUrl: 'http://localhost:3001',
      });
      await auth.init();

      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [url, init] = fetchSpy.mock.calls[0];
      expect(url).toBe('http://localhost:3001/api/v1/auth/agent/consumer-token');
      expect(init.method).toBe('POST');
      expect(JSON.parse(init.body)).toEqual({ apiKey: 'bk_test_key' });
      expect(auth.getAuthHeader()).toBe('Bearer jwt-123');
      expect(auth.getUserId()).toBe('user-1');
    });

    it('throws on init failure', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 401,
        text: async () => 'Unauthorized',
      });

      const auth = new ConsumerAuth({
        apiKey: 'bk_bad_key',
        baseUrl: 'http://localhost:3001',
      });

      await expect(auth.init()).rejects.toThrow('consumer-token failed: HTTP 401');
    });

    it('uses refresh endpoint for proactive refresh', async () => {
      // init
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          accessToken: 'jwt-1',
          refreshToken: 'rt-1',
          expiresIn: 100,
          userId: 'user-1',
        }),
      });

      const auth = new ConsumerAuth({
        apiKey: 'bk_key',
        baseUrl: 'http://localhost:3001',
      });
      await auth.init();
      expect(auth.getAuthHeader()).toBe('Bearer jwt-1');

      // Mock refresh endpoint
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          accessToken: 'jwt-2',
          refreshToken: 'rt-2',
        }),
      });

      // Advance to 80% of expiresIn (80s)
      await vi.advanceTimersByTimeAsync(80_000);

      expect(fetchSpy).toHaveBeenCalledTimes(2);
      const [url, init] = fetchSpy.mock.calls[1];
      expect(url).toBe('http://localhost:3001/api/v1/auth/agent/refresh');
      expect(init.headers['Authorization']).toBe('Bearer bk_key');
      expect(JSON.parse(init.body)).toEqual({ refreshToken: 'rt-1' });
      expect(auth.getAuthHeader()).toBe('Bearer jwt-2');

      auth.destroy();
    });

    it('falls back to consumer-token bootstrap when refresh token is invalid', async () => {
      fetchSpy
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({
            accessToken: 'jwt-1',
            refreshToken: 'rt-1',
            expiresIn: 60,
            userId: 'user-1',
          }),
        })
        .mockResolvedValueOnce({
          ok: false,
          status: 401,
          json: async () => ({
            error: 'Refresh token invalid or expired',
            code: 'REFRESH_TOKEN_INVALID',
          }),
        })
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({
            accessToken: 'jwt-2',
            refreshToken: 'rt-2',
            expiresIn: 60,
            userId: 'user-1',
          }),
        });

      const auth = new ConsumerAuth({
        apiKey: 'bk_key',
        baseUrl: 'http://localhost:3001',
      });
      await auth.init();

      const refreshed = await auth.onAuthFailure();
      expect(refreshed).toBe(true);
      expect(auth.getAuthHeader()).toBe('Bearer jwt-2');

      expect(fetchSpy).toHaveBeenCalledTimes(3);
      expect(fetchSpy.mock.calls[1][0]).toBe('http://localhost:3001/api/v1/auth/agent/refresh');
      expect(fetchSpy.mock.calls[2][0]).toBe('http://localhost:3001/api/v1/auth/agent/consumer-token');

      auth.destroy();
    });

    it('sends protocol version header on init', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          accessToken: 'jwt-1',
          refreshToken: 'rt-1',
          expiresIn: 60,
          userId: 'user-1',
        }),
      });

      const auth = new ConsumerAuth({
        apiKey: 'bk_key',
        baseUrl: 'http://localhost:3001',
      });
      await auth.init();

      const [, init] = fetchSpy.mock.calls[0];
      expect(init.headers['Blocks-Protocol-Version']).toBeDefined();

      auth.destroy();
    });
  });

  // ==========================================================================
  // Mode 2: Token endpoint
  // ==========================================================================

  describe('Mode 2: Token endpoint', () => {
    it('POSTs to endpoint URL with empty JSON body', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          token: 'endpoint-jwt',
          expiresIn: 120,
          userId: 'user-proxy',
        }),
      });

      const auth = new ConsumerAuth({
        tokenEndpoint: 'https://my-app.com/api/blocks-token',
      });
      await auth.init();

      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [url, init] = fetchSpy.mock.calls[0];
      expect(url).toBe('https://my-app.com/api/blocks-token');
      expect(init.method).toBe('POST');
      expect(JSON.parse(init.body)).toEqual({});
      expect(auth.getAuthHeader()).toBe('Bearer endpoint-jwt');
      expect(auth.getUserId()).toBe('user-proxy');

      auth.destroy();
    });

    it('refreshes by calling the same endpoint again', async () => {
      // init — includes userId
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ token: 'jwt-a', expiresIn: 50, userId: 'proxy-user' }),
      });

      const auth = new ConsumerAuth({
        tokenEndpoint: 'https://proxy.example.com/token',
      });
      await auth.init();
      expect(auth.getUserId()).toBe('proxy-user');

      // refresh (80% of 50s = 40s) — omits userId, must preserve original
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ token: 'jwt-b', expiresIn: 50 }),
      });

      await vi.advanceTimersByTimeAsync(40_000);

      expect(auth.getAuthHeader()).toBe('Bearer jwt-b');
      expect(auth.getUserId()).toBe('proxy-user'); // preserved, not cleared
      expect(fetchSpy).toHaveBeenCalledTimes(2);

      auth.destroy();
    });

    // ----------------------------------------------------------------------
    // Config-object form (widened tokenEndpoint)
    // ----------------------------------------------------------------------

    describe('config object form', () => {
      it('accepts config object with credentials and plumbs into fetch init', async () => {
        fetchSpy.mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({ token: 'cookie-jwt', expiresIn: 60 }),
        });

        const auth = new ConsumerAuth({
          tokenEndpoint: {
            url: '/api/blocks-token',
            credentials: 'include',
          },
        });
        await auth.init();

        const [url, init] = fetchSpy.mock.calls[0];
        expect(url).toBe('/api/blocks-token');
        expect(init.method).toBe('POST');
        expect(init.credentials).toBe('include');
        expect(init.headers['Content-Type']).toBe('application/json');
        expect(JSON.parse(init.body)).toEqual({});

        auth.destroy();
      });

      it('merges custom headers on top of Content-Type default', async () => {
        fetchSpy.mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({ token: 'csrf-jwt', expiresIn: 60 }),
        });

        const auth = new ConsumerAuth({
          tokenEndpoint: {
            url: 'https://proxy.example.com/token',
            headers: {
              'X-CSRF-Token': 'csrf-abc',
              'X-Session-Id': 'sess-123',
            },
          },
        });
        await auth.init();

        const [, init] = fetchSpy.mock.calls[0];
        expect(init.headers['Content-Type']).toBe('application/json');
        expect(init.headers['X-CSRF-Token']).toBe('csrf-abc');
        expect(init.headers['X-Session-Id']).toBe('sess-123');

        auth.destroy();
      });

      it('user-supplied Content-Type header overrides SDK default', async () => {
        fetchSpy.mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({ token: 'x', expiresIn: 60 }),
        });

        const auth = new ConsumerAuth({
          tokenEndpoint: {
            url: 'https://proxy.example.com/token',
            headers: { 'Content-Type': 'application/vnd.custom+json' },
          },
        });
        await auth.init();

        const [, init] = fetchSpy.mock.calls[0];
        expect(init.headers['Content-Type']).toBe('application/vnd.custom+json');

        auth.destroy();
      });

      it('replaces default empty body when body is provided', async () => {
        fetchSpy.mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({ token: 'body-jwt', expiresIn: 60 }),
        });

        const auth = new ConsumerAuth({
          tokenEndpoint: {
            url: 'https://proxy.example.com/token',
            body: { sessionId: 'sess-42', clientId: 'c-1' },
          },
        });
        await auth.init();

        const [, init] = fetchSpy.mock.calls[0];
        expect(JSON.parse(init.body)).toEqual({ sessionId: 'sess-42', clientId: 'c-1' });

        auth.destroy();
      });

      it('reuses config on refresh path (credentials, headers, body all persisted)', async () => {
        // init
        fetchSpy.mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({ token: 'init-jwt', expiresIn: 50 }),
        });

        const auth = new ConsumerAuth({
          tokenEndpoint: {
            url: 'https://proxy.example.com/token',
            credentials: 'include',
            headers: { 'X-CSRF-Token': 'csrf-xyz' },
            body: { scope: 'task:read' },
          },
        });
        await auth.init();

        // refresh (80% of 50s = 40s)
        fetchSpy.mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({ token: 'refresh-jwt', expiresIn: 50 }),
        });

        await vi.advanceTimersByTimeAsync(40_000);

        expect(fetchSpy).toHaveBeenCalledTimes(2);
        const [refreshUrl, refreshInit] = fetchSpy.mock.calls[1];
        expect(refreshUrl).toBe('https://proxy.example.com/token');
        expect(refreshInit.credentials).toBe('include');
        expect(refreshInit.headers['X-CSRF-Token']).toBe('csrf-xyz');
        expect(JSON.parse(refreshInit.body)).toEqual({ scope: 'task:read' });

        auth.destroy();
      });

      it('omits credentials from fetch init when not supplied (string form path)', async () => {
        fetchSpy.mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({ token: 'x', expiresIn: 60 }),
        });

        const auth = new ConsumerAuth({
          tokenEndpoint: 'https://proxy.example.com/token',
        });
        await auth.init();

        const [, init] = fetchSpy.mock.calls[0];
        // When credentials isn't supplied, the SDK must NOT set the key
        // on fetch's RequestInit (browser uses 'same-origin' default).
        expect(init.credentials).toBeUndefined();

        auth.destroy();
      });

      it('throws a descriptive error when url is missing from the config object', async () => {
        const auth = new ConsumerAuth({
          // JS caller bypasses TypeScript and omits url
          tokenEndpoint: { headers: { 'X-CSRF-Token': 'abc' } } as unknown as {
            url: string;
          },
        });

        await expect(auth.init()).rejects.toThrow(
          'TokenEndpointConfig.url is required and must be a non-empty string',
        );
        // Must have failed BEFORE any fetch attempt.
        expect(fetchSpy).not.toHaveBeenCalled();

        auth.destroy();
      });

      it('throws a descriptive error when url is an empty string', async () => {
        const auth = new ConsumerAuth({
          tokenEndpoint: { url: '' },
        });

        await expect(auth.init()).rejects.toThrow(
          'TokenEndpointConfig.url is required and must be a non-empty string',
        );
        expect(fetchSpy).not.toHaveBeenCalled();

        auth.destroy();
      });
    });
  });

  // ==========================================================================
  // Mode 3: Custom function
  // ==========================================================================

  describe('Mode 3: Custom function', () => {
    it('calls tokenProvider on init()', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>().mockResolvedValue({
        token: 'custom-jwt',
        expiresIn: 300,
        userId: 'custom-user',
      });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      expect(provider).toHaveBeenCalledTimes(1);
      expect(auth.getAuthHeader()).toBe('Bearer custom-jwt');
      expect(auth.getUserId()).toBe('custom-user');

      auth.destroy();
    });

    it('calls tokenProvider again on refresh', async () => {
      let callCount = 0;
      const provider = vi.fn<() => Promise<TokenResult>>().mockImplementation(async () => {
        callCount++;
        return { token: `jwt-${callCount}`, expiresIn: 100 };
      });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();
      expect(auth.getAuthHeader()).toBe('Bearer jwt-1');

      // Advance to 80% of 100s = 80s
      await vi.advanceTimersByTimeAsync(80_000);

      expect(provider).toHaveBeenCalledTimes(2);
      expect(auth.getAuthHeader()).toBe('Bearer jwt-2');

      auth.destroy();
    });
  });

  // ==========================================================================
  // Validation
  // ==========================================================================

  describe('validation', () => {
    it('throws when no mode is specified', async () => {
      const auth = new ConsumerAuth({});
      await expect(auth.init()).rejects.toThrow(
        'ConsumerAuth requires one of: apiKey, tokenEndpoint, or tokenProvider',
      );
    });
  });

  // ==========================================================================
  // Reactive refresh (onAuthFailure)
  // ==========================================================================

  describe('onAuthFailure()', () => {
    it('triggers immediate refresh and returns true on success', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-old', expiresIn: 300 })
        .mockResolvedValueOnce({ token: 'jwt-new', expiresIn: 300 });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();
      expect(auth.getAuthHeader()).toBe('Bearer jwt-old');

      const result = await auth.onAuthFailure();
      expect(result).toBe(true);
      expect(auth.getAuthHeader()).toBe('Bearer jwt-new');

      auth.destroy();
    });

    it('returns false on refresh failure', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-ok', expiresIn: 300 })
        .mockRejectedValueOnce(new Error('network error'));

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      const result = await auth.onAuthFailure();
      expect(result).toBe(false);
      // Token should remain the old one
      expect(auth.getAuthHeader()).toBe('Bearer jwt-ok');

      auth.destroy();
    });

    // A failed reactive refresh used to log and return a bare `false`, which is
    // indistinguishable from a provider that has no refresh capability at all —
    // StaticAuthProvider returns the same thing without attempting anything. Any
    // caller reading `getLastAuthError()` therefore saw a healthy provider during
    // a live auth outage, and the registry card lookup reported it as "no such
    // agent". The state is what `getLastAuthError` documents itself as holding.
    it('records the failure so a reactive outage is not silent', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-1', expiresIn: 300 })
        .mockRejectedValueOnce(new Error('token endpoint unreachable'));

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();
      expect(auth.getLastAuthError()).toBeNull();

      const refreshed = await auth.onAuthFailure();

      expect(refreshed).toBe(false);
      const err = auth.getLastAuthError();
      expect(err).toBeInstanceOf(AuthRefreshFailedError);
      expect((err?.cause as Error).message).toBe('token endpoint unreachable');

      auth.destroy();
    });

    it('clears the recorded failure once a later reactive refresh succeeds', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-1', expiresIn: 300 })
        .mockRejectedValueOnce(new Error('transient'))
        .mockResolvedValueOnce({ token: 'jwt-2', expiresIn: 300 });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      await auth.onAuthFailure();
      expect(auth.getLastAuthError()).not.toBeNull();

      // Recovery must clear it, or one transient outage would wedge the client
      // for its lifetime via the fail-fast preflight.
      await auth.onAuthFailure();
      expect(auth.getLastAuthError()).toBeNull();
      expect(auth.getAuthHeader()).toBe('Bearer jwt-2');

      auth.destroy();
    });

    it('does not record when the provider was already destroyed', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-1', expiresIn: 300 });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();
      auth.destroy();

      // Teardown is not an auth failure — no refresh is attempted, so there is
      // nothing to report and a shutting-down client must not start raising.
      expect(await auth.onAuthFailure()).toBe(false);
      expect(auth.getLastAuthError()).toBeNull();
    });

    it('returns false after destroy', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-1', expiresIn: 300 });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();
      auth.destroy();

      const result = await auth.onAuthFailure();
      expect(result).toBe(false);
    });

    it('deduplicates concurrent refresh calls (only one runs)', async () => {
      let resolveRefresh: ((r: TokenResult) => void) | undefined;
      let callCount = 0;
      const provider = vi.fn<() => Promise<TokenResult>>().mockImplementation(() => {
        callCount++;
        if (callCount === 1) {
          return Promise.resolve({ token: 'jwt-init', expiresIn: 300 });
        }
        return new Promise<TokenResult>((resolve) => {
          resolveRefresh = resolve;
        });
      });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      // Trigger two concurrent onAuthFailure calls
      const p1 = auth.onAuthFailure();
      const p2 = auth.onAuthFailure();

      // Only one refresh should be in-flight
      expect(provider).toHaveBeenCalledTimes(2); // init + one refresh

      // Resolve the refresh
      resolveRefresh!({ token: 'jwt-refreshed', expiresIn: 300 });

      const [r1, r2] = await Promise.all([p1, p2]);
      expect(r1).toBe(true);
      expect(r2).toBe(true);
      expect(auth.getAuthHeader()).toBe('Bearer jwt-refreshed');

      auth.destroy();
    });
  });

  // ==========================================================================
  // Proactive refresh with backoff
  // ==========================================================================

  describe('proactive refresh retry', () => {
    it('retries with exponential backoff on failure', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-init', expiresIn: 100 })
        .mockRejectedValueOnce(new Error('fail 1'))
        .mockRejectedValueOnce(new Error('fail 2'))
        .mockResolvedValueOnce({ token: 'jwt-recovered', expiresIn: 100 });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      // Advance to 80% of 100s = 80s (triggers first proactive refresh)
      await vi.advanceTimersByTimeAsync(80_000);
      expect(provider).toHaveBeenCalledTimes(2);

      // First backoff: 5000ms
      await vi.advanceTimersByTimeAsync(5_000);
      expect(provider).toHaveBeenCalledTimes(3);

      // Second backoff: 10000ms
      await vi.advanceTimersByTimeAsync(10_000);
      expect(provider).toHaveBeenCalledTimes(4);

      expect(auth.getAuthHeader()).toBe('Bearer jwt-recovered');

      auth.destroy();
    });

    it('calls onAuthError after max retries', async () => {
      const onAuthError = vi.fn();
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-init', expiresIn: 100 })
        .mockRejectedValueOnce(new Error('fail 1'))
        .mockRejectedValueOnce(new Error('fail 2'))
        .mockRejectedValueOnce(new Error('fail 3'));

      const auth = new ConsumerAuth({
        tokenProvider: provider,
        onAuthError,
      });
      await auth.init();

      // Trigger proactive refresh at 80s
      await vi.advanceTimersByTimeAsync(80_000);

      // First retry at 5s
      await vi.advanceTimersByTimeAsync(5_000);

      // Second retry at 10s
      await vi.advanceTimersByTimeAsync(10_000);

      expect(onAuthError).toHaveBeenCalledTimes(1);
      expect(onAuthError.mock.calls[0][0]).toBeInstanceOf(Error);
      expect(onAuthError.mock.calls[0][0].message).toBe('fail 3');

      auth.destroy();
    });

    it('logs a warn on permanent proactive failure even when onAuthError is registered', async () => {
      // the SDK contract promises a warn-level log "either way" —
      // registered callback or not. Without this, observability tooling
      // that scrapes the [ConsumerAuth] logger goes silent the moment a
      // consumer wires up onAuthError.
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const onAuthError = vi.fn();
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-init', expiresIn: 100 })
        .mockRejectedValueOnce(new Error('fail 1'))
        .mockRejectedValueOnce(new Error('fail 2'))
        .mockRejectedValueOnce(new Error('fail 3'));

      const auth = new ConsumerAuth({ tokenProvider: provider, onAuthError });
      await auth.init();

      await vi.advanceTimersByTimeAsync(80_000);
      await vi.advanceTimersByTimeAsync(5_000);
      await vi.advanceTimersByTimeAsync(10_000);

      expect(onAuthError).toHaveBeenCalledTimes(1);
      const warnEntries = warnSpy.mock.calls.filter((c) => c[0] === '[ConsumerAuth]');
      const permanentFailureEntry = warnEntries
        .map((c) => c[1] as Record<string, unknown>)
        .find((entry) => entry.message === 'proactive refresh permanently failed');
      expect(permanentFailureEntry).toBeDefined();
      expect(permanentFailureEntry?.event).toBe(
        'consumer_auth_proactive_refresh_failed',
      );
      expect(permanentFailureEntry?.error).toBe('fail 3');

      warnSpy.mockRestore();
      auth.destroy();
    });
  });

  // ==========================================================================
  // Last-auth-error state
  // ==========================================================================

  describe('lastAuthError state', () => {
    it('sets lastAuthError to AuthRefreshFailedError after 3 proactive retries exhaust', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-init', expiresIn: 100 })
        .mockRejectedValueOnce(new Error('fail 1'))
        .mockRejectedValueOnce(new Error('fail 2'))
        .mockRejectedValueOnce(new Error('fail 3'));

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      // No callback registered — without the fix this would be silent.
      expect(auth.getLastAuthError()).toBeNull();

      // Advance to proactive refresh + 3 retries (80s + 5s + 10s).
      await vi.advanceTimersByTimeAsync(80_000);
      await vi.advanceTimersByTimeAsync(5_000);
      await vi.advanceTimersByTimeAsync(10_000);

      const err = auth.getLastAuthError();
      expect(err).toBeInstanceOf(AuthRefreshFailedError);
      expect(err?.cause).toBeInstanceOf(Error);
      expect((err?.cause as Error).message).toBe('fail 3');

      auth.destroy();
    });

    it('logs a warn when reactive refresh fails (no callback registered)', async () => {
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-init', expiresIn: 100 })
        .mockRejectedValueOnce(new Error('reactive-boom'));

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      const refreshed = await auth.onAuthFailure();
      expect(refreshed).toBe(false);

      // Logger writes `[ConsumerAuth] { level: 'warn', message: 'reactive refresh failed', ... }`
      const warnEntries = warnSpy.mock.calls.filter((c) => c[0] === '[ConsumerAuth]');
      expect(warnEntries.length).toBeGreaterThan(0);
      const entry = warnEntries[warnEntries.length - 1][1] as Record<string, unknown>;
      expect(entry.message).toBe('reactive refresh failed');
      expect(entry.error).toBe('reactive-boom');

      warnSpy.mockRestore();
      auth.destroy();
    });

    it('clears lastAuthError on a successful reactive refresh', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-init', expiresIn: 100 })
        .mockRejectedValueOnce(new Error('fail 1'))
        .mockRejectedValueOnce(new Error('fail 2'))
        .mockRejectedValueOnce(new Error('fail 3'))
        // Reactive refresh recovers.
        .mockResolvedValueOnce({ token: 'jwt-recovered', expiresIn: 100 });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      await vi.advanceTimersByTimeAsync(80_000);
      await vi.advanceTimersByTimeAsync(5_000);
      await vi.advanceTimersByTimeAsync(10_000);
      expect(auth.getLastAuthError()).not.toBeNull();

      const refreshed = await auth.onAuthFailure();
      expect(refreshed).toBe(true);
      expect(auth.getLastAuthError()).toBeNull();
      expect(auth.getAuthHeader()).toBe('Bearer jwt-recovered');

      auth.destroy();
    });

    it('clears lastAuthError when API-key reactive refresh falls back to consumer-token re-bootstrap', async () => {
      // Init: succeed.
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          accessToken: 'jwt-init',
          refreshToken: 'rt-init',
          expiresIn: 100,
          userId: 'user-1',
        }),
      });

      const auth = new ConsumerAuth({
        apiKey: 'bk_key',
        baseUrl: 'http://localhost:3001',
      });
      await auth.init();

      // Simulate 3 failed proactive refreshes (refresh endpoint 5xx).
      const fail = {
        ok: false,
        status: 500,
        json: async () => ({ error: 'transient', code: 'INTERNAL' }),
      };
      fetchSpy
        .mockResolvedValueOnce(fail)
        .mockResolvedValueOnce(fail)
        .mockResolvedValueOnce(fail);

      await vi.advanceTimersByTimeAsync(80_000);
      await vi.advanceTimersByTimeAsync(5_000);
      await vi.advanceTimersByTimeAsync(10_000);

      expect(auth.getLastAuthError()).toBeInstanceOf(AuthRefreshFailedError);

      // Reactive refresh: refresh endpoint returns REFRESH_TOKEN_INVALID,
      // SDK falls back to /consumer-token re-bootstrap which succeeds.
      fetchSpy
        .mockResolvedValueOnce({
          ok: false,
          status: 401,
          json: async () => ({
            error: 'Refresh token invalid or expired',
            code: 'REFRESH_TOKEN_INVALID',
          }),
        })
        .mockResolvedValueOnce({
          ok: true,
          status: 200,
          json: async () => ({
            accessToken: 'jwt-recovered',
            refreshToken: 'rt-recovered',
            expiresIn: 100,
            userId: 'user-1',
          }),
        });

      const refreshed = await auth.onAuthFailure();
      expect(refreshed).toBe(true);
      expect(auth.getAuthHeader()).toBe('Bearer jwt-recovered');
      // Regression: the re-bootstrap path bypassed _applyTokenResult, so
      // _lastAuthError stayed set even after a successful recovery.
      expect(auth.getLastAuthError()).toBeNull();

      auth.destroy();
    });
  });

  // ==========================================================================
  // destroy()
  // ==========================================================================

  describe('destroy()', () => {
    it('cancels proactive refresh timer', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-1', expiresIn: 100 });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      auth.destroy();

      // Advance well past the refresh window
      await vi.advanceTimersByTimeAsync(200_000);

      // Should not have called provider again
      expect(provider).toHaveBeenCalledTimes(1);
    });

    it('token remains readable after destroy', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt-stale', expiresIn: 100 });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      auth.destroy();

      // Token is still readable
      expect(auth.getAuthHeader()).toBe('Bearer jwt-stale');
      expect(auth.getUserId()).toBeNull();
    });
  });

  // ==========================================================================
  // getAuthHeader()
  // ==========================================================================

  describe('getAuthHeader()', () => {
    it('returns null before init', () => {
      const auth = new ConsumerAuth({ tokenProvider: async () => ({ token: 't', expiresIn: 60 }) });
      expect(auth.getAuthHeader()).toBeNull();
    });
  });

  // ==========================================================================
  // getUserId()
  // ==========================================================================

  describe('getUserId()', () => {
    it('returns null when no userId in token result', async () => {
      const provider = vi.fn<() => Promise<TokenResult>>()
        .mockResolvedValueOnce({ token: 'jwt', expiresIn: 60 });

      const auth = new ConsumerAuth({ tokenProvider: provider });
      await auth.init();

      expect(auth.getUserId()).toBeNull();

      auth.destroy();
    });

    it('returns userId when present in token result', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          token: 'jwt',
          expiresIn: 60,
          userId: 'user-abc',
        }),
      });

      const auth = new ConsumerAuth({
        tokenEndpoint: 'http://proxy.example.com/token',
      });
      await auth.init();

      expect(auth.getUserId()).toBe('user-abc');

      auth.destroy();
    });
  });

  // ==========================================================================
  // ensureReady()
  // ==========================================================================

  describe('ensureReady()', () => {
    it('calls init() on first invocation', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          accessToken: 'jwt-lazy',
          refreshToken: 'rt-lazy',
          expiresIn: 60,
          userId: 'user-lazy',
        }),
      });

      const auth = new ConsumerAuth({
        apiKey: 'bk_lazy_key',
        baseUrl: 'http://localhost:3001',
      });

      expect(auth.getAuthHeader()).toBeNull();
      await auth.ensureReady();
      expect(auth.getAuthHeader()).toBe('Bearer jwt-lazy');
      expect(fetchSpy).toHaveBeenCalledTimes(1);
    });

    it('is a no-op on subsequent calls', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({
          accessToken: 'jwt-once',
          refreshToken: 'rt-once',
          expiresIn: 60,
          userId: 'user-once',
        }),
      });

      const auth = new ConsumerAuth({
        apiKey: 'bk_once_key',
        baseUrl: 'http://localhost:3001',
      });

      await auth.ensureReady();
      await auth.ensureReady();
      await auth.ensureReady();
      expect(fetchSpy).toHaveBeenCalledTimes(1);
    });

    it('deduplicates concurrent calls', async () => {
      let resolveInit: ((v: unknown) => void) | undefined;
      fetchSpy.mockReturnValueOnce(
        new Promise((resolve) => {
          resolveInit = resolve;
        }),
      );

      const auth = new ConsumerAuth({
        apiKey: 'bk_dedup_key',
        baseUrl: 'http://localhost:3001',
      });

      const p1 = auth.ensureReady();
      const p2 = auth.ensureReady();
      const p3 = auth.ensureReady();

      resolveInit!({
        ok: true,
        status: 200,
        json: async () => ({
          accessToken: 'jwt-dedup',
          refreshToken: 'rt-dedup',
          expiresIn: 60,
          userId: 'user-dedup',
        }),
      });

      await Promise.all([p1, p2, p3]);
      expect(fetchSpy).toHaveBeenCalledTimes(1);
      expect(auth.getAuthHeader()).toBe('Bearer jwt-dedup');
    });
  });
});

describe('AuthRefreshFailedError public export', () => {
  it('is re-exported from the package entrypoint', async () => {
    const mod = await import('../src/index.js');
    expect(mod.AuthRefreshFailedError).toBeDefined();
    const inst = new mod.AuthRefreshFailedError(new Error('boom'));
    expect(inst).toBeInstanceOf(Error);
    expect(inst.name).toBe('AuthRefreshFailedError');
    expect(inst.cause).toBeInstanceOf(Error);
    expect((inst.cause as Error).message).toBe('boom');
  });
});
