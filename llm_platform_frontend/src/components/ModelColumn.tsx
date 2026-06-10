import { useEffect, useRef } from 'react';
import { Typography, Spinner } from '@meesho/merlin-ui-tailwind';
import type { TUIMessage } from '../types';
import MessageBubble from './MessageBubble';

type TModelColumnProps = {
  model: string;
  messages: TUIMessage[];
  isLoading: boolean;
};

const ModelColumn = ({ model, messages, isLoading }: TModelColumnProps) => {
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  const showTyping =
    isLoading &&
    messages.length > 0 &&
    messages[messages.length - 1].role === 'user';

  return (
    <div className="flex flex-col flex-1 min-w-0 border-r border-solid border-primary-border last:border-r-0">
      <div className="sticky top-0 z-table-header bg-secondary-bg border-b border-solid border-primary-border px-4 py-2.5">
        <Typography variant="body" size="3" className="text-primary-text font-semi-bold">
          {model}
        </Typography>
      </div>
      <div className="flex-1 overflow-y-auto px-4 py-4 bg-primary-bg">
        {messages.map((msg, i) => (
          <MessageBubble key={i} message={msg} />
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
