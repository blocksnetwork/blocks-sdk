import type { HandlerResult, StartTaskMessage, TaskContext } from '@blocks-network/sdk';

const INTEGRATION = 'google';
const USERINFO_URL = 'https://openidconnect.googleapis.com/v1/userinfo';
const RECENT_FILES_URL = 'https://www.googleapis.com/drive/v3/files?pageSize=5&fields=files(name)';

export default async function handler(
  _task: StartTaskMessage,
  ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx) throw new Error('TaskContext is required');

  ctx.reportStatus('Connecting to Google...');
  const user = (await getJson(ctx, USERINFO_URL)) as { email?: string };

  ctx.reportStatus('Fetching Drive files...');
  const drive = (await getJson(ctx, RECENT_FILES_URL)) as { files?: Array<{ name?: string }> };

  const names = (drive.files ?? []).map((f) => f.name || '(unnamed)').join(', ');
  const result = `Signed in to Google as ${user.email ?? 'unknown'}. Recent Drive files: ${names || '(none)'}`;

  return {
    artifacts: [{ data: result, mimeType: 'text/plain', outputId: 'result' }],
  };
}

async function getJson(ctx: TaskContext, url: string): Promise<unknown> {
  let token = await ctx.connections.getToken(INTEGRATION);
  for (let attempt = 0; ; attempt++) {
    const response = await fetch(url, {
      headers: { Authorization: `${token.tokenType} ${token.accessToken}` },
    });
    // Refresh once when Google rejects a token Blocks still considered valid.
    if (response.status === 401 && attempt === 0) {
      token = await ctx.connections.getToken(INTEGRATION, { forceRefresh: true });
      continue;
    }
    if (!response.ok) {
      throw new Error(`Google answered ${response.status} ${response.statusText} for ${url}`);
    }
    return response.json();
  }
}
