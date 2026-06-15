import { useEffect, useRef } from 'react';
import { Typography, Spinner, cn } from '@meesho/merlin-ui-tailwind';
import type { TUIMessage, TModel } from '../types';
import { MODEL_LABELS } from '../types';
import type { TContextUsage } from '../hooks/useChat';
import { getContextWindow } from '../utils/tokens';
import MessageBubble from './MessageBubble';

type TModelColumnProps = {
  model: string;
  messages: TUIMessage[];
  isLoading: boolean;
  sessionId: string | null;
  contextUsage: TContextUsage;
};

function formatK(tokens: number): string {
  if (tokens >= 1000) return `${(tokens / 1000).toFixed(0)}k`;
  return `${tokens}`;
}

const ModelColumn = ({ model, messages, isLoading, sessionId, contextUsage }: TModelColumnProps) => {
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  const showTyping =
    isLoading &&
    messages.length > 0 &&
    messages[messages.length - 1].role === 'user';

  const used = contextUsage[model]?.used ?? 0;
  const win = (contextUsage[model]?.window ?? 0) > 0 ? contextUsage[model]!.window : getContextWindow(model);
  const pct = win > 0 ? Math.min(100, Math.round((used / win) * 100)) : 0;
  const remaining = win > 0 ? Math.max(0, win - used) : 0;

  return (
    <div className="flex flex-col flex-1 min-w-0 border-r border-solid border-primary-border last:border-r-0">
      <div className="sticky top-0 z-table-header bg-secondary-bg border-b border-solid border-primary-border px-4 py-2.5">
        <Typography variant="body" size="3" className="text-primary-text font-semi-bold">
          {MODEL_LABELS[model as TModel] ?? model}
        </Typography>

        {win > 0 && (
          <div className="mt-1.5">
            <div className="w-full h-1 bg-primary-bg rounded-full overflow-hidden">
              <div
                className={cn(
                  'h-full rounded-full transition-all duration-300',
                  pct >= 90 ? 'bg-red-500' : pct >= 75 ? 'bg-amber-400' : 'bg-interactive-border',
                )}
                style={{ width: `${pct}%` }}
              />
            </div>
            <Typography
              variant="body"
              size="3"
              className={cn(
                'text-[10px] mt-0.5',
                pct >= 90 ? 'text-red-400' : pct >= 75 ? 'text-amber-400' : 'text-tertiary-text',
              )}
            >
              {formatK(used)} used · {formatK(remaining)} remaining ({pct}%)
              {pct >= 90 && ' ⚠️'}
            </Typography>
          </div>
        )}
      </div>
      <div className="flex-1 overflow-y-auto px-4 py-4 bg-primary-bg">
        {messages.map((msg, i) => (
          <MessageBubble key={`${sessionId ?? 'new'}-${i}`} message={msg} model={model} />
        ))}
        {showTyping && (
          <div className="flex items-center gap-2 mb-4">
            <div className="bg-secondary-bg border border-solid border-primary-border rounded-2xl rounded-tl-sm px-4 py-2.5">
              <Spinner />
            </div>
          </div>
        )}
        <div ref={bottomRef} />
      </div>
    </div>
  );
};

export default ModelColumn;
