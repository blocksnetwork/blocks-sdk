import type {
  ConnectionToken,
  HandlerResult,
  StartTaskMessage,
  TaskContext,
} from '@blocks-network/sdk';

const INTEGRATION = 'salesforce';
const API_VERSION = 'v62.0';
const RECENT_ACCOUNTS_SOQL = 'SELECT Name FROM Account ORDER BY CreatedDate DESC LIMIT 5';

export default async function handler(
  _task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx) throw new Error('TaskContext is required');

  ctx.reportStatus('Connecting to Salesforce...');
  const user = (await salesforceGet(ctx, '/services/oauth2/userinfo')) as {
    preferred_username?: string;
    email?: string;
  };

  ctx.reportStatus('Fetching recent Accounts...');
  const query = (await salesforceGet(
    ctx,
    `/services/data/${API_VERSION}/query?q=${encodeURIComponent(RECENT_ACCOUNTS_SOQL)}`,
  )) as { records?: Array<{ Name?: string }> };

  const accounts = (query.records ?? []).map((r) => r.Name || '(unnamed)').join(', ');
  const who = user.preferred_username || user.email || 'unknown';
  const result = `Signed in to Salesforce as ${who}. Recent Accounts: ${accounts || '(none)'}`;

  return {
    artifacts: [{ data: result, mimeType: 'text/plain', outputId: 'result' }],
  };
}

async function salesforceGet(ctx: TaskContext, path: string): Promise<unknown> {
  let token = await ctx.connections.getToken(INTEGRATION);
  for (let attempt = 0; ; attempt++) {
    const response = await fetch(`${instanceUrl(token)}${path}`, {
      headers: { Authorization: `${token.tokenType} ${token.accessToken}` },
    });
    // Refresh once when Salesforce rejects a token Blocks still considered valid.
    if (response.status === 401 && attempt === 0) {
      token = await ctx.connections.getToken(INTEGRATION, { forceRefresh: true });
      continue;
    }
    if (!response.ok) {
      throw new Error(`Salesforce ${path} failed: ${response.status} ${response.statusText}`);
    }
    return response.json();
  }
}

// Salesforce returns the org's API host with the token, not a fixed URL.
function instanceUrl(token: ConnectionToken): string {
  const url = token.extra.instance_url;
  if (typeof url !== 'string' || !url) {
    throw new Error('Salesforce token has no instance_url');
  }
  return url;
}
