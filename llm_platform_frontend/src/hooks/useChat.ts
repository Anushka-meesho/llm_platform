import { useState, useCallback } from 'react';
import type {
  TUIMessage,
  TUserUIMessage,
  TAssistantUIMessage,
  TContentPart,
  TApiMessage,
  TSessionDetail,
} from '../types';
import { MODELS } from '../types';
import { api } from '../api/client';
import { getContextWindow } from '../utils/tokens';

export type TContextUsage = Record<string, { used: number; window: number }>;

const emptyConversations = (): Record<string, TUIMessage[]> =>
  Object.fromEntries(MODELS.map((m) => [m, []]));

const readFileAsDataUrl = (file: File): Promise<string> =>
  new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as string);
    reader.onerror = reject;
    reader.readAsDataURL(file);
  });

const buildApiContent = (msg: TUIMessage): string | TContentPart[] => {
  if (msg.role === 'user' && msg.images.length > 0) {
    return [
      { type: 'text', text: msg.content },
      ...msg.images.map((url) => ({
        type: 'image_url' as const,
        image_url: { url },
      })),
    ];
  }
  return msg.content;
};


export const useChat = () => {
  const [conversations, setConversations] = useState<Record<string, TUIMessage[]>>(
    emptyConversations,
  );
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [loadingModels, setLoadingModels] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const [selectedModels, setSelectedModels] = useState<string[]>([...MODELS]);
  const [temperature, setTemperature] = useState(0.7);
  const [systemPrompt, setSystemPrompt] = useState('');
  const [maxOutputTokens, setMaxOutputTokens] = useState(1000);
  const [contextUsage, setContextUsage] = useState<TContextUsage>(() =>
    Object.fromEntries(MODELS.map((m) => [m, { used: 0, window: getContextWindow(m) }])),
  );

  const isLoading = loadingModels.size > 0;

  const newChat = useCallback(() => {
    setConversations(emptyConversations());
    setSessionId(null);
    setSystemPrompt('');
    setError(null);
    setContextUsage(
      Object.fromEntries(MODELS.map((m) => [m, { used: 0, window: getContextWindow(m) }])),
    );
  }, []);

  const loadSession = useCallback((detail: TSessionDetail) => {
    const newConvs = emptyConversations();
    for (const turn of detail.turns) {
      for (const r of turn.results) {
        if (!newConvs[r.model]) newConvs[r.model] = [];
        const userMsg: TUserUIMessage = {
          role: 'user',
          content: turn.prompt,
          images: [],
          systemPrompt: turn.system_prompt ?? undefined,
        };
        const assistantMsg: TAssistantUIMessage = {
          role: 'assistant',
          content: r.success ? (r.response ?? '') : `⚠️ ${r.error}`,
          latency_ms: r.latency_ms,
          total_tokens: r.total_tokens,
          cost_usd: r.cost_usd,
          success: r.success,
          run_id: turn.run_id,
          session_id: detail.session_id,
          rating: r.rating,
          note: r.note,
        };
        newConvs[r.model].push(userMsg, assistantMsg);
      }
    }
    const loadedUsage: TContextUsage = {};
    for (const model of Object.keys(newConvs)) {
      const msgs = newConvs[model];
      for (let i = msgs.length - 1; i >= 0; i--) {
        const msg = msgs[i];
        if (msg.role === 'assistant') {
          const win = getContextWindow(model);
          if (win > 0) loadedUsage[model] = { used: (msg as TAssistantUIMessage).total_tokens, window: win };
          break;
        }
      }
    }
    setContextUsage(loadedUsage);
    setConversations(newConvs);
    setSessionId(detail.session_id);
    setSystemPrompt('');
    setError(null);
  }, []);

  const submitPrompt = useCallback(
    async (text: string, files: File[]) => {
      if (!text.trim() && files.length === 0) return;

      setError(null);
      const sid = sessionId ?? crypto.randomUUID();
      if (!sessionId) setSessionId(sid);

      const imageDataUrls = await Promise.all(files.map(readFileAsDataUrl));

      const newUserMsg: TUserUIMessage = {
        role: 'user',
        content: text,
        images: imageDataUrls,
        systemPrompt: systemPrompt || undefined,
      };

      // Snapshot conversations with the new user message for building API payload.
      const snapshotWithNewMsg: Record<string, TUIMessage[]> = {};
      for (const model of selectedModels) {
        snapshotWithNewMsg[model] = [...(conversations[model] ?? []), newUserMsg];
      }

      setConversations((prev) => {
        const next = { ...prev };
        for (const model of selectedModels) {
          next[model] = [...(prev[model] ?? []), newUserMsg];
        }
        return next;
      });

      setLoadingModels(new Set(selectedModels));

      try {
        const modelConvs: Record<string, TApiMessage[]> = {};
        for (const model of selectedModels) {
          modelConvs[model] = (snapshotWithNewMsg[model] ?? []).map((msg) => ({
            role: msg.role,
            content: buildApiContent(msg),
          }));
        }

        let runId = '';

        await api.runStream(
          {
            prompt: text,
            models: selectedModels,
            model_conversations: modelConvs,
            temperature,
            max_tokens: maxOutputTokens,
            session_id: sid,
            system_prompt: systemPrompt || undefined,
          },
          (id) => { runId = id; },
          (mr) => {
            const assistantMsg: TAssistantUIMessage = {
              role: 'assistant',
              content: mr.success ? (mr.response ?? '') : `⚠️ ${mr.error}`,
              latency_ms: mr.latency_ms,
              total_tokens: mr.total_tokens,
              cost_usd: mr.cost_usd,
              success: mr.success,
              run_id: runId,
              session_id: sid,
            };
            setConversations((prev) => ({
              ...prev,
              [mr.model]: [...(prev[mr.model] ?? []), assistantMsg],
            }));
            setContextUsage((prev) => {
              if (mr.total_tokens <= 0) return prev;
              const win = mr.context_window > 0 ? mr.context_window : getContextWindow(mr.model);
              if (win <= 0) return prev;
              return { ...prev, [mr.model]: { used: mr.total_tokens, window: win } };
            });
            setLoadingModels((prev) => {
              const next = new Set(prev);
              next.delete(mr.model);
              return next;
            });
          },
        );
      } catch (err) {
        setError(
          err instanceof Error ? err.message : 'Backend unreachable. Is the Go server running on port 8000?',
        );
        // Roll back optimistic user messages for models that never got a response.
        setConversations((prev) => {
          const next = { ...prev };
          for (const model of selectedModels) {
            const msgs = next[model] ?? [];
            if (msgs.length > 0 && msgs[msgs.length - 1].role === 'user') {
              next[model] = msgs.slice(0, -1);
            }
          }
          return next;
        });
      } finally {
        setLoadingModels(new Set());
      }
    },
    [sessionId, selectedModels, temperature, systemPrompt, maxOutputTokens, conversations],
  );

  return {
    conversations,
    sessionId,
    isLoading,
    error,
    selectedModels,
    temperature,
    systemPrompt,
    setSelectedModels,
    setTemperature,
    setSystemPrompt,
    maxOutputTokens,
    setMaxOutputTokens,
    contextUsage,
    submitPrompt,
    loadSession,
    newChat,
  };
};
