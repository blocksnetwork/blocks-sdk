#!/usr/bin/env node

import { resolve, relative, dirname } from 'node:path';
import { realpathSync, mkdirSync, writeFileSync } from 'node:fs';
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { z } from 'zod';
import {
  TaskClient,
  textPart,
  filePartFromPath,
  fetchCdmConfig,
  getAgent,
  BLOCKS_MAX_UPLOAD_BYTES,
} from '@blocks-network/sdk';

import { listAllAgentsAuthenticated } from './registry-list.js';
import { fetchAgentStatus, MAX_AGENT_NAMES } from './agent-status.js';
import { getConsumerBalance, createConsumerTopUp } from './billing.js';
import {
  sendTask,
  getTask,
  listTasks,
  cancelTask,
  pauseTask,
  resumeTask,
  retryTask,
  listAgents,
  searchAgents,
  getAgentCard,
  getAgentStatus,
  connectTask,
  downloadArtifact,
  checkBalance,
  requestTopup,
  defaultFileSize,
  type ToolDeps,
  type TaskClientLike,
  type AgentEntryLike,
} from './tools.js';

const ALLOWED_ROOT = process.env.BLOCKS_MCP_FILE_ROOT ?? process.cwd();

if (!process.env.BLOCKS_API_KEY) {
  console.error('Warning: BLOCKS_API_KEY is not set. Private agents and paid tasks will not work.');
}

function validateFilePath(filePath: string): string {
  const resolved = resolve(filePath);
  let real: string;
  try {
    real = realpathSync(resolved);
  } catch {
    throw new Error(`File not found: ${filePath}`);
  }
  const root = realpathSync(resolve(ALLOWED_ROOT));
  const rel = relative(root, real);
  if (rel.startsWith('..') || resolve(root, rel) !== real) {
    throw new Error(`File path must be within ${ALLOWED_ROOT}`);
  }
  return real;
}

function resolveSavePath(filePath: string): string {
  const root = realpathSync(resolve(ALLOWED_ROOT));
  const resolved = resolve(root, filePath);
  const rel = relative(root, resolved);
  if (rel.startsWith('..') || resolve(root, rel) !== resolved) {
    throw new Error(`Save path must be within ${ALLOWED_ROOT}`);
  }
  mkdirSync(dirname(resolved), { recursive: true });
  return resolved;
}

function writeFile(filePath: string, data: Uint8Array): void {
  writeFileSync(filePath, data);
}

const taskClients = new Map<string, TaskClient>();
let resolvedBaseUrl: string | undefined;

async function getBaseUrl(): Promise<string> {
  if (resolvedBaseUrl) return resolvedBaseUrl;
  if (process.env.BLOCKS_BACKEND_URL) {
    resolvedBaseUrl = process.env.BLOCKS_BACKEND_URL;
    return resolvedBaseUrl;
  }
  const cdm = await fetchCdmConfig(process.env.BLOCKS_CDM_URL);
  resolvedBaseUrl = cdm.api.baseUrl;
  return resolvedBaseUrl;
}

async function getTaskClient(billingMode: 'free' | 'paid' = 'free'): Promise<TaskClientLike> {
  let client = taskClients.get(billingMode);
  if (!client) {
    const apiKey = process.env.BLOCKS_API_KEY;
    client = await TaskClient.create({ apiKey, billingMode });
    taskClients.set(billingMode, client);
  }
  return client as unknown as TaskClientLike;
}

const deps: ToolDeps = {
  getBaseUrl,
  getApiKey: () => process.env.BLOCKS_API_KEY,
  getOrgId: () => process.env.BLOCKS_ORG_ID,
  getTaskClient,
  getAgentByName: (agentName, options) =>
    getAgent(agentName, options) as Promise<AgentEntryLike | null>,
  listAgents: listAllAgentsAuthenticated,
  fetchAgentStatus,
  getConsumerBalance,
  createConsumerTopUp,
  validateFilePath,
  resolveSavePath,
  writeFile,
  fileSize: defaultFileSize,
  maxUploadBytes: BLOCKS_MAX_UPLOAD_BYTES,
  filePartFromPath,
  textPart,
};

