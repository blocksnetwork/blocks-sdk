/**
 * Tool handlers for the Blocks Network MCP server.
 *
 * Each handler is a pure function that takes its dependencies (clients,
 * registry helpers, fs/path utilities) so it can be unit-tested without
 * touching the network or the filesystem.
 */

import { statSync } from 'node:fs';
import {
  textPart,
  filePartFromPath,
  type TaskInfo,
  type SendMessageRequestPart,
} from '@blocks-network/sdk';

import type { ListAgentsResult } from './registry-list.js';
import {
  MAX_AGENT_NAMES,
  AGENT_NAME_PATTERN,
  type AgentStatusResponse,
} from './agent-status.js';
import type { ConsumerBalance, TopUpSession } from './billing.js';

// ============================================================================
// Types — minimal local shapes mirroring @blocks-network/sdk surfaces.
// Using narrow interfaces (instead of importing the SDK classes) keeps
// the tests free of PubNub/runtime dependencies.
// ============================================================================

export interface ArtifactRef {
  fileName?: string;
}

export interface DownloadedArtifact {
  fileName?: string;
  mimeType: string;
  data: Uint8Array;
}

export interface StreamDescriptor {
  localDirection: string;
  format: string;
  declaredStream?: string;
  streamId: string;
}

export interface StreamClient {
  events(): AsyncIterable<unknown>;
  bytes(): AsyncIterable<Uint8Array>;
}

export interface StreamRef {
  descriptor: StreamDescriptor;
  open(): StreamClient;
}

export interface ProgressEvent {
  message?: string;
}

export interface TerminalEvent {
  state: string;
  error?: string;
  reason?: string;
}

export interface TaskSessionLike {
  taskId: string;
  onProgress(cb: (event: ProgressEvent) => void): unknown;
  onStream(cb: (ref: StreamRef) => void): unknown;
  listArtifacts(): ArtifactRef[];
  listStreams(): StreamRef[];
  downloadArtifact(ref: ArtifactRef): Promise<DownloadedArtifact>;
  waitForTerminal(timeoutMs?: number): Promise<TerminalEvent>;
  close(): void;
  asyncClose(): Promise<void>;
}

export interface TaskClientLike {
  sendMessage(params: {
    agentName: string;
    requestParts: SendMessageRequestPart[];
    taskKind?: 'request' | 'pipe';
    duration?: number;
  }): Promise<TaskSessionLike>;
  getTask(taskId: string): Promise<TaskInfo>;
  listTasks(params: {
    agentName?: string;
    state?: string;
    limit?: number;
  }): Promise<{ tasks: TaskInfo[]; totalCount?: number }>;
  cancelTask(taskId: string): Promise<unknown>;
  pauseTask(taskId: string): Promise<unknown>;
  resumeTask(taskId: string): Promise<unknown>;
  retryTask(taskId: string): Promise<unknown>;
  connect(params: { taskId: string }): Promise<TaskSessionLike>;
}

export interface AgentEntryLike {
  agentName: string;
  card?: {
    io?: {
      inputs?: Array<{ id: string; contentType: string }>;
    };
  };
  billingMode?: 'free' | 'paid';
}

export interface ToolResult {
  [k: string]: unknown;
  content: Array<{ type: 'text'; text: string }>;
  isError?: boolean;
}

export interface ToolDeps {
  getBaseUrl(): Promise<string>;
  getApiKey(): string | undefined;
  getTaskClient(billingMode?: 'free' | 'paid'): Promise<TaskClientLike>;
  getAgentByName(
    agentName: string,
    options: { baseUrl: string; apiKey?: string },
  ): Promise<AgentEntryLike | null>;
  listAgents(options: {
    baseUrl: string;
    apiKey?: string;
    /** Free-text search query (`q`); matches agent name, description, tags, etc. */
    q?: string;
    tag?: string;
    listing?: 'public' | 'private';
    /** Optional cap on the total number of agents to fetch across all pages. */
    maxAgents?: number;
  }): Promise<ListAgentsResult>;
  validateFilePath(filePath: string): string;
  resolveSavePath(filePath: string): string;
  writeFile(filePath: string, data: Uint8Array): void;
  fileSize(path: string): number;
  maxUploadBytes: number;
  filePartFromPath: typeof filePartFromPath;
  textPart: typeof textPart;
  getOrgId(): string | undefined;
  fetchAgentStatus(options: {
    baseUrl: string;
    apiKey?: string;
    agentNames: string[];
  }): Promise<AgentStatusResponse>;
  getConsumerBalance(options: {
    baseUrl: string;
    apiKey: string;
    orgId: string;
  }): Promise<ConsumerBalance>;
  createConsumerTopUp(options: {
    baseUrl: string;
    apiKey: string;
    orgId: string;
    amountUsd: number;
  }): Promise<TopUpSession>;
}

