import { getEncoding } from 'js-tiktoken';

// --- Encoder (lazy-initialized, shared across calls) ---

let _enc: ReturnType<typeof getEncoding> | null = null;
function getEnc() {
  if (!_enc) _enc = getEncoding('cl100k_base');
  return _enc;
}

// --- Live pricing data (populated from /pricing API on app mount) ---

type ModelRate = {
  input_per_1m: number;
  output_per_1m: number;
  context_window: number;
  max_output_tokens: number;
};
let _liveRates: Record<string, ModelRate> | null = null;

export function setLiveRates(data: Record<string, ModelRate>): void {
  _liveRates = data;
}

// --- Token counting ---

export function countTokens(text: string, _model?: string): number {
  if (!text) return 0;
  try {
    return getEnc().encode(text).length;
  } catch {
    return Math.ceil(text.length / 4);
  }
}

// Returns true when cl100k_base is a rough approximation for this model.
// GPT models use cl100k_base natively (exact). Claude and Gemini use different
// tokenizers — cl100k_base is within ~5–15% but not exact.
export function isApproximateTokenizer(model: string): boolean {
  return model.includes('gemini') || model.includes('claude');
}

// --- Hardcoded fallback tables (used until /pricing loads) ---

export const CONTEXT_WINDOWS: Record<string, number> = {
  'gpt-4o-mini':       128000,
  'gpt-4o':            128000,
  'gemini-2.5-flash':  1048576,
  'gemini-2.5-pro':    1048576,
  'claude-sonnet-4-6': 200000,
};

export const MAX_OUTPUT_TOKENS: Record<string, number> = {
  'gpt-4o-mini':       16384,
  'gpt-4o':            16384,
  'gemini-2.5-flash':  65536,
  'gemini-2.5-pro':    65536,
  'claude-sonnet-4-6': 64000,
};

export const PRICING: Record<string, { inputPer1M: number; outputPer1M: number }> = {
  'gpt-4o-mini':       { inputPer1M: 0.15,  outputPer1M: 0.60  },
  'gpt-4o':            { inputPer1M: 2.50,  outputPer1M: 10.00 },
  'gemini-2.5-flash':  { inputPer1M: 0.15,  outputPer1M: 0.60  },
  'gemini-2.5-pro':    { inputPer1M: 1.25,  outputPer1M: 10.00 },
  'claude-sonnet-4-6': { inputPer1M: 3.00,  outputPer1M: 15.00 },
};

// --- Live-data accessors (fall back to hardcoded tables if /pricing hasn't loaded) ---

export function getContextWindow(model: string): number {
  return _liveRates?.[model]?.context_window ?? CONTEXT_WINDOWS[model] ?? 0;
}

export function getMaxOutputTokens(model: string): number {
  return _liveRates?.[model]?.max_output_tokens ?? MAX_OUTPUT_TOKENS[model] ?? 0;
}

// --- Cost estimation ---

export function estimateCost(model: string, inputTokens: number, outputTokens: number): number {
  const inputPer1M = _liveRates?.[model]?.input_per_1m ?? PRICING[model]?.inputPer1M ?? 0;
  const outputPer1M = _liveRates?.[model]?.output_per_1m ?? PRICING[model]?.outputPer1M ?? 0;
  if (!inputPer1M && !outputPer1M) return 0;
  const raw =
    (inputTokens / 1_000_000) * inputPer1M +
    (outputTokens / 1_000_000) * outputPer1M;
  return Math.round(raw * 1_000_000) / 1_000_000;
}

export function formatCost(usd: number): string {
  if (usd < 0.001) return '<$0.001';
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(3)}`;
}
