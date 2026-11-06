import type {
  ConnectionAccessError,
  ConnectionRequiredError,
  ConnectionToken,
  HandlerResult,
  StartTaskMessage,
  TaskContext,
} from '@blocks-network/sdk';

interface Service {
  integration: string;
  whoAmIUrl: (token: ConnectionToken) => string;
  accountOf: (body: Record<string, unknown>) => unknown;
}

// One entry per `connections[]` item in agent-card.json.
const SERVICES: Service[] = [
  {
    integration: 'google',
    whoAmIUrl: () => 'https://openidconnect.googleapis.com/v1/userinfo',
    accountOf: (body) => body.email,
  },
  {
    integration: 'microsoft',
    whoAmIUrl: () => 'https://graph.microsoft.com/v1.0/me',
    accountOf: (body) => body.userPrincipalName ?? body.mail,
  },
  {
    integration: 'salesforce',
    // Salesforce returns the org's API host with the token, not a fixed URL.
    whoAmIUrl: (token) => `${token.extra.instance_url}/services/oauth2/userinfo`,
    accountOf: (body) => body.preferred_username ?? body.email,
  },
];

export default async function handler(
  _task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx) throw new Error('TaskContext is required');

  const lines: string[] = [];
  for (const service of SERVICES) {
    ctx.reportStatus(`Checking ${service.integration}...`);
    lines.push(`${service.integration}: ${await describe(ctx, service)}`);
  }

  return {
    artifacts: [{ data: lines.join('\n'), mimeType: 'text/plain', outputId: 'result' }],
  };
}

async function describe(ctx: TaskContext, service: Service): Promise<string> {
  try {
    const body = await getJson(ctx, service);
    return `signed in as ${String(service.accountOf(body) ?? 'unknown')}`;
  } catch (err) {
    if (isNamed<ConnectionRequiredError>(err, 'ConnectionRequiredError')) {
      return `${err.reason}, connect at ${err.connectUrl}`;
    }
    // For example 409: the agent's org has no integration with this name.
    if (isNamed<ConnectionAccessError>(err, 'ConnectionAccessError')) {
      return `unavailable (${err.status}${err.code ? ` ${err.code}` : ''})`;
    }
    throw err;
  }
}

async function getJson(ctx: TaskContext, service: Service): Promise<Record<string, unknown>> {
  let token = await ctx.connections.getToken(service.integration);
  for (let attempt = 0; ; attempt++) {
    const response = await fetch(service.whoAmIUrl(token), {
      headers: { Authorization: `${token.tokenType} ${token.accessToken}` },
    });
    // Refresh once when the provider rejects a token Blocks still considered valid.
    if (response.status === 401 && attempt === 0) {
      token = await ctx.connections.getToken(service.integration, { forceRefresh: true });
      continue;
    }
    if (!response.ok) {
      throw new Error(`${service.integration} answered ${response.status} ${response.statusText}`);
    }
    return (await response.json()) as Record<string, unknown>;
  }
}

// `blocks run` loads a TypeScript handler with its own copy of the SDK, so instanceof
// against the imported error classes misses the errors ctx.connections throws.
function isNamed<T extends Error>(err: unknown, name: string): err is T {
  return err instanceof Error && err.name === name;
}
