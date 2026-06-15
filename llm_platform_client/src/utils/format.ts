// formatCost always shows the exact amount — costs are rounded to 6 decimals
// server-side, so 6 fractional digits is lossless for small values; precision
// tapers off as the magnitude makes sub-cent digits noise. Keep in sync with
// llm_platform_frontend/src/utils/tokens.ts.
export const formatCost = (usd: number): string => {
  if (usd === 0) return '$0';
  if (usd < 0.01) return `$${usd.toFixed(6)}`;
  if (usd < 1) return `$${usd.toFixed(4)}`;
  return `$${usd.toFixed(2)}`;
};
