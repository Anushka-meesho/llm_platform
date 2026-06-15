import { getEncoding } from 'js-tiktoken';

let _enc: ReturnType<typeof getEncoding> | null = null;

function getEnc() {
  if (!_enc) _enc = getEncoding('cl100k_base');
  return _enc;
}

export function countTokens(text: string): number {
  if (!text) return 0;
  try {
    return getEnc().encode(text).length;
  } catch {
    return Math.ceil(text.length / 4);
  }
}

type Rate = { inputPer1M: number; outputPer1M: number };

// Fallback rates, used until the backend's /pricing is fetched. Kept in sync
// with the Go pricing.json so estimates work even offline.
const FALLBACK_PRICING: Record<string, Rate> = {
  'gpt-5.1':               { inputPer1M: 1.25,  outputPer1M: 10.0 },
  'gpt-5':                 { inputPer1M: 1.25,  outputPer1M: 10.0 },
  'gpt-5-mini':            { inputPer1M: 0.25,  outputPer1M: 2.0 },
  'gpt-5-nano':            { inputPer1M: 0.05,  outputPer1M: 0.40 },
  'gpt-4.1':               { inputPer1M: 2.0,   outputPer1M: 8.0 },
  'gpt-4.1-mini':          { inputPer1M: 0.40,  outputPer1M: 1.60 },
  'gpt-4.1-nano':          { inputPer1M: 0.10,  outputPer1M: 0.40 },
  'gpt-4o':                { inputPer1M: 2.50,  outputPer1M: 10.0 },
  'gpt-4o-mini':           { inputPer1M: 0.15,  outputPer1M: 0.60 },
  'llama-groq':            { inputPer1M: 0.05,  outputPer1M: 0.08 },
  'gemini-3-pro':          { inputPer1M: 2.0,   outputPer1M: 12.0 },
  'gemini-2.5-pro':        { inputPer1M: 1.25,  outputPer1M: 10.0 },
  'gemini-2.5-flash':      { inputPer1M: 0.30,  outputPer1M: 2.50 },
  'gemini-2.5-flash-lite': { inputPer1M: 0.10,  outputPer1M: 0.40 },
  'gemini-flash':          { inputPer1M: 0.075, outputPer1M: 0.30 },
  'claude-fable-5':        { inputPer1M: 10.0,  outputPer1M: 50.0 },
  'claude-opus-4-8':       { inputPer1M: 5.0,   outputPer1M: 25.0 },
  'claude-sonnet-4-6':     { inputPer1M: 3.0,   outputPer1M: 15.0 },
  'claude-haiku-4-5':      { inputPer1M: 1.0,   outputPer1M: 5.0 },
};

// Active table — replaced by setPricing() once /pricing resolves so the
// frontend estimates with the same rates the backend bills with.
let activePricing: Record<string, Rate> = { ...FALLBACK_PRICING };

// setPricing accepts the backend shape ({ input_per_1m, output_per_1m }) and
// updates the active table used by estimateCost.
export function setPricing(
  table: Record<string, { input_per_1m: number; output_per_1m: number }>,
): void {
  const next: Record<string, Rate> = {};
  for (const [model, r] of Object.entries(table)) {
    next[model] = { inputPer1M: r.input_per_1m, outputPer1M: r.output_per_1m };
  }
  activePricing = next;
}

export function pricedModels(): string[] {
  return Object.keys(activePricing);
}

export function estimateCost(model: string, inputTokens: number, outputTokens: number): number {
  const rate = activePricing[model];
  if (!rate) return 0;
  const raw =
    (inputTokens / 1_000_000) * rate.inputPer1M +
    (outputTokens / 1_000_000) * rate.outputPer1M;
  return Math.round(raw * 1_000_000) / 1_000_000;
}

// formatCost always shows the exact amount — costs are rounded to 6 decimals
// server-side (CalculateCost), so 6 fractional digits is lossless for small
// values; precision tapers off as the magnitude makes sub-cent digits noise.
export function formatCost(usd: number): string {
  if (usd === 0) return '$0';
  if (usd < 0.01) return `$${usd.toFixed(6)}`;
  if (usd < 1) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(2)}`;
}
