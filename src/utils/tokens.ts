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

export const PRICING: Record<string, { inputPer1M: number; outputPer1M: number }> = {
  'gpt-4o-mini':  { inputPer1M: 0.15,  outputPer1M: 0.60 },
  'llama-groq':   { inputPer1M: 0.05,  outputPer1M: 0.08 },
  'gemini-flash': { inputPer1M: 0.075, outputPer1M: 0.30 },
};

export function estimateCost(model: string, inputTokens: number, outputTokens: number): number {
  const rate = PRICING[model];
  if (!rate) return 0;
  const raw =
    (inputTokens / 1_000_000) * rate.inputPer1M +
    (outputTokens / 1_000_000) * rate.outputPer1M;
  return Math.round(raw * 1_000_000) / 1_000_000;
}

export function formatCost(usd: number): string {
  if (usd < 0.001) return '<$0.001';
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(3)}`;
}
