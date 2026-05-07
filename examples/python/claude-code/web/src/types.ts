export type TaskStatus =
  | 'idle'
  | 'submitting'
  | 'queued'
  | 'running'
  | 'streaming'
  | 'completing'
  | 'completed'
  | 'failed'
  | 'canceled';

export interface ArtifactRef {
  kind: 'inline' | 'file';
  mimeType: string;
  size: number;
  data?: string;       // base64, for kind=inline
  fileUrl?: string;    // CDN URL, for kind=file
  fileId?: string;
  fileName?: string;
}

export interface TaskArtifact {
  ok: boolean;
  text: string;
  sessionId: string;
  filesChanged: string[];
  toolCallCount: number;
  bashCommandCount: number;
  durationMs?: number;
  numTurns?: number;
  totalCostUsd?: number;
}

export interface SendMessageResult {
  taskId: string;
  idempotent: boolean;
  queued: boolean;
  warnings?: string[];
  extensions: {
    blocks: {
      streamChannels?: {
        status: string;
        stream?: string;
      };
      readToken?: string;
    };
  };
}

export type ModelOption = 'sonnet' | 'opus' | 'haiku';

export interface AgentOption {
  label: string;
  agentName: string;
}

export const AGENT_OPTIONS: AgentOption[] = [
  { label: 'Python', agentName: 'claude-code-python' },
  { label: 'Node', agentName: 'claude-code-node' },
];

export interface ConversationEntry {
  prompt: string;
  response: string;
  artifact: TaskArtifact | null;
}
