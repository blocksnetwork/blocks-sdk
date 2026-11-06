import type { HandlerResult, StartTaskMessage, TaskContext } from '@blocks-network/sdk';

const INTEGRATION = 'microsoft';
const GRAPH = 'https://graph.microsoft.com/v1.0';

export default async function handler(
  _task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx) throw new Error('TaskContext is required');

  ctx.reportStatus('Connecting to Microsoft...');
  const me = (await graphGet(ctx, '/me?$select=displayName,mail,userPrincipalName')) as {
    displayName?: string;
    mail?: string | null;
    userPrincipalName?: string;
  };

  ctx.reportStatus('Fetching OneDrive files...');
  const files = (await graphGet(ctx, '/me/drive/root/children?$top=5&$select=name')) as {
    value?: Array<{ name?: string }>;
  };

  const names = (files.value ?? []).map((f) => f.name || '(unnamed)').join(', ');
  const who = me.mail || me.userPrincipalName || me.displayName || 'unknown';
  const result = `Signed in to Microsoft as ${who}. OneDrive files: ${names || '(none)'}`;

  return {
    artifacts: [{ data: result, mimeType: 'text/plain', outputId: 'result' }],
  };
}

async function graphGet(ctx: TaskContext, path: string): Promise<unknown> {
  let token = await ctx.connections.getToken(INTEGRATION);
  for (let attempt = 0; ; attempt++) {
    const response = await fetch(`${GRAPH}${path}`, {
      headers: { Authorization: `${token.tokenType} ${token.accessToken}` },
    });
    // Refresh once when Microsoft rejects a token Blocks still considered valid.
    if (response.status === 401 && attempt === 0) {
      token = await ctx.connections.getToken(INTEGRATION, { forceRefresh: true });
      continue;
    }
    if (!response.ok) {
      throw new Error(`Microsoft Graph ${path} failed: ${response.status} ${response.statusText}`);
    }
    return response.json();
  }
}
