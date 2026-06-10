import { useState } from 'react';
import { Typography, TextArea } from '@meesho/merlin-ui-tailwind';

type TSystemPromptBarProps = {
  systemPrompt: string;
  setSystemPrompt: (value: string) => void;
};

const SystemPromptBar = ({ systemPrompt, setSystemPrompt }: TSystemPromptBarProps) => {
  const [open, setOpen] = useState(false);

  const preview = systemPrompt
    ? systemPrompt.length > 80
      ? systemPrompt.slice(0, 80) + '…'
      : systemPrompt
    : 'No system prompt set';

  return (
    <div className="border-t border-solid border-primary-border bg-primary-bg">
      <button
        onClick={() => setOpen((prev) => !prev)}
        className="w-full flex items-center gap-2 px-4 py-2.5 text-left hover:bg-tertiary-bg transition-colors"
      >
        <svg
          width="15"
          height="15"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          className="text-tertiary-text flex-shrink-0"
        >
          <circle cx="12" cy="12" r="3" />
          <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9z" />
        </svg>

        <Typography variant="body" size="2" className="text-secondary-text font-semi-bold">
          System Prompt
        </Typography>

        {!open && (
          <Typography
            variant="body"
            size="2"
            className="ml-auto text-tertiary-text italic truncate max-w-xs"
          >
            {preview}
          </Typography>
        )}

        <svg
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          className={`ml-auto flex-shrink-0 text-tertiary-text transition-transform ${open ? 'rotate-180' : ''}`}
        >
          <polyline points="6 9 12 15 18 9" />
        </svg>
      </button>

      {open && (
        <div className="px-4 pb-3">
          <TextArea
            value={systemPrompt}
            onChange={({ value }) => setSystemPrompt(value)}
            placeholder="You are a helpful assistant..."
            rows={3}
            wrapperClassName="w-full"
          />
        </div>
      )}
    </div>
  );
};

export default SystemPromptBar;
