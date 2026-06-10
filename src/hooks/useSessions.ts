import { useState, useCallback } from 'react';
import type { TSessionSummary, TSessionDetail } from '../types';
import { api } from '../api/client';

export const useSessions = () => {
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
    } catch {
      // silent — backend may not be running yet
    } finally {
      setIsLoading(false);
    }
  }, []);

  const deleteSession = useCallback(
    async (id: string) => {
      await api.deleteSessions([id]);
      setSessions((prev) => prev.filter((s) => s.session_id !== id));
    },
    [],
  );

  const loadSession = useCallback((id: string): Promise<TSessionDetail> => {
    return api.getSession(id);
  }, []);

  return { sessions, page, totalPages, isLoading, fetchPage, deleteSession, loadSession };
};
