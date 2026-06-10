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
};

export type TUIMessage = TUserUIMessage | TAssistantUIMessage;

export const MODELS = ['gpt-4o-mini', 'llama-groq', 'gemini-flash'] as const;
export type TModel = (typeof MODELS)[number];
