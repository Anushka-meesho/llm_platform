import { useState, useCallback } from 'react';
import { usePersistentState } from './usePersistentState';
import type {
  TUIMessage,
  TUserUIMessage,
  TAssistantUIMessage,
  TContentPart,
  TApiMessage,
  TSessionDetail,
} from '../types';
import { DEFAULT_COMPARE_MODELS, MODELS } from '../types';
import { api, errorMessage } from '../api/client';
import { useToast } from '../toast/context';

const emptyConversations = (): Record<string, TUIMessage[]> =>
  Object.fromEntries(MODELS.map((m) => [m, []]));

const readFileAsDataUrl = (file: File): Promise<string> =>
  new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as string);
    reader.onerror = reject;
    reader.readAsDataURL(file);
  });

const buildApiContent = (
  msg: TUIMessage,
  modelName: string,
  includeImages = true,
): string | TContentPart[] => {
  if (
    includeImages &&
    msg.role === 'user' &&
    msg.images.length > 0 &&
    !modelName.startsWith('llama-groq')
  ) {
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
  const toast = useToast();
  // The conversation, chosen models, system prompt, and sampling all persist, so
  // the Compare workspace returns exactly as left after a reload. isLoading/error
  // are transient and stay in-memory.
  const [conversations, setConversations] = usePersistentState<Record<string, TUIMessage[]>>(
    'compare.conversations',
    emptyConversations,
  );
  const [sessionId, setSessionId] = usePersistentState<string | null>('compare.sessionId', null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selectedModels, setSelectedModels] = usePersistentState<string[]>(
    'compare.selectedModels',
    [...DEFAULT_COMPARE_MODELS],
  );
  const [temperature, setTemperature] = usePersistentState('compare.temperature', 0.7);
  const [systemPrompt, setSystemPrompt] = usePersistentState('compare.systemPrompt', '');
  const [maxOutputTokens, setMaxOutputTokens] = usePersistentState('compare.maxOutputTokens', 4096);

  const newChat = useCallback(() => {
    setConversations(emptyConversations());
    setSessionId(null);
    setSystemPrompt('');
    setError(null);
  }, [setConversations, setSessionId, setSystemPrompt]);

  const loadSession = useCallback((detail: TSessionDetail) => {
    const newConvs = emptyConversations();
    for (const turn of detail.turns) {
      for (const r of turn.results) {
        if (!newConvs[r.model]) newConvs[r.model] = [];
        const userMsg: TUserUIMessage = {
          role: 'user',
          content: turn.prompt,
          images: turn.images ?? [],
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
          model: r.model,
        };
        newConvs[r.model].push(userMsg, assistantMsg);
      }
    }
    const sessionModels = [
      ...new Set(detail.turns.flatMap((turn) => turn.results.map((r) => r.model))),
    ];
    setSelectedModels(sessionModels);
    setConversations(newConvs);
    setSessionId(detail.session_id);
    setSystemPrompt('');
    setError(null);
  }, [setSelectedModels, setConversations, setSessionId, setSystemPrompt]);

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

      // Snapshot conversations with the new user message for building API payload
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

      setIsLoading(true);

      try {
        const modelConvs: Record<string, TApiMessage[]> = {};
        for (const model of selectedModels) {
          const msgs = snapshotWithNewMsg[model] ?? [];
          modelConvs[model] = msgs.map((msg, idx) => ({
            role: msg.role,
            // Only attach images on the most recent user message — re-sending images
            // from every historical turn causes the image token count to balloon and
            // confuses vision models into returning empty responses.
            content: buildApiContent(msg, model, idx === msgs.length - 1),
          }));
        }

        // One call per model — update each column as its response arrives (first come first served)
        const perModelPromises = selectedModels.map(async (model) => {
          const result = await api.run({
            prompt: text,
            models: [model],
            model_conversations: { [model]: modelConvs[model] ?? [] },
            temperature,
            max_tokens: maxOutputTokens,
            session_id: sid,
            system_prompt: systemPrompt || undefined,
          });
          const r = result.results?.[0];
          if (r) {
            const assistantMsg: TAssistantUIMessage = {
              role: 'assistant',
              content: r.success ? (r.response ?? '') : `⚠️ ${r.error}`,
              latency_ms: r.latency_ms,
              total_tokens: r.total_tokens,
              cost_usd: r.cost_usd,
              success: r.success,
              run_id: result.run_id,
              model: r.model,
            };
            setConversations((prev) => ({
              ...prev,
              [model]: [...(prev[model] ?? []), assistantMsg],
            }));
          }
        });

        const outcomes = await Promise.allSettled(perModelPromises);
        const failedIndices = outcomes
          .map((o, i) => (o.status === 'rejected' ? i : -1))
          .filter((i) => i !== -1);

        if (failedIndices.length > 0) {
          const firstReason = (outcomes[failedIndices[0]] as PromiseRejectedResult).reason;
          const msg = errorMessage(firstReason);
          setError(msg);
          toast.error(msg);
          setConversations((prev) => {
            const next = { ...prev };
            for (const i of failedIndices) {
              const model = selectedModels[i];
              next[model] = (prev[model] ?? []).slice(0, -1);
            }
            return next;
          });
        }
      } catch (err) {
        const msg = errorMessage(err);
        setError(msg);
        toast.error(msg);
        setConversations((prev) => {
          const next = { ...prev };
          for (const model of selectedModels) {
            next[model] = (prev[model] ?? []).slice(0, -1);
          }
          return next;
        });
      } finally {
        setIsLoading(false);
      }
    },
    [
      sessionId,
      selectedModels,
      temperature,
      systemPrompt,
      maxOutputTokens,
      conversations,
      toast,
      setConversations,
      setSessionId,
    ],
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
    submitPrompt,
    loadSession,
    newChat,
  };
};
