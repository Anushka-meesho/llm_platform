import { useState, useCallback } from 'react';
import type { TSessionSummary, TSessionDetail } from '../types';
import { api, errorMessage } from '../api/client';
import { useToast } from '../toast/context';

export const useSessions = () => {
  const toast = useToast();
  const [sessions, setSessions] = useState<TSessionSummary[]>([]);
  const [page, setPage] = useState(1);
  const [totalPages, setTotalPages] = useState(1);
  const [isLoading, setIsLoading] = useState(false);

  const fetchPage = useCallback(async (n: number) => {
    setIsLoading(true);
    try {
      const data = await api.listSessions(n);
      setSessions(data.sessions);
      setPage(data.page);
      setTotalPages(data.total_pages);
    } catch (e) {
      // Background/optional load — don't interrupt the user, but surface the
      // reason in the console for debugging.
      console.error('Failed to load sessions:', errorMessage(e));
    } finally {
      setIsLoading(false);
    }
  }, []);

  const deleteSession = useCallback(
    async (id: string) => {
      try {
        await api.deleteSessions([id]);
        setSessions((prev) => prev.filter((s) => s.session_id !== id));
      } catch (e) {
        // The user explicitly asked to delete — surface the failure.
        toast.error(errorMessage(e));
      }
    },
    [toast],
  );

  const loadSession = useCallback((id: string): Promise<TSessionDetail> => {
    return api.getSession(id);
  }, []);

  return { sessions, page, totalPages, isLoading, fetchPage, deleteSession, loadSession };
};