// ============================================================================
// Helpers
// ============================================================================

const TERMINAL_STATES = new Set(['completed', 'failed', 'canceled']);

function resolveBillingMode(task: TaskInfo): 'free' | 'paid' {
  const mode = task.billingMode as string | undefined;
  return mode === 'paid' ? 'paid' : 'free';
}

function isTextLikeContentType(ct: string): boolean {
  return ct.startsWith('text/') || ct === 'application/json' || ct.endsWith('+json');
}

function isTextMimeType(mimeType: string): boolean {
  return mimeType.startsWith('text/') || mimeType === 'application/json';
}

async function appendArtifacts(
  session: TaskSessionLike,
  refs: ArtifactRef[],
  out: string[],
): Promise<void> {
  for (const ref of refs) {
    try {
      const downloaded = await session.downloadArtifact(ref);
      if (isTextMimeType(downloaded.mimeType)) {
        const text = new TextDecoder().decode(downloaded.data);
        out.push(`[artifact: ${downloaded.fileName ?? 'unnamed'}]\n${text}`);
      } else {
        out.push(
          `[artifact: ${downloaded.fileName ?? 'unnamed'}] (${downloaded.mimeType}, ${downloaded.data.length} bytes)`,
        );
      }
    } catch {
      out.push(`[artifact: ${ref.fileName ?? 'unnamed'}] (download failed)`);
    }
  }
}

// ============================================================================
// Tool params
// ============================================================================

export interface SendTaskParams {
  agentName: string;
  message: string;
  filePath?: string;
  inputs?: Record<string, string>;
  taskKind?: 'request' | 'pipe';
  duration?: number;
  timeoutMs?: number;
}

export interface GetTaskParams {
  taskId: string;
}

export interface ListTasksParams {
  agentName?: string;
  state?: string;
  limit?: number;
}

export interface CancelTaskParams {
  taskId: string;
}

export interface ListAgentsParams {
  tag?: string;
  listing?: 'public' | 'private';
  limit?: number;
  /**
   * Include agents that have no online instances. Defaults to false: the
   * registry returns every registered agent regardless of whether any
   * instance is currently running, so by default we filter to agents with
   * at least one online instance to avoid surfacing unreachable agents.
   */
  includeOffline?: boolean;
}

export interface SearchAgentsParams {
  /** Free-text search query; matches agent name, description, tags, etc. */
  query: string;
  tag?: string;
  listing?: 'public' | 'private';
  limit?: number;
  /**
   * Include agents that have no online instances. Defaults to false so we
   * only surface agents that can actually take a task. See ListAgentsParams.
   */
  includeOffline?: boolean;
}

export interface GetAgentCardParams {
  agentName: string;
}

export interface ConnectTaskParams {
  taskId: string;
  timeoutMs?: number;
}

export interface DownloadArtifactParams {
  taskId: string;
  fileName: string;
  savePath?: string;
}

export interface TaskIdParams {
  taskId: string;
}

export interface GetAgentStatusParams {
  agentNames: string[];
}

export interface RequestTopUpParams {
  amountUsd: number;
}

// ============================================================================
// send_task
// ============================================================================

