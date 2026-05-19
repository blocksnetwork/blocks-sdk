/**
 * Authenticated registry list helper.
 *
 * Calls GET /api/v1/registry/agents with the Blocks-Protocol-Version
 * header (required by the backend protocol-version middleware) and an
 * optional Bearer API key for private/owned listings.
 */

export const PROTOCOL_VERSION_HEADER = 'Blocks-Protocol-Version';
export const CURRENT_PROTOCOL_VERSION = '2026-05-01';

export interface AgentListEntry {
  agentName: string;
  name?: string;
  description?: string;
  listing?: string;
  billingMode?: string;
  skills?: Array<{ id: string; name: string }>;
}

export interface ListAgentsOptions {
  baseUrl: string;
  apiKey?: string;
  skill?: string;
  listing?: 'public' | 'private';
  limit?: number;
  fetchImpl?: typeof fetch;
}

export interface ListAgentsResult {
  agents: AgentListEntry[];
  totalCount?: number;
}

export async function listAgentsAuthenticated(
  opts: ListAgentsOptions,
): Promise<ListAgentsResult> {
  const params = new URLSearchParams({ include: 'full' });
  if (opts.skill) params.set('skill', opts.skill);
  if (opts.listing) {
    params.set('listing', opts.listing);
    if (opts.listing === 'private') params.set('scope', 'owned');
  }
  if (opts.limit) params.set('limit', String(opts.limit));

  const url = `${opts.baseUrl.replace(/\/+$/, '')}/api/v1/registry/agents?${params}`;
  const headers: Record<string, string> = {
    [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
  };
  if (opts.apiKey) headers['Authorization'] = `Bearer ${opts.apiKey}`;

  const fetchFn = opts.fetchImpl ?? fetch;
  const response = await fetchFn(url, { headers });
  if (!response.ok) {
    throw new Error(`Registry list failed: HTTP ${response.status}`);
  }
  return (await response.json()) as ListAgentsResult;
}
