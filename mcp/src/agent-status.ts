/**
 * Agent presence/availability helper.
 *
 * Calls GET /api/v1/agent-status with the Blocks-Protocol-Version
 * header. Backend route is optionalAuth so the API key is forwarded
 * when available but not required.
 *
 * Response shape mirrors the service's agent-status types.
 */

import {
  PROTOCOL_VERSION_HEADER,
  CURRENT_PROTOCOL_VERSION,
} from './protocol-headers.js';

/** MUST stay in sync with the service's agent-status types. */
export const MAX_AGENT_NAMES = 50;
export const AGENT_NAME_PATTERN = /^[a-zA-Z0-9_]+$/;

export interface AgentInstanceStatus {
  instanceId: string;
  uuid: string;
  online: true;
  /**
   * Reserved — backend currently returns 0 (live activity counters are not
   * yet populated by the agent-status service). Do not use for routing or
   * availability decisions.
   */
  activeTasks: number;
  /** Reserved — backend currently returns 0. See `activeTasks`. */
  concurrentTasksPerInstance: number;
  /** Reserved — backend currently returns 0. See `activeTasks`. */
  startedAt: number;
  sdkVersion: string | null;
  cliVersion: string | null;
  preferredProtocolVersion: string | null;
  protocolVersions: string[];
}

export interface AgentStatus {
  agentName: string;
  instances: AgentInstanceStatus[];
  onlineCount: number;
  /** Reserved — backend currently returns 0. See `AgentInstanceStatus.activeTasks`. */
  totalActiveTasks: number;
  taskCount: number;
}

export interface AgentStatusResponse {
  agents: Record<string, AgentStatus>;
}

export interface FetchAgentStatusOptions {
  baseUrl: string;
  agentNames: string[];
  apiKey?: string;
  fetchImpl?: typeof fetch;
}

export async function fetchAgentStatus(
  opts: FetchAgentStatusOptions,
): Promise<AgentStatusResponse> {
  const names = opts.agentNames.map((n) => n.trim()).filter(Boolean);
  if (names.length === 0) {
    throw new Error('agentNames must contain at least one value');
  }
  if (names.length > MAX_AGENT_NAMES) {
    throw new Error(`agentNames must be at most ${MAX_AGENT_NAMES} values`);
  }
  for (const n of names) {
    if (!AGENT_NAME_PATTERN.test(n)) {
      throw new Error(`Invalid agent name: ${n}`);
    }
  }

  const params = new URLSearchParams({ agentNames: names.join(',') });
  const url = `${opts.baseUrl.replace(/\/+$/, '')}/api/v1/agent-status?${params}`;
  const headers: Record<string, string> = {
    [PROTOCOL_VERSION_HEADER]: CURRENT_PROTOCOL_VERSION,
  };
  if (opts.apiKey) headers['Authorization'] = `Bearer ${opts.apiKey}`;

  const fetchFn = opts.fetchImpl ?? fetch;
  const response = await fetchFn(url, { headers });
  if (!response.ok) {
    throw new Error(`Agent status failed: HTTP ${response.status}`);
  }
  return (await response.json()) as AgentStatusResponse;
}
