// ── API types — match Go JSON contract exactly ──────────────────────────────

export type TContentPart = {
  type: 'text' | 'image_url';
  text?: string;
  image_url?: { url: string };
};

export type TApiMessage = {
  role: 'user' | 'assistant' | 'system';
  content: string | TContentPart[];
};

export type TRunRequest = {
  prompt: string;
  models?: string[];
  model_conversations?: Record<string, TApiMessage[]>;
  temperature?: number;
  max_tokens?: number;
  session_id?: string;
  system_prompt?: string;
};

export type TModelResult = {
  model: string;
  response: string | null;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  cost_usd: number;
  success: boolean;
  error: string | null;
};

export type TRunResponse = {
  run_id: string;
  prompt: string;
  system_prompt: string | null;
  results: TModelResult[];
  total_wall_clock_ms: number;
  models_succeeded: number;
  models_failed: number;
};

export type TSessionSummary = {
  session_id: string;
  first_prompt: string;
  turn_count: number;
  created_at: string;
};

export type TSessionListResponse = {
  page: number;
  page_size: number;
  total_sessions: number;
  total_pages: number;
  sessions: TSessionSummary[];
};

export type TTurnResult = {
  model: string;
  response: string | null;
  latency_ms: number;
  total_tokens: number;
  cost_usd: number;
  success: boolean;
  error: string | null;
};

export type TSessionTurn = {
  run_id: string;
  prompt: string;
  system_prompt: string | null;
  created_at: string;
  results: TTurnResult[];
};

export type TSessionDetail = {
  session_id: string;
  turns: TSessionTurn[];
};

// ── UI types — local state, richer than API messages ────────────────────────

export type TUserUIMessage = {
  role: 'user';
  content: string;
  images: string[];
  systemPrompt?: string;
};

export type TAssistantUIMessage = {
  role: 'assistant';
  content: string;
  latency_ms: number;
  total_tokens: number;
  cost_usd: number;
  success: boolean;
  run_id?: string; // for feedback
  model?: string; // for feedback
  rating?: number; // 1–5, set after the user rates
};

export type TUIMessage = TUserUIMessage | TAssistantUIMessage;

// Mirrors the backend routing registry (internal/llm/runner.go), grouped by
// provider so pickers can render models under their parent — every key is
// callable even when that provider's API key isn't configured (the call then
// fails gracefully with an auth error).
export const MODEL_GROUPS = [
  {
    provider: 'OpenAI',
    models: [
      // 'gpt-5.1',
      // 'gpt-5',
      // 'gpt-5-mini',
      // 'gpt-5-nano',
      // 'gpt-4.1',
      // 'gpt-4.1-mini',
      // 'gpt-4.1-nano',
      'gpt-4o',
      'gpt-4o-mini',
    ],
  },
  {
    provider: 'Groq',
    models: ['llama-groq'],
  },
  {
    provider: 'Gemini',
    models: [
      // 'gemini-3-pro',
      'gemini-2.5-pro',
      'gemini-2.5-flash',
      // 'gemini-2.5-flash-lite',
      // 'gemini-flash',
    ],
  },
  {
    provider: 'Anthropic',
    models: [
      // 'claude-fable-5',
      // 'claude-opus-4-8',
      'claude-sonnet-4-6',
      // 'claude-haiku-4-5',
    ],
  },
] as const;

// Flat list in registry order — derived from the groups so the two can never
// drift apart.
export const MODELS = MODEL_GROUPS.flatMap((g) => g.models);
export type TModel = (typeof MODELS)[number];

// Default selection for Compare/Estimate — users opt in to more.
export const DEFAULT_COMPARE_MODELS: TModel[] = ['gemini-2.5-flash'];

// ── Leaderboard (manual per-model rating within a session) ───────────────────

export type TLeaderboardEntry = {
  model: string;
  avg_score: number;
  rating_count: number;
};

export type TLeaderboardResponse = {
  session_id: string;
  entries: TLeaderboardEntry[];
};

// ── Auth ────────────────────────────────────────────────────────────────────

export type TUser = {
  id: string;
  email: string;
  name: string;
  role?: string;
};

// ── Pricing / Dashboard ──────────────────────────────────────────────────────

export type TRate = { input_per_1m: number; output_per_1m: number };
export type TPricing = Record<string, TRate>;

export type TModelStats = {
  model: string;
  runs: number;
  total_tokens: number;
  cost_usd: number;
  avg_latency_ms: number;
  avg_rating: number;
  rating_count: number;
};

export type TDailyPoint = {
  date: string;
  cost_usd: number;
  total_tokens: number;
  runs: number;
};

export type TTaskStats = {
  task_id: string;
  runs: number;
  total_tokens: number;
  cost_usd: number;
  avg_latency_ms: number;
  success_rate: number; // 0..1
};

export type TDashboard = {
  total_runs: number;
  total_tokens: number;
  total_cost_usd: number;
  by_task: TTaskStats[];
  by_model: TModelStats[];
  daily: TDailyPoint[];
};

// ── Task registry / Studio ───────────────────────────────────────────────────

export type TTask = {
  id: string;
  name: string;
  description?: string;
  input_schema?: Record<string, unknown>;
  output_schema?: Record<string, unknown>;
  prompt_template: string;
  system_prompt?: string;
  prompt_version: number;
  model: string;
  fallback_models?: string[];
  temperature: number;
  max_tokens: number;
  daily_budget_usd?: number;
  cache_enabled: boolean;
  cache_ttl_seconds?: number;
  active: boolean;
  created_at: string;
  updated_at: string;
};

export type TPromptVersion = {
  task_id: string;
  version: number;
  prompt_template: string;
  system_prompt: string;
  note?: string;
  created_by?: string;
  created_at: string;
  active: boolean;
};

export type TPredictResult = {
  task_run_id: string;
  task_id: string;
  prompt_version: number;
  model: string;
  provider: string;
  output: Record<string, unknown> | null;
  output_valid: boolean | null;
  raw_response: string | null;
  error: string | null;
  fallback_used: boolean;
  cached: boolean; // served from the prediction cache — zero cost
  usage: {
    input_tokens: number;
    output_tokens: number;
    total_tokens: number;
    cost_usd: number;
  };
  latency_ms: number;
};

export type TTaskStatsDetail = {
  task_id: string;
  days: number;
  totals: TTaskStats;
  daily: TDailyPoint[];
};
