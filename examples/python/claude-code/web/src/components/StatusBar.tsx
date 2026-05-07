import { useState, useEffect } from 'react';
import type { TaskStatus, TaskArtifact } from '../types.ts';

interface Props {
  status: TaskStatus;
  startTime: number | null;
  elapsedMs: number | null;
  artifact: TaskArtifact | null;
  error: string | null;
  retryCount: number | null;
  maxRetries: number | null;
  onCancel: () => void;
  streaming: boolean;
  onStreamingChange: (enabled: boolean) => void;
}

const STATUS_CONFIG: Record<TaskStatus, { label: string; className: string }> = {
  idle: { label: 'Ready', className: 'status-idle' },
  submitting: { label: 'Submitting...', className: 'status-active' },
  queued: { label: 'Queued', className: 'status-queued' },
  running: { label: 'Running', className: 'status-active' },
  streaming: { label: 'Streaming', className: 'status-streaming' },
  completing: { label: 'Completing...', className: 'status-active' },
  completed: { label: 'Completed', className: 'status-completed' },
  failed: { label: 'Failed', className: 'status-failed' },
  canceled: { label: 'Canceled', className: 'status-failed' },
};

function formatElapsed(ms: number): string {
  const secs = ms / 1000;
  if (secs < 60) return `${secs.toFixed(1)}s`;
  const mins = Math.floor(secs / 60);
  const remainSecs = secs % 60;
  return `${mins}m ${remainSecs.toFixed(0)}s`;
}

export default function StatusBar({ status, startTime, elapsedMs, artifact, error, retryCount, maxRetries, onCancel, streaming, onStreamingChange }: Props) {
  const [liveElapsed, setLiveElapsed] = useState<number | null>(null);
  const [wasActive, setWasActive] = useState(false);

  const isActive = status === 'submitting' || status === 'queued'
    || status === 'running' || status === 'streaming' || status === 'completing';

  // Reset liveElapsed on active -> inactive transition
  if (!isActive && wasActive) {
    setWasActive(false);
    setLiveElapsed(null);
  } else if (isActive && !wasActive) {
    setWasActive(true);
  }

  useEffect(() => {
    if (isActive && startTime !== null) {
      const id = setInterval(() => {
        setLiveElapsed(Date.now() - startTime);
      }, 100);
      return () => clearInterval(id);
    }
  }, [isActive, startTime]);

  const config = STATUS_CONFIG[status];
  const displayElapsed = elapsedMs ?? liveElapsed;
  const cost = artifact?.totalCostUsd;

  return (
    <div className={`status-bar ${config.className}`}>
      <span className="status-indicator" />
      <span className="status-label">{config.label}</span>
      {status === 'queued' && retryCount !== null && (
        <span className="status-retry">
          (retry {retryCount}/{maxRetries ?? '?'})
        </span>
      )}
      {error && status === 'failed' && (
        <span className="status-error">: {error}</span>
      )}
      {displayElapsed !== null && (
        <span className="status-elapsed">{formatElapsed(displayElapsed)}</span>
      )}
      {cost !== undefined && cost !== null && (
        <span className="status-cost">${cost.toFixed(2)}</span>
      )}
      {isActive && (
        <button
          className="status-cancel-btn"
          onClick={onCancel}
          type="button"
        >
          Cancel
        </button>
      )}
      <label className="status-stream-toggle">
        <input
          type="checkbox"
          checked={streaming}
          onChange={(e) => onStreamingChange(e.target.checked)}
          disabled={isActive}
        />
        Stream
      </label>
    </div>
  );
}
