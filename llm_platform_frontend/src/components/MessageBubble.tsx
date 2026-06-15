import { useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { Typography } from '@meesho/merlin-ui-tailwind';
import type { TUIMessage } from '../types';
import { api } from '../api/client';

type TMessageBubbleProps = {
  message: TUIMessage;
  model?: string;
};

const UserBubble = ({ message }: { message: Extract<TUIMessage, { role: 'user' }> }) => (
  <div className="flex flex-col items-end gap-1 mb-4">
    {message.systemPrompt && (
      <Typography variant="body" size="1" className="text-tertiary-text mr-1">
        🔧 System: {message.systemPrompt}
      </Typography>
    )}
    <div className="max-w-[85%] bg-secondary-bg border border-solid border-primary-border rounded-2xl rounded-tr-sm px-4 py-2.5">
      <Typography variant="body" size="3" className="text-primary-text">
        {message.content}
      </Typography>
    </div>
    {message.images.map((src, i) => (
      <img key={i} src={src} alt="attachment" className="rounded-lg" style={{ maxWidth: 220 }} />
    ))}
  </div>
);

const StarRating = ({
  runId,
  sessionId,
  model,
  initialStars,
  initialNote,
}: {
  runId: string;
  sessionId: string;
  model: string;
  initialStars: number | null | undefined;
  initialNote: string | null | undefined;
}) => {
  const [selected, setSelected] = useState<number | null>(initialStars ?? null);
  const [hovered, setHovered] = useState<number | null>(null);
  const [noteText, setNoteText] = useState(initialNote ?? '');
  const [savedValue, setSavedValue] = useState<number | null>(initialStars ?? null);

  const handleStar = async (n: number) => {
    setSelected(n);
    try {
      await api.saveRating({ run_id: runId, session_id: sessionId, model, rating: n, note: noteText });
      setSavedValue(n);
    } catch {
      // silent — rating is best-effort
    }
  };

  const handleNoteBlur = async () => {
    if (selected == null) return;
    try {
      await api.saveRating({ run_id: runId, session_id: sessionId, model, rating: selected, note: noteText });
    } catch {
      // best-effort
    }
  };

  const displayFill = hovered ?? selected ?? 0;

  return (
    <div className="flex flex-col gap-1 mt-1 w-full max-w-[95%]">
      <div className="flex items-center gap-1">
        <span className="text-[10px] text-tertiary-text mr-1">Rate response</span>
        {[1, 2, 3, 4, 5].map((n) => (
          <button
            key={n}
            onMouseEnter={() => setHovered(n)}
            onMouseLeave={() => setHovered(null)}
            onClick={() => handleStar(n)}
            className="text-base leading-none focus:outline-none cursor-pointer bg-transparent border-0 p-0"
            style={{ color: n <= displayFill ? '#F97316' : '#9CA3AF' }}
          >
            ★
          </button>
        ))}
        {savedValue != null && (
          <span className="text-[10px] ml-1 font-medium" style={{ color: '#F97316' }}>
            Saved {savedValue}/5
          </span>
        )}
      </div>
      <input
        type="text"
        value={noteText}
        onChange={(e) => setNoteText(e.target.value)}
        placeholder="Add a note (optional)..."
        onBlur={handleNoteBlur}
        className="text-[11px] rounded-md border border-solid border-primary-border bg-secondary-bg text-primary-text px-2 py-1 focus:outline-none"
      />
    </div>
  );
};

const AssistantBubble = ({
  message,
  model,
}: {
  message: Extract<TUIMessage, { role: 'assistant' }>;
  model?: string;
}) => (
  <div className="flex flex-col items-start gap-1 mb-4">
    <div
      className={`max-w-[95%] rounded-2xl rounded-tl-sm px-4 py-2.5 border border-solid ${
        message.success
          ? 'bg-primary-bg border-primary-border'
          : 'bg-error-bg border-error-border'
      }`}
    >
      <div className={`text-sm ${message.success ? 'text-primary-text' : 'text-error-text'}`}>
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          components={{
            h1: ({ children }) => <h1 className="text-xl font-bold mb-2">{children}</h1>,
            h2: ({ children }) => <h2 className="text-lg font-semibold mb-2">{children}</h2>,
            h3: ({ children }) => <h3 className="text-base font-semibold mb-1">{children}</h3>,
            p: ({ children }) => <p className="mb-2 last:mb-0">{children}</p>,
            strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
            em: ({ children }) => <em className="italic">{children}</em>,
            ul: ({ children }) => <ul className="list-disc list-inside mb-2 space-y-1">{children}</ul>,
            ol: ({ children }) => <ol className="list-decimal list-inside mb-2 space-y-1">{children}</ol>,
            li: ({ children }) => <li>{children}</li>,
            pre: ({ children }) => (
              <pre className="bg-secondary-bg p-3 rounded-md text-sm font-mono overflow-x-auto mb-2 whitespace-pre-wrap">
                {children}
              </pre>
            ),
            code: ({ className, children, node }) => {
              const isBlock =
                node?.position != null
                  ? node.position.start.line !== node.position.end.line
                  : Boolean(className);
              if (isBlock) {
                return <code className={className}>{children}</code>;
              }
              return (
                <code className="bg-secondary-bg px-1 py-0.5 rounded text-sm font-mono">
                  {children}
                </code>
              );
            },
            blockquote: ({ children }) => (
              <blockquote className="border-l-4 border-primary-border pl-3 text-secondary-text italic mb-2">
                {children}
              </blockquote>
            ),
            a: ({ href, children }) => (
              <a href={href} className="text-blue-500 underline" target="_blank" rel="noopener noreferrer">
                {children}
              </a>
            ),
            hr: () => <hr className="border-primary-border my-3" />,
            table: ({ children }) => (
              <div className="overflow-x-auto mb-2">
                <table className="min-w-full border-collapse text-sm">{children}</table>
              </div>
            ),
            th: ({ children }) => (
              <th className="border border-primary-border px-3 py-1 bg-secondary-bg font-semibold text-left">
                {children}
              </th>
            ),
            td: ({ children }) => (
              <td className="border border-primary-border px-3 py-1">{children}</td>
            ),
          }}
        >
          {message.content}
        </ReactMarkdown>
      </div>
    </div>
    <div className="flex gap-3 ml-1">
      <span className="text-[10px] text-tertiary-text">
        ⏱ {message.latency_ms > 0 ? `${message.latency_ms}ms` : '< 1ms'}
      </span>
      {message.total_tokens > 0 && (
        <span className="text-[10px] text-tertiary-text">🔢 {message.total_tokens} tok</span>
      )}
      {message.cost_usd > 0 && (
        <span className="text-[10px] text-tertiary-text">${message.cost_usd.toFixed(6)}</span>
      )}
    </div>
    {message.run_id && message.session_id && model && (
      <StarRating
        runId={message.run_id}
        sessionId={message.session_id}
        model={model}
        initialStars={message.rating}
        initialNote={message.note}
      />
    )}
  </div>
);

const MessageBubble = ({ message, model }: TMessageBubbleProps) => {
  if (message.role === 'user') return <UserBubble message={message} />;
  return <AssistantBubble message={message} model={model} />;
};

export default MessageBubble;