export async function sendTask(
  params: SendTaskParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const baseUrl = await deps.getBaseUrl();
  const apiKey = deps.getApiKey();
  const entry = await deps.getAgentByName(params.agentName, { baseUrl, apiKey });
  const billingMode: 'free' | 'paid' = entry?.billingMode ?? 'free';
  const client = await deps.getTaskClient(billingMode);

  const declaredInputs = entry?.card?.io?.inputs ?? [];
  const textInput = declaredInputs.find((i) => isTextLikeContentType(i.contentType));
  const fileInput = declaredInputs.find((i) => !isTextLikeContentType(i.contentType));

  const requestParts: SendMessageRequestPart[] = [];
  const textPartId = textInput?.id ?? 'text';
  if ((textInput || declaredInputs.length === 0) && !params.inputs?.[textPartId]) {
    requestParts.push(deps.textPart(params.message, textPartId));
  }

  if (params.filePath) {
    const safePath = deps.validateFilePath(params.filePath);
    const size = deps.fileSize(safePath);
    if (size > deps.maxUploadBytes) {
      return {
        content: [
          {
            type: 'text',
            text: `File too large (${size} bytes). Maximum upload size is ${deps.maxUploadBytes} bytes (25 MB).`,
          },
        ],
        isError: true,
      };
    }
    requestParts.push(
      await deps.filePartFromPath(safePath, {
        partId: fileInput?.id ?? 'file',
        contentType: fileInput?.contentType,
      }),
    );
  }

  if (params.inputs) {
    for (const [partId, value] of Object.entries(params.inputs)) {
      requestParts.push(deps.textPart(value, partId));
    }
  }

  const session = await client.sendMessage({
    agentName: params.agentName,
    requestParts,
    taskKind: params.taskKind,
    duration: params.duration,
  });

  const timeout = params.timeoutMs ?? 60000;
  const progressLines: string[] = [];
  session.onProgress((event) => {
    if (event.message) progressLines.push(`[progress] ${event.message}`);
  });

  try {
    const terminal = await session.waitForTerminal(timeout);
    const output = [`Task ${session.taskId} ${terminal.state}`, ...progressLines];
    await appendArtifacts(session, session.listArtifacts(), output);
    if (terminal.state === 'failed') {
      output.push(`Error: ${terminal.error ?? terminal.reason ?? 'unknown'}`);
    }
    session.close();
    return { content: [{ type: 'text', text: output.join('\n') }] };
  } catch (err) {
    session.close();
    const msg = err instanceof Error ? err.message : String(err);
    return {
      content: [{ type: 'text', text: `Task ${session.taskId} error: ${msg}` }],
      isError: true,
    };
  }
}

// ============================================================================
// get_task
// ============================================================================

export async function getTask(
  params: GetTaskParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const freeClient = await deps.getTaskClient('free');
  const task = await freeClient.getTask(params.taskId);
  const output = [JSON.stringify(task, null, 2)];

  if (task.state && TERMINAL_STATES.has(task.state)) {
    try {
      const billingMode = resolveBillingMode(task);
      const client = await deps.getTaskClient(billingMode);
      const session = await client.connect({ taskId: params.taskId });
      await appendArtifacts(session, session.listArtifacts(), output);
      session.close();
    } catch {
      // connect failed — return task info without artifacts
    }
  }

  return { content: [{ type: 'text', text: output.join('\n') }] };
}

// ============================================================================
// list_tasks
// ============================================================================

export async function listTasks(
  params: ListTasksParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const client = await deps.getTaskClient();
  const result = await client.listTasks({
    agentName: params.agentName,
    state: params.state,
    limit: params.limit,
  });
  const lines = result.tasks.map(
    (t: TaskInfo) =>
      `${t.taskId} | ${t.agentName ?? '?'} | ${t.state ?? '?'} | ${t.createdTime ?? ''}`,
  );
  const header = `Tasks (${result.totalCount ?? result.tasks.length} total):`;
  return { content: [{ type: 'text', text: [header, ...lines].join('\n') }] };
}

// ============================================================================
// cancel_task
// ============================================================================

export async function cancelTask(
  params: CancelTaskParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const client = await deps.getTaskClient();
  await client.cancelTask(params.taskId);
  return { content: [{ type: 'text', text: `Task ${params.taskId} cancelled.` }] };
}

