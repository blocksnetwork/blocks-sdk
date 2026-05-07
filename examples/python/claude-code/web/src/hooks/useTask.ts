import { useState, useRef, useEffect, useCallback } from 'react';
import type PubNub from 'pubnub';
import type { TaskStatus, TaskArtifact, ModelOption } from '../types.ts';
import { sendTask, cancelTask as cancelTaskApi } from '../lib/facade.ts';
import { decodeArtifact } from '../lib/artifact.ts';
import { getPubNub, getOwnerId } from './usePubNub.ts';

/**
 * Internal state shape for the task lifecycle.
 */
interface TaskState {
  status: TaskStatus;
  taskId: string | null;
  streamedText: string;
  artifact: TaskArtifact | null;
  error: string | null;
  startTime: number | null;
  elapsedMs: number | null;
  retryCount: number | null;
  maxRetries: number | null;
}

const INITIAL_STATE: TaskState = {
  status: 'idle',
  taskId: null,
  streamedText: '',
  artifact: null,
  error: null,
  startTime: null,
  elapsedMs: null,
  retryCount: null,
  maxRetries: null,
};

/**
 * Loosely typed PubNub message payloads from the status and stream channels.
 * These match the message shapes published by the Blocks backend.
 */
interface StreamDataMessage {
  type: 'stream_data';
  chunks?: string[];
}

interface StreamEntry {
  channel: string;
  direction: string;
  format: string;
  token: string;
}

interface ProgressMessage {
  type: 'progress';
  state?: string;
  streamEvent?: string;
  streams?: Record<string, StreamEntry>;
}

interface ArtifactMessage {
  type: 'artifact';
  artifactRef: {
    kind: 'inline' | 'file';
    mimeType: string;
    size: number;
    data?: string;
    fileUrl?: string;
    fileId?: string;
    fileName?: string;
  };
}

interface TerminalMessage {
  type: 'terminal';
  state?: TaskStatus;
}

interface SystemMessage {
  type: 'system';
  status?: string;
  retryCount?: number;
  maxRetries?: number;
}

type StatusChannelMessage = ProgressMessage | ArtifactMessage | TerminalMessage | SystemMessage;

/**
 * Main task orchestration hook.
 *
 * Manages the full task lifecycle:
 *   submit -> subscribe -> stream -> artifact -> terminal -> cleanup
 *
 * Returns the current task state and a submitTask function.
 */
