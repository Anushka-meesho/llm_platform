import type {
  TRunRequest,
  TRunResponse,
  TSessionListResponse,
  TSessionDetail,
  TUser,
  TPricing,
  TDashboard,
  TTask,
  TPromptVersion,
  TPredictResult,
  TTaskStatsDetail,
  TLeaderboardResponse,
  TRunListResponse,
  TRunDetail,
  TRunFilters,
  TModelHealthResponse,
  THealthEventsResponse,
} from '../types';

const BASE = '';

// ApiError carries the HTTP status so callers (e.g. the auth gate) can react to
// 401s specifically.
export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = 'ApiError';
  }
}

async function fetchJSON<T>(
  url: string,
  options?: RequestInit,
  timeoutMs = 10_000,
): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(url, {
      ...options,
      // Send/receive the session cookie across the dev proxy.
      credentials: 'include',
      signal: controller.signal,
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      throw new ApiError(
        res.status,
        (data as { detail?: string }).detail ?? `HTTP ${res.status}`,
      );
    }
    return res.json() as Promise<T>;
  } finally {
    clearTimeout(timer);
  }
}

const jsonPost = (body: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
});

export const api = {
  // ── Auth ──────────────────────────────────────────────────────────────
  me: () => fetchJSON<{ user: TUser }>(`${BASE}/auth/me`),
  demoUsers: () => fetchJSON<{ users: TUser[] }>(`${BASE}/auth/demo-users`),
  login: (userId: string) =>
    fetchJSON<{ user: TUser }>(`${BASE}/auth/login`, jsonPost({ user_id: userId })),
  logout: () => fetchJSON<{ status: string }>(`${BASE}/auth/logout`, { method: 'POST' }),

  // ── Core ──────────────────────────────────────────────────────────────
  run: (payload: TRunRequest) =>
    fetchJSON<TRunResponse>(`${BASE}/run`, jsonPost(payload), 60_000),

  listSessions: (page = 1, pageSize = 8) =>
    fetchJSON<TSessionListResponse>(
      `${BASE}/sessions?page=${page}&page_size=${pageSize}`,
    ),

  getSession: (id: string) => fetchJSON<TSessionDetail>(`${BASE}/sessions/${id}`),

  getLeaderboard: (sessionId: string) =>
    fetchJSON<TLeaderboardResponse>(`${BASE}/sessions/${sessionId}/leaderboard`),

  deleteSessions: (ids: string[]) =>
    fetchJSON<{ deleted_count: number }>(`${BASE}/sessions`, {
      ...jsonPost({ session_ids: ids }),
      method: 'DELETE',
    }),

  // ── Estimation / feedback / dashboard ───────────────────────────────────
  pricing: () => fetchJSON<{ pricing: TPricing }>(`${BASE}/pricing`),

  feedback: (runId: string, model: string, rating: number) =>
    fetchJSON<{ rating: number }>(
      `${BASE}/feedback`,
      jsonPost({ run_id: runId, model, rating }),
    ),

  dashboard: () => fetchJSON<TDashboard>(`${BASE}/dashboard`),

  // ── Task registry / Studio ────────────────────────────────────────────
  listTasks: () => fetchJSON<{ tasks: TTask[] }>(`${BASE}/v1/tasks`),

  getTask: (id: string) => fetchJSON<TTask>(`${BASE}/v1/tasks/${id}`),

  // PUT has merge semantics server-side: only the fields present in `patch`
  // change; everything else keeps its current value.
  updateTask: (id: string, patch: Partial<TTask>) =>
    fetchJSON<TTask>(`${BASE}/v1/tasks/${id}`, {
      ...jsonPost(patch),
      method: 'PUT',
    }),

  listVersions: (id: string) =>
    fetchJSON<{ task_id: string; active_version: number; versions: TPromptVersion[] }>(
      `${BASE}/v1/tasks/${id}/versions`,
    ),

  saveDraft: (id: string, promptTemplate: string, systemPrompt: string, note: string) =>
    fetchJSON<{ task_id: string; version: number }>(
      `${BASE}/v1/tasks/${id}/versions`,
      jsonPost({ prompt_template: promptTemplate, system_prompt: systemPrompt, note }),
    ),

  deployVersion: (id: string, version: number) =>
    fetchJSON<{ task_id: string; active_version: number }>(
      `${BASE}/v1/tasks/${id}/deploy`,
      jsonPost({ version }),
    ),

  // Admin-only server-side (task:delete). Refused (409) for the active version.
  deleteVersion: (id: string, version: number) =>
    fetchJSON<{ task_id: string; version: number; status: string }>(
      `${BASE}/v1/tasks/${id}/versions/${version}`,
      { method: 'DELETE' },
    ),

  testTask: (
    id: string,
    inputs: Record<string, unknown>,
    opts?: { version?: number; model?: string },
  ) =>
    fetchJSON<TPredictResult>(
      `${BASE}/v1/tasks/${id}/test`,
      jsonPost({ inputs, version: opts?.version, model: opts?.model }),
      60_000,
    ),

  taskStats: (id: string, days = 30) =>
    fetchJSON<TTaskStatsDetail>(`${BASE}/v1/tasks/${id}/stats?days=${days}`),

  // ── Admin: prompt history (admin role only, 403 otherwise) ─────────────────
  adminRuns: (f: TRunFilters = {}) => {
    const p = new URLSearchParams();
    p.set('page', String(f.page ?? 1));
    p.set('page_size', String(f.pageSize ?? 25));
    if (f.taskId) p.set('task_id', f.taskId);
    if (f.model) p.set('model', f.model);
    if (f.userEmail) p.set('user_email', f.userEmail);
    if (f.q) p.set('q', f.q);
    if (f.status) p.set('status', f.status);
    if (f.type) p.set('type', f.type);
    return fetchJSON<TRunListResponse>(`${BASE}/v1/admin/runs?${p.toString()}`);
  },

  adminRun: (runId: string) =>
    fetchJSON<TRunDetail>(`${BASE}/v1/admin/runs/${encodeURIComponent(runId)}`),

  adminRunModels: () =>
    fetchJSON<{ models: string[] }>(`${BASE}/v1/admin/runs/models`),

  // Per-(task, model) circuit-breaker health.
  modelHealth: () => fetchJSON<TModelHealthResponse>(`${BASE}/v1/admin/model-health`),

  resetModelHealth: (taskId: string, model: string) =>
    fetchJSON<{ status: string }>(
      `${BASE}/v1/admin/model-health/reset`,
      jsonPost({ task_id: taskId, model }),
    ),

  modelHealthEvents: (taskId = '', model = '', page = 1, pageSize = 50) => {
    const p = new URLSearchParams();
    p.set('page', String(page));
    p.set('page_size', String(pageSize));
    if (taskId) p.set('task_id', taskId);
    if (model) p.set('model', model);
    return fetchJSON<THealthEventsResponse>(`${BASE}/v1/admin/model-health/events?${p.toString()}`);
  },

  health: () => fetchJSON<{ status: string }>(`${BASE}/health`),
};
