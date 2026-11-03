import type { AgentAuth } from './agent-auth.js';

export interface ConnectionToken {
  accessToken: string;
  tokenType: string;
  expiresAt: string;
  scopes: string[];
  extra: Record<string, unknown>;
}

export interface ConnectionsClient {
  getToken(integration: string, opts?: { forceRefresh?: boolean }): Promise<ConnectionToken>;
}

export class ConnectionRequiredError extends Error {
  integration: string;
  reason: string;
  connectUrl: string;

  constructor(integration: string, reason: string, connectUrl: string) {
    super(`Integration connection required: ${integration} (${reason})`);
    this.name = 'ConnectionRequiredError';
    this.integration = integration;
    this.reason = reason;
    this.connectUrl = connectUrl;
  }
}

export class ConnectionAccessError extends Error {
  status: number;
  code?: string;

  constructor(status: number, code?: string) {
    super(`Connection access error: ${status}${code ? ` (${code})` : ''}`);
    this.name = 'ConnectionAccessError';
    this.status = status;
    this.code = code;
  }
}

export function createConnectionsClient(opts: {
  agentAuth: AgentAuth;
  baseUrl: string;
  taskId: string;
}): ConnectionsClient {
  const { agentAuth, baseUrl, taskId } = opts;

  return {
    async getToken(integration: string, reqOpts?: { forceRefresh?: boolean }): Promise<ConnectionToken> {
      const url = `${baseUrl.replace(/\/+$/, '')}/api/v1/connections/token`;

      const body: { taskId: string; integration: string; forceRefresh?: boolean } = {
        taskId,
        integration,
      };

      if (reqOpts?.forceRefresh !== undefined) {
        body.forceRefresh = reqOpts.forceRefresh;
      }

      const response = await agentAuth.authenticatedFetch(url, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'omit',
        body: JSON.stringify(body),
      });

      if (response.ok) {
        const data = (await response.json()) as {
          accessToken: string;
          tokenType: string;
          expiresAt: string;
          scopes: string[];
          extra: Record<string, unknown>;
        };
        return {
          accessToken: data.accessToken,
          tokenType: data.tokenType,
          expiresAt: data.expiresAt,
          scopes: data.scopes,
          extra: data.extra,
        };
      }

      const errorBody = (await response.json().catch(() => ({}))) as {
        code?: string;
        details?: {
          integration?: string;
          reason?: string;
          connectUrl?: string;
        };
      };

      if (response.status === 403 && errorBody.code === 'AuthRequired' && errorBody.details) {
        const { integration: errIntegration, reason, connectUrl } = errorBody.details;
        if (typeof errIntegration === 'string' && typeof reason === 'string' && typeof connectUrl === 'string') {
          throw new ConnectionRequiredError(errIntegration, reason, connectUrl);
        }
      }

      throw new ConnectionAccessError(response.status, errorBody.code);
    },
  };
}
