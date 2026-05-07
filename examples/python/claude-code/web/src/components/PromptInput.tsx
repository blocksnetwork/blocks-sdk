import { useState } from 'react';
import type { KeyboardEvent } from 'react';

interface Props {
  onSubmit: (prompt: string) => void;
  disabled: boolean;
  cwd: string;
  onCwdChange: (cwd: string) => void;
  sessionId: string | null;
  onNewSession: () => void;
}

export default function PromptInput({
  onSubmit,
  disabled,
  cwd,
  onCwdChange,
  sessionId,
  onNewSession,
}: Props) {
  const [prompt, setPrompt] = useState('');

  const handleSubmit = () => {
    const trimmed = prompt.trim();
    if (!trimmed || disabled) return;
    onSubmit(trimmed);
    setPrompt('');
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.ctrlKey || e.shiftKey) && e.key === 'Enter') {
      e.preventDefault();
      handleSubmit();
    }
  };

  return (
    <div className="prompt-input">
      {sessionId && (
        <div className="prompt-session">
          Session: {sessionId}
        </div>
      )}
      <div className="prompt-cwd-row">
        <label className="prompt-cwd-label" htmlFor="cwd-input">
          Working directory:
        </label>
        <input
          id="cwd-input"
          className="prompt-cwd-input"
          type="text"
          value={cwd}
          onChange={(e) => onCwdChange(e.target.value)}
          placeholder="/path/to/repo"
          disabled={disabled}
        />
      </div>
      <textarea
        className="prompt-textarea"
        value={prompt}
        onChange={(e) => setPrompt(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder="Enter your prompt... (Ctrl+Enter to send)"
        disabled={disabled}
        rows={4}
      />
      <div className="prompt-actions">
        <button
          className="prompt-send-btn"
          onClick={handleSubmit}
          disabled={disabled || !prompt.trim()}
          type="button"
        >
          Send
        </button>
        <button
          className="prompt-new-session-btn"
          onClick={onNewSession}
          disabled={disabled}
          type="button"
        >
          New Session
        </button>
      </div>
    </div>
  );
}
