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
  TEvalDatasetsResponse,
  TEvalDatasetUploadResult,
  TEvalRun,
  TLeaderboardResponse,
  TRunListResponse,
  TRunDetail,
  TRunFilters,
  TModelHealthResponse,
  THealthEventsResponse,
} from '../types';

const BASE = '';

// ApiError carries everything needed to tell the user (and us) exactly what went
// wrong: the HTTP status, the backend's machine-readable `code`, and the
// `requestId` that correlates this failure with the server log line. status === 0
// means the request never got an HTTP response — see `code` ('timeout'|'network').
export class ApiError extends Error {
  status: number;
  code?: string;
  requestId?: string;
  constructor(status: number, message: string, code?: string, requestId?: string) {
    super(message);
    this.status = status;
    this.code = code;
    this.requestId = requestId;
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
  let res: Response;
  try {
    res = await fetch(url, {
      ...options,
      // Send/receive the session cookie across the dev proxy.
      credentials: 'include',
      signal: controller.signal,
    });
  } catch (e) {
    // No HTTP response: distinguish a client-side timeout (we aborted) from a
    // genuine network/connection failure so the message says which happened.
    if (e instanceof DOMException && e.name === 'AbortError') {
      throw new ApiError(0, `Request timed out after ${Math.round(timeoutMs / 1000)}s`, 'timeout');
    }
    throw new ApiError(0, "Can't reach the server — is it running?", 'network');
  } finally {
    clearTimeout(timer);
  }

  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as {
      detail?: string;
      code?: string;
      request_id?: string;
    };
    throw new ApiError(
      res.status,
      data.detail ?? `HTTP ${res.status}`,
      data.code,
      data.request_id ?? res.headers.get('X-Request-ID') ?? undefined,
    );
  }
  return res.json() as Promise<T>;
}

// errorMessage turns any thrown value into a user-facing string. For an ApiError
// it appends "(ref: <request_id>)" so the user can cite it and we can grep the
// server logs for the exact failure.
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.requestId ? `${err.message} (ref: ${err.requestId})` : err.message;
  }
  if (err instanceof Error) return err.message;
  return String(err);
}

const jsonPost = (body: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
});

async function fetchEvalUpload(url: string, form: FormData): Promise<TEvalDatasetUploadResult> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 60_000);
  let res: Response;
  try {
    res = await fetch(url, {
      method: 'POST',
      body: form,
      credentials: 'include',
      signal: controller.signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') {
      throw new ApiError(0, 'Request timed out after 60s', 'timeout');
    }
    throw new ApiError(0, "Can't reach the server — is it running?", 'network');
  } finally {
    clearTimeout(timer);
  }

  const data = (await res.json().catch(() => ({}))) as TEvalDatasetUploadResult & {
    code?: string;
    request_id?: string;
  };
  if (!res.ok && data.code !== 'eval_dataset_validation_failed') {
    throw new ApiError(
      res.status,
      data.detail ?? `HTTP ${res.status}`,
      data.code,
      data.request_id,
    );
  }
  return data;
}

async function fetchEvalCSV(url: string, body: unknown): Promise<Blob> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 120_000);
  let res: Response;
  try {
    res = await fetch(url, {
      ...jsonPost(body),
      credentials: 'include',
      signal: controller.signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') {
      throw new ApiError(0, 'Request timed out after 120s', 'timeout');
    }
    throw new ApiError(0, "Can't reach the server — is it running?", 'network');
  } finally {
    clearTimeout(timer);
  }

  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as {
      detail?: string;
      code?: string;
      request_id?: string;
    };
    throw new ApiError(
      res.status,
      data.detail ?? `HTTP ${res.status}`,
      data.code,
      data.request_id,
    );
  }
  return res.blob();
}

async function fetchTestTaskForBatch(url: string, body: unknown): Promise<TPredictResult> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 60_000);
  let res: Response;
  try {
    res = await fetch(url, {
      ...jsonPost(body),
      credentials: 'include',
      signal: controller.signal,
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') {
      throw new ApiError(0, 'Request timed out after 60s', 'timeout');
    }
    throw new ApiError(0, "Can't reach the server — is it running?", 'network');
  } finally {
    clearTimeout(timer);
  }

  const data = (await res.json().catch(() => ({}))) as Partial<TPredictResult> & {
    detail?: string;
    code?: string;
    request_id?: string;
  };
  if (!res.ok && typeof data.task_run_id !== 'string') {
    throw new ApiError(
      res.status,
      data.detail ?? `HTTP ${res.status}`,
      data.code,
      data.request_id ?? res.headers.get('X-Request-ID') ?? undefined,
    );
  }
  return data as TPredictResult;
}

