import { useState, useEffect, useRef, useCallback } from 'react';
import type { ModelOption, ConversationEntry } from './types.ts';
import { useTask } from './hooks/useTask.ts';
import StatusBar from './components/StatusBar.tsx';
import ModelSelector from './components/ModelSelector.tsx';
import AgentSelector from './components/AgentSelector.tsx';
import CostTracker from './components/CostTracker.tsx';
import StreamOutput from './components/StreamOutput.tsx';
import ArtifactPanel from './components/ArtifactPanel.tsx';
import ConversationThread from './components/ConversationThread.tsx';
import PromptInput from './components/PromptInput.tsx';
import './App.css';

function App() {
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [cwd, setCwd] = useState('');
  const [model, setModel] = useState<ModelOption>('sonnet');
  const [agentName, setAgentName] = useState('claude-code-python');
  const [totalCost, setTotalCost] = useState(0);
  const [history, setHistory] = useState<ConversationEntry[]>([]);
  const [lastPrompt, setLastPrompt] = useState('');
  const [userScrolledUp, setUserScrolledUp] = useState(false);
  const [streaming, setStreaming] = useState(true);

  const task = useTask();
  const mainRef = useRef<HTMLElement>(null);
  const autoScrollRef = useRef(true);
  const lastScrollTopRef = useRef(0);
  const [prevArtifact, setPrevArtifact] = useState(task.artifact);

  const isActive = task.status === 'submitting'
    || task.status === 'queued'
    || task.status === 'running'
    || task.status === 'streaming'
    || task.status === 'completing';

  const isStreaming = task.status === 'streaming';

  // Extract sessionId and accumulate cost when a new artifact arrives
  if (task.artifact && task.artifact !== prevArtifact) {
    const art = task.artifact;
    setPrevArtifact(art);
    setSessionId(art.sessionId);
    if (art.totalCostUsd) {
      setTotalCost((prev) => prev + art.totalCostUsd!);
    }
  }

  // Detect user scrolling up to disengage auto-scroll
  const handleScroll = useCallback(() => {
    const el = mainRef.current;
    if (!el) return;
    const currentTop = el.scrollTop;
    const atBottom = el.scrollHeight - currentTop - el.clientHeight < 40;

    if (currentTop < lastScrollTopRef.current && !atBottom) {
      // User scrolled up
      autoScrollRef.current = false;
      setUserScrolledUp(true);
    } else if (atBottom) {
      autoScrollRef.current = true;
      setUserScrolledUp(false);
    }
    lastScrollTopRef.current = currentTop;
  }, []);

  // Auto-scroll when new content arrives (unless user scrolled up)
  useEffect(() => {
    if (autoScrollRef.current && mainRef.current && isActive) {
      mainRef.current.scrollTop = mainRef.current.scrollHeight;
    }
  }, [task.streamedText, task.artifact, isActive]);

  const jumpToLatest = useCallback(() => {
    if (mainRef.current) {
      mainRef.current.scrollTop = mainRef.current.scrollHeight;
    }
    autoScrollRef.current = true;
    setUserScrolledUp(false);
  }, []);

  const handleSubmit = (prompt: string) => {
    // Archive the completed turn before starting a new one
    if (task.streamedText && lastPrompt) {
      setHistory((prev) => [
        ...prev,
        { prompt: lastPrompt, response: task.streamedText, artifact: task.artifact },
      ]);
    }
    setLastPrompt(prompt);
    // Re-engage auto-scroll for the new task
    autoScrollRef.current = true;
    setUserScrolledUp(false);
    task.submitTask(prompt, agentName, sessionId ?? undefined, cwd || undefined, model, streaming);
  };

  const handleNewSession = () => {
    setSessionId(null);
    setTotalCost(0);
    setHistory([]);
    setLastPrompt('');
    task.reset();
    // CWD, model, and agentName persist across sessions
  };

  const handleAgentChange = (newAgent: string) => {
    setAgentName(newAgent);
    // Sessions are agent-specific; clear when switching agents
    handleNewSession();
  };

  return (
    <div className="app">
      <div className="app-sticky-top">
        <header className="app-header">
          <h1 className="app-title">Claude Code Agent</h1>
          <div className="app-header-controls">
            <AgentSelector
              value={agentName}
              onChange={handleAgentChange}
              disabled={isActive}
            />
            <ModelSelector
              value={model}
              onChange={setModel}
              disabled={isActive}
            />
            <CostTracker totalCost={totalCost} />
          </div>
        </header>

        <StatusBar
          status={task.status}
          startTime={task.startTime}
          elapsedMs={task.elapsedMs}
          artifact={task.artifact}
          error={task.error}
          retryCount={task.retryCount}
          maxRetries={task.maxRetries}
          onCancel={task.cancelTask}
          streaming={streaming}
          onStreamingChange={setStreaming}
        />
      </div>

      <main ref={mainRef} className="app-main" onScroll={handleScroll}>
        <ConversationThread entries={history} />
        {lastPrompt && task.status !== 'idle' && (
          <div className="conversation-entry conversation-entry-active">
            <div className="conversation-prompt">
              <span className="conversation-prompt-label">You</span>
              <div className="conversation-prompt-text">{lastPrompt}</div>
            </div>
            <div className="conversation-response">
              <StreamOutput
                text={task.streamedText}
                isStreaming={isStreaming}
              />
              <ArtifactPanel artifact={task.artifact} />
            </div>
          </div>
        )}
        {userScrolledUp && isActive && (
          <button
            className="jump-to-latest"
            onClick={jumpToLatest}
            type="button"
          >
            Jump to latest
          </button>
        )}
      </main>

      <footer className="app-footer">
        <PromptInput
          onSubmit={handleSubmit}
          disabled={isActive}
          cwd={cwd}
          onCwdChange={setCwd}
          sessionId={sessionId}
          onNewSession={handleNewSession}
        />
      </footer>
    </div>
  );
}

export default App;
