import type { ConversationEntry } from '../types.ts';
import StreamOutput from './StreamOutput.tsx';
import ArtifactPanel from './ArtifactPanel.tsx';

interface Props {
  entries: ConversationEntry[];
}

export default function ConversationThread({ entries }: Props) {
  if (entries.length === 0) return null;

  return (
    <div className="conversation-thread">
      {entries.map((entry, i) => (
        <div key={i} className="conversation-entry">
          <div className="conversation-prompt">
            <span className="conversation-prompt-label">You</span>
            <div className="conversation-prompt-text">{entry.prompt}</div>
          </div>
          <div className="conversation-response">
            <StreamOutput text={entry.response} isStreaming={false} />
            <ArtifactPanel artifact={entry.artifact} />
          </div>
          <hr className="conversation-divider" />
        </div>
      ))}
    </div>
  );
}