// ============================================================================
// list_agents
// ============================================================================

export async function listAgents(
  params: ListAgentsParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const baseUrl = await deps.getBaseUrl();
  const apiKey = deps.getApiKey();
  const result = await deps.listAgents({
    baseUrl,
    apiKey,
    tag: params.tag,
    listing: params.listing,
    maxAgents: params.limit,
  });

  const total = result.totalCount ?? result.agents.length;

  // The registry returns every registered agent regardless of whether any
  // instance is currently running. Default to online-only so we never
  // surface agents that can't actually take a task; `includeOffline` opts
  // back into the full registry view.
  if (params.includeOffline) {
    const lines = result.agents.map(formatAgentRow);
    const header = `Agents (${result.agents.length} of ${total} total):`;
    return { content: [{ type: 'text', text: [header, ...lines].join('\n') }] };
  }

  const online = await filterOnlineAgents(result.agents, { baseUrl, apiKey }, deps);
  const lines = online.map(formatAgentRow);
  const header = `Agents (${online.length} online of ${total} total):`;
  return { content: [{ type: 'text', text: [header, ...lines].join('\n') }] };
}

// ============================================================================
// search_agent
// ============================================================================

export async function searchAgents(
  params: SearchAgentsParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const query = params.query.trim();
  if (!query) {
    return {
      content: [{ type: 'text', text: 'Search query must not be empty.' }],
      isError: true,
    };
  }

  const baseUrl = await deps.getBaseUrl();
  const apiKey = deps.getApiKey();
  const result = await deps.listAgents({
    baseUrl,
    apiKey,
    q: query,
    tag: params.tag,
    listing: params.listing,
    maxAgents: params.limit,
  });

  const total = result.totalCount ?? result.agents.length;

  // Mirror list_agents: default to online-only so we never surface agents
  // that can't actually take a task; `includeOffline` opts back into the
  // full set of matches.
  if (params.includeOffline) {
    const lines = result.agents.map(formatAgentRow);
    const header = `Agents matching "${query}" (${result.agents.length} of ${total} total):`;
    return { content: [{ type: 'text', text: [header, ...lines].join('\n') }] };
  }

  const online = await filterOnlineAgents(result.agents, { baseUrl, apiKey }, deps);
  const lines = online.map(formatAgentRow);
  const header = `Agents matching "${query}" (${online.length} online of ${total} total):`;
  return { content: [{ type: 'text', text: [header, ...lines].join('\n') }] };
}

function formatAgentRow(a: ListAgentsResult['agents'][number]): string {
  const tags = a.tags?.map((t) => t.name).join(', ') ?? '';
  return `${a.agentName} | ${a.name ?? a.agentName} | ${a.listing ?? 'public'} | ${tags}`;
}

/**
 * Keep only agents that have at least one online instance, as reported by
 * the agent-status service. Agent names that the status endpoint can't
 * accept (too many, or invalid characters) are queried in valid batches;
 * any name that's unqueryable is treated as offline and dropped.
 */
async function filterOnlineAgents(
  agents: ListAgentsResult['agents'],
  ctx: { baseUrl: string; apiKey?: string },
  deps: ToolDeps,
): Promise<ListAgentsResult['agents']> {
  const queryable = agents.filter((a) => AGENT_NAME_PATTERN.test(a.agentName));
  if (queryable.length === 0) return [];

  const online = new Set<string>();
  for (let i = 0; i < queryable.length; i += MAX_AGENT_NAMES) {
    const batch = queryable.slice(i, i + MAX_AGENT_NAMES);
    const status = await deps.fetchAgentStatus({
      baseUrl: ctx.baseUrl,
      apiKey: ctx.apiKey,
      agentNames: batch.map((a) => a.agentName),
    });
    for (const [name, info] of Object.entries(status.agents)) {
      if ((info.onlineCount ?? 0) > 0) online.add(name);
    }
  }

  return agents.filter((a) => online.has(a.agentName));
}

