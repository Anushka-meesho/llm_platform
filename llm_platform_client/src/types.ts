// ── API types — match the Go JSON contract exactly ──────────────────────────
// Client-portal subset: only the /v1 product API + /pricing. Keep in sync with
// llm_platform_frontend/src/types/index.ts when the Go contracts change.

export type TJSONSchema = {
  type?: string;
  properties?: Record<string, TJSONSchema>;
  required?: string[];
  description?: string;
  enum?: unknown[];
  minimum?: number;
  maximum?: number;
  additionalProperties?: TJSONSchema | boolean;
  items?: TJSONSchema;
};

export type TTask = {
  id: string;
  name: string;
  description?: string;
  input_schema?: TJSONSchema;
  output_schema?: TJSONSchema;
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

export type TUsage = {
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  cost_usd: number;
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
  usage: TUsage;
  latency_ms: number; // winning model's call time
  gateway_latency_ms: number; // end-to-end platform wall-clock (fallback walk + validation + overhead)
};

export type TTaskStats = {
  task_id: string;
  runs: number;
  total_tokens: number;
  cost_usd: number;
  avg_latency_ms: number;
  success_rate: number; // 0..1
};

export type TDailyPoint = {
  date: string;
  cost_usd: number;
  total_tokens: number;
  runs: number;
};

export type TTaskStatsDetail = {
  task_id: string;
  days: number;
  totals: TTaskStats;
  daily: TDailyPoint[];
};

export type TRate = { input_per_1m: number; output_per_1m: number };
export type TPricing = Record<string, TRate>;
