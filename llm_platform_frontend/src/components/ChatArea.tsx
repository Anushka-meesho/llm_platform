import { Typography } from '@meesho/merlin-ui-tailwind';
import type { TUIMessage } from '../types';
import ModelColumn from './ModelColumn';

type TChatAreaProps = {
  conversations: Record<string, TUIMessage[]>;
  selectedModels: string[];
  isLoading: boolean;
};

const ChatArea = ({ conversations, selectedModels, isLoading }: TChatAreaProps) => {
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
  );
};

export default ChatArea;
