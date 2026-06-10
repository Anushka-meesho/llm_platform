import { Typography } from '@meesho/merlin-ui-tailwind';
import type { TUIMessage } from '../types';

type TMessageBubbleProps = {
  message: TUIMessage;
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

const AssistantBubble = ({
  message,
}: {
  message: Extract<TUIMessage, { role: 'assistant' }>;
}) => (
  <div className="flex flex-col items-start gap-1 mb-4">
    <div
      className={`max-w-[95%] rounded-2xl rounded-tl-sm px-4 py-2.5 border border-solid ${
        message.success
          ? 'bg-primary-bg border-primary-border'
          : 'bg-error-bg border-error-border'
      }`}
    >
      <Typography
        variant="body"
        size="3"
        className={message.success ? 'text-primary-text' : 'text-error-text'}
      >
        {message.content}
      </Typography>
    </div>
    <div className="flex gap-3 ml-1">
      {message.latency_ms > 0 && (
        <span className="text-[10px] text-tertiary-text">⏱ {message.latency_ms}ms</span>
      )}
      {message.total_tokens > 0 && (
        <span className="text-[10px] text-tertiary-text">🔢 {message.total_tokens} tok</span>
      )}
      {message.cost_usd != null && (
        <span className="text-[10px] text-tertiary-text">${message.cost_usd.toFixed(6)}</span>
      )}
    </div>
  </div>
);

const MessageBubble = ({ message }: TMessageBubbleProps) => {
  if (message.role === 'user') return <UserBubble message={message} />;
  return <AssistantBubble message={message} />;
};

export default MessageBubble;
