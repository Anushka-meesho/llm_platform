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
  context_window: number;
  max_output_tokens: number;
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
  rating?: number | null;
  note?: string | null;
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
  run_id: string;
  session_id: string;
  rating?: number | null;
  note?: string | null;
};

export type TUIMessage = TUserUIMessage | TAssistantUIMessage;

export const MODELS = ['gpt-4o-mini', 'gpt-4o', 'gemini-2.5-flash', 'gemini-2.5-pro', 'claude-sonnet-4-6'] as const;
export type TModel = (typeof MODELS)[number];

export const MODEL_LABELS: Record<TModel, string> = {
  'gpt-4o-mini':       'GPT-4o Mini',
  'gpt-4o':            'GPT-4o',
  'gemini-2.5-flash':  'Gemini 2.5 Flash',
  'gemini-2.5-pro':    'Gemini 2.5 Pro',
  'claude-sonnet-4-6': 'Claude Sonnet 4.6',
};

export type TRatingRequest = {
  run_id: string;
  model: string;
  session_id: string;
  rating: number;
  note: string;
};

export type TLeaderboardEntry = {
  model: string;
  avg_score: number;
  rating_count: number;
};

export type TLeaderboardResponse = {
  session_id: string;
  entries: TLeaderboardEntry[];
};