export function useTask() {
  const [state, setState] = useState<TaskState>(INITIAL_STATE);
  const listenerRef = useRef<PubNub.Listener | null>(null);
  const channelsRef = useRef<string[]>([]);
  const startTimeRef = useRef<number | null>(null);

  /**
   * Clean up PubNub listener and channel subscriptions.
   */
  const cleanup = useCallback(() => {
    const pubnub = getPubNub();
    if (listenerRef.current) {
      pubnub.removeListener(listenerRef.current);
      listenerRef.current = null;
    }
    if (channelsRef.current.length > 0) {
      pubnub.unsubscribe({ channels: channelsRef.current });
      channelsRef.current = [];
    }
  }, []);

  /**
   * Submit a new task to the A2A facade and begin streaming.
   */
  const submitTask = useCallback(
    async (
      prompt: string,
      agentName: string,
      sessionId?: string,
      cwd?: string,
      model?: ModelOption,
      streaming?: boolean,
    ) => {
      // Reset state for new submission
      const now = Date.now();
      startTimeRef.current = now;
      setState({
        status: 'submitting',
        taskId: null,
        streamedText: '',
        artifact: null,
        error: null,
        startTime: now,
        elapsedMs: null,
        retryCount: null,
        maxRetries: null,
      });

      try {
        const result = await sendTask({
          prompt,
          ownerId: getOwnerId(),
          agentName,
          sessionId,
          cwd,
          model,
        });

        setState((s) => ({ ...s, taskId: result.taskId }));

        const initialStatus: TaskStatus = result.queued ? 'queued' : 'running';

        const channels = result.extensions?.blocks?.streamChannels;
        if (!channels || !channels.status) {
          // No streaming channels returned -- edge case where agent is
          // not registered for streaming.
          setState((s) => ({ ...s, status: initialStatus }));
          return;
        }

        const statusCh = channels.status;
        let streamCh = channels.stream; // May be set later via stream_started
        const wantStream = streaming !== false;
        channelsRef.current = streamCh && wantStream ? [statusCh, streamCh] : [statusCh];

        // Set up PubNub listener
        const pubnub = getPubNub();
        const listener: PubNub.Listener = {
          message: (event) => {
            const msg = event.message as Record<string, unknown>;
            const msgType = msg.type as string;

            // Stream channel: token-level chunks
            if (streamCh && event.channel === streamCh && msgType === 'stream_data') {
              const streamMsg = msg as unknown as StreamDataMessage;
              const text = (streamMsg.chunks || []).join('');
              if (text) {
                setState((s) => ({
                  ...s,
                  streamedText: s.streamedText + text,
                  // Auto-transition to streaming on first chunk received.
                  // The stream_started progress event may arrive before the
                  // client subscribes (race condition), so receiving actual
                  // stream data is the definitive signal.
                  status: s.status === 'running' || s.status === 'queued' || s.status === 'submitting'
                    ? 'streaming'
                    : s.status,
                }));
              }
            }

            // Status channel: lifecycle events
            if (event.channel === statusCh) {
              const statusMsg = msg as unknown as StatusChannelMessage;

              if (statusMsg.type === 'progress') {
                if (statusMsg.state === 'running') {
                  setState((s) => ({ ...s, status: 'running' }));
                }
                if (statusMsg.streamEvent === 'stream_started') {
                  // Dynamically subscribe to stream channel from stream_started event.
                  // streams is an object keyed by streamId: { "sid": { channel, token, ... } }
                  const progressMsg = statusMsg as unknown as ProgressMessage;
                  if (wantStream && !streamCh && progressMsg.streams) {
                    const streamIds = Object.keys(progressMsg.streams);
                    if (streamIds.length > 0) {
                      const entry = progressMsg.streams[streamIds[0]];
                      streamCh = entry.channel;
                      if (entry.token) {
                        pubnub.setToken(entry.token);
                      }
                      channelsRef.current = [...channelsRef.current, streamCh];
                      pubnub.subscribe({ channels: [streamCh] });
                    }
                  }
                  setState((s) => ({ ...s, status: 'streaming' }));
                }
                if (statusMsg.streamEvent === 'stream_ended') {
                  setState((s) => ({ ...s, status: 'completing' }));
                }
              }

              if (statusMsg.type === 'artifact') {
                decodeArtifact(statusMsg.artifactRef)
                  .then((artifact) => {
                    setState((s) => ({ ...s, artifact }));
                  })
                  .catch((err) => {
                    console.error('Failed to decode artifact:', err);
                    setState((s) => ({
                      ...s,
                      error: `Artifact decode failed: ${String(err)}`,
                    }));
                  });
              }

              if (statusMsg.type === 'system') {
                if (statusMsg.status === 'retry') {
                  setState((s) => ({
                    ...s,
                    status: 'queued',
                    retryCount: statusMsg.retryCount ?? null,
                    maxRetries: statusMsg.maxRetries ?? null,
                  }));
                }
              }

              if (statusMsg.type === 'terminal') {
                const elapsed = startTimeRef.current
                  ? Date.now() - startTimeRef.current
                  : null;
                const terminalStatus = statusMsg.state || 'completed';
                setState((s) => ({
                  ...s,
                  status: terminalStatus,
                  elapsedMs: elapsed,
                }));
                cleanup();
              }
            }
          },
        };

        listenerRef.current = listener;
        pubnub.addListener(listener);
        pubnub.subscribe({ channels: channelsRef.current });
        setState((s) => ({ ...s, status: initialStatus }));
      } catch (err) {
        setState((s) => ({
          ...s,
          status: 'failed',
          error: err instanceof Error ? err.message : String(err),
        }));
      }
    },
    [cleanup],
  );

  /**
   * Request cancellation of the current task via the A2A facade.
   * The framework sends CancelTask to the agent, which cooperatively
   * stops and returns a cancel artifact. The terminal/canceled event
   * arrives on the status channel and is handled by the existing listener.
   */
  const cancelTask = useCallback(async () => {
    const taskId = state.taskId;
    if (!taskId) return;
    try {
      await cancelTaskApi(taskId);
    } catch (err) {
      console.error('Failed to cancel task:', err);
    }
  }, [state.taskId]);

  /**
   * Reset task state back to idle. Used by New Session to clear
   * streamed output, artifact, and error while preserving app-level
   * state like cwd and model.
   */
  const reset = useCallback(() => {
    cleanup();
    setState(INITIAL_STATE);
    startTimeRef.current = null;
  }, [cleanup]);

  // Cleanup on unmount
  useEffect(() => {
    return cleanup;
  }, [cleanup]);

  return {
    status: state.status,
    taskId: state.taskId,
    streamedText: state.streamedText,
    artifact: state.artifact,
    error: state.error,
    startTime: state.startTime,
    elapsedMs: state.elapsedMs,
    retryCount: state.retryCount,
    maxRetries: state.maxRetries,
    submitTask,
    cancelTask,
    reset,
  };
}