const server = new McpServer({
  name: 'blocks-network',
  version: '0.1.60',
});

server.tool(
  'send_task',
  'Send a task to a Blocks Network agent and wait for the result',
  {
    agentName: z.string().describe('Name of the target agent'),
    message: z.string().describe('Text message to send to the agent'),
    filePath: z.string().optional().describe('Optional file path to attach'),
    inputs: z
      .record(z.string(), z.string())
      .optional()
      .describe('Additional named inputs as {partId: value} pairs'),
    taskKind: z.enum(['request', 'pipe']).optional().describe('Task kind (default: request)'),
    duration: z.number().optional().describe('Duration in minutes (required for pipe tasks)'),
    timeoutMs: z
      .number()
      .optional()
      .describe('Timeout in milliseconds to wait for result (default: 60000)'),
  },
  (params) => sendTask(params, deps),
);

server.tool(
  'get_task',
  'Get the current status of a task by ID, including artifact content if completed',
  { taskId: z.string().describe('The task ID to look up') },
  (params) => getTask(params, deps),
);

server.tool(
  'list_tasks',
  'List tasks, optionally filtered by agent or state',
  {
    agentName: z.string().optional().describe('Filter by agent name'),
    state: z.string().optional().describe('Filter by state (e.g. completed, failed, running)'),
    limit: z.number().optional().describe('Max results to return'),
  },
  (params) => listTasks(params, deps),
);

server.tool(
  'cancel_task',
  'Cancel a running task',
  { taskId: z.string().describe('The task ID to cancel') },
  (params) => cancelTask(params, deps),
);

server.tool(
  'pause_task',
  'Pause a running pipe task. Resume later with resume_task.',
  { taskId: z.string().describe('The task ID to pause') },
  (params) => pauseTask(params, deps),
);

server.tool(
  'resume_task',
  'Resume a paused pipe task',
  { taskId: z.string().describe('The task ID to resume') },
  (params) => resumeTask(params, deps),
);

server.tool(
  'retry_task',
  'Retry a failed task',
  { taskId: z.string().describe('The task ID to retry') },
  (params) => retryTask(params, deps),
);

server.tool(
  'list_agents',
  'List available agents in the Blocks Network registry. To find every agent published by a particular provider/organization (e.g. "all agents from Hamilton"), use this tool with the `provider` parameter — it is the correct tool for provider-scoped browsing and needs no search query. By default only agents with at least one online instance are returned; set includeOffline=true to also list registered agents that are currently offline. Use listing="private" with an API key to discover your private agents.',
  {
    tag: z.string().optional().describe('Filter by tag slug'),
    provider: z
      .string()
      .optional()
      .describe(
        'Filter to agents published by this provider (the publishing organization\'s name), matched case-insensitively as a substring (e.g. "hamilton" matches "Hamilton Labs"). This is the reliable way to scope results to a provider — prefer it over typing the provider name into a free-text query, which only fuzzy-matches names/descriptions.',
      ),
    listing: z
      .enum(['public', 'private'])
      .optional()
      .describe('Filter by listing visibility (default: public)'),
    limit: z
      .number()
      .optional()
      .describe('Max agents to fetch across all pages; omit to fetch all'),
    includeOffline: z
      .boolean()
      .optional()
      .describe('Include agents with no online instances (default: false)'),
  },
  (params) => listAgents(params, deps),
);

