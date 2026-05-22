import { vi } from 'vitest';
import type {
  AgentEntryLike,
  ArtifactRef,
  DownloadedArtifact,
  ProgressEvent,
  StreamRef,
  TaskClientLike,
  TaskSessionLike,
  TerminalEvent,
  ToolDeps,
} from '../src/tools.js';
import type { ListAgentsResult } from '../src/registry-list.js';
import type { TaskInfo } from '@blocks-network/sdk';

export interface FakeSessionConfig {
  taskId?: string;
  artifacts?: Array<{
    fileName?: string;
    mimeType: string;
    data: Uint8Array;
    downloadFails?: boolean;
  }>;
  streams?: StreamRef[];
  /** Streams delivered via onStream() callback after registration. */
  lateStreams?: StreamRef[];
  terminal?: TerminalEvent;
  terminalRejects?: Error;
  progressEvents?: ProgressEvent[];
}

export function makeFakeSession(cfg: FakeSessionConfig = {}): TaskSessionLike & {
  closeMock: ReturnType<typeof vi.fn>;
  asyncCloseMock: ReturnType<typeof vi.fn>;
  emitStream: (ref: StreamRef) => void;
} {
  const taskId = cfg.taskId ?? 'task_123';
  const refs: ArtifactRef[] = (cfg.artifacts ?? []).map((a) => ({ fileName: a.fileName }));
  const streamCallbacks: Array<(ref: StreamRef) => void> = [];
  const closeMock = vi.fn();
  const asyncCloseMock = vi.fn().mockResolvedValue(undefined);

  const session = {
    taskId,
    onProgress(cb: (e: ProgressEvent) => void) {
      for (const e of cfg.progressEvents ?? []) cb(e);
      return () => {};
    },
    onStream(cb: (ref: StreamRef) => void) {
      streamCallbacks.push(cb);
      for (const ref of cfg.lateStreams ?? []) cb(ref);
      return () => {};
    },
    listArtifacts() {
      return refs;
    },
    listStreams() {
      return cfg.streams ?? [];
    },
    async downloadArtifact(ref: ArtifactRef): Promise<DownloadedArtifact> {
      const idx = refs.indexOf(ref);
      const a = (cfg.artifacts ?? [])[idx];
      if (!a || a.downloadFails) throw new Error('download failed');
      return { fileName: a.fileName, mimeType: a.mimeType, data: a.data };
    },
    async waitForTerminal(_timeoutMs?: number) {
      if (cfg.terminalRejects) throw cfg.terminalRejects;
      return cfg.terminal ?? { state: 'completed' };
    },
    close: closeMock,
    asyncClose: asyncCloseMock,
  };

  return Object.assign(session, {
    closeMock,
    asyncCloseMock,
    emitStream: (ref: StreamRef) => {
      for (const cb of streamCallbacks) cb(ref);
    },
  });
}

export interface FakeClientConfig {
  session?: TaskSessionLike;
  task?: TaskInfo;
  listResult?: { tasks: TaskInfo[]; totalCount?: number };
}

export function makeFakeClient(cfg: FakeClientConfig = {}): TaskClientLike & {
  sendMessageMock: ReturnType<typeof vi.fn>;
  cancelTaskMock: ReturnType<typeof vi.fn>;
  connectMock: ReturnType<typeof vi.fn>;
  listTasksMock: ReturnType<typeof vi.fn>;
  getTaskMock: ReturnType<typeof vi.fn>;
} {
  const session = cfg.session ?? makeFakeSession();
  const sendMessageMock = vi.fn().mockResolvedValue(session);
  const connectMock = vi.fn().mockResolvedValue(session);
  const cancelTaskMock = vi.fn().mockResolvedValue(undefined);
  const listTasksMock = vi.fn().mockResolvedValue(
    cfg.listResult ?? { tasks: [], totalCount: 0 },
  );
  const getTaskMock = vi.fn().mockResolvedValue(cfg.task ?? { taskId: 't0', state: 'running' });

  return {
    sendMessage: sendMessageMock,
    cancelTask: cancelTaskMock,
    connect: connectMock,
    listTasks: listTasksMock,
    getTask: getTaskMock,
    sendMessageMock,
    cancelTaskMock,
    connectMock,
    listTasksMock,
    getTaskMock,
  };
}

export interface FakeDepsOverrides {
  baseUrl?: string;
  apiKey?: string;
  client?: TaskClientLike;
  agentEntry?: AgentEntryLike | null;
  listAgentsResult?: ListAgentsResult;
  maxUploadBytes?: number;
  fileSize?: number;
}

export function makeFakeDeps(overrides: FakeDepsOverrides = {}) {
  const client = overrides.client ?? makeFakeClient();
  const getBaseUrl = vi.fn().mockResolvedValue(overrides.baseUrl ?? 'http://api.test');
  const getApiKey = vi.fn().mockReturnValue(overrides.apiKey);
  const getTaskClient = vi.fn().mockResolvedValue(client);
  const getAgentByName = vi.fn().mockResolvedValue(overrides.agentEntry ?? null);
  const listAgents = vi
    .fn()
    .mockResolvedValue(overrides.listAgentsResult ?? { agents: [], totalCount: 0 });
  const validateFilePath = vi.fn((p: string) => p);
  const resolveSavePath = vi.fn((p: string) => p);
  const writeFile = vi.fn();
  const fileSize = vi.fn().mockReturnValue(overrides.fileSize ?? 100);
  const filePartFromPath = vi.fn(async (path: string, opts: { partId?: string; contentType?: string }) => ({
    type: 'file' as const,
    partId: opts.partId ?? 'file',
    file: { id: 'file-id', mimeType: opts.contentType ?? 'application/octet-stream' },
    _path: path,
  }));
  const textPart = vi.fn((text: string, partId: string) => ({
    type: 'text' as const,
    partId,
    text,
  }));

  const deps: ToolDeps = {
    getBaseUrl,
    getApiKey,
    getTaskClient,
    getAgentByName,
    listAgents,
    validateFilePath,
    resolveSavePath,
    writeFile,
    fileSize,
    maxUploadBytes: overrides.maxUploadBytes ?? 25 * 1024 * 1024,
    filePartFromPath: filePartFromPath as unknown as ToolDeps['filePartFromPath'],
    textPart: textPart as unknown as ToolDeps['textPart'],
  };

  return {
    deps,
    client,
    mocks: {
      getBaseUrl,
      getApiKey,
      getTaskClient,
      getAgentByName,
      listAgents,
      validateFilePath,
      resolveSavePath,
      writeFile,
      fileSize,
      filePartFromPath,
      textPart,
    },
  };
}

export function asyncIterFrom<T>(items: T[]): AsyncIterable<T> {
  return {
    async *[Symbol.asyncIterator]() {
      for (const i of items) yield i;
    },
  };
}