// ============================================================================
// get_agent_card
// ============================================================================

export async function getAgentCard(
  params: GetAgentCardParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const baseUrl = await deps.getBaseUrl();
  const apiKey = deps.getApiKey();
  const entry = await deps.getAgentByName(params.agentName, { baseUrl, apiKey });
  if (!entry) {
    return {
      content: [{ type: 'text', text: `Agent "${params.agentName}" not found.` }],
      isError: true,
    };
  }
  return {
    content: [{ type: 'text', text: JSON.stringify(entry.card ?? entry, null, 2) }],
  };
}

// ============================================================================
// connect_task
// ============================================================================

export async function connectTask(
  params: ConnectTaskParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const freeClient = await deps.getTaskClient('free');
  const task = await freeClient.getTask(params.taskId);
  const billingMode = resolveBillingMode(task);
  const client = await deps.getTaskClient(billingMode);
  const session = await client.connect({ taskId: params.taskId });

  const timeout = params.timeoutMs ?? 60000;
  const progressLines: string[] = [];
  const streamOutputs: Array<{ label: string; chunks: string[] }> = [];
  const streamDrains: Promise<void>[] = [];

  session.onProgress((event) => {
    if (event.message) progressLines.push(`[progress] ${event.message}`);
  });

  const drainStream = (streamRef: StreamRef) => {
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

  for (const ref of session.listStreams()) drainStream(ref);
  session.onStream(drainStream);

  try {
    const terminal = await session.waitForTerminal(timeout);
    await Promise.allSettled(streamDrains);
    const out: string[] = [`Task ${params.taskId} ${terminal.state}`, ...progressLines];

    for (const s of streamOutputs) {
      if (s.chunks.length > 0) {
        out.push(`[stream: ${s.label}]\n${s.chunks.join('')}`);
      }
    }
    await appendArtifacts(session, session.listArtifacts(), out);
    if (terminal.state === 'failed') {
      out.push(`Error: ${terminal.error ?? terminal.reason ?? 'unknown'}`);
    }
    await session.asyncClose();
    return { content: [{ type: 'text', text: out.join('\n') }] };
  } catch (err) {
    await session.asyncClose();
    const msg = err instanceof Error ? err.message : String(err);
    const partial: string[] = [`Task ${params.taskId} timed out (${msg})`];
    for (const s of streamOutputs) {
      if (s.chunks.length > 0) {
        partial.push(`[stream: ${s.label}]\n${s.chunks.join('')}`);
      }
    }
    partial.push(...progressLines);
    return { content: [{ type: 'text', text: partial.join('\n') }], isError: true };
  }
}

// ============================================================================
// download_artifact
// ============================================================================

function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
  return Buffer.from(binary, 'binary').toString('base64');
}

export async function downloadArtifact(
  params: DownloadArtifactParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const freeClient = await deps.getTaskClient('free');
  const task = await freeClient.getTask(params.taskId);
  const billingMode = resolveBillingMode(task);
  const client = await deps.getTaskClient(billingMode);

  let session: TaskSessionLike;
  try {
    session = await client.connect({ taskId: params.taskId });
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    return {
      content: [{ type: 'text', text: `Failed to connect to task ${params.taskId}: ${msg}` }],
      isError: true,
    };
  }

  try {
    const refs = session.listArtifacts();
    const ref = refs.find((r) => r.fileName === params.fileName);
    if (!ref) {
      const available = refs.map((r) => r.fileName ?? 'unnamed').join(', ') || '(none)';
      return {
        content: [
          {
            type: 'text',
            text: `Artifact "${params.fileName}" not found on task ${params.taskId}. Available: ${available}`,
          },
        ],
        isError: true,
      };
    }

    const downloaded = await session.downloadArtifact(ref);

    if (params.savePath) {
      const safePath = deps.resolveSavePath(params.savePath);
      deps.writeFile(safePath, downloaded.data);
      return {
        content: [
          {
            type: 'text',
            text: `Saved ${downloaded.data.length} bytes to ${safePath} (${downloaded.mimeType})`,
          },
        ],
      };
    }

    if (isTextMimeType(downloaded.mimeType)) {
      const text = new TextDecoder().decode(downloaded.data);
      return {
        content: [
          {
            type: 'text',
            text: `[artifact: ${downloaded.fileName ?? params.fileName}] (${downloaded.mimeType}, ${downloaded.data.length} bytes)\n${text}`,
          },
        ],
      };
    }

    return {
      content: [
        {
          type: 'text',
          text: `[artifact: ${downloaded.fileName ?? params.fileName}] (${downloaded.mimeType}, ${downloaded.data.length} bytes, base64)\n${bytesToBase64(downloaded.data)}`,
        },
      ],
    };
  } finally {
    session.close();
  }
}

