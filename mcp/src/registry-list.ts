/**
 * Authenticated registry list helper.
 *
 * Calls GET /api/v1/registry/agents with the Blocks-Protocol-Version
 * header (required by the backend protocol-version middleware) and an
 * optional Bearer API key for private/owned listings.
 */

import {
  PROTOCOL_VERSION_HEADER,
  CURRENT_PROTOCOL_VERSION,
} from './protocol-headers.js';

export interface AgentListEntry {
  agentName: string;
  name?: string;
  description?: string;
  listing?: string;
  billingMode?: string;
  tags?: Array<{ id: string; name: string }>;
}

export interface ListAgentsOptions {
  baseUrl: string;
  apiKey?: string;
  /** Free-text search query (`q`); matches agent name, description, tags, etc. */
  q?: string;
  tag?: string;
  listing?: 'public' | 'private';
  /** Page size for a single request. The backend caps this at 100. */
  limit?: number;
  /** Opaque pagination cursor returned as `next` by a previous request. */
  cursor?: string;
  fetchImpl?: typeof fetch;
}

export interface ListAgentsResult {
  agents: AgentListEntry[];
  totalCount?: number;
  /** Opaque cursor for the next page, or null/absent when there are no more. */
  next?: string | null;
  /** Total agents with at least one online instance (ignores cursor). */
  totalOnlineCount?: number;
}

/** Backend maximum page size for GET /api/v1/registry/agents. */
export const REGISTRY_PAGE_SIZE = 100;

/** Safety backstop so a misbehaving (never-null) cursor can't loop forever. */
const MAX_PAGES = 1000;

export async function listAgentsAuthenticated(
  opts: ListAgentsOptions,
): Promise<ListAgentsResult> {
  const params = new URLSearchParams({ include: 'full' });
  if (opts.q) params.set('q', opts.q);
  if (opts.tag) params.set('tag', opts.tag);
  if (opts.listing) {
    params.set('listing', opts.listing);
    if (opts.listing === 'private') params.set('scope', 'owned');
  }
  if (opts.limit) params.set('limit', String(opts.limit));
  if (opts.cursor) params.set('cursor', opts.cursor);

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

/**
 * Fetch every agent by following the `next` cursor until it's null/absent.
 *
 * Uses the backend's maximum page size (100) to minimise round-trips. The
 * optional `maxAgents` cap bounds the total number of agents collected and
 * shrinks the final page request so we never over-fetch. The latest
 * `totalCount`/`totalOnlineCount` seen are carried forward to the result.
 */
export async function listAllAgentsAuthenticated(
  opts: ListAgentsOptions & { maxAgents?: number },
): Promise<ListAgentsResult> {
  const agents: AgentListEntry[] = [];
  let cursor = opts.cursor;
  let totalCount: number | undefined;
  let totalOnlineCount: number | undefined;
  const cap = opts.maxAgents;

  for (let page = 0; page < MAX_PAGES; page++) {
    const remaining = cap !== undefined ? cap - agents.length : undefined;
    if (remaining !== undefined && remaining <= 0) break;
    const pageSize =
      remaining !== undefined
        ? Math.min(REGISTRY_PAGE_SIZE, remaining)
        : REGISTRY_PAGE_SIZE;

    const result = await listAgentsAuthenticated({
      ...opts,
      limit: pageSize,
      cursor,
    });
    agents.push(...result.agents);
    if (result.totalCount !== undefined) totalCount = result.totalCount;
    if (result.totalOnlineCount !== undefined) {
      totalOnlineCount = result.totalOnlineCount;
    }

    cursor = result.next ?? undefined;
    if (!cursor) break;
  }

  const trimmed = cap !== undefined ? agents.slice(0, cap) : agents;
  return { agents: trimmed, totalCount, totalOnlineCount, next: null };
}