// Bundles the predict response body with the degraded-mode signal, which the
// platform delivers as an X-Platform-Degraded response header.
export type PredictOutcome = { result: TPredictResult; degraded: boolean };

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

  // Author a new task (task:write — creator/admin). The server validates the
  // id slug, schemas, prompt template, and model, then activates it and seeds
  // prompt version 1. The DB is the source of truth — this is the only way new
  // tasks come into existence now that YAML seeding is gone.
  createTask: (task: Partial<TTask>) =>
    fetchJSON<TTask>(`${BASE}/v1/tasks`, jsonPost(task)),

  // PUT has merge semantics server-side: only the fields present in `patch`
  // change; everything else keeps its current value.
  updateTask: (id: string, patch: Partial<TTask>) =>
    fetchJSON<TTask>(`${BASE}/v1/tasks/${id}`, {
      ...jsonPost(patch),
      method: 'PUT',
    }),

  // Permanently delete a task and its prompt history (task:delete — admin only,
  // 403 otherwise; 409 for the built-in playground task). Irreversible.
  deleteTask: (id: string) =>
    fetchJSON<{ task_id: string; status: string }>(`${BASE}/v1/tasks/${id}`, {
      method: 'DELETE',
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

  testTaskForBatch: (
    id: string,
    inputs: Record<string, unknown>,
    opts?: { version?: number; model?: string },
  ) =>
    fetchTestTaskForBatch(
      `${BASE}/v1/tasks/${id}/test`,
      { inputs, version: opts?.version, model: opts?.model },
    ),

  // Call the real production predict endpoint and return the result together
  // with the degraded-mode flag from the X-Platform-Degraded response header.
  // Uses cookie auth like all other calls in this client.
  predict: async (id: string, inputs: Record<string, unknown>): Promise<PredictOutcome> => {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 60_000);
    let res: Response;
    try {
      res = await fetch(`${BASE}/v1/tasks/${id}/predict`, {
        ...jsonPost({ inputs }),
        credentials: 'include',
        signal: controller.signal,
      });
    } catch (e) {
      clearTimeout(timer);
      if (e instanceof DOMException && e.name === 'AbortError') {
        throw new ApiError(0, 'Request timed out after 60s', 'timeout');
      }
      throw new ApiError(0, "Can't reach the server — is it running?", 'network');
    }
    clearTimeout(timer);
    if (!res.ok) {
      const data = (await res.json().catch(() => ({}))) as Partial<TPredictResult> & {
        detail?: string;
      };
      // A 502 "no valid answer" still returns a full predict response (it carries
      // task_run_id, error_code, and the raw response). Surface it as an outcome
      // rather than throwing, so the caller can show exactly what happened —
      // including the per-model gateway trace — for failed runs too. Other errors
      // (429 budget, 4xx, network) have no predict body and still throw.
      if (typeof data.task_run_id === 'string') {
        return {
          result: data as TPredictResult,
          degraded: res.headers.get('X-Platform-Degraded') === 'true',
        };
      }
      const retryAfter = res.status === 429 ? res.headers.get('Retry-After') : null;
      throw new ApiError(
        res.status,
        data.detail ?? `HTTP ${res.status}`,
        res.status === 429 && retryAfter ? `retry_after:${retryAfter}` : undefined,
      );
    }
    const result = (await res.json()) as TPredictResult;
    return { result, degraded: res.headers.get('X-Platform-Degraded') === 'true' };
  },

  taskStats: (id: string, days = 30) =>
    fetchJSON<TTaskStatsDetail>(`${BASE}/v1/tasks/${id}/stats?days=${days}`),

  listEvalDatasets: (id: string) =>
    fetchJSON<TEvalDatasetsResponse>(`${BASE}/v1/tasks/${id}/eval-datasets`),

  uploadEvalDataset: (id: string, form: FormData) =>
    fetchEvalUpload(`${BASE}/v1/tasks/${id}/eval-datasets/upload`, form),

  createPrismEvalDataset: (
    id: string,
    payload: {
      name: string;
      sql: string;
      input_mapping: Record<string, string>;
      output_mapping?: Record<string, string>;
    },
  ) =>
    fetchJSON<TEvalDatasetUploadResult>(
      `${BASE}/v1/tasks/${id}/eval-datasets/prism`,
      jsonPost(payload),
    ),

  runEval: (
    id: string,
    version: number,
    payload: { dataset_id: number; max_items?: number; model?: string },
  ) =>
    fetchJSON<TEvalRun>(
      `${BASE}/v1/tasks/${id}/versions/${version}/eval`,
      jsonPost(payload),
      120_000,
    ),

  checkEvalDataset: (
    id: string,
    version: number,
    payload: { dataset_id: number; max_items?: number; model?: string },
  ) =>
    fetchJSON<TEvalRun>(
      `${BASE}/v1/tasks/${id}/versions/${version}/check`,
      jsonPost(payload),
      120_000,
    ),

  downloadEvalCSV: (
    id: string,
    version: number,
    payload: { dataset_id: number; max_items?: number; model?: string },
  ) => fetchEvalCSV(`${BASE}/v1/tasks/${id}/versions/${version}/check.csv`, payload),

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
    if (f.hasTask) p.set('has_task', 'true');
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
