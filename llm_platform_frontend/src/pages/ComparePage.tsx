import { useEffect, useCallback, useState } from 'react';
import { Typography, cn } from '@meesho/merlin-ui-tailwind';
import { api } from '../api/client';
import { useChat } from '../hooks/useChat';
import { useSessions } from '../hooks/useSessions';
import Sidebar from '../components/Sidebar';
import ChatArea from '../components/ChatArea';
import SystemPromptBar from '../components/SystemPromptBar';
import ChatInput from '../components/ChatInput';

// ComparePage is the multi-model chat comparison view (the platform's core).
const ComparePage = () => {
  const chat = useChat();
  const sessions = useSessions();
  const [sidebarOpen, setSidebarOpen] = useState(true);
  // The playground runs under the built-in "playground" task, so its configured
  // image upload limits apply here. Best-effort: the input works without them.
  const [imageLimits, setImageLimits] = useState({ maxImageKB: 0, maxImages: 0 });

  useEffect(() => {
    sessions.fetchPage(1);
  }, [sessions.fetchPage]);

  useEffect(() => {
    api
      .getTask('playground')
      .then((t) =>
        setImageLimits({ maxImageKB: t.max_image_kb ?? 0, maxImages: t.max_images ?? 0 }),
      )
      .catch(() => {}); // limits are optional; ignore failures
  }, []);

  const handleSubmit = useCallback(
    async (text: string, files: File[]) => {
      await chat.submitPrompt(text, files);
      await sessions.fetchPage(sessions.page);
    },
    [chat.submitPrompt, sessions.fetchPage, sessions.page],
  );

  const handleDeleteSession = useCallback(
    async (id: string) => {
      await sessions.deleteSession(id);
      if (chat.sessionId === id) {
        chat.newChat();
      }
    },
    [sessions.deleteSession, chat.sessionId, chat.newChat],
  );

  return (
    <div className="flex flex-1 overflow-hidden">
      <div
        className={cn(
          'transition-all duration-200 overflow-hidden flex-shrink-0 h-full',
          sidebarOpen ? 'w-64' : 'w-12',
        )}
      >
        <Sidebar
          selectedModels={chat.selectedModels}
          setSelectedModels={chat.setSelectedModels}
          temperature={chat.temperature}
          setTemperature={chat.setTemperature}
          sessions={sessions.sessions}
          page={sessions.page}
          totalPages={sessions.totalPages}
          sessionsLoading={sessions.isLoading}
          currentSessionId={chat.sessionId}
          isOpen={sidebarOpen}
          onToggle={() => setSidebarOpen((prev) => !prev)}
          onNewChat={chat.newChat}
          onLoadSession={sessions.loadSession}
          onSessionLoaded={chat.loadSession}
          onDeleteSession={handleDeleteSession}
          onFetchPage={sessions.fetchPage}
        />
      </div>

      <div className="flex flex-col flex-1 overflow-hidden">
        <ChatArea
          conversations={chat.conversations}
          selectedModels={chat.selectedModels}
          isLoading={chat.isLoading}
          sessionId={chat.sessionId}
        />

        {chat.error && (
          <div className="px-4 py-2 bg-error-bg border-t border-solid border-error-border">
            <Typography variant="body" size="3" className="text-error-text">
              {chat.error}
            </Typography>
          </div>
        )}

        <SystemPromptBar
          systemPrompt={chat.systemPrompt}
          setSystemPrompt={chat.setSystemPrompt}
        />

        <ChatInput
          onSubmit={handleSubmit}
          isLoading={chat.isLoading}
          selectedModels={chat.selectedModels}
          systemPrompt={chat.systemPrompt}
          conversations={chat.conversations}
          maxOutputTokens={chat.maxOutputTokens}
          setMaxOutputTokens={chat.setMaxOutputTokens}
          maxImageKB={imageLimits.maxImageKB}
          maxImages={imageLimits.maxImages}
        />
      </div>
    </div>
  );
};

export default ComparePage;