// ============================================================================
// pause_task / resume_task / retry_task
// ============================================================================

export async function pauseTask(
  params: TaskIdParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const client = await deps.getTaskClient();
  await client.pauseTask(params.taskId);
  return { content: [{ type: 'text', text: `Task ${params.taskId} paused.` }] };
}

export async function resumeTask(
  params: TaskIdParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const client = await deps.getTaskClient();
  await client.resumeTask(params.taskId);
  return { content: [{ type: 'text', text: `Task ${params.taskId} resumed.` }] };
}

export async function retryTask(
  params: TaskIdParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const client = await deps.getTaskClient();
  await client.retryTask(params.taskId);
  return { content: [{ type: 'text', text: `Task ${params.taskId} retry requested.` }] };
}

// ============================================================================
// get_agent_status
// ============================================================================

export async function getAgentStatus(
  params: GetAgentStatusParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const baseUrl = await deps.getBaseUrl();
  const apiKey = deps.getApiKey();
  const result = await deps.fetchAgentStatus({
    baseUrl,
    apiKey,
    agentNames: params.agentNames,
  });
  return {
    content: [{ type: 'text', text: JSON.stringify(result, null, 2) }],
  };
}

// ============================================================================
// check_balance / request_topup
// ============================================================================

function requireBillingContext(deps: ToolDeps): { apiKey: string; orgId: string } | ToolResult {
  const apiKey = deps.getApiKey();
  if (!apiKey) {
    return {
      content: [{ type: 'text', text: 'BLOCKS_API_KEY is required for billing operations.' }],
      isError: true,
    };
  }
  const orgId = deps.getOrgId();
  if (!orgId) {
    return {
      content: [
        {
          type: 'text',
          text: 'BLOCKS_ORG_ID is required for billing operations. Set it from your dashboard URL or `blocks whoami --json`.',
        },
      ],
      isError: true,
    };
  }
  return { apiKey, orgId };
}

export async function checkBalance(
  _params: Record<string, never>,
  deps: ToolDeps,
): Promise<ToolResult> {
  const ctx = requireBillingContext(deps);
  if ('content' in ctx) return ctx;
  const baseUrl = await deps.getBaseUrl();
  const balance = await deps.getConsumerBalance({
    baseUrl,
    apiKey: ctx.apiKey,
    orgId: ctx.orgId,
  });
  return {
    content: [{ type: 'text', text: JSON.stringify(balance, null, 2) }],
  };
}

export async function requestTopup(
  params: RequestTopUpParams,
  deps: ToolDeps,
): Promise<ToolResult> {
  const ctx = requireBillingContext(deps);
  if ('content' in ctx) return ctx;
  const baseUrl = await deps.getBaseUrl();
  const session = await deps.createConsumerTopUp({
    baseUrl,
    apiKey: ctx.apiKey,
    orgId: ctx.orgId,
    amountUsd: params.amountUsd,
  });
  const url = session.checkoutUrl;
  const body = url
    ? `Open this URL in a browser to complete the $${params.amountUsd.toFixed(2)} top-up:\n${url}`
    : JSON.stringify(session, null, 2);
  return { content: [{ type: 'text', text: body }] };
}

// ============================================================================
// Default file helpers (re-exported so index.ts can build a default ToolDeps)
// ============================================================================

export function defaultFileSize(path: string): number {
  return statSync(path).size;
}
