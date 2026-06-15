import { useEffect, useCallback, useState } from 'react';
import { cn } from '@meesho/merlin-ui-tailwind';
import { useChat } from './hooks/useChat';
import { useSessions } from './hooks/useSessions';
import { api } from './api/client';
import { setLiveRates } from './utils/tokens';
import Sidebar from './components/Sidebar';
import ChatArea from './components/ChatArea';
import SystemPromptBar from './components/SystemPromptBar';
import ChatInput from './components/ChatInput';

const App = () => {
  const chat = useChat();
  const sessions = useSessions();
  const [sidebarOpen, setSidebarOpen] = useState(true);

  useEffect(() => {
    sessions.fetchPage(1);
  }, [sessions.fetchPage]);

  useEffect(() => {
    api.fetchPricing().then(setLiveRates).catch(() => { /* silently use hardcoded fallbacks */ });
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
    <div className="flex h-screen overflow-hidden bg-primary-bg">
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
          contextUsage={chat.contextUsage}
        />

        {chat.error && (
          <div className="px-4 py-2 bg-red-950 border-t border-red-800 text-red-300 text-sm">
            ⚠️ {chat.error}
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
          contextUsage={chat.contextUsage}
        />
      </div>
    </div>
  );
};

export default App;
