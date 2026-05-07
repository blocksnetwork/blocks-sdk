import { useState, useEffect, useRef } from 'react';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism';
import type { TaskArtifact } from '../types.ts';

interface Props {
  artifact: TaskArtifact | null;
}

function formatDuration(ms?: number): string {
  if (ms === undefined || ms === null) return '--';
  return `${(ms / 1000).toFixed(1)}s`;
}

export default function ArtifactPanel({ artifact }: Props) {
  const [expanded, setExpanded] = useState(false);
  const [rawExpanded, setRawExpanded] = useState(false);
  const [prevArtifact, setPrevArtifact] = useState(artifact);
  const contentRef = useRef<HTMLDivElement>(null);
  const rawRef = useRef<HTMLDivElement>(null);

  // Reset to collapsed when a new artifact arrives
  if (artifact && artifact !== prevArtifact) {
    setPrevArtifact(artifact);
    setExpanded(false);
    setRawExpanded(false);
  }

  // Scroll into view when artifact panel expands
  useEffect(() => {
    if (expanded && contentRef.current) {
      contentRef.current.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }
  }, [expanded]);

  // Scroll into view when raw JSON expands
  useEffect(() => {
    if (rawExpanded && rawRef.current) {
      rawRef.current.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }
  }, [rawExpanded]);

  if (!artifact) return null;

  const rawJson = JSON.stringify(artifact, null, 2);

  const handleCopy = () => {
    navigator.clipboard.writeText(rawJson).catch((err) => {
      console.error('Failed to copy:', err);
    });
  };

  return (
    <div className="artifact-panel">
      <button
        className="artifact-toggle"
        onClick={() => setExpanded(!expanded)}
        type="button"
      >
        <span className="artifact-arrow">{expanded ? 'v' : '>'}</span>
        {' '}Artifact
      </button>

      {expanded && (
        <div ref={contentRef} className="artifact-content">
          <div className="artifact-summary">
            <div className="artifact-row">
              <span className="artifact-field">Session:</span>
              <span className="artifact-value">{artifact.sessionId}</span>
            </div>
            <div className="artifact-row artifact-row-grid">
              <span>
                <span className="artifact-field">Duration:</span>{' '}
                {formatDuration(artifact.durationMs)}
              </span>
              <span>
                <span className="artifact-field">Turns:</span>{' '}
                {artifact.numTurns ?? '--'}
              </span>
              <span>
                <span className="artifact-field">Cost:</span>{' '}
                {artifact.totalCostUsd !== undefined
                  ? `$${artifact.totalCostUsd.toFixed(2)}`
                  : '--'}
              </span>
            </div>
            <div className="artifact-row">
              <span className="artifact-field">Files changed:</span>{' '}
              {artifact.filesChanged?.length > 0
                ? artifact.filesChanged.join(', ')
                : '(none)'}
            </div>
            <div className="artifact-row artifact-row-grid">
              <span>
                <span className="artifact-field">Tool calls:</span>{' '}
                {artifact.toolCallCount}
              </span>
              <span>
                <span className="artifact-field">Bash commands:</span>{' '}
                {artifact.bashCommandCount}
              </span>
            </div>
          </div>

          <div className="artifact-raw">
            <button
              className="artifact-raw-toggle"
              onClick={() => setRawExpanded(!rawExpanded)}
              type="button"
            >
              {rawExpanded ? 'v' : '>'} Raw JSON
            </button>
            {rawExpanded && (
              <div ref={rawRef}>
                <button
                  className="artifact-copy-btn"
                  onClick={handleCopy}
                  type="button"
                >
                  Copy
                </button>
                <SyntaxHighlighter
                  language="json"
                  style={oneDark}
                  customStyle={{ margin: 0, borderRadius: '4px', fontSize: '0.85em' }}
                >
                  {rawJson}
                </SyntaxHighlighter>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
