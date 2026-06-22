import { useState, useRef, useCallback, useMemo } from 'react';
import { Button, TextArea, Spinner } from '@meesho/merlin-ui-tailwind';
import type { TUIMessage } from '../types';
import { countTokens, estimateCost, formatCost } from '../utils/tokens';
import { usePersistentState } from '../hooks/usePersistentState';

function imageTokensPerImage(model: string): number {
  if (model.startsWith('claude')) return 1590;
  if (model.startsWith('gpt') || model.startsWith('o1') || model.startsWith('o3')) return 765;
  if (model.startsWith('gemini')) return 258;
  return 1024;
}

type TChatInputProps = {
  onSubmit: (text: string, files: File[]) => Promise<void>;
  isLoading: boolean;
  selectedModels: string[];
  systemPrompt: string;
  conversations: Record<string, TUIMessage[]>;
  maxOutputTokens: number;
  setMaxOutputTokens: (n: number) => void;
  // Per-task image upload limits (from the playground task config). 0/undefined
  // = no limit. Enforced authoritatively by the backend; checked here too so an
  // oversized or excess image is rejected before it's ever sent.
  maxImageKB?: number;
  maxImages?: number;
};

const ChatInput = ({
  onSubmit,
  isLoading,
  selectedModels,
  systemPrompt,
  conversations,
  maxOutputTokens,
  setMaxOutputTokens,
  maxImageKB = 0,
  maxImages = 0,
}: TChatInputProps) => {
  // The unsent message draft persists so a half-typed prompt survives a reload.
  // Attached files are File objects (not serializable), so they stay in-memory.
  const [text, setText] = usePersistentState('compare.draftMessage', '');
  const [files, setFiles] = useState<File[]>([]);
  const [previews, setPreviews] = useState<string[]>([]);
  const [showBudget, setShowBudget] = useState(false);
  const [imageError, setImageError] = useState<string | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const handleFiles = useCallback(
    (selected: FileList | null) => {
      if (!selected) return;
      let accepted = Array.from(selected);
      const rejected: string[] = [];

      // Drop images over the per-image size limit (f.size is bytes).
      if (maxImageKB > 0) {
        const limitBytes = maxImageKB * 1024;
        accepted = accepted.filter((f) => {
          if (f.size > limitBytes) {
            rejected.push(`${f.name} (${Math.ceil(f.size / 1024)} KB)`);
            return false;
          }
          return true;
        });
      }

      // Cap the total number of attached images against what's already attached.
      if (maxImages > 0) {
        const room = Math.max(0, maxImages - files.length);
        if (accepted.length > room) {
          accepted = accepted.slice(0, room);
          rejected.push(`max ${maxImages} image${maxImages === 1 ? '' : 's'}`);
        }
      }

      const cap = maxImageKB > 0 ? ` (max ${maxImageKB} KB each)` : '';
      setImageError(rejected.length > 0 ? `Skipped: ${rejected.join(', ')}${cap}.` : null);

      if (accepted.length === 0) return;

      // Read every accepted file, then append files + previews together so the
      // two arrays stay index-aligned (removeFile drops the same index from both).
      const reads = accepted.map(
        (f) =>
          new Promise<string>((resolve, reject) => {
            const reader = new FileReader();
            reader.onload = () => resolve(reader.result as string);
            reader.onerror = reject;
            reader.readAsDataURL(f);
          }),
      );
      Promise.all(reads).then((urls) => {
        setFiles((prev) => [...prev, ...accepted]);
        setPreviews((prev) => [...prev, ...urls]);
      });
    },
    [maxImageKB, maxImages, files.length],
  );

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
    setImageError(null);
    await onSubmit(t, f);
  }, [text, files, isLoading, onSubmit, setText]);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        handleSubmit();
      }
    },
    [handleSubmit],
  );

  const handleMaxTokensChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const val = parseInt(e.target.value);
      if (!isNaN(val) && val > 0) setMaxOutputTokens(val);
    },
    [setMaxOutputTokens],
  );

  const estimates = useMemo(() => {
    return selectedModels.map((model) => {
      const systemTok = countTokens(systemPrompt, model);
      const msgTok = countTokens(text, model);
      const historyTok = (conversations[model] ?? []).reduce(
        (acc, msg) => acc + countTokens(msg.content, model),
        0,
      );
      const imageTok = previews.length * imageTokensPerImage(model);
      const inputTokens = systemTok + historyTok + msgTok + imageTok;
      const cost = estimateCost(model, inputTokens, maxOutputTokens);
      return { model, inputTokens, cost };
    });
  }, [selectedModels, systemPrompt, conversations, text, previews, maxOutputTokens]);

  const totalCost = useMemo(
    () => estimates.reduce((s, e) => s + e.cost, 0),
    [estimates],
  );

  const repInputTokens = estimates[0]?.inputTokens ?? 0;

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

      {imageError && (
        <div className="mb-2 text-xs text-error-text">{imageError}</div>
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
          onChange={(e) => {
            handleFiles(e.target.files);
            e.target.value = ''; // allow re-selecting the same file after a reject
          }}
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
        <div className="flex items-center gap-1.5 flex-shrink-0">
          <span className="text-xs text-secondary-text whitespace-nowrap">Max output tokens:</span>
          <input
            type="number"
            min={1}
            max={32000}
            value={maxOutputTokens}
            onChange={handleMaxTokensChange}
            disabled={isLoading}
            className="w-20 px-1.5 py-0.5 text-xs rounded border border-solid border-primary-border bg-secondary-bg text-primary-text focus:outline-none focus:border-interactive-border disabled:opacity-50"
          />
        </div>

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
                    <span className="text-primary-text">{model}</span>
                    <span className="text-secondary-text tabular-nums">{inputTokens} tok</span>
                    <span className="text-secondary-text tabular-nums font-medium">{formatCost(cost)}</span>
                  </div>
                ))}
                <div className="border-t border-solid border-primary-border pt-1.5 mt-1 flex items-center justify-between text-xs font-semibold">
                  <span className="text-secondary-text">Total per run</span>
                  <span className="text-primary-text tabular-nums">{formatCost(totalCost)}</span>
                </div>
              </div>
              <p className="text-xs text-tertiary-text mt-2 leading-relaxed">
                Approximate — text via cl100k_base · images estimated at ~765–1590 tok/image · output cost from max tokens setting.
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