server.tool(
  'search_agent',
  'Search the Blocks Network registry for agents matching a free-text query. The query matches against agent name, display name, description, tags, provider, and category, and supports field qualifiers (e.g. "agentname:translate", tag:"data"), quoted phrases, and negation ("-deprecated"). To restrict results to a specific provider/organization, set the `provider` parameter rather than putting the provider name in the query; `query` is optional, so you can search by `provider` and/or `tag` alone (e.g. provider="Hamilton" with no query returns every Hamilton agent). At least one of `query`, `provider`, or `tag` must be supplied. By default only agents with at least one online instance are returned; set includeOffline=true to also include matching agents that are currently offline. Use listing="private" with an API key to search your private agents.',
  {
    query: z
      .string()
      .optional()
      .describe(
        'Free-text search query. Optional, but at least one of `query`, `provider`, or `tag` must be given. Omit it to browse a provider or tag with no search terms (e.g. provider="Hamilton" alone returns every Hamilton agent).',
      ),
    provider: z
      .string()
      .optional()
      .describe(
        'Restrict matches to agents published by this provider (the publishing organization\'s name), matched case-insensitively as a substring (e.g. "hamilton" matches "Hamilton Labs"). Use this parameter to scope by provider instead of typing the provider name into `query`, which only fuzzy-matches names/descriptions and is unreliable.',
      ),
    tag: z.string().optional().describe('Additionally filter by tag slug'),
    listing: z
      .enum(['public', 'private'])
      .optional()
      .describe('Filter by listing visibility (default: public)'),
    limit: z
      .number()
      .optional()
      .describe('Max matching agents to fetch across all pages; omit to fetch all'),
    includeOffline: z
      .boolean()
      .optional()
      .describe('Include agents with no online instances (default: false)'),
  },
  (params) => searchAgents(params, deps),
);

server.tool(
  'get_agent_card',
  'Get the full agent card for a specific agent',
  { agentName: z.string().describe('Agent name to look up') },
  (params) => getAgentCard(params, deps),
);

server.tool(
  'get_agent_status',
  'Check live availability for one or more agents: how many instances are online, total task count for the agent, and SDK/CLI versions per instance. (Per-instance live activity counters such as `activeTasks`, `concurrentTasksPerInstance`, `startedAt`, and `totalActiveTasks` are reserved in the response shape but not yet populated by the backend — they currently return 0.)',
  {
    agentNames: z
      .array(z.string())
      .min(1)
      .max(MAX_AGENT_NAMES)
      .describe(`Agent names to check (1-${MAX_AGENT_NAMES})`),
  },
  (params) => getAgentStatus(params, deps),
);

server.tool(
  'check_balance',
  'Get the consumer billing balance for the configured org (BLOCKS_ORG_ID): ledger balance, active reservations, and available balance.',
  {},
  (_params) => checkBalance({}, deps),
);

server.tool(
  'request_topup',
  'Create a Stripe Checkout session to add USD to the consumer balance. Returns a URL the user opens in a browser to complete payment; the MCP server does not handle payment itself. Minimum top-up is $5 (platform `MIN_BILLING_AMOUNT`).',
  {
    amountUsd: z
      .number()
      .positive()
      .describe(
        'Amount to add in USD (e.g. 25 or 19.99). Whole-cent precision. Must be at least $5 (platform minimum).',
      ),
  },
  (params) => requestTopup(params, deps),
);

server.tool(
  'connect_task',
  'Connect to an existing task, stream events and data until completion',
  {
    taskId: z.string().describe('The task ID to connect to'),
    timeoutMs: z.number().optional().describe('Timeout in milliseconds (default: 60000)'),
  },
  (params) => connectTask(params, deps),
);

server.tool(
  'download_artifact',
  'Download a single artifact from a task by file name. If savePath is provided, writes the artifact to disk under BLOCKS_MCP_FILE_ROOT; otherwise returns the content inline (text decoded, binary base64-encoded).',
  {
    taskId: z.string().describe('The task ID that produced the artifact'),
    fileName: z.string().describe('Artifact file name as listed by get_task'),
    savePath: z
      .string()
      .optional()
      .describe('Optional path (relative to BLOCKS_MCP_FILE_ROOT) to write the artifact to'),
  },
  (params) => downloadArtifact(params, deps),
);

function shutdown() {
  for (const client of taskClients.values()) {
    client.destroy();
  }
  taskClients.clear();
}

async function main() {
  const transport = new StdioServerTransport();
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);
  transport.onclose = shutdown;
  await server.connect(transport);
}

main().catch((err) => {
  console.error('Fatal:', err);
  shutdown();
  process.exit(1);
});
