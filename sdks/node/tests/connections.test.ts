import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createConnectionsClient, ConnectionRequiredError, ConnectionAccessError } from '../src/runtime/connections.js';
import { AgentAuth } from '../src/runtime/agent-auth.js';

describe('connections', () => {
  const originalFetch = globalThis.fetch;
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchSpy = vi.fn();
    globalThis.fetch = fetchSpy;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
  });

  async function createInitializedAgentAuth(): Promise<AgentAuth> {
    const agentAuth = new AgentAuth('test-api-key', 'http://localhost:3001');

    fetchSpy.mockResolvedValueOnce({
      ok: true,
      status: 200,
      headers: new Headers(),
      json: async () => ({
        accessToken: 'test-jwt-token',
        refreshToken: 'test-refresh-token',
        registeredAgent: {
          agentId: 'agent-1',
          orgId: 'org-1',
          name: 'test-agent',
        },
      }),
    });

    await agentAuth.init({
      agentName: 'test-agent',
      agentVersion: '1.0.0',
      cardDigest: 'test-digest',
    });

    return agentAuth;
  }

  describe('getToken', () => {
    it('posts taskId and integration with agent bearer and protocol header', async () => {
      const agentAuth = await createInitializedAgentAuth();

      const client = createConnectionsClient({
        agentAuth,
        baseUrl: 'http://localhost:3001',
        taskId: 'task-123',
      });

      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        headers: new Headers(),
        json: async () => ({
          accessToken: 'github-token-abc',
          tokenType: 'Bearer',
          expiresAt: '2026-10-21T00:00:00Z',
          scopes: ['repo', 'user'],
          extra: { userId: '12345' },
        }),
      });

      await client.getToken('github');

      const tokenCall = fetchSpy.mock.calls.find((call) => call[0].includes('/connections/token'));
      expect(tokenCall).toBeDefined();
      const [url, init] = tokenCall;
      expect(url).toBe('http://localhost:3001/api/v1/connections/token');
      expect(init.method).toBe('POST');
      expect(init.headers.get('Authorization')).toBe('Bearer test-jwt-token');
      expect(init.headers.has('Blocks-Protocol-Version')).toBe(true);
      expect(init.credentials).toBe('omit');
      const body = JSON.parse(init.body);
      expect(body).toEqual({
        taskId: 'task-123',
        integration: 'github',
      });
    });

    it('returns the parsed ConnectionToken', async () => {
      const agentAuth = await createInitializedAgentAuth();

      const client = createConnectionsClient({
        agentAuth,
        baseUrl: 'http://localhost:3001',
        taskId: 'task-123',
      });

      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        headers: new Headers(),
        json: async () => ({
          accessToken: 'github-token-abc',
          tokenType: 'Bearer',
          expiresAt: '2026-10-21T00:00:00Z',
          scopes: ['repo', 'user'],
          extra: { userId: '12345' },
        }),
      });

      const token = await client.getToken('github');

      expect(token).toEqual({
        accessToken: 'github-token-abc',
        tokenType: 'Bearer',
        expiresAt: '2026-10-21T00:00:00Z',
        scopes: ['repo', 'user'],
        extra: { userId: '12345' },
      });
    });

    it('passes forceRefresh option when provided', async () => {
      const agentAuth = await createInitializedAgentAuth();

      const client = createConnectionsClient({
        agentAuth,
        baseUrl: 'http://localhost:3001',
        taskId: 'task-123',
      });

      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        headers: new Headers(),
        json: async () => ({
          accessToken: 'github-token-xyz',
          tokenType: 'Bearer',
          expiresAt: '2026-10-21T00:00:00Z',
          scopes: ['repo'],
          extra: {},
        }),
      });

      await client.getToken('github', { forceRefresh: true });

      const tokenCall = fetchSpy.mock.calls.find((call) => call[0].includes('/connections/token'));
      const body = JSON.parse(tokenCall[1].body);
      expect(body.forceRefresh).toBe(true);
    });

    async function failedGetToken(status: number, body: unknown): Promise<unknown> {
      const agentAuth = await createInitializedAgentAuth();
      const client = createConnectionsClient({
        agentAuth,
        baseUrl: 'http://localhost:3001',
        taskId: 'task-123',
      });
      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status,
        headers: new Headers(),
        json: async () => body,
      });
      return client.getToken('github').then(
        () => expect.unreachable('getToken should have thrown'),
        (err: unknown) => err,
      );
    }

    it('throws ConnectionRequiredError with connectUrl on 403 AuthRequired', async () => {
      const err = await failedGetToken(403, {
        error: 'Integration connection required',
        code: 'AuthRequired',
        details: {
          integration: 'github',
          reason: 'not_connected',
          connectUrl: 'http://localhost:3001/connect/org-1/github?agent=test-agent',
        },
      });

      expect(err).toBeInstanceOf(ConnectionRequiredError);
      expect(err).toMatchObject({
        integration: 'github',
        reason: 'not_connected',
        connectUrl: 'http://localhost:3001/connect/org-1/github?agent=test-agent',
      });
    });

    it.each([
      [403, { error: 'Access denied', code: 'ConnectionAccess' }, 'ConnectionAccess'],
      [500, { error: 'Internal server error' }, undefined],
    ])('throws ConnectionAccessError on %i', async (status, body, code) => {
      const err = await failedGetToken(status, body);

      expect(err).toBeInstanceOf(ConnectionAccessError);
      expect(err).toMatchObject({ status, code });
    });

    it('error messages and String(err) never contain the access token', async () => {
      const agentAuth = await createInitializedAgentAuth();

      const client = createConnectionsClient({
        agentAuth,
        baseUrl: 'http://localhost:3001',
        taskId: 'task-123',
      });

      fetchSpy.mockResolvedValueOnce({
        ok: true,
        status: 200,
        headers: new Headers(),
        json: async () => ({
          accessToken: 'SECRET_TOKEN_12345',
          tokenType: 'Bearer',
          expiresAt: '2026-10-21T00:00:00Z',
          scopes: ['repo'],
          extra: {},
        }),
      });

      const token = await client.getToken('github');
      expect(token.accessToken).toBe('SECRET_TOKEN_12345');

      fetchSpy.mockResolvedValueOnce({
        ok: false,
        status: 403,
        headers: new Headers(),
        json: async () => ({
          error: 'Integration connection required',
          code: 'AuthRequired',
          details: {
            integration: 'github',
            reason: 'not_connected',
            connectUrl: 'http://localhost:3001/connect/org-1/github?agent=test-agent',
          },
          accessToken: 'SECRET_TOKEN_67890',
        }),
      });

      try {
        await client.getToken('github');
      } catch (err) {
        const errorString = String(err);
        const errorMessage = (err as Error).message;
        expect(errorString).not.toContain('SECRET_TOKEN_67890');
        expect(errorString).not.toContain('SECRET_TOKEN_12345');
        expect(errorMessage).not.toContain('SECRET_TOKEN_67890');
        expect(errorMessage).not.toContain('SECRET_TOKEN_12345');
      }
    });
  });
});
