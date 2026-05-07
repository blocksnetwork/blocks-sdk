#!/usr/bin/env node

import { resolve, relative } from 'node:path';
import { realpathSync, statSync } from 'node:fs';
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
  type TaskInfo,
  type SendMessageRequestPart,
} from '@blocks-network/sdk';

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

function resolveBillingMode(task: TaskInfo): 'free' | 'paid' {
  const mode = task.billingMode as string | undefined;
  if (mode === 'paid') return 'paid';
  return 'free';
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

interface AgentListEntry {
  agentName: string;
  name?: string;
  description?: string;
  listing?: string;
  billingMode?: string;
  skills?: Array<{ id: string; name: string }>;
}

async function listAgentsAuthenticated(opts: {
  baseUrl: string;
  apiKey?: string;
  skill?: string;
  listing?: 'public' | 'private';
  limit?: number;
}): Promise<{ agents: AgentListEntry[]; totalCount?: number }> {
  const params = new URLSearchParams({ include: 'full' });
  if (opts.skill) params.set('skill', opts.skill);
  if (opts.listing) {
    params.set('listing', opts.listing);
    if (opts.listing === 'private') params.set('scope', 'owned');
  }
  if (opts.limit) params.set('limit', String(opts.limit));
  const url = `${opts.baseUrl.replace(/\/+$/, '')}/api/v1/registry/agents?${params}`;
  const headers: Record<string, string> = {};
  if (opts.apiKey) headers['Authorization'] = `Bearer ${opts.apiKey}`;
  const response = await fetch(url, { headers });
  if (!response.ok) {
    throw new Error(`Registry list failed: HTTP ${response.status}`);
  }
  const data = await response.json() as { agents: AgentListEntry[]; totalCount?: number };
  return data;
}

async function getTaskClient(billingMode: 'free' | 'paid' = 'free'): Promise<TaskClient> {
  let client = taskClients.get(billingMode);
  if (!client) {
    const apiKey = process.env.BLOCKS_API_KEY;
    client = await TaskClient.create({ apiKey, billingMode });
    taskClients.set(billingMode, client);
  }
  return client;
}

const server = new McpServer({
  name: 'blocks-network',
  version: '0.1.0',
});

// ============================================================================
// Tool: send_task
// ============================================================================

server.tool(
  'send_task',
  'Send a task to a Blocks Network agent and wait for the result',
  {
    agentName: z.string().describe('Name of the target agent'),
    message: z.string().describe('Text message to send to the agent'),
    filePath: z.string().optional().describe('Optional file path to attach'),
    inputs: z.record(z.string(), z.string()).optional().describe('Additional named inputs as {partId: value} pairs'),
    taskKind: z.enum(['request', 'pipe']).optional().describe('Task kind (default: request)'),
    duration: z.number().optional().describe('Duration in minutes (required for pipe tasks)'),
    timeoutMs: z.number().optional().describe('Timeout in milliseconds to wait for result (default: 60000)'),
  },
  async ({ agentName, message, filePath, inputs, taskKind, duration, timeoutMs }) => {
    const baseUrl = await getBaseUrl();
    const apiKey = process.env.BLOCKS_API_KEY;
    const entry = await getAgent(agentName, { baseUrl, apiKey });
    const billingMode = entry?.billingMode ?? 'free';
    const client = await getTaskClient(billingMode);
    const declaredInputs = entry?.card?.io?.inputs ?? [];
    const isTextLike = (ct: string) =>
      ct.startsWith('text/') || ct === 'application/json' || ct.endsWith('+json');
    const textInput = declaredInputs.find((i) => isTextLike(i.contentType));
    const fileInput = declaredInputs.find((i) => !isTextLike(i.contentType));

    const requestParts: SendMessageRequestPart[] = [];
    const textPartId = textInput?.id ?? 'text';
    if ((textInput || declaredInputs.length === 0) && !inputs?.[textPartId]) {
      requestParts.push(textPart(message, textPartId));
    }
    if (filePath) {
      const safePath = validateFilePath(filePath);
      const fileSize = statSync(safePath).size;
      if (fileSize > BLOCKS_MAX_UPLOAD_BYTES) {
        return {
          content: [{ type: 'text', text: `File too large (${fileSize} bytes). Maximum upload size is ${BLOCKS_MAX_UPLOAD_BYTES} bytes (25 MB).` }],
          isError: true,
        };
      }
      requestParts.push(await filePartFromPath(safePath, {
        partId: fileInput?.id ?? 'file',
        contentType: fileInput?.contentType,
      }));
    }
    if (inputs) {
      for (const [partId, value] of Object.entries(inputs)) {
        requestParts.push(textPart(value, partId));
      }
    }

    const session = await client.sendMessage({
      agentName,
      requestParts,
      taskKind,
      duration,
    });

    const timeout = timeoutMs ?? 60000;
    const results: string[] = [];

    session.onProgress((event) => {
      if (event.message) {
        results.push(`[progress] ${event.message}`);
      }
    });

    try {
      const terminal = await session.waitForTerminal(timeout);
      const allArtifacts = session.listArtifacts();
      const output = [`Task ${session.taskId} ${terminal.state}`, ...results];

      for (const ref of allArtifacts) {
        try {
          const downloaded = await session.downloadArtifact(ref);
          const isText = downloaded.mimeType.startsWith('text/') ||
            downloaded.mimeType === 'application/json';
          if (isText) {
            const text = new TextDecoder().decode(downloaded.data);
            output.push(`[artifact: ${downloaded.fileName ?? 'unnamed'}]\n${text}`);
          } else {
            output.push(`[artifact: ${downloaded.fileName ?? 'unnamed'}] (${downloaded.mimeType}, ${downloaded.data.length} bytes)`);
          }
        } catch {
          output.push(`[artifact: ${ref.fileName ?? 'unnamed'}] (download failed)`);
        }
      }

      if (terminal.state === 'failed') {
        output.push(`Error: ${terminal.error ?? terminal.reason ?? 'unknown'}`);
      }
      session.close();
      return { content: [{ type: 'text', text: output.join('\n') }] };
    } catch (err) {
      session.close();
      const msg = err instanceof Error ? err.message : String(err);
      return { content: [{ type: 'text', text: `Task ${session.taskId} error: ${msg}` }], isError: true };
    }
  },
);

// ============================================================================
// Tool: get_task
// ============================================================================

server.tool(
  'get_task',
  'Get the current status of a task by ID, including artifact content if completed',
  {
    taskId: z.string().describe('The task ID to look up'),
  },
  async ({ taskId }) => {
    const freeClient = await getTaskClient('free');
    const task = await freeClient.getTask(taskId);
    const output = [JSON.stringify(task, null, 2)];

    const terminalStates = new Set(['completed', 'failed', 'canceled']);
    if (task.state && terminalStates.has(task.state)) {
      try {
        const billingMode = resolveBillingMode(task);
        const client = await getTaskClient(billingMode);
        const session = await client.connect({ taskId });
        const allArtifacts = session.listArtifacts();
        for (const ref of allArtifacts) {
          try {
            const downloaded = await session.downloadArtifact(ref);
            const isText = downloaded.mimeType.startsWith('text/') ||
              downloaded.mimeType === 'application/json';
            if (isText) {
              const text = new TextDecoder().decode(downloaded.data);
              output.push(`[artifact: ${downloaded.fileName ?? 'unnamed'}]\n${text}`);
            } else {
              output.push(`[artifact: ${downloaded.fileName ?? 'unnamed'}] (${downloaded.mimeType}, ${downloaded.data.length} bytes)`);
            }
          } catch {
            output.push(`[artifact: ${ref.fileName ?? 'unnamed'}] (download failed)`);
          }
        }
        session.close();
      } catch {
        // connect failed — return task info without artifacts
      }
    }

    return { content: [{ type: 'text', text: output.join('\n') }] };
  },
);

// ============================================================================
// Tool: list_tasks
// ============================================================================

server.tool(
  'list_tasks',
  'List tasks, optionally filtered by agent or state',
  {
    agentName: z.string().optional().describe('Filter by agent name'),
    state: z.string().optional().describe('Filter by state (e.g. completed, failed, running)'),
    limit: z.number().optional().describe('Max results to return'),
  },
  async ({ agentName, state, limit }) => {
    const client = await getTaskClient();
    const result = await client.listTasks({ agentName, state, limit });
    const lines = result.tasks.map((t: TaskInfo) =>
      `${t.taskId} | ${t.agentName ?? '?'} | ${t.state ?? '?'} | ${t.createdTime ?? ''}`,
    );
    const header = `Tasks (${result.totalCount ?? result.tasks.length} total):`;
    return { content: [{ type: 'text', text: [header, ...lines].join('\n') }] };
  },
);

// ============================================================================
// Tool: cancel_task
// ============================================================================

server.tool(
  'cancel_task',
  'Cancel a running task',
  {
    taskId: z.string().describe('The task ID to cancel'),
  },
  async ({ taskId }) => {
    const client = await getTaskClient();
    await client.cancelTask(taskId);
    return { content: [{ type: 'text', text: `Task ${taskId} cancelled.` }] };
  },
);

// ============================================================================
// Tool: list_agents
// ============================================================================

server.tool(
  'list_agents',
  'List available agents in the Blocks Network registry. Use listing="private" with an API key to discover your private agents.',
  {
    skill: z.string().optional().describe('Filter by skill name'),
    listing: z.enum(['public', 'private']).optional().describe('Filter by listing visibility (default: public)'),
    limit: z.number().optional().describe('Max results to return'),
  },
  async ({ skill, listing, limit }) => {
    const baseUrl = await getBaseUrl();
    const apiKey = process.env.BLOCKS_API_KEY;
    const result = await listAgentsAuthenticated({ baseUrl, apiKey, skill, listing, limit });

    const lines = result.agents.map((a) => {
      const skills = a.skills?.map((s) => s.name).join(', ') ?? '';
      return `${a.agentName} | ${a.name ?? a.agentName} | ${a.listing ?? 'public'} | ${skills}`;
    });
    const header = `Agents (${result.totalCount ?? result.agents.length}):`;
    return { content: [{ type: 'text', text: [header, ...lines].join('\n') }] };
  },
);

// ============================================================================
// Tool: get_agent_card
// ============================================================================

server.tool(
  'get_agent_card',
  'Get the full agent card for a specific agent',
  {
    agentName: z.string().describe('Agent name to look up'),
  },
  async ({ agentName }) => {
    const baseUrl = await getBaseUrl();
    const apiKey = process.env.BLOCKS_API_KEY;
    const entry = await getAgent(agentName, { baseUrl, apiKey });
    if (!entry) {
      return { content: [{ type: 'text', text: `Agent "${agentName}" not found.` }], isError: true };
    }
    return { content: [{ type: 'text', text: JSON.stringify(entry.card ?? entry, null, 2) }] };
  },
);

// ============================================================================
// Tool: connect_task
// ============================================================================

server.tool(
  'connect_task',
  'Connect to an existing task, stream events and data until completion',
  {
    taskId: z.string().describe('The task ID to connect to'),
    timeoutMs: z.number().optional().describe('Timeout in milliseconds (default: 60000)'),
  },
  async ({ taskId, timeoutMs }) => {
    const freeClient = await getTaskClient('free');
    const task = await freeClient.getTask(taskId);
    const billingMode = resolveBillingMode(task);
    const client = await getTaskClient(billingMode);
    const session = await client.connect({ taskId });

    const timeout = timeoutMs ?? 60000;
    const results: string[] = [];
    const streamOutputs: Array<{ label: string; chunks: string[] }> = [];
    const streamDrains: Promise<void>[] = [];

    session.onProgress((event) => {
      if (event.message) {
        results.push(`[progress] ${event.message}`);
      }
    });

    const drainStream = (streamRef: { descriptor: { localDirection: string; format: string; declaredStream?: string; streamId: string }; open: () => { events: () => AsyncIterable<unknown>; bytes: () => AsyncIterable<Uint8Array> } }) => {
      const dir = streamRef.descriptor.localDirection;
      if (dir !== 'inbound' && dir !== 'bidirectional') return;
      const format = streamRef.descriptor.format;
      const label = streamRef.descriptor.declaredStream ?? streamRef.descriptor.streamId;
      const entry = { label, chunks: [] as string[] };
      streamOutputs.push(entry);
      try {
        const streamClient = streamRef.open();
        const drain = (async () => {
          if (format === 'events') {
            for await (const event of streamClient.events()) {
              entry.chunks.push(JSON.stringify(event) + '\n');
            }
          } else {
            const decoder = new TextDecoder();
            for await (const chunk of streamClient.bytes()) {
              entry.chunks.push(decoder.decode(chunk, { stream: true }));
            }
          }
        })();
        streamDrains.push(drain.catch(() => {}));
      } catch {
        // stream already ended or terminal
      }
    };

    for (const ref of session.listStreams()) {
      drainStream(ref);
    }
    session.onStream(drainStream);

    try {
      const terminal = await session.waitForTerminal(timeout);
      await Promise.allSettled(streamDrains);
      const allArtifacts = session.listArtifacts();
      results.unshift(`Task ${taskId} ${terminal.state}`);

      for (const s of streamOutputs) {
        if (s.chunks.length > 0) {
          results.push(`[stream: ${s.label}]\n${s.chunks.join('')}`);
        }
      }

      for (const ref of allArtifacts) {
        try {
          const downloaded = await session.downloadArtifact(ref);
          const isText = downloaded.mimeType.startsWith('text/') ||
            downloaded.mimeType === 'application/json';
          if (isText) {
            const text = new TextDecoder().decode(downloaded.data);
            results.push(`[artifact: ${downloaded.fileName ?? 'unnamed'}]\n${text}`);
          } else {
            results.push(`[artifact: ${downloaded.fileName ?? 'unnamed'}] (${downloaded.mimeType}, ${downloaded.data.length} bytes)`);
          }
        } catch {
          results.push(`[artifact: ${ref.fileName ?? 'unnamed'}] (download failed)`);
        }
      }

      if (terminal.state === 'failed') {
        results.push(`Error: ${terminal.error ?? terminal.reason ?? 'unknown'}`);
      }
      await session.asyncClose();
      return { content: [{ type: 'text', text: results.join('\n') }] };
    } catch (err) {
      await session.asyncClose();
      const msg = err instanceof Error ? err.message : String(err);
      const partial: string[] = [`Task ${taskId} timed out (${msg})`];
      for (const s of streamOutputs) {
        if (s.chunks.length > 0) {
          partial.push(`[stream: ${s.label}]\n${s.chunks.join('')}`);
        }
      }
      partial.push(...results);
      return { content: [{ type: 'text', text: partial.join('\n') }], isError: true };
    }
  },
);

// ============================================================================
// Start server
// ============================================================================

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
