import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { AgentAuth, AgentAuthFatalError } from '../src/runtime/agent-auth.js';
import type { RegistrationPayload } from '../src/runtime/agent-auth.js';

const TEST_BASE_URL = 'http://localhost:3001';
const TEST_API_KEY = 'bk_test-api-key-123';

const TEST_REGISTRATION_PAYLOAD: RegistrationPayload = {
  agentName: 'test_agent',
  instanceId: 'AG-test_agent-abc123',
  deviceOs: 'linux',
  sdkLanguage: 'Node',
};

/** Helper to build a registration response with agent data + tokens */
function registrationResponse(overrides?: Record<string, unknown>) {
  return {
    agentName: 'test_agent',
    name: 'test_agent',
    accessToken: 'jwt-token-1',
    refreshToken: 'rt-token-1',
    expiresIn: 60,
    pamToken: 'pam-token-1',
    ...overrides,
  };
}

describe('AgentAuth', () => {
  let fetchSpy: ReturnType<typeof vi.fn>;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  // ==========================================================================
  // Constructor validation
  // ==========================================================================

  it('throws when apiKey is empty', () => {
    expect(() => new AgentAuth('', TEST_BASE_URL)).toThrow('apiKey is required');
  });

  it('throws when baseUrl is empty', () => {
    expect(() => new AgentAuth(TEST_API_KEY, '')).toThrow('baseUrl is required');
  });

  // ==========================================================================
  // init() — registers agent and obtains tokens
  // ==========================================================================

  describe('init()', () => {
    it('registers agent and obtains JWT and refresh token', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
      const result = await auth.init(TEST_REGISTRATION_PAYLOAD);

      expect(fetchSpy).toHaveBeenCalledTimes(1);
      const [url, init] = fetchSpy.mock.calls[0];
      expect(url).toBe(`${TEST_BASE_URL}/api/v1/auth/agent/connect`);
      expect(init.method).toBe('POST');
      expect(init.headers.Authorization).toBe(`Bearer ${TEST_API_KEY}`);
      expect(init.headers['Content-Type']).toBe('application/json');

      // Verify registration payload was sent
      const body = JSON.parse(init.body);
      expect(body.agentName).toBe('test_agent');
      expect(body.instanceId).toBe('AG-test_agent-abc123');

      expect(auth.getAccessToken()).toBe('jwt-token-1');
      expect(auth.getApiKey()).toBe(TEST_API_KEY);

      // Verify full response is returned
      expect(result.accessToken).toBe('jwt-token-1');
      expect(result.refreshToken).toBe('rt-token-1');
      expect(result.expiresIn).toBe(60);
      expect(result.pamToken).toBe('pam-token-1');
    });

    it('throws AgentAuthFatalError on registration failure', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 401,
        json: async () => ({ error: 'API key invalid, expired, or revoked' }),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);

      try {
        await auth.init(TEST_REGISTRATION_PAYLOAD);
        expect.unreachable('should have thrown');
      } catch (e) {
        expect(e).toBeInstanceOf(AgentAuthFatalError);
        expect((e as Error).message).toContain('Agent registration failed');
      }
    });

    it('strips trailing slash from baseUrl', async () => {
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, `${TEST_BASE_URL}/`);
      await auth.init(TEST_REGISTRATION_PAYLOAD);

      const [url] = fetchSpy.mock.calls[0];
      expect(url).toBe(`${TEST_BASE_URL}/api/v1/auth/agent/connect`);
    });

    it('stores registration payload for re-registration', async () => {
      // First init (registration)
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
      await auth.init(TEST_REGISTRATION_PAYLOAD);

      // Refresh fails with REFRESH_TOKEN_INVALID
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 401,
        json: async () => ({
          error: 'Refresh token invalid or expired',
          code: 'REFRESH_TOKEN_INVALID',
        }),
      });

      // Re-registration succeeds
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () =>
          registrationResponse({
            accessToken: 'jwt-re-registered',
            refreshToken: 'rt-re-registered',
          }),
      });

      await auth.refresh();

      // Verify re-registration used the same payload
      const [reRegUrl, reRegInit] = fetchSpy.mock.calls[2];
      expect(reRegUrl).toBe(`${TEST_BASE_URL}/api/v1/auth/agent/connect`);
      const reRegBody = JSON.parse(reRegInit.body);
      expect(reRegBody.agentName).toBe('test_agent');
      expect(reRegBody.instanceId).toBe('AG-test_agent-abc123');

      expect(auth.getAccessToken()).toBe('jwt-re-registered');
    });
  });

  // ==========================================================================
  // refresh() — updates tokens, mutex behavior
  // ==========================================================================

  describe('refresh()', () => {
    it('refreshes tokens successfully', async () => {
      // init (registration)
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
      await auth.init(TEST_REGISTRATION_PAYLOAD);

      // refresh
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          accessToken: 'jwt-2',
          refreshToken: 'rt-2',
          expiresIn: 60,
        }),
      });

      await auth.refresh();

      expect(auth.getAccessToken()).toBe('jwt-2');

      // Verify the refresh call was made correctly
      const [url, init] = fetchSpy.mock.calls[1];
      expect(url).toBe(`${TEST_BASE_URL}/api/v1/auth/agent/refresh`);
      expect(init.method).toBe('POST');
      expect(init.headers.Authorization).toBe(`Bearer ${TEST_API_KEY}`);
      const body = JSON.parse(init.body);
      expect(body.refreshToken).toBe('rt-token-1');
    });

    it('re-registers on REFRESH_TOKEN_INVALID', async () => {
      // init (registration)
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
      await auth.init(TEST_REGISTRATION_PAYLOAD);

      // refresh fails with REFRESH_TOKEN_INVALID
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 401,
        json: async () => ({
          error: 'Refresh token invalid or expired',
          code: 'REFRESH_TOKEN_INVALID',
        }),
      });

      // re-registration succeeds
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () =>
          registrationResponse({
            accessToken: 'jwt-3',
            refreshToken: 'rt-3',
          }),
      });

      await auth.refresh();

      expect(auth.getAccessToken()).toBe('jwt-3');
      expect(fetchSpy).toHaveBeenCalledTimes(3); // init + failed refresh + re-registration

      // Verify the re-registration called /auth/agent/connect (not /agent/token)
      const [reRegUrl] = fetchSpy.mock.calls[2];
      expect(reRegUrl).toBe(`${TEST_BASE_URL}/api/v1/auth/agent/connect`);
    });

    it('throws AgentAuthFatalError on API_KEY_INVALID', async () => {
      // init (registration)
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
      await auth.init(TEST_REGISTRATION_PAYLOAD);

      // refresh fails with API_KEY_INVALID
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 401,
        json: async () => ({
          error: 'API key invalid, expired, or revoked',
          code: 'API_KEY_INVALID',
        }),
      });

      await expect(auth.refresh()).rejects.toThrow(AgentAuthFatalError);
    });

    it('concurrent refresh calls share the same promise (mutex)', async () => {
      // init (registration)
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
      await auth.init(TEST_REGISTRATION_PAYLOAD);

      // Simulate slow refresh
      let resolveRefresh!: (value: unknown) => void;
      fetchSpy.mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveRefresh = resolve;
          }),
      );

      // Fire 3 concurrent refresh calls
      const p1 = auth.refresh();
      const p2 = auth.refresh();
      const p3 = auth.refresh();

      // Only one fetch call should have been made (init + 1 refresh)
      expect(fetchSpy).toHaveBeenCalledTimes(2);

      // Resolve the single refresh
      resolveRefresh({
        ok: true,
        json: async () => ({
          accessToken: 'jwt-shared',
          refreshToken: 'rt-shared',
          expiresIn: 60,
        }),
      });

      await Promise.all([p1, p2, p3]);

      // Still only 2 fetch calls total (init + 1 refresh)
      expect(fetchSpy).toHaveBeenCalledTimes(2);
      expect(auth.getAccessToken()).toBe('jwt-shared');
    });
  });

  // ==========================================================================
  // authenticatedFetch() — adds bearer token, retries on 401
  // ==========================================================================

  describe('authenticatedFetch()', () => {
    it('adds Authorization header to requests', async () => {
      // init (registration)
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
      await auth.init(TEST_REGISTRATION_PAYLOAD);

      // authenticated request succeeds
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ data: 'hello' }),
      });

      await auth.authenticatedFetch('http://api.example.com/resource', {
        method: 'GET',
      });

      const [url, init] = fetchSpy.mock.calls[1];
      expect(url).toBe('http://api.example.com/resource');
      const headers = init.headers as Headers;
      expect(headers.get('Authorization')).toBe('Bearer jwt-token-1');
    });

    it('retries on 401 after refreshing token', async () => {
      // init (registration)
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
      await auth.init(TEST_REGISTRATION_PAYLOAD);

      // First request returns 401
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 401,
        json: async () => ({ error: 'Token expired' }),
      });

      // Refresh succeeds
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          accessToken: 'jwt-2',
          refreshToken: 'rt-2',
          expiresIn: 60,
        }),
      });

      // Retry succeeds
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        json: async () => ({ data: 'retried' }),
      });

      const response = await auth.authenticatedFetch('http://api.example.com/resource');

      // 4 total: init + 401 request + refresh + retry
      expect(fetchSpy).toHaveBeenCalledTimes(4);
      expect(response.status).toBe(200);

      // Verify retry used new token
      const [, retryInit] = fetchSpy.mock.calls[3];
      const retryHeaders = retryInit.headers as Headers;
      expect(retryHeaders.get('Authorization')).toBe('Bearer jwt-2');
    });

    it('throws when init() has not been called', async () => {
      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);

      await expect(
        auth.authenticatedFetch('http://api.example.com/resource'),
      ).rejects.toThrow('AgentAuth not initialized');
    });

    it('preserves existing headers from init parameter', async () => {
      // init (registration)
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        json: async () => registrationResponse(),
      });

      const auth = new AgentAuth(TEST_API_KEY, TEST_BASE_URL);
      await auth.init(TEST_REGISTRATION_PAYLOAD);

      // request
      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
      });

      await auth.authenticatedFetch('http://api.example.com/resource', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ key: 'value' }),
      });

      const [, init] = fetchSpy.mock.calls[1];
      const headers = init.headers as Headers;
      expect(headers.get('Content-Type')).toBe('application/json');
      expect(headers.get('Authorization')).toBe('Bearer jwt-token-1');
      expect(init.body).toBe(JSON.stringify({ key: 'value' }));
    });
  });
});
