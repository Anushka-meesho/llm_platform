import { useState } from 'react';
import { Typography } from '@meesho/merlin-ui-tailwind';
import type { TUIMessage } from '../types';
import ModelColumn from './ModelColumn';
import LeaderboardModal from './LeaderboardModal';

type TChatAreaProps = {
  conversations: Record<string, TUIMessage[]>;
  selectedModels: string[];
  isLoading: boolean;
  sessionId: string | null;
};

const ChatArea = ({ conversations, selectedModels, isLoading, sessionId }: TChatAreaProps) => {
  const [showLeaderboard, setShowLeaderboard] = useState(false);

  if (selectedModels.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center bg-primary-bg">
        <Typography variant="body" size="3" className="text-tertiary-text">
          Select at least one model from the sidebar.
        </Typography>
      </div>
    );
  }

  const hasMessages = selectedModels.some(
    (m) => (conversations[m] ?? []).length > 0,
  );

  if (!hasMessages) {
    return (
      <div className="flex-1 flex items-center justify-center bg-primary-bg">
        <Typography variant="body" size="3" className="text-tertiary-text">
          Type a message below to start comparing models.
        </Typography>
      </div>
    );
  }

  return (
    <>
      <div className="flex items-center justify-end px-4 py-1.5 bg-secondary-bg border-b border-solid border-primary-border">
        <button
          onClick={() => setShowLeaderboard(true)}
          disabled={!sessionId}
          className="flex items-center gap-1.5 text-[11px] font-medium px-2.5 py-1 rounded-lg border border-solid border-primary-border bg-primary-bg text-primary-text hover:bg-secondary-bg disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer focus:outline-none transition-colors"
        >
          🏆 Leaderboard
        </button>
      </div>

      <div className="flex flex-1 overflow-hidden">
        {selectedModels.map((model) => (
          <ModelColumn
            key={model}
            model={model}
            messages={conversations[model] ?? []}
            isLoading={isLoading}
          />
        ))}
      </div>

      {showLeaderboard && sessionId && (
        <LeaderboardModal sessionId={sessionId} onClose={() => setShowLeaderboard(false)} />
      )}
    </>
  );
};

export default ChatArea;
