import type { TPredictResult, TPricing, TTask, TTaskStatsDetail } from '../types';
import { API_TOKEN } from '../auth/token';

const BASE = '';

// ApiError carries the HTTP status plus budget metadata (429 + Retry-After)
// so the Try-it panel can explain exactly what happened.
export class ApiError extends Error {
  status: number;
  retryAfterSeconds: number | null;
  constructor(status: number, message: string, retryAfterSeconds: number | null = null) {
    super(message);
    this.status = status;
    this.retryAfterSeconds = retryAfterSeconds;
    this.name = 'ApiError';
  }
}

async function request<T>(
  url: string,
  options?: RequestInit,
  timeoutMs = 10_000,
): Promise<{ data: T; headers: Headers }> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(url, {
      ...options,
      headers: {
        Authorization: `Bearer ${API_TOKEN}`,
        ...options?.headers,
      },
      signal: controller.signal,
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      const retryAfter = res.headers.get('Retry-After');
      throw new ApiError(
        res.status,
        (data as { detail?: string }).detail ?? `HTTP ${res.status}`,
        retryAfter ? Number(retryAfter) : null,
      );
    }
    return { data: (await res.json()) as T, headers: res.headers };
  } finally {
    clearTimeout(timer);
  }
}

async function fetchJSON<T>(url: string, options?: RequestInit, timeoutMs?: number): Promise<T> {
  const { data } = await request<T>(url, options, timeoutMs);
  return data;
}

const jsonPost = (body: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
});

// PredictOutcome bundles the body with the degraded-mode signal, which the
// platform delivers as a response header (X-Platform-Degraded: true).
export type PredictOutcome = {
  result: TPredictResult;
  degraded: boolean;
};

export const api = {
  health: () => fetchJSON<{ status: string }>(`${BASE}/health`),

  listTasks: () => fetchJSON<{ tasks: TTask[] }>(`${BASE}/v1/tasks`),

  getTask: (id: string) => fetchJSON<TTask>(`${BASE}/v1/tasks/${id}`),

  predict: async (id: string, inputs: Record<string, unknown>): Promise<PredictOutcome> => {
    const { data, headers } = await request<TPredictResult>(
      `${BASE}/v1/tasks/${id}/predict`,
      jsonPost({ inputs }),
      60_000,
    );
    return { result: data, degraded: headers.get('X-Platform-Degraded') === 'true' };
  },

  getRun: (runId: string) => fetchJSON<TPredictResult>(`${BASE}/v1/tasks/runs/${runId}`),

  taskStats: (id: string, days = 30) =>
    fetchJSON<TTaskStatsDetail>(`${BASE}/v1/tasks/${id}/stats?days=${days}`),

  pricing: () => fetchJSON<{ pricing: TPricing }>(`${BASE}/pricing`),
};
