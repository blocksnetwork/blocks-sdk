import ReactMarkdown from 'react-markdown';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism';

interface Props {
  text: string;
  isStreaming: boolean;
}

export default function StreamOutput({ text, isStreaming }: Props) {
  if (!text) return null;

  return (
    <div className="stream-output">
      <ReactMarkdown
        components={{
          code({ className, children, ...props }) {
            const match = /language-(\w+)/.exec(className || '');
            const codeString = String(children).replace(/\n$/, '');
            if (match) {
              return (
                <SyntaxHighlighter
                  language={match[1]}
                  style={oneDark}
                  customStyle={{ margin: 0, borderRadius: '4px' }}
                >
                  {codeString}
                </SyntaxHighlighter>
              );
            }
            return (
              <code className={className} {...props}>
                {children}
              </code>
            );
          },
        }}
      >
        {text}
      </ReactMarkdown>
      {isStreaming && <span className="cursor-blink">|</span>}
    </div>
  );
}
