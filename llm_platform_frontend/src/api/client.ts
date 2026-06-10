import type {
  TRunRequest,
  TRunResponse,
  TSessionListResponse,
  TSessionDetail,
} from '../types';

const BASE = '';

async function fetchJSON<T>(
  url: string,
  options?: RequestInit,
  timeoutMs = 10_000,
): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(url, { ...options, signal: controller.signal });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      throw new Error((data as { detail?: string }).detail ?? `HTTP ${res.status}`);
    }
    return res.json() as Promise<T>;
  } finally {
    clearTimeout(timer);
  }
}

export const api = {
  run: (payload: TRunRequest) =>
    fetchJSON<TRunResponse>(
      `${BASE}/run`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      },
      60_000,
    ),

  listSessions: (page = 1, pageSize = 8) =>
    fetchJSON<TSessionListResponse>(
      `${BASE}/sessions?page=${page}&page_size=${pageSize}`,
    ),

  getSession: (id: string) =>
    fetchJSON<TSessionDetail>(`${BASE}/sessions/${id}`),

  deleteSessions: (ids: string[]) =>
    fetchJSON<{ deleted_count: number }>(`${BASE}/sessions`, {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_ids: ids }),
    }),

  health: () => fetchJSON<{ status: string }>(`${BASE}/health`),
};
