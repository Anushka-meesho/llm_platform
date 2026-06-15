import type {
  TRunRequest,
  TModelResult,
  TSessionListResponse,
  TSessionDetail,
  TRatingRequest,
  TLeaderboardResponse,
} from '../types';

type TModelRate = {
  input_per_1m: number;
  output_per_1m: number;
  context_window: number;
  max_output_tokens: number;
};
type TPricingResponse = { models: Record<string, TModelRate> };

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

// Streams /run results via SSE. Calls onMeta once with the run_id, then onResult
// for each model as it completes (fastest first). Resolves when the stream closes.
async function runStream(
  payload: TRunRequest,
  onMeta: (runId: string) => void,
  onResult: (result: TModelResult) => void,
): Promise<void> {
  const res = await fetch(`${BASE}/run`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });

  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error((data as { detail?: string }).detail ?? `HTTP ${res.status}`);
  }

  const reader = res.body!.getReader();
  const decoder = new TextDecoder();
  let buf = '';

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });

    let boundary: number;
    while ((boundary = buf.indexOf('\n\n')) !== -1) {
      const block = buf.slice(0, boundary);
      buf = buf.slice(boundary + 2);

      let eventType = '';
      let dataStr = '';
      for (const line of block.split('\n')) {
        if (line.startsWith('event: ')) eventType = line.slice(7).trim();
        else if (line.startsWith('data: ')) dataStr = line.slice(6);
      }
      if (!dataStr) continue;

      const parsed = JSON.parse(dataStr) as Record<string, unknown>;
      if (eventType === 'meta') onMeta(parsed.run_id as string);
      else if (eventType === 'result') onResult(parsed as unknown as TModelResult);
    }
  }
}

export const api = {
  runStream,

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

  saveRating: (payload: TRatingRequest) =>
    fetchJSON<{ ok: boolean }>(`${BASE}/ratings`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    }),

  getLeaderboard: (sessionId: string) =>
    fetchJSON<TLeaderboardResponse>(`${BASE}/sessions/${sessionId}/leaderboard`),

  fetchPricing: () =>
    fetchJSON<TPricingResponse>(`${BASE}/pricing`).then((d) => d.models),
};
