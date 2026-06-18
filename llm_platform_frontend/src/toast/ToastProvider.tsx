import { useCallback, useMemo, useRef, useState, type ReactNode } from 'react';
import { Typography, cn } from '@meesho/merlin-ui-tailwind';
import { ToastContext, type Toast, type ToastApi, type ToastTone } from './context';

const AUTO_DISMISS_MS = 6000;

const TONE_CLASS: Record<ToastTone, string> = {
  error: 'border-error-border bg-error-bg text-error-text',
  success: 'border-green-300 bg-green-50 text-green-800',
  info: 'border-primary-border bg-primary-bg text-primary-text',
};

// ToastProvider renders a fixed bottom-right stack of transient notifications and
// exposes toast.error/success/info via context. No external dependency — built on
// Merlin tokens. Error toasts persist long enough (6s) to read a request_id ref.
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextId = useRef(1);

  const dismiss = useCallback((id: number) => {
    setToasts((cur) => cur.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (tone: ToastTone, message: string) => {
      const id = nextId.current++;
      setToasts((cur) => [...cur, { id, tone, message }]);
      setTimeout(() => dismiss(id), AUTO_DISMISS_MS);
    },
    [dismiss],
  );

  const api = useMemo<ToastApi>(
    () => ({
      error: (m) => push('error', m),
      success: (m) => push('success', m),
      info: (m) => push('info', m),
    }),
    [push],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="pointer-events-none fixed bottom-4 right-4 z-50 flex w-full max-w-sm flex-col gap-2">
        {toasts.map((t) => (
          <div
            key={t.id}
            role="alert"
            className={cn(
              'pointer-events-auto flex items-start gap-3 rounded-lg border border-solid px-4 py-3 shadow-md',
              TONE_CLASS[t.tone],
            )}
          >
            <Typography variant="body" size="2" className="flex-1 break-words">
              {t.message}
            </Typography>
            <button
              type="button"
              aria-label="Dismiss"
              onClick={() => dismiss(t.id)}
              className="shrink-0 text-lg leading-none opacity-60 hover:opacity-100"
            >
              ×
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
