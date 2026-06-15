import { useState, useRef, useCallback, useMemo, useEffect } from 'react';
import { Button, TextArea, Spinner } from '@meesho/merlin-ui-tailwind';
import type { TUIMessage, TModel } from '../types';
import { MODEL_LABELS } from '../types';
import type { TContextUsage } from '../hooks/useChat';
import { countTokens, estimateCost, formatCost, getContextWindow, getMaxOutputTokens, isApproximateTokenizer } from '../utils/tokens';

type TChatInputProps = {
  onSubmit: (text: string, files: File[]) => Promise<void>;
  isLoading: boolean;
  selectedModels: string[];
  systemPrompt: string;
  conversations: Record<string, TUIMessage[]>;
  maxOutputTokens: number;
  setMaxOutputTokens: (n: number) => void;
  contextUsage: TContextUsage;
};

function fmtCtx(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${Math.round(n / 1_000)}k`;
  return `${n}`;
}

const ChatInput = ({
  onSubmit,
  isLoading,
  selectedModels,
  systemPrompt,
  conversations,
  maxOutputTokens,
  setMaxOutputTokens,
  contextUsage,
}: TChatInputProps) => {
  const [text, setText] = useState('');
  const [files, setFiles] = useState<File[]>([]);
  const [previews, setPreviews] = useState<string[]>([]);
  const [showBudget, setShowBudget] = useState(false);
  const [showContext, setShowContext] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const handleFiles = useCallback((selected: FileList | null) => {
    if (!selected) return;
    const arr = Array.from(selected);
    setFiles((prev) => [...prev, ...arr]);
    arr.forEach((f) => {
      const reader = new FileReader();
      reader.onload = () =>
        setPreviews((prev) => [...prev, reader.result as string]);
      reader.readAsDataURL(f);
    });
  }, []);

  const removeFile = useCallback((index: number) => {
    setFiles((prev) => prev.filter((_, i) => i !== index));
    setPreviews((prev) => prev.filter((_, i) => i !== index));
  }, []);

  const handleSubmit = useCallback(async () => {
    if ((!text.trim() && files.length === 0) || isLoading) return;
    const t = text;
    const f = files;
    setText('');
    setFiles([]);
    setPreviews([]);
    await onSubmit(t, f);
  }, [text, files, isLoading, onSubmit]);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        handleSubmit();
      }
    },
    [handleSubmit],
  );

  const effectiveMax = useMemo(() => {
    const limits = selectedModels.map((m) => getMaxOutputTokens(m)).filter((n) => n > 0);
    return limits.length > 0 ? Math.min(...limits) : 32000;
  }, [selectedModels]);

  const handleMaxTokensChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const val = parseInt(e.target.value);
      if (!isNaN(val) && val > 0) setMaxOutputTokens(Math.min(val, effectiveMax));
    },
    [setMaxOutputTokens, effectiveMax],
  );

  const estimates = useMemo(() => {
    return selectedModels.map((model) => {
      const systemTok = countTokens(systemPrompt, model);
      const msgTok = countTokens(text, model);
      const historyTok = (conversations[model] ?? []).reduce(
        (acc, msg) => acc + countTokens(msg.content, model),
        0,
      );
      const inputTokens = systemTok + historyTok + msgTok;
      const cost = estimateCost(model, inputTokens, maxOutputTokens);
      return { model, inputTokens, cost };
    });
  }, [selectedModels, systemPrompt, conversations, text, maxOutputTokens]);

  const totalCost = useMemo(
    () => estimates.reduce((s, e) => s + e.cost, 0),
    [estimates],
  );

  const repInputTokens = estimates[0]?.inputTokens ?? 0;

  useEffect(() => {
    if (maxOutputTokens > effectiveMax) setMaxOutputTokens(effectiveMax);
  }, [effectiveMax, maxOutputTokens, setMaxOutputTokens]);

  const contextRows = useMemo(
    () =>
      selectedModels
        .map((m) => {
          const used = contextUsage[m]?.used ?? 0;
          const win =
            (contextUsage[m]?.window ?? 0) > 0
              ? contextUsage[m]!.window
              : getContextWindow(m);
          const remaining = win > 0 ? Math.max(0, win - used) : 0;
          const pct = win > 0 ? Math.min(100, Math.round((used / win) * 100)) : 0;
          const safeMax = Math.max(1, remaining - 500);
          return { model: m, used, win, remaining, pct, safeMax };
        })
        .filter((r) => r.win > 0),
    [selectedModels, contextUsage],
  );

  const leastRemaining = useMemo(
    () =>
      contextRows.length > 0
        ? contextRows.reduce((a, b) => (a.remaining < b.remaining ? a : b))
        : null,
    [contextRows],
  );

  const overLimit = leastRemaining ? maxOutputTokens > leastRemaining.safeMax : false;

  return (
    <div className="border-t border-solid border-primary-border bg-primary-bg px-4 py-3">
      {previews.length > 0 && (
        <div className="flex gap-2 mb-2 flex-wrap">
          {previews.map((src, i) => (
            <div key={i} className="relative">
              <img
                src={src}
                alt="preview"
                className="w-14 h-14 object-cover rounded-lg border border-solid border-primary-border"
              />
              <button
                onClick={() => removeFile(i)}
                className="absolute -top-1.5 -right-1.5 bg-secondary-bg text-primary-text rounded-full w-4 h-4 text-xs flex items-center justify-center leading-none border border-solid border-primary-border"
              >
                ×
              </button>
            </div>
          ))}
        </div>
      )}

      <div className="flex items-end gap-2 mb-2">
        <Button
          variant="ghost"
          size="s"
          disabled={isLoading}
          onClick={() => fileInputRef.current?.click()}
          title="Attach images"
          type="button"
        >
          <svg
            width="18"
            height="18"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
          >
            <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48" />
          </svg>
        </Button>

        <input
          ref={fileInputRef}
          type="file"
          multiple
          accept="image/*"
          className="hidden"
          onChange={(e) => handleFiles(e.target.files)}
        />

        <div className="flex-1">
          <TextArea
            value={text}
            onChange={({ value }) => setText(value)}
            onKeyDown={handleKeyDown}
            placeholder="Message all selected models… (Enter to send, Shift+Enter for newline)"
            rows={1}
            disabled={isLoading}
            wrapperClassName="w-full"
          />
        </div>
      </div>

      <div className="flex items-center gap-3">
        {/* Max tokens + context window hover */}
        <div className="flex items-center gap-1.5 flex-shrink-0">
          <span className="text-xs text-secondary-text whitespace-nowrap">Max output tokens:</span>
          <input
            type="number"
            min={1}
            max={effectiveMax}
            value={maxOutputTokens}
            onChange={handleMaxTokensChange}
            disabled={isLoading}
            className="w-20 px-1.5 py-0.5 text-xs rounded border border-solid border-primary-border bg-secondary-bg text-primary-text focus:outline-none focus:border-interactive-border disabled:opacity-50"
          />

          {leastRemaining && (
            <div
              className="relative"
              onMouseEnter={() => setShowContext(true)}
              onMouseLeave={() => setShowContext(false)}
            >
              <span
                className={`text-xs cursor-default select-none whitespace-nowrap ${
                  overLimit ? 'text-amber-400' : 'text-tertiary-text'
                }`}
              >
                {fmtCtx(leastRemaining.remaining)} ctx left
                {overLimit && (
                  <button
                    type="button"
                    onClick={() => setMaxOutputTokens(leastRemaining.safeMax)}
                    className="ml-1.5 underline cursor-pointer bg-transparent border-0 text-amber-400 text-xs p-0"
                  >
                    use {leastRemaining.safeMax}
                  </button>
                )}
              </span>

              {showContext && contextRows.length > 0 && (
                <div className="absolute bottom-full mb-2 left-0 bg-primary-bg border border-solid border-primary-border rounded-lg shadow-lg p-3 z-50 min-w-[320px]">
                  <div className="text-xs font-semibold text-secondary-text tracking-widest mb-2 uppercase">
                    Context Window
                  </div>
                  <div className="space-y-2.5">
                    {contextRows.map(({ model, used, win, remaining, pct }) => (
                      <div key={model}>
                        <div className="flex items-center justify-between gap-2 text-xs mb-1">
                          <span className="text-primary-text font-medium">{MODEL_LABELS[model as TModel] ?? model}</span>
                          <span
                            className={`tabular-nums ${
                              pct >= 90
                                ? 'text-red-400'
                                : pct >= 75
                                  ? 'text-amber-400'
                                  : 'text-secondary-text'
                            }`}
                          >
                            {pct}%
                          </span>
                        </div>
                        <div className="w-full h-1.5 bg-secondary-bg rounded-full overflow-hidden mb-1">
                          <div
                            className={`h-full rounded-full transition-all duration-300 ${
                              pct >= 90
                                ? 'bg-red-500'
                                : pct >= 75
                                  ? 'bg-amber-400'
                                  : 'bg-interactive-border'
                            }`}
                            style={{ width: `${pct}%` }}
                          />
                        </div>
                        <div className="flex items-center justify-between text-[10px] text-tertiary-text tabular-nums">
                          <span>{fmtCtx(used)} used</span>
                          <span>{fmtCtx(remaining)} remaining</span>
                          <span className="text-secondary-text">{fmtCtx(win)} total</span>
                        </div>
                        {getMaxOutputTokens(model) > 0 && (
                          <div className="text-[10px] text-tertiary-text mt-0.5">
                            Max output:{' '}
                            <span
                              className={
                                maxOutputTokens > getMaxOutputTokens(model)
                                  ? 'text-amber-400'
                                  : 'text-secondary-text'
                              }
                            >
                              {fmtCtx(getMaxOutputTokens(model))} tokens
                            </span>
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                  <p className="text-[10px] text-tertiary-text mt-2.5 leading-relaxed border-t border-solid border-primary-border pt-2">
                    Usage reflects last response's full context fill (history + output).
                  </p>
                </div>
              )}
            </div>
          )}
        </div>

        {/* Token budget estimate */}
        <div
          className="flex-1 relative flex justify-center"
          onMouseEnter={() => setShowBudget(true)}
          onMouseLeave={() => setShowBudget(false)}
        >
          <span className="text-xs text-tertiary-text cursor-default select-none">
            ~{repInputTokens} tok · est. {formatCost(totalCost)} across {selectedModels.length} model{selectedModels.length !== 1 ? 's' : ''}
          </span>

          {showBudget && estimates.length > 0 && (
            <div className="absolute bottom-full mb-2 left-1/2 -translate-x-1/2 bg-primary-bg border border-solid border-primary-border rounded-lg shadow-lg p-3 z-50 min-w-[260px]">
              <div className="text-xs font-semibold text-secondary-text tracking-widest mb-2 uppercase">
                Token Budget · Estimate
              </div>
              <div className="space-y-1.5">
                {estimates.map(({ model, inputTokens, cost }) => (
                  <div key={model} className="flex items-center justify-between gap-3 text-xs">
                    <span className="text-primary-text">{MODEL_LABELS[model as TModel] ?? model}</span>
                    <span className="text-secondary-text tabular-nums">{isApproximateTokenizer(model) ? '~' : ''}{inputTokens} tok</span>
                    <span className="text-secondary-text tabular-nums font-medium">{formatCost(cost)}</span>
                  </div>
                ))}
                <div className="border-t border-solid border-primary-border pt-1.5 mt-1 flex items-center justify-between text-xs font-semibold">
                  <span className="text-secondary-text">Total per run</span>
                  <span className="text-primary-text tabular-nums">{formatCost(totalCost)}</span>
                </div>
              </div>
              <p className="text-xs text-tertiary-text mt-2 leading-relaxed">
                GPT: exact (cl100k_base) · Claude &amp; Gemini (~): approximate · output cost from max tokens setting.
              </p>
            </div>
          )}
        </div>

        <Button
          variant="primary"
          size="m"
          disabled={isLoading || (!text.trim() && files.length === 0)}
          onClick={handleSubmit}
          type="button"
        >
          {isLoading ? (
            <Spinner className="w-4 h-4" />
          ) : (
            <svg
              width="16"
              height="16"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2.5"
            >
              <line x1="22" y1="2" x2="11" y2="13" />
              <polygon points="22 2 15 22 11 13 2 9 22 2" />
            </svg>
          )}
        </Button>
      </div>
    </div>
  );
};

export default ChatInput;
